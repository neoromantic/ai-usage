package probe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	t = t.UTC()
	return &t
}

const fetchedAtMs = 1790000000000 // 2026-09-21T14:13:20Z

func TestClaudeCachedUsage(t *testing.T) {
	at := time.UnixMilli(fetchedAtMs).UTC()
	tests := []struct {
		name string
		body string
		want *Quota
	}{
		{
			name: "limits array",
			body: `{"oauthAccount":{"accountUuid":"acct-1","emailAddress":"x"},
				"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{
				"five_hour":{"utilization":99,"resets_at":"2026-09-22T00:00:00Z"},
				"limits":[
					{"kind":"session","percent":12.5,"resets_at":"2026-09-22T19:50:00.533540+00:00","scope":null,"group":"g","is_active":true,"severity":"ok"},
					{"kind":"weekly_all","percent":40,"resets_at":"2026-09-27T00:00:00.533561+00:00","scope":null},
					{"kind":"weekly_scoped","percent":61,"resets_at":"2026-09-27T00:00:00Z","scope":{"model":{"id":null,"display_name":"Opus 4.1 (1M)"},"surface":null}},
					{"kind":"weekly_scoped","percent":3,"scope":null},
					{"kind":"monthly_spend","percent":5,"scope":{"model":{"display_name":"Sonnet"}}},
					{"kind":"","percent":80},
					{"kind":"weekly_all","percent":null}
				]}}}`,
			want: &Quota{At: at, Source: "cache", Windows: []snapshot.Window{
				{Name: "5h", Percent: 12.5, Minutes: 300, ResetsAt: ts("2026-09-22T19:50:00.53354Z")},
				{Name: "7d", Percent: 40, Minutes: 10080, ResetsAt: ts("2026-09-27T00:00:00.533561Z")},
				{Name: "7d Opus 4.1 (1M)", Percent: 61, Minutes: 10080, ResetsAt: ts("2026-09-27T00:00:00Z")},
				{Name: "7d scoped", Percent: 3, Minutes: 10080},
				{Name: "monthly_spend Sonnet", Percent: 5},
			}},
		},
		{
			name: "fallback keys",
			body: `{"oauthAccount":{"accountUuid":"acct-1"},
				"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{
				"five_hour":{"utilization":30,"resets_at":"2026-09-21T18:00:00+00:00","limit_dollars":null},
				"seven_day":{"utilization":55.5,"resets_at":null},
				"seven_day_opus":null,
				"seven_day_sonnet":{"utilization":null},
				"extra_usage":{"utilization":100}
				}}}`,
			want: &Quota{At: at, Source: "cache", Windows: []snapshot.Window{
				{Name: "5h", Percent: 30, Minutes: 300, ResetsAt: ts("2026-09-21T18:00:00Z")},
				{Name: "7d", Percent: 55.5, Minutes: 10080},
			}},
		},
		{
			name: "limits of another shape fall back",
			body: `{"oauthAccount":{"accountUuid":"acct-1"},
				"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{
				"limits":{"session":1},
				"seven_day_opus":{"utilization":2,"resets_at":"not a time"}}}}`,
			want: &Quota{At: at, Source: "cache", Windows: []snapshot.Window{
				{Name: "7d Opus", Percent: 2, Minutes: 10080},
			}},
		},
		{
			name: "cache of another account",
			body: `{"oauthAccount":{"accountUuid":"acct-2"},"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{"five_hour":{"utilization":30}}}}`,
		},
		{
			name: "nobody logged in",
			body: `{"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{"five_hour":{"utilization":30}}}}`,
		},
		{
			name: "cache without account",
			body: `{"oauthAccount":{"accountUuid":""},"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"utilization":{"five_hour":{"utilization":30}}}}`,
		},
		{
			name: "no cache",
			body: `{"oauthAccount":{"accountUuid":"acct-1"},"numStartups":3}`,
		},
		{
			name: "never fetched",
			body: `{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":0,"accountUuid":"acct-1","utilization":{"five_hour":{"utilization":30}}}}`,
		},
		{
			name: "no windows",
			body: `{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{"limits":[],"spend":{}}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".claude.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := claudeCachedUsage(path)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", describe(got), describe(tc.want))
			}
		})
	}
}

func describe(q *Quota) any {
	if q == nil {
		return nil
	}
	out := []string{q.At.Format(time.RFC3339Nano), q.Source}
	for _, w := range q.Windows {
		r := "-"
		if w.ResetsAt != nil {
			r = w.ResetsAt.Format(time.RFC3339Nano)
		}
		out = append(out, fmt.Sprintf("%s|%g|%d|%s", w.Name, w.Percent, w.Minutes, r))
	}
	return out
}

func TestClaudeCachedUsageErrors(t *testing.T) {
	dir := t.TempDir()
	if q, err := claudeCachedUsage(filepath.Join(dir, "missing.json")); q != nil || err != nil {
		t.Errorf("missing file: %v, %v", q, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"oauthAccount":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if q, err := claudeCachedUsage(bad); q != nil || err == nil {
		t.Errorf("bad JSON: %v, %v", q, err)
	}
	// A directory where the file should be is an error, not "no reading".
	if q, err := claudeCachedUsage(dir); q != nil || err == nil {
		t.Errorf("unreadable: %v, %v", q, err)
	}
}

// A legacy .config.json in the home comes first. TestClaudeDefaultHome,
// TestClaudeDefaultHomeNamedByEnv, and TestClaudeCustomHome read the file
// from each usual place.
func TestClaudeConfigFile(t *testing.T) {
	user := t.TempDir()
	home := filepath.Join(user, ".claude")
	legacy := filepath.Join(home, ".config.json")
	writeFile(t, legacy, "{}")
	for _, configDir := range []string{"", home} {
		if got := claudeConfigFile(user, home, configDir); got != legacy {
			t.Errorf("CLAUDE_CONFIG_DIR=%q: got %q, want %q", configDir, got, legacy)
		}
	}
}

// The same three tests cover the default home, with and without the
// variable, and a custom home.
func TestClaudeConfigDir(t *testing.T) {
	user := filepath.Join(string(filepath.Separator)+"u", "me")
	custom := filepath.Join(user, "work-claude")
	sep := string(filepath.Separator)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, env, home, want string
	}{
		{"custom home keeps the exact string", "CLAUDE_CONFIG_DIR=" + custom + sep, custom, custom + sep},
		{"a relative value becomes the home", "CLAUDE_CONFIG_DIR=rel-claude", filepath.Join(wd, "rel-claude"), filepath.Join(wd, "rel-claude")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{HomeDir: user, Environ: []string{"PATH=/bin", tc.env}}
			if got := env.claudeConfigDir(tc.home); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

const claudeCache = `{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{"limits":[{"kind":"session","percent":12}]}}}`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeDefaultHome(t *testing.T) {
	// A CLAUDE_CONFIG_DIR inherited from the caller would point the CLI at
	// another home, so it is removed for the default one.
	env, record := fakeEnv(t, "claude-ok", "CLAUDE_CONFIG_DIR=/stale")
	home := filepath.Join(env.HomeDir, ".claude")
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
	writeFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":1,"accountUuid":"acct-1","utilization":{"limits":[{"kind":"session","percent":99}]}}}`)

	r, err := Claude(context.Background(), env, home)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if r.Account != "dev@example.com" || r.Plan != "max" {
		t.Errorf("reading = %+v", r)
	}
	if r.Quota == nil || len(r.Quota.Windows) != 1 || r.Quota.Windows[0].Percent != 12 || !r.Quota.At.Equal(time.UnixMilli(fetchedAtMs)) {
		t.Errorf("quota = %+v", describe(r.Quota))
	}
	rec, _ := readRecord(t, record)
	if rec.Name != "claude" || strings.Join(rec.Args, " ") != "auth status --json" {
		t.Errorf("ran %s %q", rec.Name, rec.Args)
	}
	if v, ok := rec.Env["CLAUDE_CONFIG_DIR"]; ok {
		t.Errorf("CLAUDE_CONFIG_DIR=%q reached the default home", v)
	}
	// Not the caller's directory, whose project settings can switch the
	// provider claude reports.
	sameDir(t, rec.Dir, env.HomeDir)
}

