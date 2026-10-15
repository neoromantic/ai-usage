package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
)

// fakeBrew is a Homebrew prefix with ai-usage installed: a keg, its opt
// link, and a brew that upgrades the keg to the release in the file tap,
// from the bottles it was given. `brew update` takes the release in
// tap.next, as a tap that brew reads again. Each binary reports its
// release, and records the update it is asked to finish in after.args.
// While the file fail exists, brew fails with what it says, and while
// fail.update exists, so does `brew update`.
type fakeBrew struct {
	t      *testing.T
	prefix string
}

const fakeBrewScript = `#!/bin/sh
PATH=/usr/bin:/bin
p=$(cd "$(dirname "$0")/.." && pwd)
echo "$*" >> "$p/brew.log"
if [ -f "$p/fail" ]; then
	echo "==> Upgrading 1 outdated package"
	echo "Error: $(cat "$p/fail")" >&2
	exit 1
fi
case "$*" in
"update --quiet")
	if [ -f "$p/fail.update" ]; then
		echo "Error: $(cat "$p/fail.update")" >&2
		exit 1
	fi
	if [ -f "$p/tap.next" ]; then mv "$p/tap.next" "$p/tap"; fi
	exit 0 ;;
"upgrade neoromantic/tap/ai-usage") ;;
*) echo "Error: unexpected brew $*" >&2; exit 2 ;;
esac
v=$(cat "$p/tap")
if [ -d "$p/Cellar/ai-usage/$v" ]; then
	echo "Warning: ai-usage $v already installed" >&2
	exit 0
fi
old=$(readlink "$p/opt/ai-usage")
mkdir -p "$p/Cellar/ai-usage/$v/bin"
cp "$p/bottles/$v" "$p/Cellar/ai-usage/$v/bin/ai-usage"
rm "$p/opt/ai-usage"
ln -s "../Cellar/ai-usage/$v" "$p/opt/ai-usage"
rm -rf "$p/opt/$old"
echo "==> Upgrading ai-usage $v"
`

// newFakeBrew installs release running in a new prefix, with bottles of the
// other releases brew may upgrade to.
func newFakeBrew(t *testing.T, running string, bottles ...string) *fakeBrew {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Homebrew does not run on Windows")
	}
	b := &fakeBrew{t: t, prefix: t.TempDir()}
	cur := strings.TrimPrefix(running, "v")
	for _, v := range append(bottles, cur) {
		b.set(filepath.Join("bottles", v), fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nversion) echo v%s ;;\n"+
			"update) echo \"$*\" > %q; echo \"installed v%s with Homebrew; the next run uses it\" ;;\nesac\n",
			v, filepath.Join(b.prefix, "after.args"), v))
	}
	b.set(filepath.Join("Cellar", "ai-usage", cur, "bin", "ai-usage"), b.read(filepath.Join("bottles", cur)))
	b.set(filepath.Join("bin", "brew"), fakeBrewScript)
	b.set("tap", cur)
	if err := os.MkdirAll(filepath.Join(b.prefix, "opt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/ai-usage/"+cur, filepath.Join(b.prefix, "opt", "ai-usage")); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *fakeBrew) set(rel, body string) {
	b.t.Helper()
	path := filepath.Join(b.prefix, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		b.t.Fatal(err)
	}
}

func (b *fakeBrew) read(rel string) string {
	b.t.Helper()
	body, _ := os.ReadFile(filepath.Join(b.prefix, rel))
	return string(body)
}

// homebrewBuild makes this release build a copy Homebrew installed, whose
// release checks go to a fake v1.3.0 release.
func homebrewBuild(t *testing.T, running string, bottles ...string) (*fakeBrew, *fakeGitHub) {
	t.Helper()
	b := newFakeBrew(t, running, bottles...)
	version = running
	g := newFakeGitHub(t, "v1.3.0")
	keg := filepath.Join(b.prefix, "Cellar", "ai-usage", strings.TrimPrefix(running, "v"), "bin", "ai-usage")
	brewPrefix = func() string { return b.prefix }
	newUpdater = func() *selfupdate.Updater {
		return &selfupdate.Updater{Current: version, Exe: keg, GitHub: g.URL, HTTP: g.Client(),
			Managed: selfupdate.Homebrew, Stable: brewOpt(b.prefix)}
	}
	return b, g
}

