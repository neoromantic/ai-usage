package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/view"
	"github.com/neoromantic/ai-usage/relay"
)

// TestMain lets this test binary stand in for a release binary: run as
// "<binary> version" with AIU_FAKE_RELEASE set, it prints that version. With
// AIU_AS_CLI set, it is the command itself.
func TestMain(m *testing.M) {
	if v := os.Getenv("AIU_FAKE_RELEASE"); v != "" && len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(v)
		os.Exit(0)
	}
	if os.Getenv("AIU_AS_CLI") == "1" {
		// As hermetic has it in the test that started this one.
		probeEnv = fakeProbeEnv
		hostname = func() string { return "test-host" }
		osUser = func() string { return "tester" }
		os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// hermetic keeps a test away from the machine running it: no harness binary
// is found through PATH or the usual install places, harness homes come only
// from each device's fake user home, and the scheduler and updater fail the
// test if they are reached without a test asking for them.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("AI_USAGE_NO_SCHEDULE", "1")
	t.Setenv("AI_USAGE_RELAY", "")
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GROK_HOME", "HERMES_HOME"} {
		t.Setenv(k, "")
	}
	// Orca's app data folder moves with these.
	t.Setenv("APPDATA", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	// The console reads these; a test sees the same output everywhere.
	for _, k := range []string{"COLUMNS", "NO_COLOR", "TERM", "LC_CTYPE", "LANG", "WT_SESSION", "TERM_PROGRAM"} {
		t.Setenv(k, "")
	}
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("PATH", t.TempDir())

	saved := struct {
		version, defaultRelay string
		clock                 func() time.Time
		probeEnv              func() probe.Env
		newScheduler          func() schedule.Scheduler
		newUpdater            func() *selfupdate.Updater
		hostname, osUser      func() string
	}{version, defaultRelay, clock, probeEnv, newScheduler, newUpdater, hostname, osUser}
	t.Cleanup(func() {
		version, defaultRelay, clock = saved.version, saved.defaultRelay, saved.clock
		probeEnv, newScheduler, newUpdater = saved.probeEnv, saved.newScheduler, saved.newUpdater
		hostname, osUser = saved.hostname, saved.osUser
	})
	version, defaultRelay, clock = "dev", "", time.Now
	// A CI runner's host name can be long enough to change the layout.
	hostname = func() string { return "test-host" }
	osUser = func() string { return "tester" }
	probeEnv = fakeProbeEnv
	newScheduler = func() schedule.Scheduler {
		return schedule.Scheduler{GOOS: runtime.GOOS, Run: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			t.Errorf("scheduler reached: %s %v", name, args)
			return nil, fmt.Errorf("no scheduler in tests")
		}}
	}
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("update check reached: %s", r.URL)
		http.Error(w, "no", http.StatusTeapot)
	}))
	t.Cleanup(guard.Close)
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, API: guard.URL, HTTP: guard.Client(), Exe: filepath.Join(t.TempDir(), "ai-usage")}
	}
}

// fakeProbeEnv finds every harness at a fake path and runs this test binary
// in its place, so no real claude or codex is ever started.
func fakeProbeEnv() probe.Env {
	home, _ := os.UserHomeDir()
	return probe.Env{
		LookPath: func(name string) (string, error) { return filepath.Join(home, "fake-bin", name), nil },
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)...)
		},
		Environ: []string{"AIU_FAKE_HARNESS=1"},
		HomeDir: home,
		Now:     time.Now,
		Timeout: 30 * time.Second,
	}
}

// TestHelperProcess is the fake claude and codex.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("AIU_FAKE_HARNESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch cmd := filepath.Base(args[0]) + " " + strings.Join(args[1:], " "); cmd {
	case "claude auth status --json":
		fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai","email":"dev@example.com","subscriptionType":"max"}`)
	case "codex app-server":
		fakeCodexAppServer(os.Stdin, os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "fake harness does not know %q\n", cmd)
		os.Exit(2)
	}
	os.Exit(0)
}

func fakeCodexAppServer(in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil {
			continue
		}
		var result any
		switch msg.Method {
		case "initialize":
			result = map[string]any{"userAgent": "fake-codex"}
		case "account/read":
			// Reading must never refresh a token.
			var p struct {
				RefreshToken *bool `json:"refreshToken"`
			}
			if json.Unmarshal(msg.Params, &p) != nil || p.RefreshToken == nil || *p.RefreshToken {
				os.Exit(3)
			}
			result = map[string]any{"account": map[string]any{"type": "chatgpt", "email": "dev@example.com", "planType": "plus"}}
		case "account/rateLimits/read":
			reset := time.Now().Add(2 * time.Hour).Unix()
			result = map[string]any{"rateLimits": map[string]any{
				"limitId":   "codex",
				"primary":   map[string]any{"usedPercent": 80, "windowDurationMins": 300, "resetsAt": reset},
				"secondary": map[string]any{"usedPercent": 20, "windowDurationMins": 10080, "resetsAt": reset + 86400},
			}}
		default:
			b, _ := json.Marshal(map[string]any{"id": *msg.ID, "error": map[string]any{"message": "unknown method"}})
			_, _ = out.Write(append(b, '\n'))
			continue
		}
		b, _ := json.Marshal(map[string]any{"id": *msg.ID, "result": result})
		_, _ = out.Write(append(b, '\n'))
	}
}

// device is one install: a fake user home and a collector directory.
type device struct {
	t    *testing.T
	home string
	dir  string
}

