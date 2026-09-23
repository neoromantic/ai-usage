package state

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func tempDir(t *testing.T) Dir {
	t.Helper()
	return Dir(filepath.Join(t.TempDir(), "ai-usage"))
}

func TestDefaultDirHonorsEnv(t *testing.T) {
	want := t.TempDir()
	t.Setenv("AI_USAGE_HOME", want)
	d, err := DefaultDir()
	if err != nil || string(d) != want {
		t.Fatalf("DefaultDir = %q, %v; want %q", d, err, want)
	}
}

func TestKeyRoundTrip(t *testing.T) {
	parts := []string{"claude", "/Users/me/.claude", "a@b.c"}
	if got := SplitKey(Key(parts...)); !reflect.DeepEqual(got, parts) {
		t.Fatalf("SplitKey(Key(%q)) = %q", parts, got)
	}
}

func TestLockIsExclusive(t *testing.T) {
	d := tempDir(t)
	unlock, err := d.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lock(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock while the first is held = %v", err)
	}
	if b, _ := os.ReadFile(d.Path("run.lock")); string(b) != strconv.Itoa(os.Getpid())+"\n" {
		t.Fatalf("run.lock names %q", b)
	}
	unlock()
	if b, _ := os.ReadFile(d.Path("run.lock")); len(b) != 0 {
		t.Fatalf("released run.lock names %q", b)
	}
	unlock2, err := d.Lock()
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
}

// TestLockHonorsEarlierRelease: a release from before the system's lock holds
// run.lock by having written "pid nonce" into it. Its lock holds a run off
// while its process runs, for up to 10 minutes; a file a crashed run left
// holds nothing.
func TestLockHonorsEarlierRelease(t *testing.T) {
	d := tempDir(t)
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		t.Fatal(err)
	}
	path := d.Path("run.lock")
	write := func(owner string, age time.Duration) {
		t.Helper()
		if err := os.WriteFile(path, []byte(owner), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	live := strconv.Itoa(os.Getppid()) + " 0011223344556677"
	write(live, time.Minute)
	if _, err := d.Lock(); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock while an earlier release runs = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != live {
		t.Fatalf("the earlier release's lock became %q", b)
	}
	cases := map[string]string{"over 10 minutes old": live, "not a lock": "12345\n"}
	if runtime.GOOS != "windows" {
		gone := exec.Command(os.Args[0], "-test.run=^$")
		if err := gone.Run(); err != nil {
			t.Fatal(err)
		}
		cases["its process gone"] = strconv.Itoa(gone.Process.Pid) + " 0011223344556677"
	}
	for name, owner := range cases {
		age := time.Minute
		if name == "over 10 minutes old" {
			age = time.Hour
		}
		write(owner, age)
		unlock, err := d.Lock()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		unlock()
	}
}

// TestHoldLock is the other process of TestLockEndsWithItsProcess.
func TestHoldLock(t *testing.T) {
	dir := os.Getenv("AI_USAGE_TEST_HOLD_LOCK")
	if dir == "" {
		t.Skip("run by TestLockEndsWithItsProcess")
	}
	if _, err := Dir(dir).Lock(); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("held\n")
	time.Sleep(time.Minute)
}

// TestLockEndsWithItsProcess: another process's lock holds a run off, and a
// run that was killed leaves no lock behind.
func TestLockEndsWithItsProcess(t *testing.T) {
	d := tempDir(t)
	holder := exec.Command(os.Args[0], "-test.run=^TestHoldLock$")
	holder.Env = append(os.Environ(), "AI_USAGE_TEST_HOLD_LOCK="+string(d))
	out, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	defer holder.Process.Kill()
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("holder: %q, %v", line, err)
	}
	if _, err := d.Lock(); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock while another process holds it = %v", err)
	}
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	unlock, err := d.Lock()
	if err != nil {
		t.Fatalf("the lock of a killed run was left behind: %v", err)
	}
	unlock()
}

