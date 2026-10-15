package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// appZip is the menu bar app of release v, zipped as a release has it.
func appZip(t *testing.T, v string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range [][2]string{
		{selfupdate.AppBundle + "/Contents/Info.plist", `<plist><dict><key>CFBundleShortVersionString</key><string>` + strings.TrimPrefix(v, "v") + `</string></dict></plist>`},
		{selfupdate.AppBundle + "/Contents/MacOS/AIUsageBar", "app " + v},
	} {
		fw, err := w.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeMac answers the commands app install runs as a Mac of macOS version
// does, with someone logged in at the screen and no app running, and
// records them.
type fakeMac struct {
	mu      sync.Mutex
	version string
	ran     []string
}

func (m *fakeMac) command(_ context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ran = append(m.ran, name)
	switch name {
	case "sw_vers":
		return []byte(m.version + "\n"), nil
	case "launchctl", "open":
		return nil, nil
	}
	return nil, errors.New("exit status 1")
}

// app install puts the menu bar app of this release in place and opens it,
// on a Mac that runs it, whatever installed ai-usage.
func TestAppInstall(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	if r := d.run("", "app"); r.code != 2 || !helpShown(r.stderr) {
		t.Fatalf("app = %+v", r)
	}
	if runtime.GOOS != "darwin" {
		if r := d.run("", "app", "install"); r.code != 1 || !strings.Contains(r.stderr, "macOS only") {
			t.Fatalf("app install = %+v", r)
		}
		return
	}
	if r := d.run("", "app", "install"); r.code != 1 || !strings.Contains(r.stderr, "development build") {
		t.Fatalf("app install of a dev build = %+v", r)
	}
	version = "v1.3.0"
	g := newFakeGitHub(t, "v1.3.0")
	g.set(func(g *fakeGitHub) { g.app = appZip(t, "v1.3.0") })
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, GitHub: g.URL, HTTP: g.Client(), GOOS: "darwin", App: menuBarApp()}
	}
	mac := &fakeMac{version: "13.6"}
	appCommand = mac.command
	if r := d.run("", "app", "install"); r.code != 1 || !strings.Contains(r.stderr, "needs macOS 14 or later; this Mac has 13.6") {
		t.Fatalf("app install on macOS 13 = %+v", r)
	}

	mac.version = "15.4"
	d.env()
	if !appMissing(context.Background()) {
		t.Fatal("the guide does not offer the app")
	}
	app := filepath.Join(d.home, "Applications", selfupdate.AppBundle)
	out := d.ok("app", "install")
	for _, want := range []string{"installed the menu bar app of v1.3.0 to " + app, "the app is in the menu bar", "run ai-usage first, so the app finds a report"} {
		if !strings.Contains(out, want) {
			t.Fatalf("app install printed no %q:\n%s", want, out)
		}
	}
	if b, err := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "AIUsageBar")); err != nil || string(b) != "app v1.3.0" {
		t.Fatalf("the app = %q, %v", b, err)
	}
	if got := strings.Join(mac.ran, " "); got != "sw_vers sw_vers sw_vers pkill launchctl open" {
		t.Fatalf("ran %s", got)
	}
	if appMissing(context.Background()) {
		t.Fatal("the guide offers an app that is installed")
	}

	d.ok("collect", "--offline")
	if out := d.ok("app", "install"); !strings.HasPrefix(out, "the menu bar app of v1.3.0 is installed\n") || strings.Contains(out, "run ai-usage first") {
		t.Fatalf("app install again printed:\n%s", out)
	}
}