func newDevice(t *testing.T) *device {
	t.Helper()
	root := t.TempDir()
	d := &device{t: t, home: filepath.Join(root, "home"), dir: filepath.Join(root, "ai-usage")}
	if err := os.MkdirAll(d.home, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

type result struct {
	code           int
	stdout, stderr string
}

// run calls the CLI in-process as this device.
func (d *device) run(stdin string, args ...string) result {
	d.t.Helper()
	d.t.Setenv("AI_USAGE_HOME", d.dir)
	d.t.Setenv("HOME", d.home)
	d.t.Setenv("USERPROFILE", d.home)
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return result{code, out.String(), errb.String()}
}

// ok runs a command that must succeed and returns its output.
func (d *device) ok(args ...string) string {
	d.t.Helper()
	r := d.run("", args...)
	if r.code != 0 {
		d.t.Fatalf("ai-usage %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r.stdout
}

func (d *device) report() view.Report {
	d.t.Helper()
	var r view.Report
	if err := json.Unmarshal([]byte(d.ok("report", "--json")), &r); err != nil {
		d.t.Fatal(err)
	}
	return r
}

func (d *device) state() *state.State {
	d.t.Helper()
	st, err := state.Dir(d.dir).LoadState()
	if err != nil {
		d.t.Fatal(err)
	}
	return st
}

func (d *device) config() state.Config {
	d.t.Helper()
	var c state.Config
	b, err := os.ReadFile(filepath.Join(d.dir, "config.json"))
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil {
		d.t.Fatal(err)
	}
	return c
}

func (d *device) write(rel, body string) {
	d.t.Helper()
	path := filepath.Join(d.home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		d.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		d.t.Fatal(err)
	}
}

// claude adds one Claude Code session with n times the base usage, and the
// usage Claude Code cached for the logged-in account.
func (d *device) claude(session, project string, n int64) {
	d.write(".claude/projects/"+strings.ReplaceAll(project, "/", "-")+"/"+session+".jsonl",
		fmt.Sprintf(`{"type":"user","cwd":%q,"message":{"role":"user","content":"a prompt that is never read"}}`+"\n"+
			`{"type":"assistant","cwd":%q,"message":{"id":"msg_1","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`+"\n",
			project, project, 100*n, 50*n, 1000*n, 10*n))
	reset := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	d.write(".claude.json", fmt.Sprintf(`{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":"acct-1","utilization":{"five_hour":{"utilization":42,"resets_at":%q},"seven_day":{"utilization":91,"resets_at":%q}}}}`,
		time.Now().UnixMilli(), reset, reset))
}

// codex adds one Codex session. Codex counts cached input inside input.
func (d *device) codex(session, project string) {
	d.write(".codex/sessions/2026/09/23/rollout-"+session+".jsonl",
		fmt.Sprintf(`{"timestamp":"2026-09-23T10:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n"+
			`{"timestamp":"2026-09-23T10:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":500,"cached_input_tokens":200,"output_tokens":70}}}}`+"\n",
			session, project))
}

func provider(t *testing.T, r view.Report, name string) view.Provider {
	t.Helper()
	for _, p := range r.Providers {
		if p.Provider == name {
			return p
		}
	}
	t.Fatalf("no %s section", name)
	return view.Provider{}
}

func TestVersionHelpAndUsageErrors(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	for _, args := range [][]string{{"version"}, {"-version"}, {"--version"}} {
		if out := d.ok(args...); out != "dev\n" {
			t.Fatalf("%v printed %q", args, out)
		}
	}
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"report", "-h"}, {"collect", "--help"}} {
		if out := d.ok(args...); !strings.Contains(out, "Usage:") {
			t.Fatalf("%v printed %q", args, out)
		}
	}
	for _, args := range [][]string{
		{"bogus"},
		{"collect", "--bogus"},
		{"collect", "extra"},
		{"report", "extra"},
		{"team", "bogus"},
		{"team", "key", "extra"},
		{"team", "join", "a", "b"},
		{"team", "forget-device"},
		{"team", "forget-device", "../other-team"},
		{"relay", "bogus"},
		{"relay", "set"},
		{"schedule"},
		{"schedule", "bogus"},
	} {
		r := d.run("", args...)
		if r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
		}
	}
	if r := d.run("", "bogus"); !strings.Contains(r.stderr, `unknown command "bogus"`) {
		t.Fatalf("stderr = %q", r.stderr)
	}
}

func TestReportBeforeAnyRunWritesNothing(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	r := d.run("", "report", "--json")
	if r.code != 1 || !strings.Contains(r.stderr, "nothing collected yet") {
		t.Fatalf("report: exit %d, stderr %q", r.code, r.stderr)
	}
	if _, err := os.Stat(d.dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(d.dir)
		t.Fatalf("report created collector files: %v", entries)
	}
}

// TestCollectWhileAnotherRunCollects: while the scheduled run holds the run
// lock, a run the person started waits and shows that run's result instead
// of collecting twice, the scheduler's own run skips, and editing the config
// does not wait at all. After a change to what would be collected, or a
// holder that did not collect, the waiting run collects itself.
func TestCollectWhileAnotherRunCollects(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	dir := state.Dir(d.dir)
	clock = func() time.Time { return time.Now().Add(time.Hour) }
	// hold stands in for the scheduled run: it holds the lock, and when it
	// ends it has collected, with the same inputs, or not.
	hold := func(collects bool) time.Time {
		t.Helper()
		held, err := dir.Lock()
		if err != nil {
			t.Fatal(err)
		}
		collected := d.state().LastRunAt.Add(15 * time.Minute)
		go func() {
			time.Sleep(300 * time.Millisecond)
			if collects {
				st, _ := dir.LoadState()
				st.LastRunAt = collected
				if err := dir.SaveState(st); err != nil {
					t.Error(err)
				}
			}
			held()
		}()
		return collected
	}

	collected := hold(true)
	if r := d.run("", "collect", "--quiet", "--offline"); r.code == 0 || !strings.Contains(r.stderr, "in progress") {
		t.Fatalf("scheduled run with the lock held: %+v", r)
	}
	r := d.run("", "collect", "--offline")
	if r.code != 0 || !strings.Contains(r.stderr, "collecting now") || !strings.Contains(r.stdout, "ACCOUNTS") {
		t.Fatalf("run the person started: %+v", r)
	}
	if got := d.state().LastRunAt; !got.Equal(collected) {
		t.Fatalf("collected a second time: last run %s, want %s", got, collected)
	}

	// A home added while the scheduled run collects is read by the run that
	// waited for it.
	collected = hold(true)
	extra := filepath.Join(d.home, "work-claude")
	if err := os.MkdirAll(extra, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	d.ok("home", "add", "claude", extra)
	if time.Since(start) > 5*time.Second {
		t.Fatalf("home add waited %s for the collection", time.Since(start))
	}
	if r := d.run("", "collect", "--offline"); r.code != 0 || !strings.Contains(r.stderr, "collecting now") {
		t.Fatalf("run after home add: %+v", r)
	}
	st := d.state()
	if !st.LastRunAt.After(collected) || !slices.Contains(st.Sources["claude"].Homes, extra) {
		t.Fatalf("did not collect the added home: last run %s, homes %q", st.LastRunAt, st.Sources["claude"].Homes)
	}
	if homes := d.config().Homes["claude"]; !slices.Contains(homes, extra) {
		t.Fatalf("the added home was lost: %q", homes)
	}

	// A holder that did not collect, such as `ai-usage update`, leaves the
	// collection to the run that waited.
	before := d.state().LastRunAt
	clock = func() time.Time { return time.Now().Add(2 * time.Hour) }
	hold(false)
	if r := d.run("", "collect", "--offline"); r.code != 0 || !strings.Contains(r.stderr, "collecting now") {
		t.Fatalf("run after a holder that did not collect: %+v", r)
	}
	if got := d.state().LastRunAt; !got.After(before) {
		t.Fatalf("did not collect: last run %s", got)
	}
}

func TestCollectOfflineThenViews(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.codex("c-1", "/work/api")

	if out := d.ok("collect", "--quiet", "--offline"); out != "" {
		t.Fatalf("--quiet printed %q", out)
	}
	r := d.report()
	if r.SchemaVersion != 2 || r.Collector.Version != "dev" || r.Collector.Device != d.config().Device {
		t.Fatalf("collector = %+v", r.Collector)
	}
	if r.Collector.Relay.URL != nil || r.Collector.LastSuccessAt == nil {
		t.Fatalf("relay or last success = %+v", r.Collector)
	}

	claude := provider(t, r, "claude")
	if claude.Status != "ok" || len(claude.Accounts) != 1 {
		t.Fatalf("claude = %+v", claude)
	}
	a := claude.Accounts[0]
	if a.Label != "dev@example.com" || !a.Current || a.Plan == nil || *a.Plan != "max" {
		t.Fatalf("claude account = %+v", a)
	}
	if want := (snapshot.Tokens{Input: 100, Output: 50, CacheRead: 1000, CacheWrite: 10}); a.Tokens != want {
		t.Fatalf("claude tokens = %+v, want %+v", a.Tokens, want)
	}
	if a.HeadlinePercent == nil || *a.HeadlinePercent != 91 || a.Level != "critical" || a.Quota == nil || a.Quota.Source != "cache" {
		t.Fatalf("claude quota = %+v %v %s", a.Quota, a.HeadlinePercent, a.Level)
	}
	if len(a.Projects) != 1 || a.Projects[0].Path != "/work/app" {
		t.Fatalf("claude projects = %+v", a.Projects)
	}

	codex := provider(t, r, "codex")
	if codex.Status != "ok" || len(codex.Accounts) != 1 {
		t.Fatalf("codex = %+v", codex)
	}
	c := codex.Accounts[0]
	if want := (snapshot.Tokens{Input: 300, Output: 70, CacheRead: 200}); c.Label != "dev@example.com" || c.Tokens != want {
		t.Fatalf("codex account = %+v", c)
	}
	if c.HeadlinePercent == nil || *c.HeadlinePercent != 80 || c.Level != "warning" || c.Quota.Source != "harness" {
		t.Fatalf("codex quota = %+v", c.Quota)
	}
	if len(r.Team.Devices) != 1 || !r.Team.Devices[0].This {
		t.Fatalf("team devices = %+v", r.Team.Devices)
	}

	text := d.ok("report")
	for _, want := range []string{
		"  · no relay  ",
		"\nACCOUNTS  2 · 1 critical · 1 warning\n",
		"\n● dev@example.com  █████▍  91% !!   42%  ",
		"\nclaude ● dev@example.com    ",
		"\ncodex  ● dev@example.com    ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text lacks %q:\n%s", want, text)
		}
	}
	// The bare command collects and prints the same console report.
	if out := d.ok("--offline"); !strings.Contains(out, "\nACCOUNTS  2 ") {
		t.Fatalf("bare run printed:\n%s", out)
	}
	var fresh view.Report
	if err := json.Unmarshal([]byte(d.ok("collect", "--offline", "--json")), &fresh); err != nil || fresh.SchemaVersion != 2 {
		t.Fatalf("collect --json: %v %+v", err, fresh.SchemaVersion)
	}

	status := d.ok("status", "--width", "160")
	for _, want := range []string{
		"\nversion         dev\n",
		"\ndirectory       " + d.dir + "\n",
		"\nrelay         · not configured; the team view shows this device only\n",
		"\nschedule      ✕ not registered; dev builds do not register themselves\n",
		"\nsources       ✓ claude  ~/.claude · 1 account\n",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
	var sj struct {
		SchemaVersion int `json:"schema_version"`
		Sources       []struct {
			Provider string `json:"provider"`
			Status   string `json:"status"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(d.ok("status", "--json")), &sj); err != nil || sj.SchemaVersion != 2 || len(sj.Sources) != 4 || sj.Sources[0].Provider != "claude" || sj.Sources[0].Status != "ok" {
		t.Fatalf("status --json = %+v, %v", sj, err)
	}

	teamOut := d.ok("team")
	if !strings.Contains(teamOut, "team "+r.Collector.Team) || !strings.Contains(teamOut, "has not been read from the relay") {
		t.Fatalf("team printed:\n%s", teamOut)
	}
}

func TestTeamKeyAndJoin(t *testing.T) {
	hermetic(t)
	a := newDevice(t)
	key := strings.TrimSpace(a.ok("team", "key"))
	if !strings.HasPrefix(key, "aiu-team-1:") {
		t.Fatalf("team key printed %q", key)
	}
	fp := fingerprint(t, a)

	// A new device joins from stdin, as a pasted line, before it has a key.
	b := newDevice(t)
	r := b.run(key+"\n", "team", "join")
	if r.code != 0 || !strings.Contains(r.stdout, "joined team "+fp) || strings.Contains(r.stdout, "previous key") {
		t.Fatalf("join: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "team.key.previous")); !os.IsNotExist(err) {
		t.Fatal("joining without a key saved a made-up previous key")
	}
	if got := fingerprint(t, b); got != fp {
		t.Fatalf("fingerprints differ: %s and %s", got, fp)
	}
	if out := b.ok("team", "join", key); !strings.Contains(out, "already in team "+fp) {
		t.Fatalf("second join printed %q", out)
	}
	if r := b.run("", "team", "join", "not-a-key"); r.code != 1 {
		t.Fatalf("bad key: exit %d", r.code)
	}

	// A device already in another team keeps that key beside the new one.
	c := newDevice(t)
	old := fingerprint(t, c)
	if out := c.ok("team", "join", key); !strings.Contains(out, "previous key saved to") {
		t.Fatalf("join printed %q", out)
	}
	prev, err := team.Load(filepath.Join(c.dir, "team.key.previous"))
	if err != nil || prev.Fingerprint() != old {
		t.Fatalf("previous key: %v", err)
	}

	// A damaged key does not block joining, and its bytes are kept.
	e := newDevice(t)
	if err := os.MkdirAll(e.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, "team.key"), []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.ok("team", "join", key)
	if got := fingerprint(t, e); got != fp {
		t.Fatalf("fingerprint after replacing a damaged key = %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(e.dir, "team.key.previous")); string(b) != "garbage\n" {
		t.Fatalf("damaged key was not kept: %q", b)
	}
}

// A key pasted at a terminal ends with Enter, not with end of input.
func TestTeamJoinReadsOneLine(t *testing.T) {
	hermetic(t)
	key := strings.TrimSpace(newDevice(t).ok("team", "key"))
	b := newDevice(t)
	b.run("", "version") // points the environment at b
	pr, pw := io.Pipe()
	go func() { _, _ = io.WriteString(pw, key+"\n") }()
	done := make(chan int, 1)
	var out bytes.Buffer
	go func() { done <- run(context.Background(), []string{"team", "join"}, pr, &out, io.Discard) }()
	select {
	case code := <-done:
		if code != 0 || !strings.Contains(out.String(), "joined team") {
			t.Fatalf("join: exit %d, %q", code, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("team join waited for end of input after the key line")
	}
}

func fingerprint(t *testing.T, d *device) string {
	t.Helper()
	line, _, _ := strings.Cut(d.ok("team"), "\n")
	fp, ok := strings.CutPrefix(line, "team ")
	if !ok || !snapshot.ValidTeam(fp) {
		t.Fatalf("team printed %q", line)
	}
	return fp
}

func TestRelayServeClientIPHeader(t *testing.T) {
	hermetic(t)
	for _, k := range []string{"KV_REST_API_URL", "KV_REST_API_TOKEN"} {
		t.Setenv(k, "")
	}
	// A stopped context shuts the relay down as soon as it starts.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if err := cmdRelay(ctx, []string{"serve", "--addr", "127.0.0.1:0", "--client-ip-header", " X-Real-Ip "}, &stdout, &stderr); err != nil {
		t.Fatalf("relay serve: %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "memory store; clients by X-Real-Ip from a local proxy") {
		t.Fatalf("stderr = %q", got)
	}
	err := cmdRelay(ctx, []string{"serve", "--addr", "127.0.0.1:0", "--client-ip-header", "X-Real-Ip: 1.2.3.4"}, &stdout, &stderr)
	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("a header value instead of a name: %v", err)
	}
}

func TestRelayCommands(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	if out := d.ok("relay", "show"); out != "no relay configured\n" {
		t.Fatalf("show = %q", out)
	}
	if out := d.ok("relay", "set", " https://relay.example.test/ "); out != "relay set to https://relay.example.test\n" {
		t.Fatalf("set = %q", out)
	}
	if out := d.ok("relay"); out != "https://relay.example.test\n" {
		t.Fatalf("show = %q", out)
	}
	for _, bad := range []string{"ftp://relay.example.test", "https://", "relay.example.test", "https://relay.example.test/?team=x", "https://relay.example.test/#x"} {
		if r := d.run("", "relay", "set", bad); r.code != 2 {
			t.Fatalf("relay set %q: exit %d", bad, r.code)
		}
	}
	if d.config().Relay != "https://relay.example.test" {
		t.Fatal("a rejected URL changed the config")
	}
	t.Setenv("AI_USAGE_RELAY", "http://127.0.0.1:9")
	if out := d.ok("relay", "show"); out != "http://127.0.0.1:9\n" {
		t.Fatalf("show with AI_USAGE_RELAY = %q", out)
	}
	t.Setenv("AI_USAGE_RELAY", "")
	if out := d.ok("relay", "clear"); out != "relay cleared\n" {
		t.Fatalf("clear = %q", out)
	}
	if out := d.ok("relay", "show"); out != "no relay configured\n" {
		t.Fatalf("show after clear = %q", out)
	}

	defaultRelay = "https://default.example.test"
	if out := d.ok("relay", "clear"); !strings.Contains(out, "default https://default.example.test") {
		t.Fatalf("clear with a built-in relay = %q", out)
	}
	defaultRelay = ""

	r := d.run("", "team", "forget-device", "d-0123456789abcdef01234567")
	if r.code != 1 || !strings.Contains(r.stderr, "no relay configured") {
		t.Fatalf("forget-device without a relay: exit %d, %q", r.code, r.stderr)
	}
}

func TestTwoDevicesShareATeam(t *testing.T) {
	hermetic(t)
	srv := httptest.NewServer(relay.NewServer(relay.NewMemory(), relay.Limits{}))
	defer srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)

	a, b := newDevice(t), newDevice(t)
	a.claude("aaaa", "/work/app", 1)
	a.codex("c-a", "/work/api")
	b.claude("bbbb", "/work/web", 2)

	a.ok("collect", "--quiet")
	b.ok("team", "join", strings.TrimSpace(a.ok("team", "key")))
	b.ok("collect", "--quiet")
	// A scheduled run reads the team at most hourly: it publishes, and a
	// report shows the last read.
	a.ok("collect", "--quiet")
	if r := a.report(); len(r.Team.Devices) != 1 || r.Collector.Relay.LastPushAt == nil || r.Collector.Relay.LastPushAt.Before(*r.Collector.Relay.LastPullAt) {
		t.Fatalf("a scheduled run read the team again: %d devices, relay %+v", len(r.Team.Devices), r.Collector.Relay)
	}
	// A run someone started reads it every time.
	a.ok("collect", "--json")

	r := a.report()
	if r.Collector.Relay.LastError != nil || r.Collector.Relay.Pending || r.Team.PulledAt == nil {
		t.Fatalf("relay = %+v", r.Collector.Relay)
	}
	var ids []string
	for _, dev := range r.Team.Devices {
		ids = append(ids, dev.Device)
	}
	want := []string{a.config().Device, b.config().Device}
	if !reflect.DeepEqual(ids, want) || !r.Team.Devices[0].This || r.Team.Devices[1].This {
		t.Fatalf("team devices = %v, want %v (this device first)", ids, want)
	}
	var claude *view.TeamAccount
	for _, p := range r.Team.Providers {
		if p.Provider == "claude" && len(p.Accounts) == 1 {
			claude = &p.Accounts[0]
		}
	}
	// Tokens add across devices; the quota is one reading, not a sum.
	if want := (snapshot.Tokens{Input: 300, Output: 150, CacheRead: 3000, CacheWrite: 30}); claude == nil || claude.Tokens != want || len(claude.Devices) != 2 {
		t.Fatalf("team claude = %+v", claude)
	}
	if claude.HeadlinePercent == nil || *claude.HeadlinePercent != 91 {
		t.Fatalf("team claude headline = %v", claude.HeadlinePercent)
	}
	if out := a.ok("team"); !strings.Contains(out, b.config().Device) || !strings.Contains(out, "read from the relay") {
		t.Fatalf("team printed:\n%s", out)
	}
	if out := a.ok("status"); !strings.Contains(out, "\nrelay         ✓ "+srv.URL+"\n") {
		t.Fatalf("status printed:\n%s", out)
	}

	// Forgetting B takes it off the relay; A's next read has only itself.
	if out := a.ok("team", "forget-device", b.config().Device); !strings.Contains(out, "removed "+b.config().Device) {
		t.Fatalf("forget-device printed %q", out)
	}
	// The cached team read drops it at once, before the next collection.
	if out := a.ok("team"); strings.Contains(out, b.config().Device) || !strings.Contains(out, a.config().Device) {
		t.Fatalf("team after forget printed:\n%s", out)
	}
	a.ok("collect", "--quiet")
	if r := a.report(); len(r.Team.Devices) != 1 {
		t.Fatalf("team devices after forget = %+v", r.Team.Devices)
	}
}

// home add registers homes by absolute path and, for Hermes, the home whose
// login they bill through, which it reads too. home remove undoes it.
func TestHomeCommands(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	root := filepath.Dir(d.home)
	bots := filepath.Join(root, "bots")
	for _, dir := range []string{".codex", ".grok", ".hermes-a/profiles/p", ".hermes-b", ".codex-gone"} {
		if err := os.MkdirAll(filepath.Join(bots, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bots, ".hermes-a/profiles/p/state.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(bots)
	out := d.ok("home", "add", "hermes", ".hermes-a", ".hermes-b", "--quota-from", "codex:.codex")
	codex, a, b := filepath.Join(bots, ".codex"), filepath.Join(bots, ".hermes-a"), filepath.Join(bots, ".hermes-b")
	lines := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		lines[strings.Join(strings.Fields(line), " ")] = true
	}
	for _, want := range []string{
		"codex " + codex + " added",
		"hermes " + a + " added · quota from codex " + codex,
		filepath.Join(a, "profiles", "p") + " quota from codex " + codex,
		b + " added · quota from codex " + codex,
	} {
		if !lines[want] {
			t.Fatalf("home add output lacks %q:\n%s", want, out)
		}
	}
	cfg := d.config()
	if !reflect.DeepEqual(cfg.Homes, map[string][]string{"codex": {codex}, "hermes": {a, b}}) ||
		!reflect.DeepEqual(cfg.QuotaFrom, map[string]map[string]string{a: {"codex": codex}, b: {"codex": codex}}) {
		t.Fatalf("config = %+v %+v", cfg.Homes, cfg.QuotaFrom)
	}

	// A home can take each harness's quota from its own home.
	grok := filepath.Join(bots, ".grok")
	d.ok("home", "add", "hermes", ".hermes-a", "--quota-from=grok:.grok")
	if got := d.config().QuotaFrom[a]; !reflect.DeepEqual(got, map[string]string{"codex": codex, "grok": grok}) {
		t.Fatalf("quota from = %+v", got)
	}

	d.ok("home", "remove", "hermes", b)
	cfg = d.config()
	if !reflect.DeepEqual(cfg.Homes["hermes"], []string{a}) || len(cfg.QuotaFrom) != 1 {
		t.Fatalf("after remove = %+v %+v", cfg.Homes, cfg.QuotaFrom)
	}

	// An added home that is gone is listed as missing, not dropped.
	gone := filepath.Join(bots, ".codex-gone")
	d.ok("home", "add", "codex", gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if out := d.ok("home"); !strings.Contains(strings.Join(strings.Fields(out), " "), "codex "+gone+" missing") {
		t.Fatalf("home lacks the missing home:\n%s", out)
	}
	d.ok("home", "remove", "codex", gone)

	// The default Hermes home can be named a quota home, and unnamed again,
	// with no other Hermes home added.
	defHermes := filepath.Join(d.home, ".hermes")
	if err := os.MkdirAll(defHermes, 0o700); err != nil {
		t.Fatal(err)
	}
	d.ok("home", "add", "hermes", defHermes, "--quota-from", "codex:"+codex, "--quota-from", "grok:"+grok)
	if got := d.config().QuotaFrom[defHermes]; !reflect.DeepEqual(got, map[string]string{"codex": codex, "grok": grok}) {
		t.Fatalf("quota from = %+v", d.config().QuotaFrom)
	}
	d.ok("home", "remove", "hermes", defHermes)
	cfg = d.config()
	if _, ok := cfg.QuotaFrom[defHermes]; ok {
		t.Fatalf("quota from after remove = %+v", cfg.QuotaFrom)
	}

	for _, bad := range [][]string{
		{"home", "add", "hermes"},
		{"home", "add", "nope", ".codex"},
		{"home", "add", "codex", ".codex", "--quota-from", "codex:.codex"},
		{"home", "add", "hermes", ".hermes-b", "--quota-from", "claude:.codex"},
		{"home", "add", "hermes", ".hermes-b", "--quota-from"},
		{"home", "remove", "hermes", ".hermes-b", "--quota-from", "codex:.codex"},
		{"home", "frob"},
		// An empty directory, as from an unset variable, is not the
		// working directory.
		{"home", "add", "hermes", ""},
		{"home", "add", "hermes", ".hermes-b", "--quota-from", "codex:"},
		{"home", "add", "hermes", ".hermes-b", "--quota-from="},
		{"home", "add", "hermes", ".hermes-b", "--quota-from", "codex:.codex", "--quota-from", "codex:.grok"},
	} {
		if r := d.run("", bad...); r.code != 2 {
			t.Fatalf("%v: exit %d, %s", bad, r.code, r.stderr)
		}
	}
	for _, bad := range [][]string{
		{"home", "add", "hermes", "missing"},
		{"home", "remove", "hermes", ".hermes-b"},
		{"home", "remove", "codex", filepath.Join(d.home, ".codex")},
		{"home", "remove", "hermes", defHermes},
		// A Hermes home is not a Codex home.
		{"home", "remove", "codex", a},
	} {
		if r := d.run("", bad...); r.code != 1 {
			t.Fatalf("%v: exit %d, %s", bad, r.code, r.stderr)
		}
	}
	// Removing the home Hermes takes its quota from would quietly move it to
	// the default login.
	if r := d.run("", "home", "remove", "codex", codex); r.code != 1 || !strings.Contains(r.stderr, "take their quota from") {
		t.Fatalf("removing a quota home: exit %d, %s", r.code, r.stderr)
	}

	// A config with no added homes at all: naming the default Hermes home
	// after the default Codex home adds none, and unnaming it is fine.
	e := newDevice(t)
	for _, dir := range []string{".hermes", ".codex"} {
		if err := os.MkdirAll(filepath.Join(e.home, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.ok("home", "add", "hermes", filepath.Join(e.home, ".hermes"), "--quota-from", "codex:"+filepath.Join(e.home, ".codex"))
	e.ok("home", "remove", "hermes", filepath.Join(e.home, ".hermes"))
	if cfg := e.config(); len(cfg.Homes) != 0 || len(cfg.QuotaFrom) != 0 {
		t.Fatalf("config = %+v %+v", cfg.Homes, cfg.QuotaFrom)
	}
	if !reflect.DeepEqual(d.config().Homes, cfg.Homes) {
		t.Fatal("a rejected command changed the config")
	}
}

// A home another collector reads now, such as a bot's in its own container,
// is removed with the sessions counted from it, so the team does not count
// them twice.
func TestHomeRemoveForgetsItsSessions(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.codex("c-own", "/work/api")
	bot := filepath.Join(d.home, "bot", ".codex")
	d.write("bot/.codex/sessions/2026/09/23/rollout-c-bot.jsonl",
		`{"timestamp":"2026-09-23T10:00:00Z","type":"session_meta","payload":{"id":"c-bot","cwd":"/work/bot"}}`+"\n"+
			`{"timestamp":"2026-09-23T10:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":900,"output_tokens":90}}}}`+"\n")
	d.ok("home", "add", "codex", bot)
	d.ok("collect", "--quiet", "--offline")
	own, botKey := state.Key("codex", "c-own"), state.Key("codex", "c-bot")
	if st := d.state(); st.Sessions[own] == nil || st.Sessions[botKey] == nil {
		t.Fatalf("sessions before = %v", st.Sessions)
	}

	if out := d.ok("home", "remove", "codex", bot, "--forget"); !strings.Contains(out, "forgot 1 session counted") {
		t.Fatalf("home remove --forget printed:\n%s", out)
	}
	if st := d.state(); st.Sessions[botKey] != nil || st.Sessions[own] == nil {
		t.Fatalf("sessions after = %v", st.Sessions)
	}
	if cfg := d.config(); len(cfg.Homes) != 0 {
		t.Fatalf("homes after = %v", cfg.Homes)
	}
	d.ok("collect", "--quiet", "--offline")
	if d.state().Sessions[botKey] != nil {
		t.Fatal("the next run read the removed home again")
	}
	// Forgetting can be tried again once the home is out of the config.
	if out := d.ok("home", "remove", "codex", bot, "--forget"); !strings.Contains(out, "forgot 0 sessions") {
		t.Fatalf("second --forget printed:\n%s", out)
	}

	// The default home is always read, so its sessions would only be counted
	// again from nothing.
	if r := d.run("", "home", "remove", "codex", filepath.Join(d.home, ".codex"), "--forget"); r.code != 1 || d.state().Sessions[own] == nil {
		t.Fatalf("forgetting the default home: exit %d, %s", r.code, r.stderr)
	}
	if r := d.run("", "home", "add", "codex", bot, "--forget"); r.code != 2 {
		t.Fatalf("home add --forget: exit %d, %s", r.code, r.stderr)
	}
	// A home that cannot be read changes nothing.
	gone := filepath.Join(d.home, "gone")
	if r := d.run("", "home", "remove", "codex", gone, "--forget"); r.code != 1 || !strings.Contains(r.stderr, "cannot read") {
		t.Fatalf("forgetting a missing home: exit %d, %s", r.code, r.stderr)
	}
}

// A device keeps collecting while the relay is down and says so.
func TestUnreachableRelay(t *testing.T) {
	hermetic(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	d.ok("collect", "--quiet")
	r := d.report()
	if !r.Collector.Relay.Pending || r.Collector.Relay.LastError == nil || r.Collector.LastSuccessAt == nil {
		t.Fatalf("collector = %+v", r.Collector)
	}
	if len(provider(t, r, "claude").Accounts) != 1 {
		t.Fatal("collection stopped with the relay")
	}
}

// fakeCrontab is the user's crontab in memory.
type fakeCrontab struct {
	mu     sync.Mutex
	tab    string
	writes int
}

func (f *fakeCrontab) scheduler() schedule.Scheduler {
	return schedule.Scheduler{GOOS: "linux", Run: func(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch strings.Join(append([]string{name}, args...), " ") {
		case "crontab -l":
			if f.tab == "" {
				return nil, fmt.Errorf("crontab: no crontab for tester")
			}
			return []byte(f.tab), nil
		case "crontab -":
			f.tab = string(stdin)
			f.writes++
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected %s %v", name, args)
	}}
}

func TestScheduleCommands(t *testing.T) {
	hermetic(t)
	cron := &fakeCrontab{tab: "0 3 * * * /usr/local/bin/backup\n"}
	newScheduler = cron.scheduler
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	if out := d.ok("schedule", "status"); out != "not registered\n" {
		t.Fatalf("status = %q", out)
	}
	if out := d.ok("schedule", "install"); !strings.Contains(out, "registered "+exe) {
		t.Fatalf("install = %q", out)
	}
	if !strings.HasPrefix(cron.tab, "0 3 * * * /usr/local/bin/backup\n") || !strings.Contains(cron.tab, "collect --quiet --home '"+d.dir+"'") {
		t.Fatalf("crontab = %q", cron.tab)
	}
	if out := d.ok("schedule", "status"); !strings.HasPrefix(out, "registered: "+exe) {
		t.Fatalf("status = %q", out)
	}

	for _, line := range []string{schedule.Line("/elsewhere/ai-usage", d.dir, "/bin"), schedule.Line(exe, "/elsewhere/state", "/bin")} {
		cron.tab = "0 3 * * * /usr/local/bin/backup\n" + line + "\n"
		if out := d.ok("schedule", "status"); !strings.Contains(out, "different binary path or state folder") {
			t.Fatalf("status = %q", out)
		}
	}

	if out := d.ok("schedule", "remove"); !strings.Contains(out, "removed") {
		t.Fatalf("remove = %q", out)
	}
	if cron.tab != "0 3 * * * /usr/local/bin/backup\n" || !d.config().ScheduleOff {
		t.Fatalf("after remove: crontab %q, config %+v", cron.tab, d.config())
	}
	d.ok("schedule", "install")
	if d.config().ScheduleOff {
		t.Fatal("install left schedule_off set")
	}
}

// Where there is no crontab, as in most containers, `schedule run` is the
// scheduler: it collects at once and then at each quarter hour, in a process
// of the binary on disk, until it is stopped. Runs meanwhile see that it is
// there and never reach for the system scheduler.
func TestScheduleRun(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	reached := 0
	newScheduler = func() schedule.Scheduler {
		return schedule.Scheduler{GOOS: "linux", Run: func(_ context.Context, name string, _ []string, _ []byte) ([]byte, error) {
			reached++
			return nil, fmt.Errorf("%s: %w", name, exec.ErrNotFound)
		}}
	}
	savedCollect, savedSleep, savedClock := collectOnce, sleepCtx, clock
	t.Cleanup(func() { collectOnce, sleepCtx, clock = savedCollect, savedSleep, savedClock })
	clock = func() time.Time { return time.Date(2026, 9, 23, 10, 7, 30, 0, time.UTC) }

	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	if st := d.state(); st.Schedule.Registered || !strings.Contains(st.Schedule.Error, "keep `ai-usage schedule run` running") {
		t.Fatalf("schedule without crontab = %+v", st.Schedule)
	}

	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	var collected []string
	var statuses []string
	collectOnce = func(ctx context.Context, gotExe, home string, stderr io.Writer) error {
		collected = append(collected, gotExe+" "+home)
		reached = 0
		var out bytes.Buffer
		if code := run(ctx, []string{"collect", "--quiet", "--offline", "--home", home}, strings.NewReader(""), &out, stderr); code != 0 {
			t.Errorf("collection: exit %d", code)
		}
		if reached != 0 {
			t.Errorf("a run under schedule run reached the system scheduler %d times", reached)
		}
		statuses = append(statuses, d.ok("status"))
		return nil
	}
	var slept []time.Duration
	sleepCtx = func(_ context.Context, dur time.Duration) bool {
		slept = append(slept, dur)
		return len(slept) < 2
	}
	r := d.run("", "schedule", "run")
	if r.code != 0 || !strings.Contains(r.stderr, "collecting every 15m0s with state folder "+d.dir) {
		t.Fatalf("schedule run: exit %d, stderr %q", r.code, r.stderr)
	}
	if want := []string{exe + " " + d.dir, exe + " " + d.dir}; !reflect.DeepEqual(collected, want) {
		t.Fatalf("collected %q, want %q", collected, want)
	}
	// The next run is at the next quarter hour, as the system schedulers run it.
	if want := []time.Duration{7*time.Minute + 30*time.Second, 7*time.Minute + 30*time.Second}; !reflect.DeepEqual(slept, want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for _, out := range statuses {
		if !strings.Contains(out, "every 15 minutes by `ai-usage schedule run`") {
			t.Fatalf("status while schedule run runs:\n%s", out)
		}
	}
	if st := d.state(); !st.Schedule.Registered || !st.Schedule.Foreground || st.Schedule.Error != "" {
		t.Fatalf("schedule after schedule run = %+v", st.Schedule)
	}

	// Once it stops, the report says so instead of claiming a schedule.
	if r := d.report(); r.Collector.Schedule.Registered || r.Collector.Schedule.Error == nil || !strings.Contains(*r.Collector.Schedule.Error, "has stopped") {
		t.Fatalf("schedule in the report after it stopped = %+v", r.Collector.Schedule)
	}
	if out := d.ok("status"); !strings.Contains(out, "`ai-usage schedule run` has stopped") {
		t.Fatalf("status after schedule run stopped:\n%s", out)
	}

	// One folder has one schedule run.
	unlock, err := state.Dir(d.dir).ScheduleLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if r := d.run("", "schedule", "run"); r.code != 1 || !strings.Contains(r.stderr, "another `ai-usage schedule run` already collects into") {
		t.Fatalf("a second schedule run: exit %d, stderr %q", r.code, r.stderr)
	}
}

// A collection schedule run starts is a process of its own, which sees the
// schedule lock the runner holds.
func TestScheduleRunStartsACollection(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	t.Setenv("AIU_AS_CLI", "1")
	t.Setenv("HOME", d.home)
	t.Setenv("USERPROFILE", d.home)
	unlock, err := state.Dir(d.dir).ScheduleLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := collectOnce(context.Background(), exe, d.dir, &stderr); err != nil {
		t.Fatalf("collectOnce: %v, %s", err, stderr.String())
	}
	st := d.state()
	if st.LastRunAt.IsZero() || !st.Schedule.Registered || !st.Schedule.Foreground {
		t.Fatalf("after the child's collection: last run %v, schedule %+v, stderr %s", st.LastRunAt, st.Schedule, stderr.String())
	}
	// It read this device's fake home, and asked the fake claude.
	if a := st.Accounts[state.Key("claude", "dev@example.com")]; a == nil || st.Sources["claude"].Status != "ok" {
		t.Fatalf("claude: %+v, accounts %v", st.Sources["claude"], st.Accounts)
	}
}

// A device goes by the name it is given instead of its host name, in its own
// report and in the team's, and AI_USAGE_NAME overrides both.
func TestDeviceName(t *testing.T) {
	hermetic(t)
	srv := httptest.NewServer(relay.NewServer(relay.NewMemory(), relay.Limits{}))
	defer srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)
	t.Setenv("AI_USAGE_NAME", "")

	a, b := newDevice(t), newDevice(t)
	a.claude("aaaa", "/work/app", 1)
	if out := a.ok("name"); !strings.HasPrefix(out, "test-host (the host name;") {
		t.Fatalf("name = %q", out)
	}
	for _, bad := range []string{"", "  ", "a\tb", strings.Repeat("я", maxName+1)} {
		if r := a.run("", "name", "set", bad); r.code != 2 {
			t.Fatalf("name set %q: exit %d, %s", bad, r.code, r.stderr)
		}
	}
	a.ok("name", "set", "  Mita bot  ")
	if got := a.config().Name; got != "Mita bot" {
		t.Fatalf("config name = %q", got)
	}
	if out := a.ok("name"); out != "Mita bot\n" {
		t.Fatalf("name = %q", out)
	}
	a.ok("collect", "--quiet")
	b.ok("team", "join", strings.TrimSpace(a.ok("team", "key")))
	b.ok("collect", "--json")

	label := func(r view.Report, device string) string {
		for _, dev := range r.Team.Devices {
			if dev.Device == device {
				return dev.Label
			}
		}
		return ""
	}
	if r := a.report(); r.Collector.DeviceLabel != "Mita bot" || label(r, a.config().Device) != "Mita bot" {
		t.Fatalf("own label = %q, in the team %q", r.Collector.DeviceLabel, label(r, a.config().Device))
	}
	if got := label(b.report(), a.config().Device); got != "Mita bot" {
		t.Fatalf("the team sees %q", got)
	}

	t.Setenv("AI_USAGE_NAME", "from-env")
	if out := a.ok("name"); out != "from-env (from AI_USAGE_NAME, in runs that see it; runs without it, such as the system scheduler's, use Mita bot)\n" {
		t.Fatalf("name with AI_USAGE_NAME = %q", out)
	}
	if out := a.ok("name", "set", "Mita bot"); out != "saved Mita bot; while AI_USAGE_NAME is set, this device is from-env\n" {
		t.Fatalf("name set with AI_USAGE_NAME = %q", out)
	}
	a.ok("collect", "--quiet")
	t.Setenv("AI_USAGE_NAME", "")
	b.ok("collect", "--json")
	if got := label(b.report(), a.config().Device); got != "from-env" {
		t.Fatalf("the team sees %q after AI_USAGE_NAME", got)
	}

	// A name set would refuse is not used from the environment either.
	t.Setenv("AI_USAGE_NAME", "evil\nFAKE LINE\x1b[2J")
	if out := a.ok("name"); out != "AI_USAGE_NAME is ignored: a name cannot contain control characters\nMita bot\n" {
		t.Fatalf("name with a bad AI_USAGE_NAME = %q", out)
	}
	if r := a.report(); r.Collector.DeviceLabel != "Mita bot" {
		t.Fatalf("own label with a bad AI_USAGE_NAME = %q", r.Collector.DeviceLabel)
	}
	t.Setenv("AI_USAGE_NAME", "")

	if out := a.ok("name", "clear"); !strings.Contains(out, "test-host") {
		t.Fatalf("name clear = %q", out)
	}
	a.ok("collect", "--quiet")
	b.ok("collect", "--json")
	if got := label(b.report(), a.config().Device); got != "test-host" {
		t.Fatalf("the team sees %q after name clear", got)
	}
}

// fakeRelease serves a v1.3.0 release for this platform. Its binary is this
// test binary, which starts and reports v1.3.0 as a release would.
func fakeRelease(t *testing.T) (*httptest.Server, *int, []byte) {
	t.Helper()
	asset := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	t.Setenv("AIU_FAKE_RELEASE", "v1.3.0")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bin)
	downloads := new(int)
	var mu sync.Mutex
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + selfupdate.Repo + "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v1.3.0","assets":[{"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
				asset, srv.URL+"/bin", srv.URL+"/sums")
		case "/bin":
			mu.Lock()
			*downloads++
			mu.Unlock()
			_, _ = w.Write(bin)
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, downloads, bin
}

func TestHousekeepingOnReleaseBuild(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	version = "v1.2.0"
	cron := &fakeCrontab{tab: "0 3 * * * /usr/local/bin/backup\n"}
	newScheduler = cron.scheduler
	srv, downloads, bin := fakeRelease(t)
	binDir := t.TempDir()
	exe := filepath.Join(binDir, "ai-usage")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, API: srv.URL, HTTP: srv.Client()}
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) { clock = func() time.Time { return t0.Add(d) } }
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)

	// A binary this user cannot replace reports why, without downloading.
	readOnly := runtime.GOOS != "windows" && os.Geteuid() != 0
	if readOnly {
		if err := os.Chmod(binDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(binDir, 0o755) })
		at(0)
		d.ok("collect", "--quiet", "--offline")
		st := d.state()
		if !st.Schedule.Registered || st.Update.Latest != "v1.3.0" || !strings.Contains(st.Update.Error, "cannot write beside") || st.Update.Installed != "" {
			t.Fatalf("schedule %+v update %+v", st.Schedule, st.Update)
		}
		if *downloads != 0 {
			t.Fatal("downloaded a binary it cannot install")
		}
		if out := d.ok("status"); !strings.Contains(out, "cannot write beside") {
			t.Fatalf("status hides the update error:\n%s", out)
		}
		if err := os.Chmod(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The next check installs the release for the next run.
	at(7 * time.Hour)
	d.ok("collect", "--quiet", "--offline")
	st := d.state()
	if st.Update.Installed != "v1.3.0" || st.Update.Error != "" {
		t.Fatalf("update = %+v", st.Update)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, bin) {
		t.Fatal("binary was not replaced")
	}
	if out := d.ok("status"); !strings.Contains(out, "\nupdate        ↑ v1.3.0 is installed and runs next time\n") {
		t.Fatalf("status:\n%s", out)
	}

	// The next run is v1.3.0: nothing is staged any more.
	version = "v1.3.0"
	at(8 * time.Hour)
	d.ok("collect", "--quiet", "--offline")
	if r := d.report(); r.Collector.Update.Staged != nil || r.Collector.Update.Latest == nil || *r.Collector.Update.Latest != "v1.3.0" {
		t.Fatalf("update = %+v", r.Collector.Update)
	}

	// A failed check keeps the last release it saw.
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, API: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: 5 * time.Second}}
	}
	at(15 * time.Hour)
	d.ok("collect", "--quiet", "--offline")
	if st := d.state(); st.Update.Latest != "v1.3.0" || st.Update.Error == "" {
		t.Fatalf("update after a failed check = %+v", st.Update)
	}

	// Registration happened once and kept the person's own line.
	if cron.writes != 1 || !strings.HasPrefix(cron.tab, "0 3 * * * /usr/local/bin/backup\n") {
		t.Fatalf("crontab written %d times: %q", cron.writes, cron.tab)
	}

	// After `schedule remove`, runs do not register again.
	d.ok("schedule", "remove")
	writes := cron.writes
	at(9 * time.Hour)
	d.ok("collect", "--quiet", "--offline")
	st = d.state()
	if st.Schedule.Registered || !strings.Contains(st.Schedule.Error, "schedule remove") || cron.writes != writes {
		t.Fatalf("schedule = %+v, crontab writes %d -> %d", st.Schedule, writes, cron.writes)
	}
}

func TestUpdateCommandRecordsResult(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	if r := d.run("", "update"); r.code != 1 || !strings.Contains(r.stderr, "development build") {
		t.Fatalf("dev update = %+v", r)
	}

	version = "v1.2.0"
	srv, _, _ := fakeRelease(t)
	exe := filepath.Join(t.TempDir(), "ai-usage")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, API: srv.URL, HTTP: srv.Client()}
	}
	if out := d.ok("update"); !strings.Contains(out, "installed v1.3.0") {
		t.Fatalf("update:\n%s", out)
	}
	st := d.state()
	if st.Update.Installed != "v1.3.0" || st.Update.Latest != "v1.3.0" || st.Update.CheckedAt.IsZero() || st.Update.Error != "" {
		t.Fatalf("update = %+v", st.Update)
	}

	// A failed check is recorded too, and keeps the release it last saw.
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, API: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: 5 * time.Second}}
	}
	if r := d.run("", "update"); r.code != 1 {
		t.Fatalf("failed update = %+v", r)
	}
	if st := d.state(); st.Update.Error == "" || st.Update.Latest != "v1.3.0" {
		t.Fatalf("update after a failed check = %+v", st.Update)
	}
}

// releaseBuild makes this a release build whose update checks go to a fake
// v1.3.0 release that installs into exe.
func releaseBuild(t *testing.T, running string) (exe string, downloads *int, bin []byte) {
	t.Helper()
	version = running
	srv, downloads, bin := fakeRelease(t)
	exe = filepath.Join(t.TempDir(), "ai-usage")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, API: srv.URL, HTTP: srv.Client()}
	}
	return exe, downloads, bin
}