// CLAUDE_CONFIG_DIR set to the default home still moves Claude Code's config
// inside it and its login to another keychain entry, so it is kept.
func TestClaudeDefaultHomeNamedByEnv(t *testing.T) {
	env, record := fakeEnv(t, "claude-ok")
	home := filepath.Join(env.HomeDir, ".claude")
	value := home + string(filepath.Separator)
	env.Environ = append(env.Environ, "CLAUDE_CONFIG_DIR="+value)
	writeFile(t, filepath.Join(home, ".claude.json"), claudeCache)
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":1,"accountUuid":"acct-1","utilization":{"limits":[{"kind":"session","percent":99}]}}}`)

	r, err := Claude(context.Background(), env, home)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if r.Quota == nil || len(r.Quota.Windows) != 1 || r.Quota.Windows[0].Percent != 12 {
		t.Errorf("quota = %+v", describe(r.Quota))
	}
	rec, _ := readRecord(t, record)
	if got := rec.Env["CLAUDE_CONFIG_DIR"]; got != value {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", got, value)
	}
}

func TestClaudeCustomHome(t *testing.T) {
	env, record := fakeEnv(t, "claude-ok", "CLAUDE_CONFIG_DIR=/stale")
	home := filepath.Join(env.HomeDir, "work-claude")
	writeFile(t, filepath.Join(home, ".claude.json"), claudeCache)

	r, err := Claude(context.Background(), env, home)
	if err != nil || r.Account != "dev@example.com" || r.Quota == nil {
		t.Fatalf("reading = %+v, %v", r, err)
	}
	rec, _ := readRecord(t, record)
	if got := rec.Env["CLAUDE_CONFIG_DIR"]; got != home {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", got, home)
	}
}

