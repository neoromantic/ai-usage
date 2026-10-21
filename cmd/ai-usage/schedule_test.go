package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/state"
)

// fakeCrontab is the user's crontab in memory.
type fakeCrontab struct {
	mu     sync.Mutex
	tab    string
	writes int
}

func (f *fakeCrontab) scheduler() schedule.Scheduler {
	return schedule.Scheduler{GOOS: "linux", Run: func(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch strings.Join(append([]string{name}, args...), " ") {
		case "crontab -l":
			if f.tab == "" {
				return nil, fmt.Errorf("crontab: no crontab for tester")
			}
			return []byte(f.tab), nil
		case "crontab -":
			f.tab = string(stdin)
			f.writes++
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected %s %v", name, args)
	}}
}

func TestScheduleCommands(t *testing.T) {
	hermetic(t)
	cron := &fakeCrontab{tab: "0 3 * * * /usr/local/bin/backup\n"}
	newScheduler = cron.scheduler
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	if out := d.ok("schedule", "status"); !strings.Contains(out, "not registered") {
		t.Fatalf("status = %q", out)
	}
	d.ok("schedule", "install")
	if !strings.HasPrefix(cron.tab, "0 3 * * * /usr/local/bin/backup\n") || !strings.Contains(cron.tab, d.dir) {
		t.Fatalf("crontab = %q", cron.tab)
	}
	if out := d.ok("schedule", "status"); !strings.Contains(out, exe) {
		t.Fatalf("status = %q", out)
	}

	// Telling entries apart is the scheduler's test; status reports it.
	cron.tab = "0 3 * * * /usr/local/bin/backup\n" + schedule.Line(exe, "/elsewhere/state", "/bin") + "\n"
	if out := d.ok("schedule", "status"); !strings.Contains(out, "different") {
		t.Fatalf("status = %q", out)
	}

	d.ok("schedule", "remove")
	if cron.tab != "0 3 * * * /usr/local/bin/backup\n" || !d.config().ScheduleOff {
		t.Fatalf("after remove: crontab %q, config %+v", cron.tab, d.config())
	}
	d.ok("schedule", "install")
	if d.config().ScheduleOff {
		t.Fatal("install left schedule_off set")
	}
}

// Where there is no crontab, as in most containers, `schedule run` is the
// scheduler: it collects at once and then at each quarter hour, in a process
// of the binary on disk, until it is stopped. Runs meanwhile see that it is
// there and never reach for the system scheduler.
func TestScheduleRun(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	reached := 0
	newScheduler = func() schedule.Scheduler {
		return schedule.Scheduler{GOOS: "linux", Run: func(_ context.Context, name string, _ []string, _ []byte) ([]byte, error) {
			reached++
			return nil, fmt.Errorf("%s: %w", name, exec.ErrNotFound)
		}}
	}
	clock = func() time.Time { return time.Date(2026, 9, 23, 10, 7, 30, 0, time.UTC) }

	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	// The installer's first run. Its guide offers no scheduler command,
	// since there is no scheduler here to pause.
	if out := d.ok("--offline"); !strings.Contains(out, "\nHOW IT WORKS") || strings.Contains(out, "schedule remove") {
		t.Fatalf("the first run's guide without a crontab:\n%s", out)
	}
	if st := d.state(); st.Schedule.Registered || !strings.Contains(st.Schedule.Error, "schedule run") {
		t.Fatalf("schedule without crontab = %+v", st.Schedule)
	}

	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	var collected []string
	var statuses []string
	collectOnce = func(ctx context.Context, gotExe, home string, stderr io.Writer) error {
		collected = append(collected, gotExe+" "+home)
		reached = 0
		var out bytes.Buffer
		if code := run(ctx, []string{"collect", "--quiet", "--offline", "--home", home}, strings.NewReader(""), &out, stderr); code != 0 {
			t.Errorf("collection: exit %d", code)
		}
		if reached != 0 {
			t.Errorf("a run under schedule run reached the system scheduler %d times", reached)
		}
		statuses = append(statuses, d.ok("status"))
		return nil
	}
	var slept []time.Duration
	sleepCtx = func(_ context.Context, dur time.Duration) bool {
		slept = append(slept, dur)
		return len(slept) < 2
	}
	r := d.run("", "schedule", "run")
	if r.code != 0 || !strings.Contains(r.stderr, d.dir) {
		t.Fatalf("schedule run: exit %d, stderr %q", r.code, r.stderr)
	}
	if want := []string{exe + " " + d.dir, exe + " " + d.dir}; !reflect.DeepEqual(collected, want) {
		t.Fatalf("collected %q, want %q", collected, want)
	}
	// The next run is at the next quarter hour, as the system schedulers run it.
	if want := []time.Duration{7*time.Minute + 30*time.Second, 7*time.Minute + 30*time.Second}; !reflect.DeepEqual(slept, want) {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	for _, out := range statuses {
		if !strings.Contains(out, "schedule run") || strings.Contains(out, "stopped") {
			t.Fatalf("status while schedule run runs:\n%s", out)
		}
	}
	if st := d.state(); !st.Schedule.Registered || !st.Schedule.Foreground || st.Schedule.Error != "" {
		t.Fatalf("schedule after schedule run = %+v", st.Schedule)
	}

	// Once it stops, the report says so instead of claiming a schedule.
	if r := d.report(); r.Collector.Schedule.Registered || r.Collector.Schedule.Error == nil || !strings.Contains(*r.Collector.Schedule.Error, "stopped") {
		t.Fatalf("schedule in the report after it stopped = %+v", r.Collector.Schedule)
	}
	if out := d.ok("status"); !strings.Contains(out, "stopped") {
		t.Fatalf("status after schedule run stopped:\n%s", out)
	}

	// One folder has one schedule run.
	unlock, err := state.Dir(d.dir).ScheduleLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if r := d.run("", "schedule", "run"); r.code != 1 || !strings.Contains(r.stderr, "already") {
		t.Fatalf("a second schedule run: exit %d, stderr %q", r.code, r.stderr)
	}
}

// A collection schedule run starts is a process of its own, which sees the
// schedule lock the runner holds.
func TestScheduleRunStartsACollection(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	t.Setenv("AIU_AS_CLI", "1")
	d.env()
	unlock, err := state.Dir(d.dir).ScheduleLock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := collectOnce(context.Background(), exe, d.dir, &stderr); err != nil {
		t.Fatalf("collectOnce: %v, %s", err, stderr.String())
	}
	st := d.state()
	if st.LastRunAt.IsZero() || !st.Schedule.Registered || !st.Schedule.Foreground {
		t.Fatalf("after the child's collection: last run %v, schedule %+v, stderr %s", st.LastRunAt, st.Schedule, stderr.String())
	}
	// It read this device's fake home, and asked the fake claude.
	if a := st.Accounts[state.Key("claude", "dev@example.com")]; a == nil || st.Sources["claude"].Status != "ok" {
		t.Fatalf("claude: %+v, accounts %v", st.Sources["claude"], st.Accounts)
	}
}

// Cron sees neither AI_USAGE_HOME nor the shell's XDG_CONFIG_HOME, so the
// entry names the folder. Otherwise scheduled runs would start a second
// device with its own team key in the default folder.
func TestScheduledRunsUseTheSameFolder(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	cron := &fakeCrontab{}
	newScheduler = cron.scheduler
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	d.ok("collect", "--quiet", "--offline")
	if !strings.Contains(cron.tab, d.dir) {
		t.Fatalf("crontab = %q", cron.tab)
	}
	device, key := d.config().Device, strings.TrimSpace(d.ok("team", "key"))

	// What cron runs, in an environment that would pick another folder.
	elsewhere := filepath.Join(t.TempDir(), "default")
	t.Setenv("AI_USAGE_HOME", elsewhere)
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"collect", "--quiet", "--offline", "--home", d.dir}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("scheduled run: exit %d, %s", code, errb.String())
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Fatal("the scheduled run used the folder from its environment")
	}
	if d.config().Device != device || strings.TrimSpace(d.ok("team", "key")) != key {
		t.Fatal("the scheduled run changed the device or the team")
	}
	if st := d.state(); !st.Schedule.Registered || st.Schedule.Error != "" || cron.writes != 1 {
		t.Fatalf("schedule %+v after %d crontab writes", st.Schedule, cron.writes)
	}

	// A run from a moved folder moves the entry with it.
	moved := newDevice(t)
	moved.ok("collect", "--quiet", "--offline")
	if cron.writes != 2 || !strings.Contains(cron.tab, moved.dir) || strings.Contains(cron.tab, d.dir) {
		t.Fatalf("crontab after a run from another folder (%d writes) = %q", cron.writes, cron.tab)
	}
}

