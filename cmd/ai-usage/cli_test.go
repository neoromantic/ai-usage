package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/view"
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
	// With these set, relay serve would keep its records in a real KV store.
	for _, k := range []string{"KV_REST_API_URL", "KV_REST_API_TOKEN"} {
		t.Setenv(k, "")
	}
	// Orca's app data folder moves with these.
	t.Setenv("APPDATA", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	// The console reads these; a test sees the same output everywhere.
	for _, k := range []string{"COLUMNS", "NO_COLOR", "TERM", "LC_CTYPE", "LANG", "WT_SESSION", "TERM_PROGRAM",
		"COLORTERM", "CLICOLOR", "CLICOLOR_FORCE", "TTY_FORCE", "COLORFGBG"} {
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
		collectOnce           func(context.Context, string, string, io.Writer) error
		sleepCtx              func(context.Context, time.Duration) bool
	}{version, defaultRelay, clock, probeEnv, newScheduler, newUpdater, hostname, osUser, collectOnce, sleepCtx}
	t.Cleanup(func() {
		version, defaultRelay, clock = saved.version, saved.defaultRelay, saved.clock
		probeEnv, newScheduler, newUpdater = saved.probeEnv, saved.newScheduler, saved.newUpdater
		hostname, osUser = saved.hostname, saved.osUser
		collectOnce, sleepCtx = saved.collectOnce, saved.sleepCtx
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
		return &selfupdate.Updater{Current: version, GitHub: guard.URL, HTTP: guard.Client(), Exe: filepath.Join(t.TempDir(), "ai-usage")}
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
	case `claude -p /usage --no-session-persistence --model ai-usage-no-model --settings {"disableAllHooks":true}`:
		// It leaves the usage each test caches as it is.
		fmt.Println("Current session: 42% used")
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

// env points the environment at this device.
func (d *device) env() {
	d.t.Helper()
	d.t.Setenv("AI_USAGE_HOME", d.dir)
	d.t.Setenv("HOME", d.home)
	d.t.Setenv("USERPROFILE", d.home)
}

// run calls the CLI in-process as this device.
func (d *device) run(stdin string, args ...string) result {
	d.t.Helper()
	d.env()
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

func fingerprint(t *testing.T, d *device) string {
	t.Helper()
	line, _, _ := strings.Cut(d.ok("team"), "\n")
	fp, ok := strings.CutPrefix(line, "team ")
	if !ok || !snapshot.ValidTeam(fp) {
		t.Fatalf("team printed %q", line)
	}
	return fp
}

// fullest is the percent of a quota's fullest window.
func fullest(q *view.Quota) float64 {
	var p float64
	for _, w := range q.Windows {
		p = max(p, w.Percent)
	}
	return p
}
