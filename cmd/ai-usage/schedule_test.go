package main

import (
	"bytes"
	"context"
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