// A person who comments the line out has paused the collector: runs say so
// and do not turn it back on.
func TestCommentedOutScheduleIsLeftAlone(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	paused := "# " + schedule.Line(exe, d.dir, "/usr/bin:/bin") + "\n"
	cron := &fakeCrontab{tab: paused}
	newScheduler = cron.scheduler
	d.ok("collect", "--quiet", "--offline")
	if cron.writes != 0 || cron.tab != paused {
		t.Fatalf("crontab written %d times: %q", cron.writes, cron.tab)
	}
	if st := d.state(); st.Schedule.Registered || !strings.Contains(st.Schedule.Error, "disabled by hand") {
		t.Fatalf("schedule = %+v", st.Schedule)
	}
	// Status names the command that turns it back on, once.
	if out := d.ok("status"); !strings.Contains(out, "disabled by hand") || strings.Count(out, "schedule install") != 1 {
		t.Fatalf("status:\n%s", out)
	}
	if out := d.ok("schedule", "status"); !strings.Contains(out, "disabled by hand") {
		t.Fatalf("schedule status = %q", out)
	}
	d.ok("schedule", "install")
	if cron.tab != schedule.Line(exe, d.dir, os.Getenv("PATH"))+"\n" {
		t.Fatalf("crontab after install = %q", cron.tab)
	}
}