// Cron sees neither AI_USAGE_HOME nor the shell's XDG_CONFIG_HOME, so the
// entry names the folder. Otherwise scheduled runs would start a second
// device with its own team key in the default folder.
func TestScheduledRunsUseTheSameFolder(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	cron := &fakeCrontab{}
	newScheduler = cron.scheduler
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	if !strings.Contains(cron.tab, " collect --quiet --home '"+d.dir+"' ") {
		t.Fatalf("crontab = %q", cron.tab)
	}
	device, key := d.config().Device, strings.TrimSpace(d.ok("team", "key"))

	// What cron runs, in an environment that would pick another folder.
	elsewhere := filepath.Join(t.TempDir(), "default")
	t.Setenv("AI_USAGE_HOME", elsewhere)
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"collect", "--quiet", "--offline", "--home", d.dir}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("scheduled run: exit %d, %s", code, errb.String())
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Fatal("the scheduled run used the folder from its environment")
	}
	if d.config().Device != device || strings.TrimSpace(d.ok("team", "key")) != key {
		t.Fatal("the scheduled run changed the device or the team")
	}
	if st := d.state(); !st.Schedule.Registered || st.Schedule.Error != "" || cron.writes != 1 {
		t.Fatalf("schedule %+v after %d crontab writes", st.Schedule, cron.writes)
	}

	// A run from a moved folder moves the entry with it.
	moved := newDevice(t)
	moved.ok("collect", "--quiet", "--offline")
	if cron.writes != 2 || !strings.Contains(cron.tab, "--home '"+moved.dir+"'") || strings.Count(cron.tab, "# ai-usage") != 1 {
		t.Fatalf("crontab after a run from another folder (%d writes) = %q", cron.writes, cron.tab)
	}
}

