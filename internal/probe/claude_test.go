package probe

import (
	"context"
	"encoding/json"
	"errors"
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
			cfg, err := readClaudeConfig(path)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got := cfg.quota(); !reflect.DeepEqual(got, tc.want) {
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

func TestReadClaudeConfigErrors(t *testing.T) {
	dir := t.TempDir()
	if cfg, err := readClaudeConfig(filepath.Join(dir, "missing.json")); !reflect.DeepEqual(cfg, claudeConfig{}) || err != nil {
		t.Errorf("missing file: %+v, %v", cfg, err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"oauthAccount":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaudeConfig(bad); err == nil {
		t.Error("bad JSON: no error")
	}
	// A directory where the file should be is an error, not "no reading".
	if _, err := readClaudeConfig(dir); err == nil {
		t.Error("unreadable: no error")
	}
}

// A legacy .config.json in the home comes first.
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

func TestClaudeConfigDir(t *testing.T) {
	user := filepath.Join(string(filepath.Separator)+"u", "me")
	def := filepath.Join(user, ".claude")
	custom := filepath.Join(user, "work-claude")
	sep := string(filepath.Separator)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, env, remembered, home, want string
	}{
		{"custom home keeps the exact string", "CLAUDE_CONFIG_DIR=" + custom + sep, "", custom, custom + sep},
		{"a relative value becomes the home", "CLAUDE_CONFIG_DIR=rel-claude", "", filepath.Join(wd, "rel-claude"), filepath.Join(wd, "rel-claude")},
		{"variable names another home", "CLAUDE_CONFIG_DIR=" + custom, def + sep, def, def + sep},
		{"variable unset", "", def + sep, def, def + sep},
		{"nothing remembered at the default home", "", "", def, ""},
		{"variable naming this home beats remembered", "CLAUDE_CONFIG_DIR=" + custom, custom + sep, custom, custom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := Env{HomeDir: user, Environ: []string{"PATH=/bin", tc.env}}
			if got := env.claudeConfigDir(tc.home, tc.remembered); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

const claudeCache = `{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{"limits":[{"kind":"session","percent":12}]}}}`

// claudeCacheAt is claudeCache, fetched at at.
func claudeCacheAt(at time.Time) string {
	return strings.Replace(claudeCache, "1790000000000", fmt.Sprint(at.UnixMilli()), 1)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The config file is inside the home when claude runs with CLAUDE_CONFIG_DIR,
// and beside the default home when it does not.
func TestClaudeHomes(t *testing.T) {
	tests := []struct {
		name, leaf string
		// named is CLAUDE_CONFIG_DIR naming the home in Environ, where it is
		// otherwise another home's.
		named bool
	}{
		// A CLAUDE_CONFIG_DIR inherited from the caller would point the CLI at
		// another home, so it is removed for the default one.
		{"default home", ".claude", false},
		// CLAUDE_CONFIG_DIR set to the default home still moves Claude Code's
		// config inside it and its login to another keychain entry, so it is
		// kept.
		{"default home named by the variable", ".claude", true},
		{"custom home", "work-claude", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, record := fakeEnv(t, "claude-ok")
			home := filepath.Join(env.HomeDir, tc.leaf)
			// configDir is the CLAUDE_CONFIG_DIR claude should get, or "" for
			// none.
			var configDir string
			environ := "CLAUDE_CONFIG_DIR=/stale"
			switch {
			case tc.named:
				configDir = home + string(filepath.Separator)
				environ = "CLAUDE_CONFIG_DIR=" + configDir
			case tc.leaf != ".claude":
				configDir = home
			}
			env.Environ = append(env.Environ, environ)
			cache, decoy := filepath.Join(home, ".claude.json"), filepath.Join(env.HomeDir, ".claude.json")
			if configDir == "" {
				cache, decoy = decoy, cache
			}
			writeFile(t, cache, claudeCache)
			writeFile(t, decoy, `{"oauthAccount":{"accountUuid":"acct-1"},"cachedUsageUtilization":{"fetchedAtMs":1,"accountUuid":"acct-1","utilization":{"limits":[{"kind":"session","percent":99}]}}}`)

			r, err := Claude(context.Background(), env, home, "", time.Time{})
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
			if got, set := rec.Env["CLAUDE_CONFIG_DIR"]; got != configDir || set != (configDir != "") {
				t.Errorf("CLAUDE_CONFIG_DIR = %q (set %v), want %q", got, set, configDir)
			}
			// Not the caller's directory, whose project settings can switch the
			// provider claude reports.
			sameDir(t, rec.Dir, env.HomeDir)
		})
	}
}

func TestClaudeAnswers(t *testing.T) {
	tests := []struct {
		mode      string
		account   string
		wantErr   string
		loggedOut bool
	}{
		// The cached limits are a claude.ai subscription's, not an API
		// key's or a cloud provider's.
		{mode: "claude-no-email", account: "api_key"},
		{mode: "claude-bedrock", account: "third_party"},
		// wantErr is part of the error, which says why.
		{mode: "claude-logged-out", wantErr: "not logged in", loggedOut: true},
		{mode: "claude-not-json", wantErr: "not JSON"},
		{mode: "claude-fail", wantErr: "exit status 2: boom"},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			env, _ := fakeEnv(t, tc.mode)
			home := filepath.Join(env.HomeDir, ".claude")
			writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
			r, err := Claude(context.Background(), env, home, "", time.Time{})
			if (err == nil) != (tc.wantErr == "") || !strings.Contains(errText(err), tc.wantErr) {
				t.Errorf("error = %q, want one with %q", errText(err), tc.wantErr)
			}
			if got := errors.Is(err, ErrNotLoggedIn); got != tc.loggedOut {
				t.Errorf("logged out = %v, want %v", got, tc.loggedOut)
			}
			if r.Account != tc.account {
				t.Errorf("account = %q, want %q", r.Account, tc.account)
			}
			// A quota is only kept for an account the CLI named.
			if r.Quota != nil {
				t.Errorf("quota = %+v", describe(r.Quota))
			}
		})
	}
}

func TestClaudeBinaryMissing(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), claudeCache)
	env := Env{LookPath: notOnPath, HomeDir: home, Environ: []string{}}
	r, err := Claude(context.Background(), env, filepath.Join(home, ".claude"), "", time.Time{})
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
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", time.Time{})
	if r.Account != "dev@example.com" || r.Quota != nil || err == nil {
		t.Errorf("reading = %+v, %v", r, err)
	}
}

// The config's profile of the account gives a Max plan its tier, and says
// who bills it and when it began; another account's profile says nothing.
func TestClaudeSubscription(t *testing.T) {
	profile := func(email, tier, billing string) string {
		return `{"oauthAccount":{"accountUuid":"acct-1","emailAddress":"` + email + `","organizationRateLimitTier":` + tier +
			`,"userRateLimitTier":"default_claude_max_5x","billingType":"` + billing + `","subscriptionCreatedAt":"2025-03-09T10:11:12.123456Z"},` +
			`"cachedUsageUtilization":{"fetchedAtMs":1790000000000,"accountUuid":"acct-1","utilization":{"limits":[{"kind":"session","percent":12}]}}}`
	}
	began := time.Date(2025, 3, 9, 10, 11, 12, 123456000, time.UTC)
	for _, tc := range []struct {
		name, config, plan, billing string
		began                       time.Time
	}{
		{"max 20x", profile("Dev@Example.com", `"default_claude_max_20x"`, "stripe_subscription"), "max 20x", "stripe_subscription", began},
		{"the user's tier when the organization has none", profile("dev@example.com", `null`, "apple_subscription"), "max 5x", "apple_subscription", began},
		{"another tier", profile("dev@example.com", `"default_claude_zero"`, "stripe_subscription"), "max", "stripe_subscription", began},
		{"another account's profile", profile("kim@example.com", `"default_claude_max_20x"`, "apple_subscription"), "max", "", time.Time{}},
		{"no profile", claudeCache, "max", "", time.Time{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := fakeEnv(t, "claude-ok")
			writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), tc.config)
			r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if r.Plan != tc.plan || r.Billing != tc.billing || !r.SubscribedAt.Equal(tc.began) {
				t.Errorf("plan %q, billing %q, began %v", r.Plan, r.Billing, r.SubscribedAt)
			}
		})
	}
}

