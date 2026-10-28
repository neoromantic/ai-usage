package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// statsServer stands in for the usage stats endpoint and keeps what it got.
type statsServer struct {
	t      *testing.T
	mu     sync.Mutex
	bodies []string
	// all is every body, kept through take.
	all []string
}

type statsEvent struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
}

// newStatsServer turns usage stats on for the test, sent to a local server,
// with an AI agent running the commands, so the notice prints without a
// terminal.
func newStatsServer(t *testing.T) *statsServer {
	t.Helper()
	s := &statsServer{t: t}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		s.all = append(s.all, string(b))
		s.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	savedEndpoint, savedVersion := statsEndpoint, version
	t.Cleanup(func() { statsEndpoint, version = savedEndpoint, savedVersion })
	statsEndpoint = srv.URL
	// A development build, which never checks for a release on its own.
	version = "v9.9.9-local"
	t.Setenv("AI_USAGE_TELEMETRY", "1")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("AI_AGENT", "")
	return s
}

// events waits a moment for n events and returns them all.
func (s *statsServer) events(n int) []statsEvent {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		bodies := append([]string(nil), s.bodies...)
		s.mu.Unlock()
		if len(bodies) >= n || time.Now().After(deadline) {
			out := make([]statsEvent, len(bodies))
			for i, b := range bodies {
				if err := json.Unmarshal([]byte(b), &out[i]); err != nil {
					s.t.Fatalf("%s: %v", b, err)
				}
			}
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// take returns the events since the last take, after waiting for n.
func (s *statsServer) take(n int) []statsEvent {
	s.t.Helper()
	got := s.events(n)
	s.mu.Lock()
	s.bodies = nil
	s.mu.Unlock()
	return got
}

// names are the events' names, with their feature or error type, sorted:
// each is sent on its own, so they can come in any order.
func names(evs []statsEvent) []string {
	var out []string
	for _, e := range evs {
		n := e.Event
		if f, ok := e.Properties["feature"].(string); ok {
			n += ":" + f
		}
		if f, ok := e.Properties["error_type"].(string); ok {
			n += ":" + f
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// find is the event of that name.
func find(t *testing.T, evs []statsEvent, name string) statsEvent {
	t.Helper()
	for _, e := range evs {
		if e.Event == name {
			return e
		}
	}
	t.Fatalf("no %s in %v", name, names(evs))
	return statsEvent{}
}

// TestUsageStats: nothing is sent before the notice, which prints once; a
// command then sends its start and its name; errors whose text can hold
// what a person typed send their type alone; the app's own runs record
// nothing; telemetry off stops it all.
func TestUsageStats(t *testing.T) {
	hermetic(t)
	s := newStatsServer(t)
	d := newDevice(t)

	r := d.run("", "name", "show")
	if r.code != 0 || !strings.Contains(r.stderr, "ai-usage shares usage stats with its author") || !strings.Contains(r.stderr, "`ai-usage telemetry off`") {
		t.Fatalf("first run: exit %d, stderr %q", r.code, r.stderr)
	}
	if got := s.take(0); len(got) != 0 {
		t.Fatalf("the run that showed the notice sent %v", names(got))
	}

	if r := d.run("", "name", "show"); strings.Contains(r.stderr, "shares usage stats") {
		t.Fatalf("the notice printed twice: %q", r.stderr)
	}
	got := s.take(3)
	if strings.Join(names(got), " ") != "app_installed app_started feature_used:name" {
		t.Fatalf("events %v", names(got))
	}
	e := find(t, got, "feature_used")
	p := e.Properties
	if p["product"] != "ai-usage" || p["$app_version"] != "9.9.9-local-dev" || p["mode"] != "agent" || p["channel"] != "binary" || e.DistinctID == "" {
		t.Errorf("properties %v, id %q", p, e.DistinctID)
	}

	// A usage error repeats what was typed, an account here.
	d.run("", "alias", "ann@acme.dev", "--bogus")
	got = s.take(3)
	if u := find(t, got, "error_occurred").Properties; u["error_type"] != "usage" || u["error_message"] != nil {
		t.Fatalf("usage error %v", u)
	}
	// A failed report sends its text, a report from a file its type alone.
	d.run("", "report")
	if u := find(t, s.take(3), "error_occurred").Properties; !strings.Contains(u["error_message"].(string), "nothing collected yet") {
		t.Fatalf("report's error %v", u)
	}
	d.run("", "report", "--from", filepath.Join(d.home, "ann-team.json"))
	if u := find(t, s.take(3), "error_occurred").Properties; u["error_type"] != "ENOENT" || u["error_message"] != nil {
		t.Fatalf("report --from's error %v", u)
	}

	// The app's reads record nothing; its own events say they are the app's.
	t.Setenv(callerEnv, "app")
	d.ok("report", "--from", filepath.Join("..", "..", "docs", "demo", "solo.json"), "--json")
	d.ok("telemetry", "record", "app_started")
	d.ok("telemetry", "record", "feature_used", "popover")
	got = s.take(2)
	if a := find(t, got, "feature_used").Properties; strings.Join(names(got), " ") != "app_started feature_used:popover" || a["mode"] != "app" || a["channel"] != "app" {
		t.Fatalf("app events %v %v", names(got), got)
	}
	t.Setenv(callerEnv, "")

	// A scheduled run says so.
	d.ok("collect", "--quiet", "--offline", "--home", d.dir)
	got = s.take(2)
	if f := find(t, got, "feature_used").Properties; f["feature"] != "collect" || f["mode"] != "scheduled" {
		t.Fatalf("scheduled run %v %v", names(got), got)
	}

	for _, b := range s.all {
		if strings.Contains(b, "ann@") || strings.Contains(b, "ann-team") {
			t.Errorf("sent %s", b)
		}
	}

	// Off forgets the id. AI_USAGE_TELEMETRY=1 still turns usage stats on.
	if out := d.ok("telemetry", "off"); !strings.Contains(out, "usage stats  on: AI_USAGE_TELEMETRY is set") {
		t.Fatalf("off printed %q", out)
	}
	var st struct {
		ID      *string `json:"id"`
		Enabled *bool   `json:"enabled"`
	}
	b, err := os.ReadFile(filepath.Join(d.dir, "telemetry.json"))
	if err == nil {
		err = json.Unmarshal(b, &st)
	}
	if err != nil || st.ID != nil || st.Enabled == nil || *st.Enabled {
		t.Fatalf("telemetry.json %s, %v", b, err)
	}
}

// TestUsageStatsNotice: the installers' notice prints once, for a person or
// an agent, and marks it seen; the app marks it seen after showing it
// itself, and reads it from the status while it is due.
func TestUsageStatsNotice(t *testing.T) {
	hermetic(t)
	s := newStatsServer(t)
	d := newDevice(t)
	status := func(d *device) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(d.ok("telemetry", "status", "--json")), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := status(d); m["enabled"] != true || m["reason"] != "env" || m["id"] != nil || !strings.Contains(m["notice"].(string), "Nothing has been sent yet") {
		t.Fatalf("status before the notice %v", m)
	}
	// BB's installer runs it too, and shows its output to nobody.
	t.Setenv(callerEnv, "bb")
	if out := d.ok("telemetry", "notice"); out != "" {
		t.Fatalf("notice printed for BB: %q", out)
	}
	t.Setenv(callerEnv, "")
	if out := d.ok("telemetry", "notice"); !strings.HasPrefix(out, "ai-usage shares usage stats") {
		t.Fatalf("notice printed %q", out)
	}
	if out := d.ok("telemetry", "notice"); out != "" {
		t.Fatalf("notice printed again: %q", out)
	}
	if m := status(d); m["notice"] != nil || m["id"] == nil {
		t.Fatalf("status after the notice %v", m)
	}

	e := newDevice(t)
	e.ok("telemetry", "notice", "--seen")
	e.ok("name", "show")
	if got := s.take(3); len(got) != 3 {
		t.Fatalf("after the app showed the notice: %v", names(got))
	}

	// A scheduled run shows nobody the notice, even where an agent's
	// variables or a hidden console look like someone reads it, and sends
	// nothing until someone has seen it.
	f := newDevice(t)
	if r := f.run("", "collect", "--quiet", "--offline", "--home", f.dir); r.code != 0 || strings.Contains(r.stderr, "shares usage stats") {
		t.Fatalf("scheduled run: exit %d, stderr %q", r.code, r.stderr)
	}
	if m := status(f); m["notice"] == nil {
		t.Fatalf("a scheduled run marked the notice seen: %v", m)
	}
	if got := s.take(0); len(got) != 0 {
		t.Fatalf("a scheduled run before the notice sent %v", names(got))
	}
}

// TestUsageStatsOffInTests: a test run sends nothing and writes nothing.
func TestUsageStatsOffInTests(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.ok("name", "show")
	if out := d.ok("telemetry"); !strings.Contains(out, "usage stats  off: in tests") {
		t.Fatalf("status %q", out)
	}
	if _, err := os.Stat(filepath.Join(d.dir, "telemetry.json")); !os.IsNotExist(err) {
		t.Fatalf("telemetry.json: %v", err)
	}
}

func TestInstallChannel(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.env()
	saved := selfExe
	t.Cleanup(func() { selfExe = saved })
	exe := filepath.Join(d.home, ".local", "bin", "ai-usage")
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(d.home, "AppData", "Local"))
		exe = filepath.Join(d.home, "AppData", "Local", "Programs", "ai-usage", "ai-usage.exe")
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	selfExe = func() (string, error) { return exe, nil }
	if c := installChannel(); c != "install-script" {
		t.Errorf("the installer's copy: %s", c)
	}
	selfExe = func() (string, error) { return filepath.Join(d.home, "Downloads", "ai-usage"), nil }
	if c := installChannel(); c != "binary" {
		t.Errorf("a copy elsewhere: %s", c)
	}
	brewPrefix = func() string { return "/opt/homebrew" }
	if c := installChannel(); c != "homebrew" {
		t.Errorf("Homebrew's copy: %s", c)
	}
}

func TestStatsFlags(t *testing.T) {
	for _, c := range []struct {
		args  []string
		quiet bool
		home  string
	}{
		{[]string{"--quiet", "--home", "/s"}, true, "/s"},
		{[]string{"-quiet", "-home=/s"}, true, "/s"},
		{[]string{"--quiet=false", "--json"}, false, ""},
		{[]string{"--", "--quiet"}, false, ""},
	} {
		home, _ := flagValue(c.args, "home")
		if q := hasFlag(c.args, "quiet"); q != c.quiet || home != c.home {
			t.Errorf("%v: quiet %v, home %q", c.args, q, home)
		}
	}
}
