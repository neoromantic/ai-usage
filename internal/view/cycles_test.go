package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

func period(window string, reset time.Time, pct float64, last time.Time) snapshot.Cycle {
	return snapshot.Cycle{Window: window, ResetsAt: reset, Percent: pct, LastReadAt: last}
}

// cyclesDoc is another device's published snapshot, on version, with the
// quota cycles cs when version sends them.
func cyclesDoc(t *testing.T, key *team.Key, device, version string, at time.Time, st *state.State, cs collect.Cycles) snapshot.Doc {
	t.Helper()
	doc := collect.BuildDoc(st, cs, key, state.Config{Device: device}, collect.Repos{}, "otherbox", "kim", version, at)
	if collect.BeforeCycles(version) {
		doc = doc.WithoutCycles()
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	back, err := snapshot.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return back
}

// cyclesFixture is a team of this device and another on version, which
// both read claude ann's weekly window; this device also reads its 5-hour
// window and codex bob's, which Codex reset early.
func cyclesFixture(t *testing.T, version string) *fixture {
	day := 24 * time.Hour
	st := emptyState()
	addAccount(st, "claude", "ann", true, &state.Quota{At: now, Source: "cache", Windows: []snapshot.Window{week7(30, now.Add(2*day))}}, 10)
	addAccount(st, "codex", "bob", true, codexQuota(now, 5), 10)
	addHermes(st, "openai-codex", "codex", "bob", 5)
	f := newFixture(t, st)
	ann := []snapshot.Cycle{
		// The week now, which has not ended.
		period("7d", now.Add(2*day), 30, now),
		// Last read 2 days before it reset, here; the other device read it
		// to its end.
		period("7d", now.Add(-5*day), 60, now.Add(-7*day)),
		period("7d", now.Add(-12*day), 100, now.Add(-13*day)),
	}
	for i := range 16 {
		reset := now.Add(-time.Duration(i)*5*time.Hour - time.Hour)
		ann = append(ann, period("5h", reset, float64(10*(i%11)), reset.Add(-20*time.Minute)))
	}
	f.cycles = collect.Cycles{
		collect.HistoryKey("claude", "ann"): ann,
		collect.HistoryKey("codex", "bob"): {
			// Codex began a new week before this one's reset: it ended,
			// last read a day before the new one began.
			period("7d", now.Add(5*day), 5, now),
			period("7d", now.Add(day), 45, now.Add(-3*day)),
			// And one read until the next began.
			period("7d", now.Add(-4*day), 70, now.Add(-6*day-time.Hour)),
		},
	}
	f.seal(now)
	other := emptyState()
	addAccount(other, "claude", "ann", false, nil, 10)
	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{
		cyclesDoc(t, f.key, "d-other-device", version, now.Add(-time.Hour), other, collect.Cycles{
			collect.HistoryKey("claude", "ann"): {
				period("7d", now.Add(-5*day+30*time.Second), 85, now.Add(-5*day-time.Hour)),
				period("7d", now.Add(-19*day), 40, now.Add(-19*day-time.Hour)),
				// Older than the 90 days a device keeps.
				period("7d", now.Add(-96*day), 90, now.Add(-96*day-time.Hour)),
			},
		}),
	}}
	return f
}

// TestUtilization: each subscription's fullest percent before each reset,
// newest first, from this device's cycles and the team's: of a week, the
// fuller of two readings of one reset, and the one read to its end; not the
// week now, which has not ended, nor one older than 90 days; of a window
// shorter than a day, the last 12 that ended, without the ones read only
// empty; and the week Codex replaced early, partly read. Hermes, which
// borrows a reading, has none of its own.
func TestUtilization(t *testing.T) {
	r := Build(cyclesFixture(t, "v0.5.0").in)
	ann, bob := findTeamAccount(t, r, "claude", "ann"), findTeamAccount(t, r, "codex", "bob")
	if h := findTeamAccount(t, r, "hermes", "openai-codex"); h.Utilization == nil || len(h.Utilization) != 0 {
		t.Errorf("hermes: %+v", h.Utilization)
	}
	b, err := json.MarshalIndent(struct {
		HeldBackBy []string      `json:"cycles_held_back_by"`
		Reason     *string       `json:"cycles_reason"`
		Claude     []Utilization `json:"claude_ann"`
		Codex      []Utilization `json:"codex_bob"`
	}{r.Team.CyclesHeldBackBy, r.Team.CyclesReason, ann.Utilization, bob.Utilization}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "utilization.json.golden", string(b)+"\n")
}

// golden compares got with testdata/name, which -update rewrites.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs; run go test ./internal/view -update and review the diff\ngot:\n%s", name, got)
	}
}

// The team gets only this device's cycles while a device runs a release
// before v0.5.0, or the relay refused them, and the report says why.
func TestCyclesHeldBack(t *testing.T) {
	r := Build(cyclesFixture(t, "v0.4.0").in)
	if got := r.Team.CyclesHeldBackBy; len(got) != 1 || got[0] != "kim@otherbox" {
		t.Fatalf("held back by %v", got)
	}
	if r.Team.CyclesReason == nil || *r.Team.CyclesReason != "kim@otherbox runs v0.4.0: it should update within 15 minutes" {
		t.Fatalf("reason %v", r.Team.CyclesReason)
	}
	// The cycles a device on v0.4.0 never sends are this device's alone.
	if c := findTeamAccount(t, r, "claude", "ann").Utilization[0].Cycles; len(c) != 2 || c[0].Percent != 60 || !c[0].Partial {
		t.Fatalf("weekly cycles: %+v", c)
	}
	text := plainText(r, Options{Width: 120, Loc: time.UTC})
	if !strings.Contains(text, "  only this device's cycles: kim@otherbox runs v0.4.0: it should update within 15 minutes\n") {
		t.Fatalf("the page does not say why:\n%s", text)
	}

	f := cyclesFixture(t, "v0.5.0")
	f.in.State.Relay.NoCyclesUntil = now.Add(20 * time.Hour)
	f.in.Loc = time.UTC
	r = Build(f.in)
	if r.Team.CyclesReason == nil || *r.Team.CyclesReason != "the relay did not accept cycles; trying again Sep 2 08:00" || len(r.Team.CyclesHeldBackBy) != 0 {
		t.Fatalf("refused: %v %v", r.Team.CyclesHeldBackBy, r.Team.CyclesReason)
	}

	// Without a team there is no one to keep them from.
	f = cyclesFixture(t, "v0.4.0")
	f.in.Team = collect.TeamCache{}
	if r = Build(f.in); r.Team.CyclesReason != nil || r.Team.CyclesHeldBackBy == nil {
		t.Fatalf("no team: %v %v", r.Team.CyclesHeldBackBy, r.Team.CyclesReason)
	}
}