// A claude that hangs, and leaves a child holding its output, times out.
func TestClaudeTimeout(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	env, record := fakeEnv(t, "claude-orphan", "PROBE_RELEASE="+release)
	env.Timeout = 300 * time.Millisecond
	start := time.Now()
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", time.Time{})
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
// usage again, as auth status runs, and the reading is what it cached. A
// cache from a clock that ran ahead is stale too, and a use since it is not
// needed, as for a cache that is missing.
func TestClaudeRefreshesUsage(t *testing.T) {
	future := claudeCacheAt(time.Now().Add(24 * time.Hour))
	for _, tc := range []struct {
		name, home, cache string
		idle              time.Duration
	}{
		{name: "default home", home: ".claude", cache: claudeCache},
		{name: "another home", home: "work-claude", cache: claudeCache},
		{name: "no cache", home: ".claude", cache: `{"oauthAccount":{"accountUuid":"acct-1"}}`, idle: 50 * time.Minute},
		{name: "cache from the future", home: ".claude", cache: future, idle: time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, record := fakeEnv(t, "claude-ok", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=0")
			env.Now = time.Now
			home := filepath.Join(env.HomeDir, tc.home)
			file := filepath.Join(home, ".claude.json")
			if tc.home == ".claude" {
				file = filepath.Join(env.HomeDir, ".claude.json")
			}
			writeFile(t, file, tc.cache)
			env.Environ = append(env.Environ, "PROBE_CACHE="+file)
			start := time.Now()

			r, err := Claude(context.Background(), env, home, "", start.Add(-tc.idle))
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
// used in the last hour, or not since its cache, whose login it would keep
// alive. Nor when its config names no account, since the cache Claude Code
// writes there is not read, or cannot be read at all, since Claude Code
// would meet the same file. Nor in a home another OS user owns, whose files
// it would take over. A skipped read is no problem. The Claude app's
// agent-mode homes are not probed at all: collect's
// TestClaudeAppSessionsGoToTheirRecordedAccount covers them.
func TestClaudeSkipsUsageRefresh(t *testing.T) {
	cache := func(age time.Duration) string { return claudeCacheAt(testNow.Add(-age)) }
	tests := []struct {
		name, mode, config string
		othersHome         bool
		// idle is how long before now the home was last used, or -1 for
		// never.
		idle time.Duration
	}{
		{name: "fresh cache", mode: "claude-ok", config: cache(5 * time.Minute)},
		{name: "not used since the cache", mode: "claude-ok", config: cache(30 * time.Minute), idle: 40 * time.Minute},
		{name: "never used", mode: "claude-ok", config: claudeCache, idle: -1},
		{name: "no cache, used over an hour ago", mode: "claude-ok", config: `{"oauthAccount":{"accountUuid":"acct-1"}}`, idle: 61 * time.Minute},
		{name: "another account's cache, used 80 days ago", mode: "claude-ok", config: strings.Replace(claudeCache, `"acct-1"}`, `"acct-2"}`, 1), idle: 80 * 24 * time.Hour},
		{name: "cache older than a use over an hour ago", mode: "claude-ok", config: cache(40 * 24 * time.Hour), idle: 30 * 24 * time.Hour},
		// A config reset while the login stays: Claude Code caches the usage
		// without an account.
		{name: "config names no account", mode: "claude-ok", config: `{}`},
		{name: "config not JSON", mode: "claude-ok", config: `{`},
		// Its account has no usage cache, as a subscription's would.
		{name: "console login", mode: "claude-console", config: `{"oauthAccount":{"accountUuid":"acct-1"}}`},
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
			file := filepath.Join(env.HomeDir, ".claude.json")
			writeFile(t, file, tc.config)
			env.Environ = append(env.Environ, "PROBE_CACHE="+file)
			used := testNow.Add(-tc.idle)
			if tc.idle < 0 {
				used = time.Time{}
			}
			_, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", used)
			if runs := readRuns(t, record); len(runs) != 1 {
				t.Errorf("ran %d commands, the last %q", len(runs), runs[len(runs)-1].Args)
			}
			if strings.Contains(errText(err), "/usage") {
				t.Errorf("error = %q", errText(err))
			}
		})
	}
}

// A read that leaves no new reading, as Claude Code's does offline, is made
// again at each run while the home is in use, and not once the home has been
// idle for an hour.
func TestClaudeStopsAskingOnceTheHomeIsIdle(t *testing.T) {
	env, record := fakeEnv(t, "claude-ok", "PROBE_USAGE=offline")
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
	used := testNow
	runs := 0
	for _, tc := range []struct {
		after time.Duration
		ask   bool
	}{
		{15 * time.Minute, true},
		{time.Hour, true},
		{time.Hour + 15*time.Minute, false},
		{59 * 24 * time.Hour, false},
	} {
		now := used.Add(tc.after)
		env.Now = func() time.Time { return now }
		_, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", used)
		n := len(readRuns(t, record))
		if asked := n-runs == 2; asked != tc.ask {
			t.Errorf("%v after the last use: asked %v", tc.after, asked)
		}
		runs = n
		if got := strings.Contains(errText(err), "no new reading"); got != tc.ask {
			t.Errorf("%v after the last use: error = %q", tc.after, errText(err))
		}
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
		{usage: "offline", wantErr: "/usage", percent: 12},
	}
	for _, tc := range tests {
		t.Run(tc.usage, func(t *testing.T) {
			env, _ := fakeEnv(t, "claude-ok", "PROBE_USAGE="+tc.usage)
			env.Now = time.Now
			file := filepath.Join(env.HomeDir, ".claude.json")
			writeFile(t, file, claudeCache)
			env.Environ = append(env.Environ, "PROBE_CACHE="+file)
			r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", time.Now())
			if (err == nil) != (tc.wantErr == "") || !strings.Contains(errText(err), tc.wantErr) || errors.Is(err, ErrNotLoggedIn) {
				t.Errorf("error = %q, want one with %q", errText(err), tc.wantErr)
			}
			if r.Account != "dev@example.com" || r.Quota == nil || r.Quota.Windows[0].Percent != tc.percent {
				t.Errorf("reading = %+v, quota %+v", r, describe(r.Quota))
			}
		})
	}
}

// A read that hangs times out, and the cached reading stays.
func TestClaudeUsageRefreshTimeout(t *testing.T) {
	timeout := claudeUsageTimeout
	t.Cleanup(func() { claudeUsageTimeout = timeout })
	claudeUsageTimeout = 500 * time.Millisecond
	env, _ := fakeEnv(t, "claude-ok", "PROBE_USAGE=hang")
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
	start := time.Now()
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", testNow)
	if took := time.Since(start); took > 8*time.Second {
		t.Errorf("took %v", took)
	}
	if !strings.Contains(errText(err), "in time") || r.Quota == nil || r.Quota.Windows[0].Percent != 12 {
		t.Errorf("quota %+v, error %v", describe(r.Quota), err)
	}
}

// A read that leaves the cache stale says why, from the forms of what each
// Claude Code prints, with the cache it left. What it printed of the usage,
// when windows reset, and what contributes to the usage never reach the
// error, nor does the model catalog's warning, even as its last line. A
// Claude Code too old to read the usage, or that shows it in a form not
// known, is a problem of the quota alone.
func TestClaudeUsageRefreshSaysWhy(t *testing.T) {
	const update = "no new reading: this Claude Code cannot read the usage; update it (cache 1d old)"
	tests := []struct {
		usage, want string
	}{
		{"unknown-skill", update},
		{"unknown-command", update},
		{"unavailable", update},
		{"no-option", update},
		{"old", update},
		{"shows-odd", "no new reading: Claude Code showed the usage but did not cache it; update it (cache 1d old)"},
		{"offline", "no new reading: could not read the usage (cache 1d old)"},
		{"overage-offline", "no new reading: could not read the usage (cache 1d old)"},
		{"cost", "no new reading: Claude Code sees no claude.ai plan (cache 1d old)"},
		{"silent", "no new reading: it printed nothing (cache 1d old)"},
		{"fail", "exit status 1 (cache 1d old): Error: usage is unavailable right now"},
		{"catalog-fail", "exit status 1 (cache 1d old)"},
		{"odd-fail", "exit status 1 (cache 1d old)"},
	}
	for _, tc := range tests {
		t.Run(tc.usage, func(t *testing.T) {
			env, record := fakeEnv(t, "claude-ok", "PROBE_USAGE="+tc.usage)
			writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
			r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", testNow)
			if runs := readRuns(t, record); len(runs) != 2 {
				t.Errorf("ran %d commands", len(runs))
			}
			if got, want := errText(err), "claude /usage: "+tc.want; got != want {
				t.Errorf("error = %q\nwant    %q", got, want)
			}
			if quotaOnly := strings.Contains(tc.want, "update it"); errors.Is(err, ErrNoQuota) != quotaOnly {
				t.Errorf("error %q is ErrNoQuota: %v", errText(err), !quotaOnly)
			}
			for _, secret := range []string{"zebra", "heron", "otter", "walrus", "contributing", "Europe/Berlin", "resets", "%", "catalog"} {
				if strings.Contains(errText(err), secret) {
					t.Errorf("error %q has %q", errText(err), secret)
				}
			}
			if r.Quota == nil || r.Quota.Windows[0].Percent != 12 {
				t.Errorf("quota = %+v", describe(r.Quota))
			}
		})
	}
}

// The cache a read left is told in a few words.
func TestClaudeCacheState(t *testing.T) {
	for _, tc := range []struct{ config, want string }{
		{`{"oauthAccount":{"accountUuid":"acct-1"}}`, "no cache"},
		{strings.Replace(claudeCache, `"acct-1"}`, `"acct-2"}`, 1), "cache of another account"},
		{claudeCacheAt(testNow.Add(-40 * time.Minute)), "cache 40m old"},
		{claudeCacheAt(testNow.Add(-3*time.Hour - 20*time.Minute)), "cache 3h old"},
		{claudeCacheAt(testNow.Add(-90 * 24 * time.Hour)), "cache 90d old"},
		{claudeCacheAt(testNow.Add(time.Hour)), "cache from the future"},
	} {
		var cfg claudeConfig
		if err := json.Unmarshal([]byte(tc.config), &cfg); err != nil {
			t.Fatal(err)
		}
		if got := cfg.cacheState(testNow); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.config, got, tc.want)
		}
	}
}

// link makes a symbolic link, or skips the test on a system that does not
// let this user make one.
func link(t *testing.T, target, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("no symbolic links: %v", err)
	}
}

