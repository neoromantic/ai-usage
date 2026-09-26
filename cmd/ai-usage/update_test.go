package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
)

// fakeGitHub serves the release tag for this platform, laid out as on
// github.com. Its binary is this test binary, which starts and reports the
// version in AIU_FAKE_RELEASE as a release would; newFakeGitHub sets that
// to tag. While busy, the lookup of the latest release fails. The release
// tagged badSum was uploaded broken: its binary does not match its
// checksum. Tests change these with set.
type fakeGitHub struct {
	*httptest.Server
	mu                 sync.Mutex
	tag, badSum        string
	busy               bool
	lookups, downloads int
	bin                []byte
}

func newFakeGitHub(t *testing.T, tag string) *fakeGitHub {
	t.Helper()
	asset := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	t.Setenv("AIU_FAKE_RELEASE", tag)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bin)
	g := &fakeGitHub{tag: tag, bin: bin}
	releases := "/" + selfupdate.Repo + "/releases"
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.URL.Path {
		case releases + "/latest":
			g.lookups++
			if g.busy {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			http.Redirect(w, r, releases+"/tag/"+g.tag, http.StatusFound)
		case releases + "/download/" + g.tag + "/" + asset:
			g.downloads++
			_, _ = w.Write(g.bin)
		case releases + "/download/" + g.tag + "/checksums.txt":
			if g.tag == g.badSum {
				fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), asset)
				return
			}
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// set changes what g serves.
func (g *fakeGitHub) set(change func(*fakeGitHub)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	change(g)
}

// counts is how many times g was asked for the latest release and for the
// binary.
func (g *fakeGitHub) counts() (lookups, downloads int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lookups, g.downloads
}

func TestHousekeepingOnReleaseBuild(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	cron := &fakeCrontab{tab: "0 3 * * * /usr/local/bin/backup\n"}
	newScheduler = cron.scheduler
	exe, g := releaseBuild(t, "v1.2.0")
	binDir := filepath.Dir(exe)
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
		if _, downloads := g.counts(); downloads != 0 {
			t.Fatal("downloaded a binary it cannot install")
		}
		if out := d.ok("status"); !strings.Contains(out, "cannot write beside") {
			t.Fatalf("status hides the update error:\n%s", out)
		}
		if err := os.Chmod(binDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The next scheduled run installs the release for the run after it.
	at(15 * time.Minute)
	d.ok("collect", "--quiet", "--offline")
	st := d.state()
	if st.Update.Installed != "v1.3.0" || st.Update.Error != "" {
		t.Fatalf("update = %+v", st.Update)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, g.bin) {
		t.Fatal("binary was not replaced")
	}
	if out := d.ok("status"); !strings.Contains(out, "v1.3.0 is installed") {
		t.Fatalf("status:\n%s", out)
	}

	// The next run is v1.3.0: nothing is staged any more.
	version = "v1.3.0"
	at(30 * time.Minute)
	d.ok("collect", "--quiet", "--offline")
	if r := d.report(); r.Collector.Update.Staged != nil || r.Collector.Update.Latest == nil || *r.Collector.Update.Latest != "v1.3.0" {
		t.Fatalf("update = %+v", r.Collector.Update)
	}

	// Registration happened once and kept the person's own line.
	if cron.writes != 1 || !strings.HasPrefix(cron.tab, "0 3 * * * /usr/local/bin/backup\n") {
		t.Fatalf("crontab written %d times: %q", cron.writes, cron.tab)
	}

	// After `schedule remove`, runs do not register again.
	d.ok("schedule", "remove")
	writes := cron.writes
	at(45 * time.Minute)
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

	exe, _ := releaseBuild(t, "v1.2.0")
	if out := d.ok("update"); !strings.Contains(out, "v1.3.0") {
		t.Fatalf("update:\n%s", out)
	}
	st := d.state()
	if st.Update.Installed != "v1.3.0" || st.Update.Latest != "v1.3.0" || st.Update.CheckedAt.IsZero() || st.Update.Error != "" {
		t.Fatalf("update = %+v", st.Update)
	}

	// A failed check is recorded too, and keeps the release it last saw.
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, GitHub: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: 5 * time.Second}}
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
func releaseBuild(t *testing.T, running string) (exe string, g *fakeGitHub) {
	t.Helper()
	version = running
	g = newFakeGitHub(t, "v1.3.0")
	exe = filepath.Join(t.TempDir(), "ai-usage")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, GitHub: g.URL, HTTP: g.Client()}
	}
	return exe, g
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
	_, g := releaseBuild(t, "v1.2.0")
	clock = func() time.Time { return t0 }
	d.ok("collect", "--quiet", "--offline")
	_, downloads := g.counts()
	if st := d.state(); !st.Update.CheckedAt.Equal(t0) || st.Update.Installed != "v1.3.0" || downloads != 1 {
		t.Fatalf("update = %+v after %d downloads", st.Update, downloads)
	}
}

