package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func TestCodexChatGPT(t *testing.T) {
	// A CODEX_HOME inherited from the caller would point the server at
	// another home, so it is removed for the default one.
	env, record := fakeEnv(t, "codex-ok", "CODEX_HOME=/stale")
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if r.Account != "dev@example.com" || r.Plan != "pro" {
		t.Errorf("reading = %+v", r)
	}
	want := &Quota{At: testNow, Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 12, Minutes: 300, ResetsAt: unix(1790003600)},
		{Name: "7d", Percent: 40, Minutes: 10080, ResetsAt: unix(1790500000)},
		{Name: "codex_other 1h", Percent: 9, Minutes: 60, ResetsAt: unix(1790000000)},
	}}
	if !reflect.DeepEqual(r.Quota, want) {
		t.Errorf("quota\n got  %v\n want %v", describe(r.Quota), describe(want))
	}

	rec, msgs := readRecord(t, record)
	if rec.Name != "codex" || strings.Join(rec.Args, " ") != "app-server" {
		t.Errorf("ran %s %q", rec.Name, rec.Args)
	}
	if v, ok := rec.Env["CODEX_HOME"]; ok {
		t.Errorf("CODEX_HOME=%q reached the default home", v)
	}
	if got := methods(msgs); !slices.Equal(got, []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}) {
		t.Errorf("conversation = %q", got)
	}
	if _, ok := msgs[1]["id"]; ok {
		t.Error("initialized must be a notification")
	}
	sameDir(t, rec.Dir, env.HomeDir)
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

// app-server gets to finish what it does after its input ends.
func TestCodexExitsByItself(t *testing.T) {
	env, record := fakeEnv(t, "codex-slow-exit")
	if _, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex")); err != nil {
		t.Fatal(err)
	}
	_, msgs := readRecord(t, record)
	if last := msgs[len(msgs)-1]; last["exit"] != "clean" {
		t.Errorf("app-server was killed before it exited; last line %v", last)
	}
}

func TestCodexKilledWhenItLingers(t *testing.T) {
	grace := codexExitGrace
	codexExitGrace = 200 * time.Millisecond
	t.Cleanup(func() { codexExitGrace = grace })
	env, record := fakeEnv(t, "codex-linger")
	start := time.Now()
	if _, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex")); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %v", took)
	}
	rec, _ := readRecord(t, record)
	if runtime.GOOS != "windows" && !processGone(rec.PID) {
		t.Errorf("app-server %d still running", rec.PID)
	}
}

func unix(sec int64) *time.Time {
	t := time.Unix(sec, 0).UTC()
	return &t
}

func TestCodexCustomHome(t *testing.T) {
	env, record := fakeEnv(t, "codex-ok")
	home := filepath.Join(env.HomeDir, "work-codex")
	if _, err := Codex(context.Background(), env, home); err != nil {
		t.Fatal(err)
	}
	rec, _ := readRecord(t, record)
	if got := rec.Env["CODEX_HOME"]; got != home {
		t.Errorf("CODEX_HOME = %q, want %q", got, home)
	}
}

func TestCodexAnswers(t *testing.T) {
	tests := []struct {
		mode      string
		account   string
		plan      string
		wantErr   string
		wantCalls []string
		loggedOut bool
	}{
		{
			mode: "codex-no-email", account: "chatgpt", plan: "plus",
			wantCalls: []string{"initialize", "initialized", "account/read", "account/rateLimits/read"},
		},
		// wantErr is part of the error, which says why.
		{
			mode: "codex-apikey", account: "api key",
			wantErr:   "rate limits need a ChatGPT login",
			wantCalls: []string{"initialize", "initialized", "account/read", "account/rateLimits/read"},
		},
		{
			mode:      "codex-logged-out",
			wantErr:   "not logged in",
			wantCalls: []string{"initialize", "initialized", "account/read"},
			loggedOut: true,
		},
		{
			mode:      "codex-account-error",
			wantErr:   "boom",
			wantCalls: []string{"initialize", "initialized", "account/read"},
		},
		{
			// TestCodexTriesEachBinary has one that says why on its way out.
			mode:      "codex-exit",
			wantErr:   "exited without answering",
			wantCalls: []string{"initialize"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			env, record := fakeEnv(t, tc.mode)
			r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
			if (err == nil) != (tc.wantErr == "") || !strings.Contains(errText(err), tc.wantErr) {
				t.Errorf("error = %q, want one with %q", errText(err), tc.wantErr)
			}
			if got := errors.Is(err, ErrNotLoggedIn); got != tc.loggedOut {
				t.Errorf("logged out = %v, want %v", got, tc.loggedOut)
			}
			if r.Account != tc.account || r.Plan != tc.plan {
				t.Errorf("reading = %+v", r)
			}
			if tc.wantErr != "" && r.Quota != nil {
				t.Errorf("quota = %v", describe(r.Quota))
			}
			_, msgs := readRecord(t, record)
			if got := methods(msgs); !slices.Equal(got, tc.wantCalls) {
				t.Errorf("conversation = %q, want %q", got, tc.wantCalls)
			}
			waitNoGoroutine(t, "probe.(*codexRPC)")
		})
	}
}