// TestStoppedHousekeeping: a release check or a scheduler lookup that a
// stop cuts short records nothing, since it failed at nothing.
func TestStoppedHousekeeping(t *testing.T) {
	hermetic(t)
	version = "v9.9.9"
	newScheduler = func() schedule.Scheduler {
		return schedule.Scheduler{GOOS: "linux", Run: func(ctx context.Context, name string, args []string, _ []byte) ([]byte, error) {
			if ctx.Err() == nil {
				t.Errorf("%s %v ran unstopped", name, args)
			}
			return nil, ctx.Err()
		}}
	}
	d := newDevice(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st := &state.State{Schedule: state.Schedule{Registered: true, CheckedAt: time.Unix(1e9, 0).UTC()}}
	want := *st
	if updateIfDue(ctx, st, time.Now(), true) || !reflect.DeepEqual(st.Update, want.Update) {
		t.Errorf("a stopped release check: %+v", st.Update)
	}
	ensureSchedule(ctx, state.Dir(d.dir), st, time.Now())
	if !reflect.DeepEqual(st.Schedule, want.Schedule) {
		t.Errorf("a stopped scheduler lookup: %+v", st.Schedule)
	}
}

// fakeAgent is launchd in memory, holding the macOS launch agent whose plist
// is in dir. pinned says launchd pinned the agent to its binary's code
// signature, as macOS 27 does at login, and replaced that an update replaced
// the binary since: launchd refuses the next start, and repairs the pin at
// the start after. refused says it refused the last start, and stubborn that
// it refuses every start. inAgent makes this process a run the agent
// started. argv is the command launchd loaded, which it keeps whatever the
// plist says since.
type fakeAgent struct {
	mu                                sync.Mutex
	dir                               string
	loaded, pinned, replaced, refused bool
	stubborn, inAgent                 bool
	runs, kicks, boots                int
	argv                              []string
}

// plistArgv is the command in a launch agent plist, its first array.
func plistArgv(plist []byte) ([]string, error) {
	var p struct {
		Arrays []struct {
			Strings []string `xml:"string"`
		} `xml:"dict>array"`
	}
	if err := xml.Unmarshal(plist, &p); err != nil || len(p.Arrays) == 0 {
		return nil, fmt.Errorf("plist: %v\n%s", err, plist)
	}
	return p.Arrays[0].Strings, nil
}

func newFakeAgent(t *testing.T) *fakeAgent {
	return &fakeAgent{dir: filepath.Join(t.TempDir(), "LaunchAgents")}
}

func (f *fakeAgent) plist() string { return filepath.Join(f.dir, schedule.AgentLabel+".plist") }

func (f *fakeAgent) scheduler() schedule.Scheduler {
	f.mu.Lock()
	defer f.mu.Unlock()
	svc := "gui/501/" + schedule.AgentLabel
	return schedule.Scheduler{GOOS: "darwin", AgentDir: f.dir, UID: 501, InAgent: f.inAgent, Run: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch strings.Join(append([]string{name}, args...), " ") {
		case "launchctl print gui/501", "launchctl print-disabled gui/501", "launchctl enable " + svc:
			return nil, nil
		case "launchctl print " + svc:
			if !f.loaded {
				return nil, fmt.Errorf("launchctl: Could not find service %q in domain for user gui: 501", schedule.AgentLabel)
			}
			out := fmt.Sprintf("%s = {\n\tstate = not running\n\truns = %d\n", svc, f.runs)
			if f.argv != nil {
				out += "\targuments = {\n\t\t" + strings.Join(f.argv, "\n\t\t") + "\n\t}\n"
			}
			switch {
			case f.refused:
				out += "\tlast exit reason = OS_REASON_CODESIGNING\n\tjob state = spawn failed\n" +
					"\tproperties = inferred program | needs LWCR update | managed LWCR | has LWCR\n"
			case f.pinned:
				out += "\tlast exit code = 0\n\tjob state = exited\n\tproperties = inferred program | managed LWCR | has LWCR\n"
			default:
				out += "\tlast exit code = 0\n\tjob state = exited\n\tproperties = inferred program\n"
			}
			return []byte(out + "}\n"), nil
		case "launchctl bootout " + svc:
			f.loaded = false
			return nil, nil
		case "launchctl bootstrap gui/501 " + f.plist():
			b, err := os.ReadFile(f.plist())
			if err != nil {
				return nil, err
			}
			if f.argv, err = plistArgv(b); err != nil {
				return nil, err
			}
			f.loaded, f.pinned, f.refused, f.runs = true, false, false, 0
			f.boots++
			return nil, nil
		case "launchctl kickstart " + svc:
			f.runs++
			f.kicks++
			f.refused = f.pinned && (f.stubborn || f.replaced && !f.refused)
			if !f.refused {
				f.replaced = false
			}
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected %s %v", name, args)
	}}
}

// load is the agent of exe collecting into home, with the PATH path, as
// launchd loaded it at login and pinned it. It returns the plist.
func (f *fakeAgent) load(t *testing.T, exe, home, path string) []byte {
	t.Helper()
	plist := []byte(schedule.AgentPlist(exe, home, path))
	if err := os.MkdirAll(f.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.plist(), plist, 0o644); err != nil {
		t.Fatal(err)
	}
	argv, err := plistArgv(plist)
	if err != nil {
		t.Fatal(err)
	}
	f.loaded, f.pinned, f.argv = true, true, argv
	return plist
}

// check fails t unless launchd was asked to start the agent n times in all,
// and the agent keeps the plist it was loaded with, never loaded again.
func (f *fakeAgent) check(t *testing.T, n int, plist []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, err := os.ReadFile(f.plist()); err != nil || !bytes.Equal(b, plist) {
		t.Fatalf("the plist changed: %v\n%s", err, b)
	}
	if f.kicks != n || !f.loaded || f.boots != 0 {
		t.Fatalf("agent started %d times, want %d; loaded %v, bootstrapped %d times", f.kicks, n, f.loaded, f.boots)
	}
}

// Once an update replaced the binary of the launch agent launchd loaded at
// login, launchd refuses to start it at its next run, and repairs that at
// the run after. A run a person starts, collecting or reading the report,
// has launchd start it now, which repairs it. The agent's own run leaves it
// to launchd.
func TestRefusedAgentIsRepaired(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	agent := newFakeAgent(t)
	newScheduler = agent.scheduler
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	plist := agent.load(t, exe, d.dir, "/opt/acme/bin")
	d.ok("collect", "--quiet", "--offline")
	agent.check(t, 0, plist)

	agent.refused, agent.runs = true, 1
	agent.inAgent = true
	d.ok("collect", "--quiet", "--offline")
	agent.check(t, 0, plist)
	agent.inAgent = false
	if out := d.ok("schedule", "status"); !strings.Contains(out, "registered, but launchd refused") || !strings.Contains(out, "ai-usage status") {
		t.Fatalf("schedule status of a refused agent = %q", out)
	}
	// A dev build reads the report and leaves the agent alone.
	version = "devel"
	d.ok("status")
	agent.check(t, 0, plist)
	version = "v1.3.0"

	if out := d.ok("status"); strings.Contains(out, "refuse") || !strings.Contains(out, "registered with the system scheduler") {
		t.Fatalf("status after the repair:\n%s", out)
	}
	agent.check(t, 1, plist)

	agent.refused = true
	if r := d.report(); !r.Collector.Schedule.Registered || r.Collector.Schedule.Error != nil {
		t.Fatalf("schedule in the report = %+v", r.Collector.Schedule)
	}
	agent.check(t, 2, plist)

	agent.refused = true
	d.ok("collect", "--offline")
	agent.check(t, 3, plist)
	if st := d.state(); !st.Schedule.Registered || st.Schedule.Error != "" {
		t.Fatalf("schedule after a collection = %+v", st.Schedule)
	}

	// An agent launchd still refuses is a problem status names, with what
	// fixes it.
	agent.refused, agent.stubborn = true, true
	d.ok("collect", "--offline")
	if st := d.state(); st.Schedule.Registered || st.Schedule.Error != stillRefused {
		t.Fatalf("schedule of an agent launchd still refuses = %+v", st.Schedule)
	}
	out := d.ok("status")
	if !strings.Contains(out, "not scheduled") || !strings.Contains(out, "launchd refuses") || strings.Count(out, "schedule install") != 1 {
		t.Fatalf("status of an agent launchd still refuses:\n%s", out)
	}
	agent.check(t, 5, plist)
}

// A release that a run a person starts installs, as `ai-usage update` and the
// menu bar app's Update button do, has launchd start the launch agent at once
// when it pinned it: launchd refuses that start and repairs the pin at the
// next, so no scheduled run is refused. The agent's own run cannot, and an
// agent pinned to nothing needs nothing.
func TestUpdateRepairsTheAgent(t *testing.T) {
	for _, tc := range []struct {
		name            string
		args            []string
		inAgent, pinned bool
		want            int
	}{
		{"update", []string{"update"}, false, true, 2},
		{"collect", []string{"collect", "--offline"}, false, true, 2},
		{"scheduled", []string{"collect", "--quiet", "--offline"}, true, true, 0},
		{"pinned to nothing", []string{"update"}, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hermetic(t)
			t.Setenv("AI_USAGE_NO_SCHEDULE", "")
			binary, _ := releaseBuild(t, "v1.2.0")
			agent := newFakeAgent(t)
			agent.inAgent = tc.inAgent
			newScheduler = agent.scheduler
			exe, err := executable()
			if err != nil {
				t.Fatal(err)
			}
			d := newDevice(t)
			plist := agent.load(t, exe, d.dir, "/opt/acme/bin")
			agent.pinned, agent.replaced = tc.pinned, true
			d.ok(tc.args...)
			if b, _ := os.ReadFile(binary); string(b) == "old binary" {
				t.Fatal("the release was not installed")
			}
			agent.check(t, tc.want, plist)
			if agent.refused || agent.replaced != (tc.want == 0) {
				t.Fatalf("after the update: refused %v, replaced %v", agent.refused, agent.replaced)
			}
			if st := d.state(); st.Update.Installed != "v1.3.0" || !tc.inAgent && (!st.Schedule.Registered || st.Schedule.Error != "") {
				t.Fatalf("update %+v, schedule %+v", st.Update, st.Schedule)
			}
		})
	}
}

