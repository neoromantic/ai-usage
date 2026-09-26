package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

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