// `ai-usage update` of a Homebrew copy has brew upgrade it and the new
// binary finish, without the run lock, which a scheduled run may hold.
func TestUpdateWithHomebrew(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	b, g := homebrewBuild(t, "v1.2.0", "1.3.0")
	b.set("tap", "1.3.0")
	unlock, err := state.Dir(d.dir).Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	r := d.run("", "update")
	if r.code != 0 || r.stdout != "installed v1.3.0 with Homebrew; the next run uses it\n" || !strings.Contains(r.stderr, "==> Upgrading ai-usage 1.3.0") {
		t.Fatalf("update = %+v", r)
	}
	if got := b.read("after.args"); got != "update --after-brew v1.2.0\n" {
		t.Fatalf("the new binary was asked %q", got)
	}
	if got := b.read("brew.log"); got != "upgrade "+fsutil.HomebrewTap+"\n" {
		t.Fatalf("brew ran %q", got)
	}
	if _, downloads := g.counts(); downloads != 0 {
		t.Fatal("downloaded a release Homebrew installs")
	}
}

// A tap brew read before the release reached it is read again, once. Until
// the release reaches it, update says so and fails, never "up to date".
func TestUpdateWithHomebrewBehindGitHub(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	b, _ := homebrewBuild(t, "v1.2.0", "1.3.0")
	b.set("tap.next", "1.3.0")
	if r := d.run("", "update"); r.code != 0 || !strings.Contains(r.stdout, "installed v1.3.0 with Homebrew") {
		t.Fatalf("update = %+v", r)
	}
	if got, want := b.read("brew.log"), "upgrade "+fsutil.HomebrewTap+"\nupdate --quiet\nupgrade "+fsutil.HomebrewTap+"\n"; got != want {
		t.Fatalf("brew ran %q, want %q", got, want)
	}

	b, _ = homebrewBuild(t, "v1.2.0", "1.3.0")
	r := d.run("", "update")
	if r.code != 1 || r.stdout != "Homebrew's formula is at v1.2.0; GitHub has v1.3.0, which reaches Homebrew soon\n" || strings.Contains(r.stderr, "ai-usage: ") {
		t.Fatalf("update = %+v", r)
	}
	if b.read("after.args") != "" {
		t.Fatal("the same binary was asked to finish")
	}

	// A tap that holds a release between this one and GitHub's latest still
	// has that release finish the update, which then fails.
	b, _ = homebrewBuild(t, "v1.2.0", "1.2.5")
	b.set("tap", "1.2.5")
	r = d.run("", "update")
	if want := "installed v1.2.5 with Homebrew; the next run uses it\nHomebrew's formula is at v1.2.5; GitHub has v1.3.0, which reaches Homebrew soon\n"; r.code != 1 || r.stdout != want {
		t.Fatalf("update = %+v", r)
	}
	if got := b.read("after.args"); got != "update --after-brew v1.2.0\n" {
		t.Fatalf("the new binary was asked %q", got)
	}

	// So does one whose second try fails.
	b, _ = homebrewBuild(t, "v1.2.0", "1.2.5")
	b.set("tap", "1.2.5")
	b.set("fail.update", "Another active Homebrew update process is already in progress.")
	r = d.run("", "update")
	if r.code != 1 || !strings.Contains(r.stdout, "installed v1.2.5 with Homebrew") || !strings.HasSuffix(r.stderr, "ai-usage: brew update failed: Another active Homebrew update process is already in progress.\n") {
		t.Fatalf("update = %+v", r)
	}
	if got := b.read("after.args"); got != "update --after-brew v1.2.0\n" {
		t.Fatalf("the new binary was asked %q", got)
	}
}

// brew's failure is the update's, in brew's last words, and an up-to-date
// copy says so.
func TestUpdateWithHomebrewFailsOrHasNothingToDo(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	b, _ := homebrewBuild(t, "v1.2.0", "1.3.0")
	b.set("fail", "Another active Homebrew update process is already in progress.")
	r := d.run("", "update")
	if r.code != 1 || !strings.HasSuffix(r.stderr, "ai-usage: brew upgrade failed: Another active Homebrew update process is already in progress.\n") {
		t.Fatalf("update = %+v", r)
	}

	homebrewBuild(t, "v1.3.0")
	if r := d.run("", "update"); r.code != 0 || r.stdout != "up to date (v1.3.0)\n" {
		t.Fatalf("update = %+v", r)
	}
}