// A person who comments the line out has paused the collector: runs say so
// and do not turn it back on.
func TestCommentedOutScheduleIsLeftAlone(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	paused := "# " + schedule.Line(exe, d.dir, "/usr/bin:/bin") + "\n"
	cron := &fakeCrontab{tab: paused}
	newScheduler = cron.scheduler
	d.ok("collect", "--quiet", "--offline")
	if cron.writes != 0 || cron.tab != paused {
		t.Fatalf("crontab written %d times: %q", cron.writes, cron.tab)
	}
	if st := d.state(); st.Schedule.Registered || !strings.Contains(st.Schedule.Error, "disabled by hand") {
		t.Fatalf("schedule = %+v", st.Schedule)
	}
	if out := d.ok("status"); !strings.Contains(out, "\nschedule      ✕ not registered: disabled by hand") || strings.Contains(out, "register: ai-usage schedule install") {
		t.Fatalf("status:\n%s", out)
	}
	if out := d.ok("schedule", "status"); !strings.Contains(out, "disabled by hand") {
		t.Fatalf("schedule status = %q", out)
	}
	d.ok("schedule", "install")
	if cron.tab != schedule.Line(exe, d.dir, os.Getenv("PATH"))+"\n" {
		t.Fatalf("crontab after install = %q", cron.tab)
	}
}