func TestCodexTimeoutKillsServer(t *testing.T) {
	env, record := fakeEnv(t, "codex-hang")
	env.Timeout = 300 * time.Millisecond
	start := time.Now()
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	// Two seconds is the WaitDelay a server that survived the kill would cost.
	if took := time.Since(start); took > 1900*time.Millisecond {
		t.Errorf("took %v", took)
	}
	if !strings.Contains(errText(err), "in time") || r.Account != "" {
		t.Errorf("reading = %+v, %v", r, err)
	}
	rec, _ := readRecord(t, record)
	if runtime.GOOS != "windows" && !processGone(rec.PID) {
		t.Errorf("app-server %d still running", rec.PID)
	}
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

func TestCodexBinaryMissing(t *testing.T) {
	env := Env{LookPath: notOnPath, HomeDir: t.TempDir(), Environ: []string{}}
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if !strings.Contains(errText(err), "not found") || r != (Reading{}) {
		t.Errorf("reading = %+v, %v", r, err)
	}
}

// An npm install of codex is a script that runs `env node`, with node beside
// it. The system scheduler's PATH has neither, and app-server still answers.
func TestCodexScriptFindsItsInterpreter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts with #! lines are for Unix")
	}
	dir := filepath.Join(t.TempDir(), "npm", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	node := "#!/bin/sh\nexec \"$PROBE_TESTBIN\" -test.run='^TestHelperProcess$' -- codex \"$2\"\n"
	if err := os.WriteFile(filepath.Join(dir, "fakenode"), []byte(node), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/usr/bin/env fakenode\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env, _ := fakeEnv(t, "codex-ok", "PATH=/usr/bin:/bin", "PROBE_TESTBIN="+os.Args[0])
	env.Command = nil
	env.LookPath = func(string) (string, error) { return filepath.Join(dir, "codex"), nil }
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if err != nil || r.Account != "dev@example.com" {
		t.Fatalf("reading %+v, error %v", r, err)
	}
}

// withRan makes env's harness commands pass their whole path to the fake,
// and lists the paths run.
func withRan(env *Env) *[]string {
	var ran []string
	env.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		ran = append(ran, name)
		argv := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		return exec.CommandContext(ctx, os.Args[0], argv...)
	}
	return &ran
}

// bundle puts an executable at home/rel, modified at mod.
func bundle(t *testing.T, home, rel string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(home, rel)
	writeExe(t, p)
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	chatGPTApp = filepath.Join("Applications", "ChatGPT.app", "Contents", "Resources", exeName("codex"))
	vscodeExt  = filepath.Join(".vscode", "extensions", "openai.chatgpt-26.5.1-darwin-arm64", "bin", "macos-aarch64", exeName("codex"))
)