// The binary brew installed finishes the update: it checks, under the run
// lock, and has launchd start the agent, whose binary brew replaced.
func TestAfterBrew(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	homebrewBuild(t, "v1.3.0")
	agent := newFakeAgent(t)
	newScheduler = agent.scheduler
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	plist := agent.load(t, exe, d.dir, "/opt/acme/bin")
	agent.replaced = true
	if out := d.ok("update", "--after-brew", "v1.2.0"); out != "installed v1.3.0 with Homebrew; the next run uses it\n" {
		t.Fatalf("update --after-brew printed %q", out)
	}
	agent.check(t, 2, plist)
	if st := d.state(); st.Update.CheckedAt.IsZero() || st.Update.Latest != "v1.3.0" || st.Update.Error != "" {
		t.Fatalf("update = %+v", st.Update)
	}
}

// Under Homebrew, a run never replaces the binary, the report says how it
// updates, and a run of a release brew installed since the last one has
// launchd start the agent once, as after a self-update.
func TestRunsUnderHomebrew(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	b, g := homebrewBuild(t, "v1.2.0", "1.3.0")
	agent := newFakeAgent(t)
	newScheduler = agent.scheduler
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	plist := agent.load(t, exe, d.dir, "/opt/acme/bin")
	// A release that failed to install before brew took over is forgotten.
	st := d.state()
	st.Update.Failed = &state.FailedRelease{Tag: "v1.1.0", At: time.Now(), Error: "checksum"}
	if err := state.Dir(d.dir).SaveState(st); err != nil {
		t.Fatal(err)
	}
	d.ok("collect", "--quiet", "--offline")
	st = d.state()
	keg := filepath.Join(b.prefix, "Cellar", "ai-usage", "1.2.0", "bin", "ai-usage")
	if st.RanVersion != "v1.2.0" || st.Update.Latest != "v1.3.0" || st.Update.Installed != "" || st.Update.Failed != nil || !strings.Contains(b.read("Cellar/ai-usage/1.2.0/bin/ai-usage"), "v1.2.0") {
		t.Fatalf("ran %q, update %+v", st.RanVersion, st.Update)
	}
	if _, downloads := g.counts(); downloads != 0 {
		t.Fatalf("downloaded a release over %s", keg)
	}
	u := d.report().Collector.Update
	if u.Method != "homebrew" || u.Command == nil || *u.Command != "brew upgrade neoromantic/tap/ai-usage" {
		t.Fatalf("update in the report = %+v", u)
	}
	if out := d.ok("status"); !strings.Contains(strings.Join(strings.Fields(out), " "), "v1.3.0 is out; run brew upgrade neoromantic/tap/ai-usage or ai-usage update") {
		t.Fatalf("status:\n%s", out)
	}
	agent.check(t, 0, plist)

	// Someone runs brew upgrade; the agent's binary is replaced.
	b.set("tap", "1.3.0")
	if _, err := brewUpgrade(context.Background(), b.prefix, brewOpt(b.prefix), new(strings.Builder)); err != nil {
		t.Fatal(err)
	}
	agent.replaced = true
	// A run of the old release, such as the view, sees the release staged.
	t0 := time.Now()
	clock = func() time.Time { return t0.Add(15 * time.Minute) }
	d.ok("collect", "--offline")
	if st := d.state(); st.Update.Installed != "v1.3.0" {
		t.Fatalf("update after brew upgrade = %+v", st.Update)
	}
	agent.check(t, 2, plist)

	// The report the menu bar app reads, now from the new release, has
	// launchd start the agent, whose run then collects.
	agent.replaced = true
	version = "v1.3.0"
	d.report()
	agent.check(t, 4, plist)
	agent.inAgent = true
	d.ok("collect", "--quiet", "--offline")
	agent.inAgent = false
	d.report()
	agent.check(t, 4, plist)
}