// Claude Code's version is told by where it is installed, without running it.
func TestClaudeVersion(t *testing.T) {
	pkg := func(name, version string) string {
		return fmt.Sprintf(`{"name":%q,"version":%q,"bin":{"claude":"cli.js"}}`, name, version)
	}
	tests := []struct {
		name string
		// install lays out an install under dir and gives the binary found.
		install func(t *testing.T, dir string) string
		want    string
	}{
		{"native", func(t *testing.T, dir string) string {
			bin := filepath.Join(dir, ".local", "share", "claude", "versions", "9.9.9")
			writeExe(t, bin)
			link(t, bin, filepath.Join(dir, ".local", "bin", "claude"))
			return filepath.Join(dir, ".local", "bin", "claude")
		}, "9.9.9"},
		{"homebrew cask", func(t *testing.T, dir string) string {
			bin := filepath.Join(dir, "Caskroom", "claude-code", "2.1.150", "claude")
			writeExe(t, bin)
			link(t, bin, filepath.Join(dir, "bin", "claude"))
			return filepath.Join(dir, "bin", "claude")
		}, "2.1.150"},
		{"homebrew cask of the latest", func(t *testing.T, dir string) string {
			bin := filepath.Join(dir, "Caskroom", "claude-code@latest", "2.1.283", "claude")
			writeExe(t, bin)
			link(t, bin, filepath.Join(dir, "bin", "claude"))
			return filepath.Join(dir, "bin", "claude")
		}, "2.1.283"},
		// A version manager's shim is its own binary, in a folder named
		// after the manager's version.
		{"volta shim", func(t *testing.T, dir string) string {
			bin := filepath.Join(dir, "Cellar", "volta", "2.0.2", "bin", "volta-shim")
			writeExe(t, bin)
			link(t, bin, filepath.Join(dir, ".volta", "bin", "claude"))
			return filepath.Join(dir, ".volta", "bin", "claude")
		}, ""},
		{"mise shim", func(t *testing.T, dir string) string {
			bin := filepath.Join(dir, "Cellar", "mise", "2025.9.10", "bin", "mise")
			writeExe(t, bin)
			link(t, bin, filepath.Join(dir, ".local", "share", "mise", "shims", "claude"))
			return filepath.Join(dir, ".local", "share", "mise", "shims", "claude")
		}, ""},
		{"another tool's versions", func(t *testing.T, dir string) string {
			writeExe(t, filepath.Join(dir, ".nodenv", "versions", "22.1.0", "bin", "claude"))
			return filepath.Join(dir, ".nodenv", "versions", "22.1.0", "bin", "claude")
		}, ""},
		{"npm", func(t *testing.T, dir string) string {
			// Under a node whose folder is named as a version, too.
			pkgDir := filepath.Join(dir, "node", "22.1.0", "lib", "node_modules", "@anthropic-ai", "claude-code")
			writeFile(t, filepath.Join(pkgDir, "package.json"), pkg("@anthropic-ai/claude-code", "2.1.230"))
			writeExe(t, filepath.Join(pkgDir, "cli.js"))
			link(t, filepath.Join(pkgDir, "cli.js"), filepath.Join(dir, "node", "22.1.0", "bin", "claude"))
			return filepath.Join(dir, "node", "22.1.0", "bin", "claude")
		}, "2.1.230"},
		{"npm on windows", func(t *testing.T, dir string) string {
			writeFile(t, filepath.Join(dir, "npm", "node_modules", "@anthropic-ai", "claude-code", "package.json"), pkg("@anthropic-ai/claude-code", "2.1.99"))
			writeExe(t, filepath.Join(dir, "npm", "claude.cmd"))
			return filepath.Join(dir, "npm", "claude.cmd")
		}, "2.1.99"},
		{"another package", func(t *testing.T, dir string) string {
			pkgDir := filepath.Join(dir, "lib", "node_modules", "claude-wrapper")
			writeFile(t, filepath.Join(pkgDir, "package.json"), pkg("claude-wrapper", "3.0.0"))
			writeExe(t, filepath.Join(pkgDir, "cli.js"))
			return filepath.Join(pkgDir, "cli.js")
		}, ""},
		{"a plain file", func(t *testing.T, dir string) string {
			writeExe(t, filepath.Join(dir, "bin", "claude"))
			return filepath.Join(dir, "bin", "claude")
		}, ""},
		{"missing", func(t *testing.T, dir string) string { return filepath.Join(dir, "nowhere", "claude") }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeVersion(tc.install(t, t.TempDir())); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A Claude Code older than 2.1.193 cannot read the usage, so it is not
// asked, and at every run the error says to update it, naming it, as a
// problem of the quota alone. One since is asked, and its version is told:
// one from 2.1.193 prints the usage it does not cache.
func TestClaudeOldVersionIsNotAsked(t *testing.T) {
	for _, tc := range []struct {
		version, want string
	}{
		{"2.1.192", "update Claude Code to 2.1.208 or newer (~/Caskroom/claude-code/2.1.192/claude is 2.1.192, Homebrew)"},
		{"1.0.128", "update Claude Code to 2.1.208 or newer (~/Caskroom/claude-code/1.0.128/claude is 1.0.128, Homebrew)"},
		{"2.1.193", "claude /usage: no new reading: could not read the usage (Claude Code 2.1.193, cache 1d old)"},
		{"2.1.208", "claude /usage: no new reading: could not read the usage (Claude Code 2.1.208, cache 1d old)"},
		{"9.9.9", "claude /usage: no new reading: could not read the usage (Claude Code 9.9.9, cache 1d old)"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			env, record := fakeEnv(t, "claude-ok", "PROBE_USAGE=offline")
			bin := filepath.Join(env.HomeDir, "Caskroom", "claude-code", tc.version, "claude")
			writeExe(t, bin)
			env.LookPath = func(string) (string, error) { return bin, nil }
			writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
			tooOld := strings.HasPrefix(tc.want, "update")
			for range 2 {
				r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", testNow)
				if errText(err) != tc.want {
					t.Errorf("error = %q\nwant    %q", errText(err), tc.want)
				}
				var nq *NoQuotaError
				if errors.As(err, &nq) != tooOld || errors.Is(err, ErrNoQuota) != tooOld {
					t.Errorf("error %q is a NoQuotaError: %v", errText(err), !tooOld)
				}
				if tooOld && (nq.Path != bin || nq.Version != tc.version || nq.Kind != kindHomebrew) {
					t.Errorf("about %+v", nq)
				}
				if r.Quota == nil || r.Quota.Windows[0].Percent != 12 {
					t.Errorf("quota = %+v", describe(r.Quota))
				}
			}
			usage := 0
			for _, run := range readRuns(t, record) {
				if slices.Contains(run.Args, "/usage") {
					usage++
				}
			}
			if asked := usage > 0; asked == tooOld {
				t.Errorf("asked %d times", usage)
			}
		})
	}
}

// A version manager's shim tells nothing of Claude Code's version, however
// the manager's own folder is named, so Claude Code is asked.
func TestClaudeBehindAShimIsAsked(t *testing.T) {
	env, record := fakeEnv(t, "claude-ok", "PROBE_USAGE=offline")
	shim := filepath.Join(env.HomeDir, "Cellar", "volta", "2.0.2", "bin", "volta-shim")
	writeExe(t, shim)
	bin := filepath.Join(env.HomeDir, ".volta", "bin", "claude")
	link(t, shim, bin)
	env.LookPath = func(string) (string, error) { return bin, nil }
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
	_, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", testNow)
	if want := "claude /usage: no new reading: could not read the usage (cache 1d old)"; errText(err) != want {
		t.Errorf("error = %q\nwant    %q", errText(err), want)
	}
	if runs := readRuns(t, record); len(runs) != 2 {
		t.Errorf("ran %d commands", len(runs))
	}
}

// Each reason fits twice in the 300 bytes a source's error keeps, each after
// its home, so that two homes that fail differently are both told in full,
// with the longest cache state and a version. A line an error printed comes
// last, to be cut first.
func TestClaudeUsageErrorFitsTwice(t *testing.T) {
	budget := (300 - len("~/.claude-work: ; ~/.claude: ")) / 2
	dir := t.TempDir()
	file := filepath.Join(dir, ".claude.json")
	writeFile(t, file, strings.Replace(claudeCache, `"acct-1"}`, `"acct-2"}`, 1))
	cfg, _ := readClaudeConfig(file)
	var msgs []string
	for _, version := range []string{"", "2.1.281"} {
		for _, run := range []claudeRun{
			{timedOut: true},
			{out: []string{"Unknown skill: usage"}},
			{out: []string{"Current session: 7% used"}},
			{out: []string{"Total cost:            $0.0000"}},
			{out: []string{"You are currently using your subscription to power your Claude Code usage"}},
			{out: []string{"You are currently using your overages to power your Claude Code usage. We will automatically switch you back to your subscription rate limits when they reset"}},
			{err: errors.New("exit status 1")},
			{},
			{out: []string{"Something unexpected happened"}},
		} {
			why, _, _ := run.why(version)
			msgs = append(msgs, errText(claudeUsageError(why, version, cfg.cacheState(testNow), "")))
		}
	}
	// A reason Claude Code is not run for, which runs nothing.
	env := Env{HomeDir: dir, Now: func() time.Time { return testNow }}
	writeFile(t, filepath.Join(dir, ".claude", "settings.json"), `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1"}}`)
	current := filepath.Join(dir, "Caskroom", "claude-code", "2.1.281", "claude")
	writeExe(t, current)
	_, _, err := claudeReadUsage(context.Background(), env, current, []candidate{{current, "2.1.281", kindHomebrew}}, "", filepath.Join(dir, ".claude"), file, cfg)
	msgs = append(msgs, errText(err))
	for _, msg := range msgs {
		if !strings.HasPrefix(msg, "claude /usage: ") || !strings.Contains(msg, "cache of another account") {
			t.Errorf("error = %q", msg)
		}
	}
	msgs = append(msgs, "claude /usage: another Claude Code process rewrote ~/.claude-work/.claude.json; reading is 23h old")
	for _, msg := range msgs {
		if len(msg) > budget {
			t.Errorf("error = %q, %d bytes, over %d", msg, len(msg), budget)
		}
	}
	// Nor is one too old, which names itself, as the desktop app's copy does
	// with its long path. It is every home's reason, so it is told once.
	app := "~/Library/Application Support/Claude/claude-code/2.1.192/claude.app/Contents/MacOS/claude"
	_, _, err = claudeReadUsage(context.Background(), env, app, []candidate{{app, "2.1.192", kindApp}}, "", filepath.Join(dir, ".claude"), file, cfg)
	if msg := errText(err); len(msg) > 300-len("~/.claude-work: ") {
		t.Errorf("error = %q, %d bytes", msg, len(msg))
	}
}

// An organization's settings are where Claude Code reads them on each OS.
func TestClaudeManaged(t *testing.T) {
	for goos, want := range map[string]string{
		"darwin":  "/Library/Application Support/ClaudeCode/managed-settings.json",
		"linux":   "/etc/claude-code/managed-settings.json",
		"windows": `C:\Program Files\ClaudeCode\managed-settings.json`,
	} {
		if got := claudeManaged(goos); !slices.Equal(got, []string{want}) {
			t.Errorf("%s: got %q, want %q", goos, got, want)
		}
	}
}

// Claude Code whose settings turn nonessential traffic off does not read the
// usage, whatever its own environment, so it is not asked, and the error
// says so at every run. The settings that win decide, and nothing else in
// them reaches the error. Only the files Claude Code reads count, and only
// the key it reads: "env" exactly, and the variable as the OS names it.
func TestClaudeNoTrafficIsNotAsked(t *testing.T) {
	on := `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1","ANTHROPIC_API_KEY":"sk-fake-secret-7"}}`
	off := `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"","ANTHROPIC_API_KEY":"sk-fake-secret-7"}}`
	windows := runtime.GOOS == "windows"
	tests := []struct {
		name  string
		files map[string]string // relative to the user's home
		home  string
		asked bool
	}{
		{name: "user settings", files: map[string]string{".claude/settings.json": on}, home: ".claude"},
		{name: "project local settings", files: map[string]string{".claude/settings.local.json": on}, home: ".claude"},
		{name: "another home's settings", files: map[string]string{"work-claude/settings.json": on}, home: "work-claude"},
		// Claude Code reads no local settings in its config folder, only in
		// the project's .claude.
		{name: "another home's local settings", files: map[string]string{"work-claude/settings.local.json": on}, home: "work-claude", asked: true},
		{name: "managed settings", files: map[string]string{"managed/managed-settings.json": `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":1}}`}, home: ".claude"},
		{name: "managed over local", files: map[string]string{"managed/managed-settings.json": on, ".claude/settings.local.json": off}, home: ".claude"},
		{name: "managed drop-in", files: map[string]string{"managed/managed-settings.d/50-traffic.json": on}, home: ".claude"},
		{name: "drop-in over managed", files: map[string]string{"managed/managed-settings.json": off, "managed/managed-settings.d/50-traffic.json": on}, home: ".claude"},
		{name: "later drop-in over earlier", files: map[string]string{"managed/managed-settings.d/10-a.json": on, "managed/managed-settings.d/20-b.json": off}, home: ".claude", asked: true},
		{name: "hidden or not JSON drop-ins", files: map[string]string{"managed/managed-settings.d/.50-traffic.json": on, "managed/managed-settings.d/50-traffic.txt": on}, home: ".claude", asked: true},
		{name: "local over user", files: map[string]string{".claude/settings.local.json": off, ".claude/settings.json": on}, home: ".claude", asked: true},
		{name: "null sets nothing", files: map[string]string{".claude/settings.local.json": `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":null}}`, ".claude/settings.json": on}, home: ".claude"},
		{name: "empty", files: map[string]string{".claude/settings.json": off}, home: ".claude", asked: true},
		{name: "other variables", files: map[string]string{".claude/settings.json": `{"env":{"ANTHROPIC_API_KEY":"sk-fake-secret-7"}}`}, home: ".claude", asked: true},
		{name: "not JSON", files: map[string]string{".claude/settings.json": `{"env":`}, home: ".claude", asked: true},
		{name: "env in another case", files: map[string]string{".claude/settings.json": `{"ENV":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1"}}`}, home: ".claude", asked: true},
		// Only Windows ignores the case of a variable's name, and there the
		// last of a name's spellings is set last.
		{name: "variable in another case", files: map[string]string{".claude/settings.json": `{"env":{"claude_code_disable_nonessential_traffic":"1"}}`}, home: ".claude", asked: !windows},
		{name: "variable in two cases", files: map[string]string{".claude/settings.json": `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"","Claude_Code_Disable_Nonessential_Traffic":"1"}}`}, home: ".claude", asked: !windows},
		{name: "variable in two cases, turned on last", files: map[string]string{".claude/settings.json": `{"env":{"Claude_Code_Disable_Nonessential_Traffic":"1","CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":""}}`}, home: ".claude", asked: true},
		{name: "variable twice", files: map[string]string{".claude/settings.json": `{"env":{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":"1","CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC":""}}`}, home: ".claude", asked: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, record := fakeEnv(t, "claude-ok", "PROBE_USAGE=offline")
			env.ClaudeManaged = []string{filepath.Join(env.HomeDir, "managed", "managed-settings.json")}
			for name, body := range tc.files {
				writeFile(t, filepath.Join(env.HomeDir, filepath.FromSlash(name)), body)
			}
			home := filepath.Join(env.HomeDir, tc.home)
			file := filepath.Join(env.HomeDir, ".claude.json")
			if tc.home != ".claude" {
				file = filepath.Join(home, ".claude.json")
			}
			writeFile(t, file, claudeCache)
			for range 2 {
				_, err := Claude(context.Background(), env, home, "", testNow)
				want := "claude /usage: nonessential traffic is off in Claude Code's settings (cache 1d old)"
				if tc.asked {
					want = "claude /usage: no new reading: could not read the usage (cache 1d old)"
				}
				if errText(err) != want || strings.Contains(errText(err), "secret") {
					t.Errorf("error = %q, want %q", errText(err), want)
				}
			}
			if runs := readRuns(t, record); (len(runs) == 4) != tc.asked {
				t.Errorf("ran %d commands", len(runs))
			}
		})
	}
}

// claudeLayout lays out a Claude Code of version under dir, where its kind
// of install keeps it, and returns its binary: a Homebrew cask, or the
// native installer's versions.
func claudeLayout(t *testing.T, dir, kind, version string) string {
	t.Helper()
	switch kind {
	case kindHomebrew:
		bin := filepath.Join(dir, "homebrew", "Caskroom", "claude-code", version, "claude")
		writeExe(t, bin)
		link(t, bin, filepath.Join(dir, "homebrew", "bin", "claude"))
		return filepath.Join(dir, "homebrew", "bin", "claude")
	case kindNative:
		bin := filepath.Join(dir, ".local", "share", "claude", "versions", version)
		writeExe(t, bin)
		return bin
	}
	t.Fatalf("no layout for %s", kind)
	return ""
}

// The usage is read by the newest Claude Code installed, though an old one
// is first on PATH and answers who is logged in.
func TestClaudeReadsWithTheNewestInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the layouts are macOS and Linux installs")
	}
	env, record := fakeEnv(t, "claude-ok")
	env.Now = time.Now
	old := claudeLayout(t, env.HomeDir, kindHomebrew, "2.1.42")
	current := claudeLayout(t, env.HomeDir, kindNative, "2.1.210")
	claudeLayout(t, env.HomeDir, kindNative, "2.1.199")
	env.LookPath = func(string) (string, error) { return old, nil }
	file := filepath.Join(env.HomeDir, ".claude.json")
	writeFile(t, file, claudeCache)
	env.Environ = append(env.Environ, "PROBE_CACHE="+file)
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", time.Now())
	if err != nil || r.Quota == nil || r.Quota.Windows[0].Percent != 7 {
		t.Fatalf("quota %+v, error %v", describe(r.Quota), err)
	}
	runs := readRuns(t, record)
	if len(runs) != 2 || runs[0].Path != old || runs[1].Path != current || !slices.Contains(runs[1].Args, "/usage") {
		t.Errorf("ran %+v", runs)
	}
}