// One run with the clock far ahead stores a check time in the future. That
// must not stop checks until the clock gets there.
func TestUpdateCheckAfterClockRanAhead(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.ok("collect", "--quiet", "--offline")
	t0 := time.Now().UTC().Truncate(time.Second)
	st := d.state()
	st.Update.CheckedAt = t0.AddDate(5, 0, 0)
	if err := state.Dir(d.dir).SaveState(st); err != nil {
		t.Fatal(err)
	}
	_, downloads, _ := releaseBuild(t, "v1.2.0")
	clock = func() time.Time { return t0 }
	d.ok("collect", "--quiet", "--offline")
	if st := d.state(); !st.Update.CheckedAt.Equal(t0) || st.Update.Installed != "v1.3.0" || *downloads != 1 {
		t.Fatalf("update = %+v after %d downloads", st.Update, *downloads)
	}
}

// A release that cannot read this device's files must still be replaceable
// by the next release: the check runs although the collection failed.
func TestUpdateWhenCollectionFails(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"state.json", "state.json"},
		{"config.json", "config.json"},
		{"team.key", "team key"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			hermetic(t)
			exe, downloads, bin := releaseBuild(t, "v1.2.0")
			d := newDevice(t)
			// A folder where the file should be cannot be read.
			path := filepath.Join(d.dir, tc.file)
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			r := d.run("", "collect", "--quiet", "--offline")
			if r.code != 1 || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("collect: exit %d, stderr %q", r.code, r.stderr)
			}
			if b, _ := os.ReadFile(exe); *downloads != 1 || !bytes.Equal(b, bin) {
				t.Fatalf("release was not installed (%d downloads)", *downloads)
			}
			if unlock, err := state.Dir(d.dir).Lock(); err != nil {
				t.Fatalf("the run lock was left held: %v", err)
			} else {
				unlock()
			}
			if tc.file != "state.json" {
				if st := d.state(); st.Update.Installed != "v1.3.0" || st.Update.CheckedAt.IsZero() {
					t.Fatalf("update = %+v", st.Update)
				}
			}
		})
	}
}