// TestEditConfigKeepsConcurrentEdits: commands and runs that change the
// config at the same time each keep their change.
func TestEditConfigKeepsConcurrentEdits(t *testing.T) {
	defer func(d time.Duration) { lockPoll = d }(lockPoll)
	lockPoll = time.Millisecond
	d := tempDir(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.EditConfig(func(c *Config) error {
				if c.Homes == nil {
					c.Homes = map[string][]string{}
				}
				c.Homes["claude"] = append(c.Homes["claude"], "/h/"+strconv.Itoa(i))
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	c, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Homes["claude"]) != 8 {
		t.Fatalf("homes = %q", c.Homes["claude"])
	}
}

func TestLockWait(t *testing.T) {
	defer func(d time.Duration) { lockPoll = d }(lockPoll)
	lockPoll = 5 * time.Millisecond
	ctx := context.Background()
	d := tempDir(t)
	held, err := d.Lock()
	if err != nil {
		t.Fatal(err)
	}
	// The holder finishes while the other run waits.
	waited := 0
	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		held()
		close(done)
	}()
	unlock, err := d.LockWait(ctx, 10*time.Second, func() { waited++ })
	if err != nil {
		t.Fatalf("LockWait: %v", err)
	}
	<-done
	if waited != 1 {
		t.Fatalf("waiting called %d times", waited)
	}
	// No wait at all when the lock is free, and busy once the wait runs out.
	waited = 0
	start := time.Now()
	if _, err := d.LockWait(ctx, 30*time.Millisecond, func() { waited++ }); !errors.Is(err, ErrBusy) {
		t.Fatalf("LockWait while held = %v", err)
	}
	if waited != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("waited %d times for %s", waited, time.Since(start))
	}
	// Ctrl-C ends the wait.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.LockWait(cctx, time.Hour, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("LockWait after cancel = %v", err)
	}
	unlock()
	unlock, err = d.LockWait(ctx, 0, func() { t.Fatal("waited for a free lock") })
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestLoadConfigCreatesDeviceOnce(t *testing.T) {
	d := tempDir(t)
	c1, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ValidDevice(c1.Device) {
		t.Fatalf("device id %q is not valid", c1.Device)
	}
	c2, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c2.Device != c1.Device {
		t.Fatalf("device id changed between loads: %q then %q", c1.Device, c2.Device)
	}
	checkPrivate(t, d.Path("config.json"))
}

func TestLoadConfigReplacesInvalidDeviceAndKeepsTheRest(t *testing.T) {
	d := tempDir(t)
	if err := d.SaveConfig(Config{Device: "NOT A DEVICE", Relay: "https://relay.example", ScheduleOff: true}); err != nil {
		t.Fatal(err)
	}
	c, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ValidDevice(c.Device) || c.Relay != "https://relay.example" || !c.ScheduleOff {
		t.Fatalf("config = %+v", c)
	}
	again, _ := d.LoadConfig()
	if again.Device != c.Device {
		t.Fatal("replacement device id was not saved")
	}
}

func TestLoadConfigRejectsDamagedFile(t *testing.T) {
	d := tempDir(t)
	if err := WriteFile(d.Path("config.json"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LoadConfig(); err == nil {
		t.Fatal("damaged config.json loaded without error")
	}
}

func TestLoadStateMissingIsEmpty(t *testing.T) {
	st, err := tempDir(t).LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if st.Sources == nil || st.Current == nil || st.Accounts == nil || st.Sessions == nil {
		t.Fatalf("empty state has nil maps: %+v", st)
	}
}

func TestLoadStateStartsAgainFromADamagedFile(t *testing.T) {
	for _, body := range []string{"", "{\"last_run_at\": \"2026-09", "\x00\x00\x00", `{"sessions": 5}`} {
		d := tempDir(t)
		if err := WriteFile(d.Path("state.json"), []byte(body)); err != nil {
			t.Fatal(err)
		}
		st, err := d.LoadState()
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if st.Damage == "" || st.LastError != st.Damage || st.LastErrorAt.IsZero() || !st.LastRunAt.IsZero() {
			t.Fatalf("%q: state = %+v", body, st)
		}
		if st.Sources == nil || st.Current == nil || st.Accounts == nil || st.Sessions == nil {
			t.Fatalf("%q: nil maps: %+v", body, st)
		}
		if b, err := os.ReadFile(d.Path("state.json.bad")); err != nil || string(b) != body {
			t.Fatalf("%q: kept %q, %v", body, b, err)
		}
		// Damage is for this run only; saving writes a good file.
		if err := d.SaveState(st); err != nil {
			t.Fatal(err)
		}
		again, err := d.LoadState()
		if err != nil || again.Damage != "" || again.LastError != st.LastError {
			t.Fatalf("%q: reloaded %+v, %v", body, again, err)
		}
	}
}

func TestLoadStateFailsWhenTheFileCannotBeRead(t *testing.T) {
	d := tempDir(t)
	if err := os.MkdirAll(d.Path("state.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := d.LoadState(); err == nil {
		t.Fatal("an unreadable state.json loaded as empty")
	}
}

func TestStateRoundTrip(t *testing.T) {
	d := tempDir(t)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	reset := at.Add(5 * time.Hour)
	in := &State{
		LastRunAt:     at,
		LastSuccessAt: at,
		Sources:       map[string]Source{"claude": {Status: "ok", Homes: []string{"/h/.claude"}}},
		Current:       map[string]string{Key("claude", "/h/.claude"): "a@b.c"},
		Accounts: map[string]*Account{Key("claude", "a@b.c"): {
			Provider: "claude", Label: "a@b.c", Plan: "max", LastSeenAt: at,
			Quota: &Quota{At: at, Source: "cache", Windows: []snapshot.Window{{Name: "5h", Percent: 12.5, ResetsAt: &reset, Minutes: 300}}},
		}},
		Sessions: map[string]*Session{Key("claude", "s1"): {
			Provider: "claude", Project: "/p", Seen: snapshot.Tokens{Input: 5}, By: map[string]snapshot.Tokens{"a@b.c": {Input: 5}}, Updated: at,
		}},
		Relay: Relay{LastPushAt: at, Pending: true},
	}
	if err := d.SaveState(in); err != nil {
		t.Fatal(err)
	}
	out, err := d.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the state:\n in %+v\nout %+v", in, out)
	}
	checkPrivate(t, d.Path("state.json"))
}

func TestWriteFileReplacesAtomicallyAndPrivately(t *testing.T) {
	d := tempDir(t)
	path := filepath.Join(string(d), "nested", "file.json")
	if err := WriteFile(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("content = %q, %v", b, err)
	}
	checkPrivate(t, path)
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temporary files left behind: %v", names)
	}
}

func TestWriteFileTightensExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("y")); err != nil {
		t.Fatal(err)
	}
	checkPrivate(t, path)
}

