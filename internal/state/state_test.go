package state

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
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
	unlock, err := d.Lock(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Lock(10 * time.Minute); err == nil {
		t.Fatal("second lock succeeded while the first is held")
	}
	unlock()
	unlock2, err := d.Lock(10 * time.Minute)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
	if _, err := os.Stat(d.Path("run.lock")); !os.IsNotExist(err) {
		t.Fatalf("lock file left behind: %v", err)
	}
}

func TestLockTakesOverStaleLock(t *testing.T) {
	d := tempDir(t)
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		t.Fatal(err)
	}
	path := d.Path("run.lock")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	unlock, err := d.Lock(10 * time.Minute)
	if err != nil {
		t.Fatalf("stale lock was not taken over: %v", err)
	}
	unlock()
}

func TestLockReleaseKeepsLockTakenOverByLaterRun(t *testing.T) {
	d := tempDir(t)
	unlockSlow, err := d.Lock(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// The slow run outlives the stale limit and a later run takes over.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(d.Path("run.lock"), old, old); err != nil {
		t.Fatal(err)
	}
	unlockLater, err := d.Lock(10 * time.Minute)
	if err != nil {
		t.Fatalf("take over: %v", err)
	}
	unlockSlow()
	if _, err := d.Lock(10 * time.Minute); err == nil {
		t.Fatal("the slow run's release removed the later run's lock")
	}
	unlockLater()
	unlock, err := d.Lock(10 * time.Minute)
	if err != nil {
		t.Fatalf("lock after the later run released: %v", err)
	}
	unlock()
}

// A crashed run's leftover lock, stamped before the clock went back, must not
// hold off every run until the clock catches up.
func TestLockTakesOverLockFromTheFuture(t *testing.T) {
	d := tempDir(t)
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		t.Fatal(err)
	}
	path := d.Path("run.lock")
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, ahead, ahead); err != nil {
		t.Fatal(err)
	}
	unlock, err := d.Lock(10 * time.Minute)
	if err != nil {
		t.Fatalf("lock from the future was not taken over: %v", err)
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