func TestCodexTriesEachBinary(t *testing.T) {
	tests := []struct {
		name, mode string
		// bundles are under HomeDir, the newest first.
		bundles []string
		// ran is how many of the codex on PATH and the bundles, in that
		// order, were started.
		ran              int
		account, wantErr string
		loggedOut        bool
	}{
		// A codex installed long ago cannot serve; the one the user's apps
		// bundle, the newest first, answers for the same home.
		{name: "old on PATH", mode: "codex-old-on-path", bundles: []string{vscodeExt, chatGPTApp}, ran: 2, account: "dev@example.com"},
		// A codex with app-server but older than account/read turns it down
		// as unknown; a bundled copy that has it answers.
		{name: "no account read on PATH", mode: "codex-no-account-read-on-path", bundles: []string{chatGPTApp}, ran: 2, account: "dev@example.com"},
		// A codex that named the account and then died has served: the account
		// stands, with the reason its quota is missing.
		{name: "exits after the account", mode: "codex-exit-after-account", ran: 1, account: "dev@example.com", wantErr: "exited without answering: no backend"},
		{name: "exits after the account, with a bundle", mode: "codex-exit-after-account", bundles: []string{chatGPTApp}, ran: 1, account: "dev@example.com", wantErr: "exited without answering: no backend"},
		// When none can, the one on PATH says why.
		{name: "none serves", mode: "codex-exit-says", bundles: []string{chatGPTApp}, ran: 2, wantErr: "exited without answering: error: unrecognized subcommand 'app-server'"},
		// A codex that answered, even that nobody is logged in, is the answer:
		// another binary reads the same home.
		{name: "logged out", mode: "codex-logged-out", bundles: []string{chatGPTApp}, ran: 1, wantErr: "not logged in", loggedOut: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := fakeEnv(t, tc.mode)
			ran := withRan(&env)
			bins := []string{filepath.Join("fake", "bin", "codex")}
			for i, rel := range tc.bundles {
				bins = append(bins, bundle(t, env.HomeDir, rel, testNow.Add(-time.Duration(i)*time.Hour)))
			}
			r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
			if want := bins[:tc.ran]; !slices.Equal(*ran, want) {
				t.Errorf("ran %q, want %q", *ran, want)
			}
			if r.Account != tc.account {
				t.Errorf("account = %q, want %q", r.Account, tc.account)
			}
			if (err == nil) != (tc.wantErr == "") || !strings.Contains(errText(err), tc.wantErr) {
				t.Errorf("error = %q, want one with %q", errText(err), tc.wantErr)
			}
			if got := errors.Is(err, ErrNotLoggedIn); got != tc.loggedOut {
				t.Errorf("logged out = %v, want %v", got, tc.loggedOut)
			}
			if (r.Quota != nil) != (tc.wantErr == "") {
				t.Errorf("quota = %v", describe(r.Quota))
			}
		})
	}
}

func TestCodexOnlyBundled(t *testing.T) {
	// Someone who uses only the app has no codex on PATH.
	env, _ := fakeEnv(t, "codex-ok")
	env.LookPath = notOnPath
	ran := withRan(&env)
	apps := t.TempDir()
	env.AppDirs = []string{filepath.Join(apps, "missing"), apps}
	app := bundle(t, apps, filepath.Join("ChatGPT.app", "Contents", "Resources", exeName("codex")), testNow)
	if !env.Find("codex") || env.Find("claude") {
		t.Error("Find disagrees with the bundled codex")
	}
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if err != nil || r.Account != "dev@example.com" || !slices.Equal(*ran, []string{app}) {
		t.Errorf("ran %q: %+v, %v", *ran, r, err)
	}
}

func TestCodexLimits(t *testing.T) {
	window := func(pct float64, mins int, reset int64) string {
		return fmt.Sprintf(`{"usedPercent":%g,"windowDurationMins":%d,"resetsAt":%d}`, pct, mins, reset)
	}
	tests := []struct {
		name    string
		raw     string
		want    []snapshot.Window
		plan    string
		wantErr bool
	}{
		{
			name: "single bucket",
			raw:  `{"rateLimits":{"limitId":null,"planType":"plus","primary":` + window(12, 300, 1790003600) + `,"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":null}}}`,
			want: []snapshot.Window{
				{Name: "5h", Percent: 12, Minutes: 300, ResetsAt: unix(1790003600)},
				{Name: "7d", Percent: 40, Minutes: 10080},
			},
			plan: "plus",
		},
		{
			name: "buckets put codex first, then by id",
			raw: `{"rateLimits":{"primary":` + window(99, 300, 0) + `},"rateLimitsByLimitId":{` +
				`"zeta":{"primary":` + window(3, 60, 0) + `,"planType":"team"},` +
				`"codex":{"limitId":"codex","primary":` + window(1, 300, 0) + `,"secondary":null,"planType":"pro"},` +
				`"alpha":{"limitId":"alpha","secondary":` + window(2, 1440, 0) + `},` +
				`"gone":null}}`,
			want: []snapshot.Window{
				{Name: "5h", Percent: 1, Minutes: 300},
				{Name: "alpha 1d", Percent: 2, Minutes: 1440},
				{Name: "zeta 1h", Percent: 3, Minutes: 60},
			},
			plan: "pro",
		},
		{
			name: "no windows",
			raw:  `{"rateLimits":{"primary":null,"secondary":null,"planType":"free"},"rateLimitsByLimitId":null}`,
			plan: "free",
		},
		{
			name:    "not an object",
			raw:     `[1,2]`,
			wantErr: true,
		},
	}
	now := testNow
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, plan, err := codexLimits(json.RawMessage(tc.raw), now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v", err)
			}
			if plan != tc.plan {
				t.Errorf("plan = %q, want %q", plan, tc.plan)
			}
			var want *Quota
			if tc.want != nil {
				want = &Quota{At: now, Source: "harness", Windows: tc.want}
			}
			if !reflect.DeepEqual(q, want) {
				t.Errorf("got  %v\nwant %v", describe(q), describe(want))
			}
		})
	}
}
