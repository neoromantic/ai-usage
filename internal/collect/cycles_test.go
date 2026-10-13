package collect

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// read is an account's quota reading at at in a sample.
func read(provider, label string, at time.Time, ws ...snapshot.Window) state.SampleAccount {
	return state.SampleAccount{Provider: provider, Label: label, QuotaAt: &at, Windows: ws}
}

func win(name string, pct float64, reset time.Time) snapshot.Window {
	return snapshot.Window{Name: name, Percent: pct, ResetsAt: &reset}
}

func cycle(window string, reset time.Time, pct float64, last time.Time) snapshot.Cycle {
	return snapshot.Cycle{Window: window, ResetsAt: reset, Percent: pct, LastReadAt: last}
}

func samplesOf(at time.Time, accts ...state.SampleAccount) state.Sample {
	return state.Sample{At: at, Accounts: accts}
}

func TestFindCycles(t *testing.T) {
	h := time.Hour
	day := 24 * h
	w1 := t0.Add(-7 * day) // a week that ended
	w2 := t0.Add(2 * day)  // the week now
	ann := func(at time.Time, ws ...snapshot.Window) state.SampleAccount {
		return read("claude", "ann@acme.dev", at, ws...)
	}
	samples := []state.Sample{
		// No account read: a sample of growth alone, or of nothing.
		{At: w1.Add(-6 * day)},
		samplesOf(w1.Add(-6*day), ann(w1.Add(-6*day), win("7d", 30, w1), win("5h", 50, w1.Add(-6*day+2*h)))),
		// The same reading again, as each sample of the day repeats it.
		samplesOf(w1.Add(-6*day+15*time.Minute), ann(w1.Add(-6*day), win("7d", 30, w1), win("5h", 50, w1.Add(-6*day+2*h)))),
		// The reset a second later, and one read from a log rounded to the
		// minute: the same week.
		samplesOf(w1.Add(-3*day), ann(w1.Add(-3*day), win("7d", 80, w1.Add(time.Second)))),
		samplesOf(w1.Add(-2*day), ann(w1.Add(-2*day), win("7d", 100, w1.Add(-40*time.Second)))),
		// Usage taken back, 30 points down in the same week: the week
		// still ran out.
		samplesOf(w1.Add(-day), ann(w1.Add(-day), win("7d", 70, w1))),
		// A reading taken after the reset it names is of the week it names.
		samplesOf(w1.Add(time.Hour), ann(w1.Add(time.Minute), win("7d", 72, w1))),
		// The next week, read once, early on, then again now.
		samplesOf(w2.Add(-6*day), ann(w2.Add(-6*day), win("7d", 4, w2))),
		samplesOf(t0, ann(t0, win("7d", 41, w2)),
			// The same login under another case of its email.
			read("claude", "Ann@Acme.dev", t0.Add(-time.Minute), win("7d sonnet", 12, w2)),
			// Another account the login switched to.
			read("claude", "bob@acme.dev", t0, win("7d", 9, w2.Add(-day))),
			// Hermes reads another login's quota.
			read("hermes", "openai-codex", t0, win("5h", 60, t0.Add(2*h))),
			// An account no log names, and one with no label.
			read("claude", UnknownAccount, t0, win("7d", 50, w2)),
			read("claude", "", t0, win("7d", 50, w2)),
			// A window without a reset time, and an account not read.
			read("codex", "ann@acme.dev", t0, snapshot.Window{Name: "5h", Percent: 20}),
			state.SampleAccount{Provider: "grok", Label: "ann@acme.dev", Windows: []snapshot.Window{win("credits", 10, w2)}},
		),
	}
	// An idle Codex window: each reading moves its reset, and reads it
	// empty, until it is used.
	for i := range 8 {
		at := t0.Add(-4*h + time.Duration(i)*15*time.Minute)
		pct := 0.0
		reset := at.Add(5 * h)
		if i >= 6 {
			pct, reset = float64(i), t0.Add(-4*h+5*15*time.Minute+5*h)
		}
		samples = append(samples, samplesOf(at, read("codex", "ann@acme.dev", at, win("5h", pct, reset))))
	}
	got := FindCycles(samples)
	want := Cycles{
		HistoryKey("claude", "ann@acme.dev"): {
			cycle("5h", w1.Add(-6*day+2*h), 50, w1.Add(-6*day)),
			cycle("7d", w2, 41, t0),
			cycle("7d", w1.Add(time.Second), 100, w1),
			cycle("7d sonnet", w2, 12, t0.Add(-time.Minute)),
		},
		HistoryKey("claude", "bob@acme.dev"): {cycle("7d", w2.Add(-day), 9, t0)},
		HistoryKey("codex", "ann@acme.dev"): {
			cycle("5h", t0.Add(-4*h+5*15*time.Minute+5*h), 7, t0.Add(-4*h+7*15*time.Minute)),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cycles:\n%s\nwant\n%s", dumpCycles(got), dumpCycles(want))
	}
}

func dumpCycles(cs Cycles) string {
	var b strings.Builder
	for k, l := range cs {
		fmt.Fprintf(&b, "%q\n", k)
		for _, c := range l {
			fmt.Fprintf(&b, "  %s %s %v %s\n", c.Window, c.ResetsAt.Format(time.RFC3339), c.Percent, c.LastReadAt.Format(time.RFC3339))
		}
	}
	return b.String()
}

// The team's cycles are the fullest reading of each period any device read,
// by window, newest first.
func TestMergeCycles(t *testing.T) {
	r := t0.Add(-24 * time.Hour)
	here := []snapshot.Cycle{cycle("7d", r, 60, r.Add(-48*time.Hour)), cycle("7d", r.Add(-7*24*time.Hour), 90, r.Add(-8*24*time.Hour))}
	there := []snapshot.Cycle{
		cycle("7d", r.Add(2*time.Second), 100, r.Add(-time.Hour)),
		cycle("5h", r.Add(-time.Hour), 30, r.Add(-2*time.Hour)),
		// A period read only empty.
		cycle("5h", r.Add(-6*time.Hour), 0, r.Add(-7*time.Hour)),
	}
	got := MergeCycles(here, there)
	want := []snapshot.Cycle{
		cycle("5h", r.Add(-time.Hour), 30, r.Add(-2*time.Hour)),
		cycle("7d", r.Add(2*time.Second), 100, r.Add(-time.Hour)),
		cycle("7d", r.Add(-7*24*time.Hour), 90, r.Add(-8*24*time.Hour)),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged = %+v", got)
	}
	// Resets a few minutes apart, one after another, are one period; ten
	// minutes apart are two.
	chain := MergeCycles([]snapshot.Cycle{
		cycle("5h", r, 10, r.Add(-time.Hour)),
		cycle("5h", r.Add(4*time.Minute), 20, r.Add(-time.Hour)),
		cycle("5h", r.Add(8*time.Minute), 15, r.Add(-30*time.Minute)),
		cycle("5h", r.Add(18*time.Minute), 5, r.Add(-30*time.Minute)),
	})
	if len(chain) != 2 || chain[1].Percent != 20 || !chain[1].ResetsAt.Equal(r.Add(8*time.Minute)) || !chain[1].LastReadAt.Equal(r.Add(-30*time.Minute)) {
		t.Fatalf("chain = %+v", chain)
	}
	if MergeCycles() != nil {
		t.Fatal("nothing merged to something")
	}
}

// A snapshot keeps every period of a window a day long or longer that the
// samples hold, the newest of a shorter one, and no more than it may.
func TestFindCyclesBounds(t *testing.T) {
	var samples []state.Sample
	for i := range 90 {
		at := t0.Add(-time.Duration(i) * 24 * time.Hour)
		reset5h := at.Add(time.Hour)
		samples = append(samples, samplesOf(at,
			read("claude", "ann@acme.dev", at, snapshot.Window{Name: "5h", Percent: 40, ResetsAt: &reset5h, Minutes: 300},
				win("7d", 50, t0.Add(-time.Duration(i/7)*7*24*time.Hour)))))
	}
	got := FindCycles(samples)[HistoryKey("claude", "ann@acme.dev")]
	n := map[string]int{}
	for _, c := range got {
		n[c.Window]++
	}
	if n["5h"] != ShortCycles || n["7d"] != 13 {
		t.Fatalf("kept %v", n)
	}
	if got[0].Window != "5h" || !got[0].ResetsAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("not the newest first: %+v", got[0])
	}

	// More windows than a snapshot holds: the window with the most loses
	// its oldest first.
	samples = samples[:0]
	for i := range 40 {
		at := t0.Add(-time.Duration(i) * 24 * time.Hour)
		samples = append(samples, samplesOf(at, read("claude", "ann@acme.dev", at,
			win("1d", 50, at.Add(time.Hour)), win("2d", 50, at.Add(2*time.Hour)))))
	}
	got = FindCycles(samples)[HistoryKey("claude", "ann@acme.dev")]
	n = map[string]int{}
	for _, c := range got {
		n[c.Window]++
	}
	if len(got) != snapshot.MaxCycles || n["1d"] != 32 || n["2d"] != 32 {
		t.Fatalf("kept %v", n)
	}
	if last := got[len(got)-1]; !last.ResetsAt.Equal(t0.Add(-31*24*time.Hour + 2*time.Hour)) {
		t.Fatalf("oldest kept: %+v", last)
	}
}

func TestLoadCycles(t *testing.T) {
	d := state.Dir(t.TempDir())
	if cs, err := LoadCycles(d, t0); err != nil || len(cs) != 0 {
		t.Fatalf("no samples: %v, %v", cs, err)
	}
	reset := t0.Add(-time.Hour)
	for _, at := range []time.Time{t0.Add(-91 * 24 * time.Hour), t0.Add(-3 * time.Hour)} {
		if err := d.AppendSample(samplesOf(at, read("claude", "ann@acme.dev", at, win("5h", 70, reset)))); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := LoadCycles(d, t0)
	if err != nil || !reflect.DeepEqual(cs, Cycles{HistoryKey("claude", "ann@acme.dev"): {cycle("5h", reset, 70, t0.Add(-3*time.Hour))}}) {
		t.Fatalf("cycles = %v, %v", cs, err)
	}
}
