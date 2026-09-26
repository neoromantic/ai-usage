package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/view"
	"github.com/neoromantic/ai-usage/relay"
)

// TestCollectWhileAnotherRunCollects: while the scheduled run holds the run
// lock, a run the person started waits and shows that run's result instead
// of collecting twice, with the guide when it is the first report; the
// scheduler's own run skips, and editing the config does not wait at all.
// After a change to what would be collected, or a holder that did not
// collect, the waiting run collects itself.
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
	if r.code != 0 || !strings.Contains(r.stderr, "collecting now") || !strings.Contains(r.stdout, "\nSUBSCRIPTIONS  ") {
		t.Fatalf("run the person started: %+v", r)
	}
	if !strings.Contains(r.stdout, "\nHOW IT WORKS") || d.state().GuideDue {
		t.Fatalf("the first report, which waited, printed no guide or left it due:\n%s", r.stdout)
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
	if r.SchemaVersion != view.SchemaVersion || r.Collector.Version != "dev" || r.Collector.Device != d.config().Device {
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
	if a.Quota == nil || fullest(a.Quota) != 91 || a.Quota.Source != "cache" {
		t.Fatalf("claude quota = %+v", a.Quota)
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
	if c.Quota == nil || fullest(c.Quota) != 80 || c.Quota.Source != "harness" {
		t.Fatalf("codex quota = %+v", c.Quota)
	}
	if len(r.Team.Devices) != 1 || !r.Team.Devices[0].This {
		t.Fatalf("team devices = %+v", r.Team.Devices)
	}

	text := d.ok("report")
	for _, want := range []string{
		"ai-usage · test-host · team " + r.Collector.Team[:8] + "  ",
		"\nSUBSCRIPTIONS  2 · 2 over\n",
		"\n  /work/app  <1 ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text lacks %q:\n%s", want, text)
		}
	}
	// The bare command collects and prints the same console report.
	if out := d.ok("--offline"); !strings.Contains(out, "\nSUBSCRIPTIONS  2 ") {
		t.Fatalf("bare run printed:\n%s", out)
	}
	var fresh view.Report
	if err := json.Unmarshal([]byte(d.ok("collect", "--offline", "--json")), &fresh); err != nil || fresh.SchemaVersion != view.SchemaVersion {
		t.Fatalf("collect --json: %v %+v", err, fresh.SchemaVersion)
	}

	status := strings.Join(strings.Fields(d.ok("status", "--width", "160")), " ")
	for _, want := range []string{
		"version dev",
		"directory " + d.dir,
		"relay · not configured",
		"schedule ✕ not registered",
		"claude ~/.claude · 1 account",
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
	if err := json.Unmarshal([]byte(d.ok("status", "--json")), &sj); err != nil || sj.SchemaVersion != view.SchemaVersion || len(sj.Sources) != 4 || sj.Sources[0].Provider != "claude" || sj.Sources[0].Status != "ok" {
		t.Fatalf("status --json = %+v, %v", sj, err)
	}

	teamOut := d.ok("team")
	if !strings.Contains(teamOut, "team "+r.Collector.Team) || !strings.Contains(teamOut, "has not been read from the relay") {
		t.Fatalf("team printed:\n%s", teamOut)
	}
}

// A new device prints a short guide under the first report a person reads,
// and never again. It names the scheduler it registered with, and never the
// team key. The scheduler's runs leave it to the first run a person starts.
func TestGuideAfterInstall(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	newScheduler = (&fakeCrontab{}).scheduler
	srv := httptest.NewServer(relay.NewServer(relay.NewMemory()))
	defer srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)

	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	first := d.ok()
	_, guide, ok := strings.Cut(first, "\nHOW IT WORKS")
	if !ok {
		t.Fatalf("the first run printed no guide:\n%s", first)
	}
	for _, want := range []string{"cron", "test-host", "emails", "ai-usage status", "ai-usage name set", "AI_USAGE_TEAM_KEY", "ai-usage schedule remove"} {
		if !strings.Contains(guide, want) {
			t.Fatalf("the guide lacks %q:\n%s", want, guide)
		}
	}
	if key := strings.TrimSpace(d.ok("team", "key")); strings.Contains(first, key) {
		t.Fatal("the guide printed the team key")
	}
	if out := d.ok(); strings.Contains(out, "HOW IT WORKS") {
		t.Fatalf("the second run printed the guide again:\n%s", out)
	}

	s := newDevice(t)
	if out := s.ok("collect", "--quiet"); out != "" {
		t.Fatalf("a scheduled run printed %q", out)
	}
	if out := s.ok("--json"); !json.Valid([]byte(out)) {
		t.Fatalf("--json printed more than JSON:\n%s", out)
	}
	if out := s.ok(); !strings.Contains(out, "\nHOW IT WORKS") {
		t.Fatalf("the first report after scheduled runs printed no guide:\n%s", out)
	}
}

// TestStopAfterSample: a collection stopped once its sample is written
// saves what it collected, with its snapshot pending, but not the stop as a
// relay failure, and it leaves the release check to the next run.
func TestStopAfterSample(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("11111111-aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	version = "v9.9.9"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The collection is stopped while the relay takes its snapshot. The
		// server sees the client go only once the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(cancel)
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)
	if _, err := (collection{d: state.Dir(d.dir)}).run(ctx); err != nil {
		t.Fatal(err)
	}
	st := d.state()
	if !st.Relay.Pending || st.Relay.LastError != "" || st.LastError != "" {
		t.Errorf("relay %+v, last error %q", st.Relay, st.LastError)
	}
	if !reflect.DeepEqual(st.Update, state.Update{}) {
		t.Errorf("update %+v", st.Update)
	}
}