// With only a Claude Code too old to read the usage, and that one not on
// PATH, nothing reads it, and the error names that install for collect to
// compare with the version the sessions ran.
func TestClaudeOnlyAnOldInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the layout is a macOS and Linux install")
	}
	env, record := fakeEnv(t, "claude-ok")
	env.LookPath = notOnPath
	dir := t.TempDir()
	bin := claudeLayout(t, dir, kindHomebrew, "2.1.42")
	env.SystemBinDirs = []string{filepath.Dir(bin)}
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", testNow)
	var nq *NoQuotaError
	if !errors.Is(err, ErrNoQuota) || !errors.As(err, &nq) {
		t.Fatalf("error = %#v", err)
	}
	if nq.Path != bin || nq.Version != "2.1.42" || nq.Kind != "Homebrew" {
		t.Errorf("about %+v", nq)
	}
	if want := "update Claude Code to 2.1.208 or newer (" + bin + " is 2.1.42, Homebrew)"; errText(err) != want {
		t.Errorf("error = %q\nwant    %q", errText(err), want)
	}
	if r.Account != "dev@example.com" || r.Quota == nil || r.Quota.Windows[0].Percent != 12 {
		t.Errorf("reading %+v, quota %+v", r, describe(r.Quota))
	}
	if runs := readRuns(t, record); len(runs) != 1 || runs[0].Path != bin {
		t.Errorf("ran %+v", runs)
	}
}