// Every scheduled run looks for a release, so one reaches the device within
// a quarter of an hour. A run a person starts less than updateFloor after a
// check, such as the view's r, does not ask GitHub again, even after a failed
// check. The next scheduled run tries a failed check again, so a failure
// shows for one run at most.
func TestUpdateCheckEveryScheduledRun(t *testing.T) {
	hermetic(t)
	_, g := releaseBuild(t, "v1.3.0")
	d := newDevice(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	const scheduled, byHand = true, false
	run := func(at time.Duration, scheduled, fail bool, want int) *state.State {
		t.Helper()
		g.set(func(g *fakeGitHub) { g.busy = fail })
		clock = func() time.Time { return t0.Add(at) }
		if scheduled {
			d.ok("collect", "--quiet", "--offline")
		} else {
			d.ok("collect", "--offline")
		}
		if lookups, _ := g.counts(); lookups != want {
			t.Fatalf("after the run at %s: %d lookups, want %d", at, lookups, want)
		}
		return d.state()
	}
	run(0, scheduled, false, 1)
	run(15*time.Minute, scheduled, false, 2)
	// A run 3 minutes after the last check does not check.
	run(18*time.Minute, byHand, false, 2)
	run(30*time.Minute, scheduled, false, 3)
	if st := run(45*time.Minute, scheduled, true, 4); !strings.Contains(st.Update.Error, "HTTP 503") {
		t.Fatalf("update after a failed check = %+v", st.Update)
	}
	// Runs by hand keep to the floor after a failed check too.
	run(47*time.Minute, byHand, false, 4)
	if st := run(50*time.Minute, byHand, false, 4); !strings.Contains(st.Update.Error, "HTTP 503") {
		t.Fatalf("update after runs by hand = %+v", st.Update)
	}
	run(56*time.Minute, byHand, true, 5)
	// The next scheduled run tries again, although the failed check was 4
	// minutes before it.
	if st := run(60*time.Minute, scheduled, false, 6); st.Update.Error != "" || st.Update.Latest != "v1.3.0" {
		t.Fatalf("update after the check was tried again = %+v", st.Update)
	}
	run(62*time.Minute, byHand, false, 6)
	if _, downloads := g.counts(); downloads != 0 {
		t.Fatalf("the release it runs was downloaded %d times", downloads)
	}
}

// A release that was downloaded and did not install is not downloaded again
// for retryFailed, while every scheduled run still looks up the latest
// release. Its error stays shown. ai-usage update tries it at once, and a
// newer release is installed at the next run.
func TestFailedReleaseIsNotDownloadedAgain(t *testing.T) {
	hermetic(t)
	exe, g := releaseBuild(t, "v1.2.0")
	// v1.3.0 was uploaded broken: its binary does not match.
	g.set(func(g *fakeGitHub) { g.badSum = "v1.3.0" })
	d := newDevice(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	at := func(d time.Duration) { clock = func() time.Time { return t0.Add(d) } }
	run := func(when time.Duration, fail bool, wantLookups, wantDownloads int) *state.State {
		t.Helper()
		g.set(func(g *fakeGitHub) { g.busy = fail })
		at(when)
		d.ok("collect", "--quiet", "--offline")
		if lookups, downloads := g.counts(); lookups != wantLookups || downloads != wantDownloads {
			t.Fatalf("after the run at %s: %d lookups and %d downloads, want %d and %d", when, lookups, downloads, wantLookups, wantDownloads)
		}
		return d.state()
	}
	broken := func(st *state.State) {
		t.Helper()
		if !strings.Contains(st.Update.Error, "checksum") || st.Update.Latest != "v1.3.0" || st.Update.Installed != "" {
			t.Fatalf("update = %+v", st.Update)
		}
	}

	st := run(0, false, 1, 1)
	broken(st)
	if f := st.Update.Failed; f == nil || f.Tag != "v1.3.0" || !f.At.Equal(t0) {
		t.Fatalf("failed release = %+v", f)
	}
	broken(run(15*time.Minute, false, 2, 1))
	// A failed lookup shows its own error, and the release's error comes
	// back once the lookup finds the release again.
	if st := run(30*time.Minute, true, 3, 1); !strings.Contains(st.Update.Error, "HTTP 503") {
		t.Fatalf("update after a failed lookup = %+v", st.Update)
	}
	broken(run(45*time.Minute, false, 4, 1))
	broken(run(retryFailed, false, 5, 2))
	broken(run(retryFailed+15*time.Minute, false, 6, 2))

	at(retryFailed + 20*time.Minute)
	if r := d.run("", "update"); r.code != 1 || !strings.Contains(r.stderr, "checksum") {
		t.Fatalf("update = %+v", r)
	}
	if _, downloads := g.counts(); downloads != 3 {
		t.Fatalf("update downloaded %d times in all", downloads)
	}

	g.set(func(g *fakeGitHub) { g.tag = "v1.3.1" })
	t.Setenv("AIU_FAKE_RELEASE", "v1.3.1")
	st = run(retryFailed+30*time.Minute, false, 8, 4)
	if st.Update.Installed != "v1.3.1" || st.Update.Error != "" || st.Update.Failed != nil {
		t.Fatalf("update after a newer release = %+v", st.Update)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, g.bin) {
		t.Fatal("binary was not replaced")
	}
}

// The menu bar app's error after the binary installed is shown, but the
// release did install: it is not one to skip.
func TestAppErrorIsNotAFailedRelease(t *testing.T) {
	st := &state.State{}
	now := time.Now().UTC()
	noteUpdate(st, now, selfupdate.Result{Latest: "v1.3.0", Installed: true, Downloaded: true}, errors.New("menu bar app: nope"))
	if st.Update.Installed != "v1.3.0" || st.Update.Error != "menu bar app: nope" || st.Update.Failed != nil {
		t.Fatalf("update = %+v", st.Update)
	}
}

// A menu bar app that was downloaded and did not install is, like a
// release, not downloaded again for retryFailed. Its error stays shown, and
// a check that has nothing left to do forgets it.
func TestFailedAppIsNotDownloadedAgain(t *testing.T) {
	hermetic(t)
	releaseBuild(t, "v1.3.0")
	var u *selfupdate.Updater
	made := newUpdater
	newUpdater = func() *selfupdate.Updater {
		u = made()
		return u
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	appErr := fmt.Errorf("%w: rename: permission denied", selfupdate.ErrApp)
	failed := func() *state.State {
		st := &state.State{}
		noteUpdate(st, t0, selfupdate.Result{Latest: "v1.3.0", AppDownloaded: true}, appErr)
		return st
	}
	st := failed()
	if f := st.Update.AppFailed; f == nil || f.Tag != "v1.3.0" || !f.At.Equal(t0) || st.Update.Failed != nil || st.Update.Error != appErr.Error() {
		t.Fatalf("update = %+v", st.Update)
	}
	for _, tc := range []struct {
		after time.Duration
		skip  string
	}{{15 * time.Minute, "v1.3.0"}, {retryFailed, ""}} {
		updateIfDue(context.Background(), failed(), t0.Add(tc.after), true)
		if u.SkipApp != tc.skip || u.Skip != "" {
			t.Fatalf("after %s: skip %q, app %q; want app %q", tc.after, u.Skip, u.SkipApp, tc.skip)
		}
	}
	noteUpdate(st, t0.Add(time.Hour), selfupdate.Result{Latest: "v1.3.0"}, selfupdate.ErrAppSkipped)
	if st.Update.Error != appErr.Error() || st.Update.AppFailed == nil {
		t.Fatalf("update of a skipped app = %+v", st.Update)
	}
	noteUpdate(st, t0.Add(2*time.Hour), selfupdate.Result{Latest: "v1.3.0"}, nil)
	if st.Update.Error != "" || st.Update.AppFailed != nil {
		t.Fatalf("update with nothing left to do = %+v", st.Update)
	}
}

// ai-usage update says what became of the binary before the menu bar app's
// error.
func TestUpdateCommandWithAppError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions differ on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	hermetic(t)
	d := newDevice(t)
	exe, g := releaseBuild(t, "v1.3.0")
	app := filepath.Join(t.TempDir(), "AI Usage.app")
	plist := `<plist><dict><key>CFBundleShortVersionString</key><string>1.2.0</string></dict></plist>`
	if err := os.MkdirAll(filepath.Join(app, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(app), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(app), 0o755) })
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: exe, GitHub: g.URL, HTTP: g.Client(), GOOS: "darwin", App: app}
	}
	r := d.run("", "update")
	if r.code != 1 || !strings.Contains(r.stdout, "up to date (v1.3.0; latest v1.3.0)") || !strings.Contains(r.stderr, "menu bar app: cannot write beside") {
		t.Fatalf("update = %+v", r)
	}
}

