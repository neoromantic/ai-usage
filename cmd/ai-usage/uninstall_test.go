package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

// uninstallable makes this binary a copy uninstall can remove, as the
// installer would have put in place.
func uninstallable(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "bin", "ai-usage")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := selfExe
	selfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExe = saved })
	return fsutil.RealPath(exe)
}

// Uninstall removes the schedule, the menu bar app, and the binary the
// installer put in place, and keeps the state folder, team key and all.
func TestUninstall(t *testing.T) {
	hermetic(t)
	releaseBuild(t, "v1.2.0")
	cron := &fakeCrontab{tab: "0 3 * * * /usr/local/bin/backup\n"}
	newScheduler = cron.scheduler
	exe := uninstallable(t)
	d := newDevice(t)
	d.ok("collect", "--offline")
	d.ok("schedule", "install")
	key, err := os.ReadFile(filepath.Join(d.dir, "team.key"))
	if err != nil {
		t.Fatal(err)
	}
	var app, agent string
	var ran []string
	appCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(append([]string{name}, args...), " "))
		return nil, errors.New("no such process")
	}
	if runtime.GOOS == "darwin" {
		app, agent = filepath.Join(d.home, "Applications", "AI Usage.app"), filepath.Join(d.home, "Library", "LaunchAgents", appAgent+".plist")
		for _, f := range []string{filepath.Join(app, "Contents", "Info.plist"), agent} {
			if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, []byte("<plist/>"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	out := d.ok("uninstall")
	for _, want := range []string{"removed the schedule from cron", "removed " + exe, "kept the state folder " + d.dir} {
		if !strings.Contains(out, want) {
			t.Errorf("uninstall printed no %q:\n%s", want, out)
		}
	}
	if cron.tab != "0 3 * * * /usr/local/bin/backup\n" {
		t.Errorf("crontab = %q", cron.tab)
	}
	if _, err := os.Stat(exe); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("binary: %v", err)
	}
	if kept, _ := os.ReadFile(filepath.Join(d.dir, "team.key")); string(kept) != string(key) {
		t.Error("the team key changed")
	}
	if runtime.GOOS == "darwin" {
		for _, p := range []string{app, agent} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s: %v", p, err)
			}
		}
		for _, want := range []string{"pkill -x -U", "launchctl bootout gui/", "defaults delete " + appAgent} {
			if !slices.ContainsFunc(ran, func(c string) bool { return strings.HasPrefix(c, want) }) {
				t.Errorf("ran no %q: %q", want, ran)
			}
		}
		if !strings.Contains(out, "removed the menu bar app at "+app) {
			t.Errorf("uninstall printed:\n%s", out)
		}
	}
	// Again, there is nothing left but the state folder.
	if out := d.ok("uninstall"); strings.Contains(out, "menu bar app") {
		t.Errorf("a second uninstall printed:\n%s", out)
	}
}

// --purge drops the device from its team on the relay and removes the
// state folder.
func TestUninstallPurge(t *testing.T) {
	hermetic(t)
	releaseBuild(t, "v1.2.0")
	newScheduler = (&fakeCrontab{}).scheduler
	uninstallable(t)
	srv := httptest.NewServer(relay.NewServer(relay.NewMemory()))
	defer srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)
	d := newDevice(t)
	d.ok("collect", "--json")
	key, err := team.Load(filepath.Join(d.dir, "team.key"))
	if err != nil {
		t.Fatal(err)
	}
	devices := func() int {
		t.Helper()
		pulled, err := (&relay.Client{BaseURL: srv.URL, Key: key}).Pull(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return len(pulled.Devices)
	}
	if n := devices(); n != 1 {
		t.Fatalf("relay has %d devices", n)
	}
	out := d.ok("uninstall", "--purge")
	if !strings.Contains(out, "removed this device from team "+key.Fingerprint()) || !strings.Contains(out, "removed the state folder "+d.dir) {
		t.Fatalf("uninstall --purge printed:\n%s", out)
	}
	if n := devices(); n != 0 {
		t.Fatalf("relay still has %d devices", n)
	}
	if _, err := os.Stat(d.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state folder: %v", err)
	}
}

// A copy Homebrew installed is Homebrew's to remove, and a build from source
// stays where it is.
func TestUninstallLeavesHomebrewAndSource(t *testing.T) {
	hermetic(t)
	newScheduler = (&fakeCrontab{}).scheduler
	exe := uninstallable(t)
	d := newDevice(t)
	if out := d.ok("uninstall"); !strings.Contains(out, "left this development build at "+exe) {
		t.Fatalf("a build from source: %s", out)
	}
	releaseBuild(t, "v1.2.0")
	brewPrefix = func() string { return "/opt/homebrew" }
	if out := d.ok("uninstall"); !strings.Contains(out, "Homebrew installed /opt/homebrew/opt/ai-usage/bin/ai-usage; remove it with brew uninstall neoromantic/tap/ai-usage") {
		t.Fatalf("a Homebrew copy: %s", out)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("binary: %v", err)
	}
}