func TestClaudeAnswers(t *testing.T) {
	tests := []struct {
		mode      string
		account   string
		wantErr   string
		wantQuota bool
		loggedOut bool
	}{
		// The cached limits are a claude.ai subscription's, not an API
		// key's or a cloud provider's.
		{mode: "claude-no-email", account: "api_key"},
		{mode: "claude-bedrock", account: "third_party"},
		// wantErr is part of the error, which says why.
		{mode: "claude-logged-out", wantErr: "not logged in", loggedOut: true},
		{mode: "claude-not-json", wantErr: "not JSON"},
		{mode: "claude-fail", wantErr: "exit status 2"},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			env, _ := fakeEnv(t, tc.mode)
			home := filepath.Join(env.HomeDir, ".claude")
			writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
			r, err := Claude(context.Background(), env, home)
			if (err == nil) != (tc.wantErr == "") || !strings.Contains(errText(err), tc.wantErr) {
				t.Errorf("error = %q, want one with %q", errText(err), tc.wantErr)
			}
			if got := saysLoggedOut(err); got != tc.loggedOut {
				t.Errorf("logged out = %v, want %v", got, tc.loggedOut)
			}
			if r.Account != tc.account {
				t.Errorf("account = %q, want %q", r.Account, tc.account)
			}
			// A quota is only kept for an account the CLI named.
			if (r.Quota != nil) != tc.wantQuota {
				t.Errorf("quota = %+v", describe(r.Quota))
			}
		})
	}
}

func TestClaudeBinaryMissing(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), claudeCache)
	env := Env{LookPath: notOnPath, HomeDir: home, Environ: []string{}}
	r, err := Claude(context.Background(), env, filepath.Join(home, ".claude"))
	if !strings.Contains(errText(err), "not found") {
		t.Errorf("error = %v", err)
	}
	if r.Account != "" || r.Quota != nil {
		t.Errorf("reading = %+v", r)
	}
}

func TestClaudeBadCacheKeepsAccount(t *testing.T) {
	env, _ := fakeEnv(t, "claude-ok")
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), `{`)
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"))
	if r.Account != "dev@example.com" || r.Quota != nil || err == nil {
		t.Errorf("reading = %+v, %v", r, err)
	}
}