// A bug that panics in a collection, outside any one source, is kept as the
// last error, and the run still looks for the release that fixes it.
func TestPanicIsRecordedAndStillUpdates(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	exe, downloads, bin := releaseBuild(t, "v1.2.0")
	t0 := time.Now().UTC().Truncate(time.Second)
	// The run's first read of the clock panics; the rescue after it reads
	// the clock again.
	reads := 0
	clock = func() time.Time {
		if reads++; reads == 1 {
			panic("clock bug")
		}
		return t0
	}
	r := d.run("", "collect", "--quiet", "--offline")
	if r.code != 1 || !strings.Contains(r.stderr, "collection stopped by a bug: clock bug\n") || !strings.Contains(r.stderr, "goroutine") {
		t.Fatalf("collect: exit %d, stderr %q", r.code, r.stderr)
	}
	st := d.state()
	if st.LastError != "collection stopped by a bug: clock bug" || !st.LastErrorAt.Equal(t0) {
		t.Fatalf("last error %q at %s", st.LastError, st.LastErrorAt)
	}
	if b, _ := os.ReadFile(exe); st.Update.Installed != "v1.3.0" || *downloads != 1 || !bytes.Equal(b, bin) {
		t.Fatalf("update = %+v after %d downloads", st.Update, *downloads)
	}
	if unlock, err := state.Dir(d.dir).Lock(); err != nil {
		t.Fatalf("the run lock was left held: %v", err)
	} else {
		unlock()
	}
	if out := d.ok("status"); !strings.Contains(out, "clock bug") {
		t.Fatalf("status hides the panic:\n%s", out)
	}
}