// When auth status fails without saying nobody is logged in, the usage
// Claude Code cached for the account its config names is still the reading,
// and that account the one logged in. A config that names no email, or whose
// cache is another account's, gives none, and the error says what auth
// status printed.
func TestClaudeAuthFailsKeepsTheCache(t *testing.T) {
	named := strings.Replace(claudeCache, `{"accountUuid":"acct-1"}`, `{"accountUuid":"acct-1","emailAddress":" ann@acme.dev "}`, 1)
	for _, tc := range []struct {
		name, config, account string
	}{
		{"cache of the account", named, "ann@acme.dev"},
		{"no email", claudeCache, ""},
		{"another account's cache", strings.Replace(named, `"accountUuid":"acct-1","utilization"`, `"accountUuid":"acct-2","utilization"`, 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, record := fakeEnv(t, "claude-auth-fails")
			writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), tc.config)
			r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", testNow)
			if r.Account != tc.account {
				t.Errorf("account = %q", r.Account)
			}
			if tc.account == "" {
				if want := "claude auth status: exit status 1: Error: keychain is locked"; errText(err) != want || r.Quota != nil {
					t.Errorf("error = %q, quota %+v", errText(err), describe(r.Quota))
				}
				return
			}
			if err != nil || r.Quota == nil || r.Quota.Windows[0].Percent != 12 {
				t.Errorf("quota %+v, error %v", describe(r.Quota), err)
			}
			if runs := readRuns(t, record); len(runs) != 1 {
				t.Errorf("ran %d commands", len(runs))
			}
		})
	}
}