// Self-update keeps the app where install.sh puts it, on macOS only.
func TestMenuBarApp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := ""
	if runtime.GOOS == "darwin" {
		want = filepath.Join(home, "Applications", "AI Usage.app")
	}
	if got := menuBarApp(); got != want {
		t.Fatalf("menuBarApp() = %q, want %q", got, want)
	}
}

// A release that cannot read this device's files must still be replaceable
// by the next release: the check runs although the collection failed.
func TestUpdateWhenCollectionFails(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"state.json", "state.json"},
		{"config.json", "config.json"},
		// Read under the run lock, with the state still readable. A key that
		// does not load is the error, not a reason to write a new one.
		{"team.key", "team key"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			hermetic(t)
			exe, g := releaseBuild(t, "v1.2.0")
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
			_, downloads := g.counts()
			if b, _ := os.ReadFile(exe); downloads != 1 || !bytes.Equal(b, g.bin) {
				t.Fatalf("release was not installed (%d downloads)", downloads)
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
	exe, g := releaseBuild(t, "v1.2.0")
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
	if r.code != 1 || !strings.Contains(r.stderr, "clock bug") || !strings.Contains(r.stderr, "goroutine") {
		t.Fatalf("collect: exit %d, stderr %q", r.code, r.stderr)
	}
	// The stack goes to stderr, not into the state.
	st := d.state()
	if !strings.Contains(st.LastError, "clock bug") || strings.Contains(st.LastError, "\n") || !st.LastErrorAt.Equal(t0) {
		t.Fatalf("last error %q at %s", st.LastError, st.LastErrorAt)
	}
	_, downloads := g.counts()
	if b, _ := os.ReadFile(exe); st.Update.Installed != "v1.3.0" || downloads != 1 || !bytes.Equal(b, g.bin) {
		t.Fatalf("update = %+v after %d downloads", st.Update, downloads)
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