// A claude that hangs, and leaves a child holding its output, times out.
func TestClaudeTimeout(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	env, record := fakeEnv(t, "claude-orphan", "PROBE_RELEASE="+release)
	env.Timeout = 300 * time.Millisecond
	start := time.Now()
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"))
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("took %v", took)
	}
	if !strings.Contains(errText(err), "in time") || r.Account != "" {
		t.Errorf("reading = %+v, %v", r, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// The child it left behind goes with it.
	_, msgs := readRecord(t, record)
	if len(msgs) == 0 {
		t.Fatal("no child recorded")
	}
	if pid := int(msgs[0]["child_pid"].(float64)); !waitGone(pid) {
		t.Errorf("child %d still running", pid)
	}
}

// A claude.ai subscription whose cache is stale has Claude Code read the
// usage again, as auth status runs, and the reading is what it cached.
func TestClaudeRefreshesUsage(t *testing.T) {
	for _, name := range []string{".claude", "work-claude"} {
		t.Run(name, func(t *testing.T) {
			env, record := fakeEnv(t, "claude-ok", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=0")
			env.Now = time.Now
			home := filepath.Join(env.HomeDir, name)
			file := filepath.Join(home, ".claude.json")
			if name == ".claude" {
				file = filepath.Join(env.HomeDir, ".claude.json")
			}
			writeFile(t, file, claudeCache)
			env.Environ = append(env.Environ, "PROBE_CACHE="+file)
			start := time.Now()

			r, err := Claude(WithLastUse(context.Background(), start), env, home)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if r.Quota == nil || len(r.Quota.Windows) != 1 || r.Quota.Windows[0].Percent != 7 || r.Quota.At.Before(start.Add(-time.Second)) {
				t.Errorf("quota = %+v", describe(r.Quota))
			}
			runs := readRuns(t, record)
			if len(runs) != 2 {
				t.Fatalf("ran %d commands", len(runs))
			}
			auth, usage := runs[0], runs[1]
			want := []string{"-p", "/usage", "--no-session-persistence", "--model", "ai-usage-no-model", "--settings", `{"disableAllHooks":true}`}
			if usage.Name != "claude" || !slices.Equal(usage.Args, want) {
				t.Errorf("ran %s %q", usage.Name, usage.Args)
			}
			dir, set := usage.Env["CLAUDE_CONFIG_DIR"]
			if authDir, authSet := auth.Env["CLAUDE_CONFIG_DIR"]; dir != authDir || set != authSet {
				t.Errorf("CLAUDE_CONFIG_DIR = %q, auth status had %q", dir, authDir)
			}
			if got := usage.Env["DISABLE_AUTOUPDATER"]; got != "1" {
				t.Errorf("DISABLE_AUTOUPDATER = %q", got)
			}
			// With it, Claude Code does not read the usage.
			if v, ok := usage.Env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"]; ok {
				t.Errorf("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=%q reached claude", v)
			}
			if usage.Stdin != "0 bytes" {
				t.Errorf("stdin: %s", usage.Stdin)
			}
			sameDir(t, usage.Dir, env.HomeDir)
		})
	}
}

// Claude Code is not asked to read the usage when its cache is fresh, for a
// login without usage limits, or with nobody logged in. Nor for a home not
// used since its cache, or never, whose login it would keep alive. Nor when
// its config cannot be read, since Claude Code would meet the same file,
// nor in a home another OS user owns, whose files it would take over. A
// skipped read is no problem. The Claude app's agent-mode homes are not probed at all:
// collect's TestClaudeAppSessionsGoToTheirRecordedAccount covers them.
func TestClaudeSkipsUsageRefresh(t *testing.T) {
	fresh := fmt.Sprintf(`{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":%d,"accountUuid":"acct-1","utilization":{"limits":[{"kind":"session","percent":12}]}}}`,
		testNow.Add(-5*time.Minute).UnixMilli())
	tests := []struct {
		name, mode, config string
		othersHome         bool
		// unused is how long before the cache the home was last used, or
		// -1 for never.
		unused time.Duration
	}{
		{name: "fresh cache", mode: "claude-ok", config: fresh},
		{name: "not used since the cache", mode: "claude-ok", config: claudeCache, unused: time.Second},
		{name: "never used", mode: "claude-ok", config: claudeCache, unused: -1},
		{name: "config not JSON", mode: "claude-ok", config: `{`},
		{name: "console login", mode: "claude-console", config: claudeCache},
		{name: "api key", mode: "claude-no-email", config: claudeCache},
		{name: "cloud provider", mode: "claude-bedrock", config: claudeCache},
		{name: "logged out", mode: "claude-logged-out", config: claudeCache},
		{name: "another user's home", mode: "claude-ok", config: claudeCache, othersHome: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.othersHome {
				owned := claudeOwned
				t.Cleanup(func() { claudeOwned = owned })
				claudeOwned = func(string, string) bool { return false }
			}
			env, record := fakeEnv(t, tc.mode)
			writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), tc.config)
			used := testNow
			switch {
			case tc.unused > 0:
				used = time.UnixMilli(fetchedAtMs).Add(-tc.unused)
			case tc.unused < 0:
				used = time.Time{}
			}
			_, err := Claude(WithLastUse(context.Background(), used), env, filepath.Join(env.HomeDir, ".claude"))
			if runs := readRuns(t, record); len(runs) != 1 {
				t.Errorf("ran %d commands, the last %q", len(runs), runs[len(runs)-1].Args)
			}
			if strings.Contains(errText(err), "/usage") {
				t.Errorf("error = %q", errText(err))
			}
		})
	}
}