// launchd keeps the launch agent it loaded until it is loaded again, so once
// a scheduled run of another binary rewrote the plist for this one, as the
// installer's copy does for Homebrew's, launchd still runs the other.
// Status says so and how to fix it, where the plist alone would look
// registered; a run a person starts, or `ai-usage schedule install`, loads
// the plist. A schedule launchd has not loaded says so too.
func TestStaleAgent(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.3.0")
	agent := newFakeAgent(t)
	newScheduler = agent.scheduler
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	old := filepath.Join(t.TempDir(), "bin", "ai-usage")
	agent.load(t, old, d.dir, "/opt/acme/bin")

	agent.inAgent = true
	d.ok("collect", "--quiet", "--offline")
	agent.inAgent = false
	if b, _ := os.ReadFile(agent.plist()); !bytes.Equal(b, []byte(schedule.AgentPlist(exe, d.dir, os.Getenv("PATH")))) || agent.boots != 0 || agent.argv[0] != old {
		t.Fatalf("a scheduled run loaded the agent: booted %d times, loaded %q, plist:\n%s", agent.boots, agent.argv, b)
	}
	if st := d.state(); st.Schedule.Registered || st.Schedule.Error != staleAgent {
		t.Fatalf("schedule after a scheduled run = %+v", st.Schedule)
	}
	if out := d.ok("schedule", "status"); !strings.Contains(out, "registered, but launchd still runs the launch agent it loaded before") {
		t.Fatalf("schedule status = %q", out)
	}
	// Status reads launchd now, whatever the last run saved.
	st := d.state()
	st.Schedule.Registered, st.Schedule.Error = true, ""
	if err := state.Dir(d.dir).SaveState(st); err != nil {
		t.Fatal(err)
	}
	out := strings.Join(strings.Fields(d.ok("status")), " ")
	if !strings.Contains(out, "not registered: launchd still runs") || strings.Count(out, "schedule install") != 1 {
		t.Fatalf("status of a stale agent:\n%s", out)
	}
	if r := d.report(); r.Collector.Schedule.Registered || r.Collector.Schedule.Error == nil || *r.Collector.Schedule.Error != staleAgent {
		t.Fatalf("schedule in the report = %+v", r.Collector.Schedule)
	}
	if agent.boots != 0 {
		t.Fatal("reading the report loaded the agent")
	}

	d.ok("schedule", "install")
	if agent.boots != 1 || agent.argv[0] != exe {
		t.Fatalf("after schedule install: booted %d times, loaded %q", agent.boots, agent.argv)
	}
	if out := d.ok("status"); strings.Contains(out, "launchd still runs") || !strings.Contains(out, "registered with the system scheduler") {
		t.Fatalf("status after schedule install:\n%s", out)
	}

	// A run a person starts loads the plist itself.
	agent.argv[0] = old
	d.ok("collect", "--offline")
	if st := d.state(); agent.boots != 2 || agent.argv[0] != exe || !st.Schedule.Registered || st.Schedule.Error != "" {
		t.Fatalf("after a collection: booted %d times, loaded %q, schedule %+v", agent.boots, agent.argv, st.Schedule)
	}

	agent.loaded = false
	if out := d.ok("schedule", "status"); !strings.Contains(out, "registered, but launchd has not loaded the launch agent") {
		t.Fatalf("schedule status of an agent not loaded = %q", out)
	}
	if out := strings.Join(strings.Fields(d.ok("status")), " "); !strings.Contains(out, "not registered: launchd has not loaded") {
		t.Fatalf("status of an agent not loaded:\n%s", out)
	}
	d.ok("collect", "--offline")
	if st := d.state(); !agent.loaded || agent.boots != 3 || !st.Schedule.Registered || st.Schedule.Error != "" {
		t.Fatalf("after a collection: loaded %v, booted %d times, schedule %+v", agent.loaded, agent.boots, st.Schedule)
	}
}

// A repair that fails is the schedule's error, with what registers again.
func TestFailedRepairIsTheScheduleError(t *testing.T) {
	hermetic(t)
	t.Setenv("AI_USAGE_NO_SCHEDULE", "")
	releaseBuild(t, "v1.2.0")
	agent := newFakeAgent(t)
	exe, err := executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newDevice(t)
	agent.load(t, exe, d.dir, "/opt/acme/bin")
	newScheduler = func() schedule.Scheduler {
		s := agent.scheduler()
		run := s.Run
		s.Run = func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
			if args[0] == "kickstart" {
				return nil, fmt.Errorf("launchctl: Could not kickstart service: 5: Input/output error")
			}
			return run(ctx, name, args, stdin)
		}
		return s
	}
	d.ok("update")
	if st := d.state(); st.Schedule.Registered || !strings.Contains(st.Schedule.Error, "starting the launch agent: launchctl: Could not kickstart") {
		t.Fatalf("schedule = %+v", st.Schedule)
	}
	if out := d.ok("status"); !strings.Contains(out, "Input/output error") || !strings.Contains(out, "register: ai-usage schedule install") {
		t.Fatalf("status:\n%s", out)
	}
}
