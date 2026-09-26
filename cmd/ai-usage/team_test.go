package main

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/view"
	"github.com/neoromantic/ai-usage/relay"
)

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
	if r.code != 0 || !strings.Contains(r.stdout, fp) || strings.Contains(r.stdout, "previous key") {
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
	backup := filepath.Join(c.dir, "team.key.previous")
	if out := c.ok("team", "join", key); !strings.Contains(out, backup) {
		t.Fatalf("join printed %q", out)
	}
	prev, err := team.Load(backup)
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
	b.env()
	pr, pw := io.Pipe()
	go func() { _, _ = io.WriteString(pw, key+"\n") }()
	done := make(chan int, 1)
	go func() { done <- run(context.Background(), []string{"team", "join"}, pr, io.Discard, io.Discard) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("join: exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("team join waited for end of input after the key line")
	}
}

func TestTwoDevicesShareATeam(t *testing.T) {
	hermetic(t)
	srv := httptest.NewServer(relay.NewServer(relay.NewMemory()))
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
	if claude.Quota == nil || fullest(claude.Quota) != 91 {
		t.Fatalf("team claude quota = %+v", claude.Quota)
	}
	if out := a.ok("team"); !strings.Contains(out, b.config().Device) || !strings.Contains(out, "read from the relay") {
		t.Fatalf("team printed:\n%s", out)
	}
	if out := a.ok("status"); !strings.Contains(out, "✓ "+srv.URL) {
		t.Fatalf("status printed:\n%s", out)
	}
	// Two devices have the matrix, and --devices prints the status of each
	// instead.
	if out := a.ok("report"); !strings.Contains(out, "\nDEVICES × SUBSCRIPTIONS  2 · 7d") {
		t.Fatalf("report printed:\n%s", out)
	}
	out := a.ok("report", "--devices", "--width", "120")
	if !strings.Contains(out, "\nDEVICES  2 · by 7d") || strings.Contains(out, "DEVICES × SUBSCRIPTIONS") {
		t.Fatalf("report --devices printed:\n%s", out)
	}

	// Forgetting B takes it off the relay; A's next read has only itself.
	a.ok("team", "forget-device", b.config().Device)
	// The cached team read drops it at once, before the next collection.
	if out := a.ok("team"); strings.Contains(out, b.config().Device) || !strings.Contains(out, a.config().Device) {
		t.Fatalf("team after forget printed:\n%s", out)
	}
	a.ok("collect", "--quiet")
	if r := a.report(); len(r.Team.Devices) != 1 {
		t.Fatalf("team devices after forget = %+v", r.Team.Devices)
	}
}