// Only a home this OS user owns, with its config file or the folder that
// file would be made in, is one Claude Code may be run for.
func TestClaudeOwned(t *testing.T) {
	user := t.TempDir()
	home := filepath.Join(user, "work-claude")
	file := filepath.Join(home, ".claude.json")
	if !claudeOwned(file, home) {
		t.Error("a home not made yet, in a folder this user owns, is not owned")
	}
	writeFile(t, file, "{}")
	if !claudeOwned(file, home) {
		t.Error("a home this user made is not owned")
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	root := string(filepath.Separator)
	if claudeOwned(file, root) || claudeOwned(filepath.Join(root, ".claude.json"), home) {
		t.Error("root's folder is owned")
	}
}

// A read that fails, or leaves no new cache, keeps the reading Claude Code
// cached before, and says why, unless the cache is fresh after all.
func TestClaudeUsageRefreshFails(t *testing.T) {
	tests := []struct {
		usage   string
		wantErr string
		percent float64
	}{
		{usage: "fail", wantErr: "exit status 1", percent: 12},
		// One that sent /usage to a model is too old to read it.
		{usage: "old", wantErr: "update", percent: 12},
		{usage: "write-then-fail", percent: 7},
		// Claude Code exits 0 when it could not reach the usage.
		{usage: "no-write", wantErr: "/usage", percent: 12},
	}
	for _, tc := range tests {
		t.Run(tc.usage, func(t *testing.T) {
			env, _ := fakeEnv(t, "claude-ok", "PROBE_USAGE="+tc.usage)
			env.Now = time.Now
			file := filepath.Join(env.HomeDir, ".claude.json")
			writeFile(t, file, claudeCache)
			env.Environ = append(env.Environ, "PROBE_CACHE="+file)
			r, err := Claude(WithLastUse(context.Background(), time.Now()), env, filepath.Join(env.HomeDir, ".claude"))
			if (err == nil) != (tc.wantErr == "") || !strings.Contains(errText(err), tc.wantErr) || saysLoggedOut(err) {
				t.Errorf("error = %q, want one with %q", errText(err), tc.wantErr)
			}
			if r.Account != "dev@example.com" || r.Quota == nil || r.Quota.Windows[0].Percent != tc.percent {
				t.Errorf("reading = %+v, quota %+v", r, describe(r.Quota))
			}
		})
	}
}

// A read that hangs, and leaves a child holding its output, times out with
// its whole process group, and the cached reading stays.
func TestClaudeUsageRefreshTimeout(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	timeout := claudeUsageTimeout
	t.Cleanup(func() { claudeUsageTimeout = timeout })
	claudeUsageTimeout = 500 * time.Millisecond
	env, record := fakeEnv(t, "claude-ok", "PROBE_USAGE=hang", "PROBE_RELEASE="+release)
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
	start := time.Now()
	r, err := Claude(WithLastUse(context.Background(), testNow), env, filepath.Join(env.HomeDir, ".claude"))
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("took %v", took)
	}
	if !strings.Contains(errText(err), "in time") || r.Quota == nil || r.Quota.Windows[0].Percent != 12 {
		t.Errorf("quota %+v, error %v", describe(r.Quota), err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	_, msgs := readRecord(t, record)
	pid := 0
	for _, m := range msgs {
		if p, ok := m["child_pid"].(float64); ok {
			pid = int(p)
		}
	}
	if pid == 0 {
		t.Fatal("no child recorded")
	}
	if !waitGone(pid) {
		t.Errorf("child %d still running", pid)
	}
}