// A bug that panics while one source is read or probed is that source's
// error. The run finishes, keeps the other sources, and still updates.
func TestPanicInASourceIsItsError(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	d.codex("bbbb", "/work/api")
	d.ok("collect", "--quiet", "--offline")
	exe, downloads, bin := releaseBuild(t, "v1.2.0")
	probeEnv = func() probe.Env {
		env := fakeProbeEnv()
		env.LookPath = func(name string) (string, error) {
			if name == "claude" {
				panic("parser bug")
			}
			return filepath.Join(d.home, "fake-bin", name), nil
		}
		return env
	}
	r := d.run("", "collect", "--quiet", "--offline")
	if r.code != 0 {
		t.Fatalf("collect: exit %d, stderr %q", r.code, r.stderr)
	}
	st := d.state()
	if src := st.Sources["claude"]; src.Status != "error" || src.Error != "stopped by a bug: parser bug" {
		t.Fatalf("claude source = %+v", src)
	}
	if src := st.Sources["codex"]; src.Status != "ok" {
		t.Fatalf("codex source = %+v", src)
	}
	if !strings.Contains(st.LastError, "claude: stopped by a bug: parser bug") {
		t.Fatalf("last error %q", st.LastError)
	}
	if b, _ := os.ReadFile(exe); st.Update.Installed != "v1.3.0" || *downloads != 1 || !bytes.Equal(b, bin) {
		t.Fatalf("update = %+v after %d downloads", st.Update, *downloads)
	}
	if out := d.ok("status"); !strings.Contains(out, "parser bug") {
		t.Fatalf("status hides the panic:\n%s", out)
	}
}