func checkPrivate(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %v, want 0600", filepath.Base(path), info.Mode().Perm())
	}
}

func sampleAt(at time.Time, label string) Sample {
	return Sample{At: at, Accounts: []SampleAccount{{Provider: "claude", Label: label, Growth: snapshot.Tokens{Input: 1}}}}
}

func TestSamplesAppendAndLoadAcrossDays(t *testing.T) {
	d := tempDir(t)
	day1 := time.Date(2026, 9, 1, 23, 50, 0, 0, time.UTC)
	times := []time.Time{
		day1,
		day1.Add(20 * time.Minute), // day 2, 00:10
		day1.Add(24 * time.Hour),   // day 2, 23:50
		day1.Add(30 * time.Hour),   // day 3
	}
	// Append out of order: loading sorts.
	for _, i := range []int{3, 0, 2, 1} {
		if err := d.AppendSample(sampleAt(times[i], "l"+string(rune('0'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(d.SamplesDir())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"2026-09-01.jsonl", "2026-09-02.jsonl", "2026-09-03.jsonl"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("day files = %v, want %v", names, want)
	}
	checkPrivate(t, filepath.Join(d.SamplesDir(), "2026-09-02.jsonl"))

	// A damaged line in a day file is skipped, not fatal.
	f, err := os.OpenFile(filepath.Join(d.SamplesDir(), "2026-09-02.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{damaged\n")
	_ = f.Close()

	all, err := d.LoadSamples(day1.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("loaded %d samples, want 4", len(all))
	}
	for i, s := range all {
		if !s.At.Equal(times[i]) {
			t.Fatalf("sample %d at %v, want %v", i, s.At, times[i])
		}
	}

	since := day1.Add(time.Hour) // day 2, 00:50: drops the first two
	some, err := d.LoadSamples(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(some) != 2 || !some[0].At.Equal(times[2]) || !some[1].At.Equal(times[3]) {
		t.Fatalf("LoadSamples(since) = %+v", some)
	}
}

func TestLoadSamplesWithoutDirectory(t *testing.T) {
	got, err := tempDir(t).LoadSamples(time.Time{})
	if err != nil || got != nil {
		t.Fatalf("LoadSamples = %v, %v", got, err)
	}
}

func TestPruneSamplesKeepsRetention(t *testing.T) {
	d := tempDir(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-Retention - 24*time.Hour)
	edge := now.Add(-Retention) // same day as the cutoff: kept
	recent := now.Add(-time.Hour)
	for _, at := range []time.Time{old, edge, recent} {
		if err := d.AppendSample(sampleAt(at, "x")); err != nil {
			t.Fatal(err)
		}
	}
	other := filepath.Join(d.SamplesDir(), "notes.txt")
	if err := os.WriteFile(other, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.PruneSamples(now); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(d.SamplesDir())
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	joined := strings.Join(names, " ")
	if strings.Contains(joined, old.Format(dayLayout)) {
		t.Fatalf("day older than retention kept: %v", names)
	}
	for _, keep := range []string{edge.Format(dayLayout), recent.Format(dayLayout), "notes.txt"} {
		if !strings.Contains(joined, keep) {
			t.Fatalf("%s pruned: %v", keep, names)
		}
	}
	if err := tempDir(t).PruneSamples(now); err != nil {
		t.Fatalf("prune without a samples dir: %v", err)
	}
}