// A login whose subscription type auth status does not tell is still a
// subscription when its account has a usage cache, and the usage is read.
func TestClaudeSubscriptionTypeNotTold(t *testing.T) {
	env, record := fakeEnv(t, "claude-console")
	env.Now = time.Now
	file := filepath.Join(env.HomeDir, ".claude.json")
	writeFile(t, file, claudeCache)
	env.Environ = append(env.Environ, "PROBE_CACHE="+file)
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", time.Now())
	if err != nil || r.Quota == nil || r.Quota.Windows[0].Percent != 7 {
		t.Errorf("quota %+v, error %v", describe(r.Quota), err)
	}
	if runs := readRuns(t, record); len(runs) != 2 {
		t.Errorf("ran %d commands", len(runs))
	}
}

// Claude Code from 2.1.193 to 2.1.207 prints the usage without caching it,
// and what it printed is the reading, as of the read.
func TestClaudePrintedUsageIsTheReading(t *testing.T) {
	env, record := fakeEnv(t, "claude-ok", "PROBE_USAGE=shows")
	bin := filepath.Join(env.HomeDir, "Caskroom", "claude-code", "2.1.200", "claude")
	writeExe(t, bin)
	env.LookPath = func(string) (string, error) { return bin, nil }
	writeFile(t, filepath.Join(env.HomeDir, ".claude.json"), claudeCache)
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", testNow)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// The weekly reset is further than a week ahead of the test's clock.
	want := &Quota{At: testNow, Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 7, Minutes: 300, ResetsAt: ts("2026-09-23T13:40:00Z")},
		{Name: "7d", Percent: 40, Minutes: 10080},
	}}
	if !reflect.DeepEqual(r.Quota, want) {
		t.Errorf("quota\n got  %v\n want %v", describe(r.Quota), describe(want))
	}
	if runs := readRuns(t, record); len(runs) != 2 {
		t.Errorf("ran %d commands", len(runs))
	}
}