// Where the installer's copy and Homebrew's both are, only Homebrew's
// registers, and each says to remove the installer's.
func TestTwoCopies(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	cron := &fakeCrontab{}
	newScheduler = cron.scheduler
	d := newDevice(t)
	script := filepath.Join(d.home, ".local", "bin", "ai-usage")
	d.write(".local/bin/ai-usage", "a script install")

	// Homebrew's copy registers, and names the other.
	b, _ := homebrewBuild(t, "v1.3.0")
	d.ok("collect", "--quiet", "--offline")
	st := d.state()
	if !st.Schedule.Registered || st.Schedule.Error != "another ai-usage at "+script+" also runs; remove it: rm "+script || cron.writes != 1 {
		t.Fatalf("schedule %+v after %d crontab writes", st.Schedule, cron.writes)
	}
	if out := d.ok("status"); !strings.Contains(out, "registered with the system scheduler") || !strings.Contains(out, "also runs") {
		t.Fatalf("status:\n%s", out)
	}

	// The installer's copy hands the entry to Homebrew's, and leaves it
	// releases.
	_, g := releaseBuild(t, "v1.3.0")
	brewPrefix = func() string { return "" }
	brewPrefixes = func() []string { return []string{"", filepath.Join(d.home, "nowhere"), b.prefix} }
	cron.tab = ""
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d.ok("collect", "--quiet", "--offline")
	st = d.state()
	want := "Homebrew's ai-usage at " + filepath.Join(b.prefix, "bin", "ai-usage") + " runs the schedule; remove this copy: rm " + exe
	entry := "*/15 * * * * '" + brewOpt(b.prefix) + "' collect --quiet --home '" + d.dir + "' >/dev/null 2>&1 # ai-usage\n"
	if !st.Schedule.Registered || st.Schedule.Error != want || cron.writes != 2 || !strings.Contains(cron.tab, "'"+brewOpt(b.prefix)+"'") {
		t.Fatalf("schedule %+v after %d crontab writes:\n%s", st.Schedule, cron.writes, cron.tab)
	}
	if lookups, _ := g.counts(); lookups != 0 {
		t.Fatal("the installer's copy checked for a release")
	}
	// It counts Homebrew's entry as the schedule.
	cron.tab = entry
	d.ok("collect", "--quiet", "--offline")
	if st := d.state(); !st.Schedule.Registered || st.Schedule.Error != want || cron.writes != 2 {
		t.Fatalf("schedule %+v after %d crontab writes", st.Schedule, cron.writes)
	}
	// Asked to register, it registers Homebrew's.
	cron.tab = ""
	if out := d.ok("schedule", "install"); !strings.Contains(out, "registered "+brewOpt(b.prefix)+" ") {
		t.Fatalf("schedule install printed %q", out)
	}

	// A copy the installer put in Homebrew's bin folder, which keeps brew
	// from linking its own there, is replaced by brew.
	b, _ = homebrewBuild(t, "v1.3.0")
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	b.set("bin/ai-usage", "a script install")
	d.ok("collect", "--quiet", "--offline")
	if st := d.state(); st.Schedule.Error != "another ai-usage at "+filepath.Join(b.prefix, "bin", "ai-usage")+" keeps Homebrew from linking this one; replace it: brew link --overwrite neoromantic/tap/ai-usage" {
		t.Fatalf("schedule %+v", st.Schedule)
	}
}

// A collection that fails under Homebrew, with a newer release out, says
// brew upgrade may fix it.
func TestFailedCollectionUnderHomebrew(t *testing.T) {
	hermetic(t)
	homebrewBuild(t, "v1.2.0")
	d := newDevice(t)
	if err := os.MkdirAll(filepath.Join(d.dir, "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := d.run("", "collect", "--quiet", "--offline")
	if r.code != 1 || !strings.Contains(r.stderr, "config.json") || !strings.HasSuffix(r.stderr, " — `brew upgrade neoromantic/tap/ai-usage` may fix it\n") {
		t.Fatalf("collect = %+v", r)
	}
}

// Root cannot update a Homebrew copy, and the report gives it no command.
func TestHomebrewAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	hermetic(t)
	homebrewBuild(t, "v1.2.0")
	d := newDevice(t)
	if r := d.run("", "update"); r.code != 1 || !strings.Contains(r.stderr, errBrewRoot.Error()) {
		t.Fatalf("update as root = %+v", r)
	}
	if brewCommand() != "" {
		t.Fatal("root gets a brew command")
	}
}

// The menu bar app's failure is recorded against the release it tried,
// which under Homebrew is the binary's own, not the latest.
func TestAppFailedUnderHomebrew(t *testing.T) {
	st := &state.State{}
	err := fmt.Errorf("%w: rename: permission denied", selfupdate.ErrApp)
	noteUpdate(st, time.Now(), selfupdate.Result{Latest: "v1.4.0", AppDownloaded: true, AppTag: "v1.3.0"}, err)
	if f := st.Update.AppFailed; f == nil || f.Tag != "v1.3.0" || !errors.Is(err, selfupdate.ErrApp) {
		t.Fatalf("app failed = %+v", f)
	}
}