// Each window /usage prints is read with its reset, in the time zone it
// names, and the year or day it leaves out is the next to come.
func TestClaudePrintedQuota(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	lines := []string{
		"You are currently using your subscription to power your Claude Code usage",
		"Current session: 47% used · resets Sep 29 at 3pm (Europe/Madrid)",
		"Current week (all models): 80% used · resets Sep 30 at 5:30pm (Europe/Madrid)",
		"Current week (Opus): 1.5% used · resets Oct 1, 9am (Europe/Madrid)",
		"Current week (Sonnet): 0% used",
		"Current week (Haiku): 2% used · resets whenever (Europe/Madrid)",
		"Current week (Fable): 3% used · resets Oct 1, 9am (Nowhere/Nothing)",
		"Current month: 5% used · resets Oct 31 at 9am (Europe/Madrid)",
		"Last 24h · 21190 requests · 7 sessions",
	}
	want := &Quota{At: now, Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 47, Minutes: 300, ResetsAt: ts("2026-09-29T13:00:00Z")},
		{Name: "7d", Percent: 80, Minutes: 10080, ResetsAt: ts("2026-09-30T15:30:00Z")},
		{Name: "7d Opus", Percent: 1.5, Minutes: 10080, ResetsAt: ts("2026-10-01T07:00:00Z")},
		{Name: "7d Sonnet", Percent: 0, Minutes: 10080},
		{Name: "7d Haiku", Percent: 2, Minutes: 10080},
		{Name: "7d Fable", Percent: 3, Minutes: 10080},
	}}
	if got := claudePrintedQuota(lines, now); !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", describe(got), describe(want))
	}
	if got := claudePrintedQuota([]string{"Current session: a lot used"}, now); got != nil {
		t.Errorf("got %v", describe(got))
	}
	for _, tc := range []struct {
		when, zone string
		now        time.Time
		minutes    int
		want       *time.Time
	}{
		{"11:40pm", "Asia/Tokyo", now, 300, ts("2026-09-29T14:40:00Z")},
		// A time of day already past today is tomorrow's, here too far ahead.
		{"3:40pm", "Asia/Tokyo", now, 300, nil},
		{"Jan 2 at 9am", "UTC", time.Date(2026, 12, 31, 20, 0, 0, 0, time.UTC), 10080, ts("2027-01-02T09:00:00Z")},
		{"Sep 29 at 9am", "UTC", now, 300, ts("2026-09-29T09:00:00Z")},
		{"Sep 28 at 9am", "UTC", now, 300, nil},
		{"3pm", "", now, 300, nil},
	} {
		if got := claudeResetAt(tc.when, tc.zone, tc.now, tc.minutes); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s (%s): got %v, want %v", tc.when, tc.zone, got, tc.want)
		}
	}
}

// Another Claude Code that saves its config as it had it hours ago takes
// the cache back in time. The reading that was there stands, and the error
// says so, as a problem of the quota alone.
func TestClaudeCacheTakenBack(t *testing.T) {
	env, _ := fakeEnv(t, "claude-ok", "PROBE_USAGE=regress")
	env.Now = time.Now
	file := filepath.Join(env.HomeDir, ".claude.json")
	writeFile(t, file, claudeCacheAt(time.Now().Add(-20*time.Minute)))
	env.Environ = append(env.Environ, "PROBE_CACHE="+file)
	r, err := Claude(context.Background(), env, filepath.Join(env.HomeDir, ".claude"), "", time.Now())
	if want := "claude /usage: another Claude Code process rewrote ~/.claude.json; reading is 20m old"; errText(err) != want {
		t.Errorf("error = %q\nwant    %q", errText(err), want)
	}
	if !errors.Is(err, ErrNoQuota) {
		t.Error("not ErrNoQuota")
	}
	if r.Quota == nil || r.Quota.Windows[0].Percent != 12 {
		t.Errorf("quota = %+v", describe(r.Quota))
	}
}
