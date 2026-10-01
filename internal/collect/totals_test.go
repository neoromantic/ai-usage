package collect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func TestTotals(t *testing.T) {
	st := ledger()
	got := Totals(st, nil)
	var order []string
	for _, a := range got {
		order = append(order, a.Provider+"/"+a.Label)
	}
	// Providers in display order, the logged-in account first; accounts with
	// no usage, no quota, and no login are left out.
	want := []string{"claude/ann@example.com", "claude/old@example.com", "hermes/openrouter"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	ann := got[0]
	if !ann.Current || ann.Plan != "max" || ann.Quota == nil || ann.Sessions != 3 || ann.Tokens != tok(405) {
		t.Fatalf("ann = %+v", ann)
	}
	if !ann.LastActive.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("last active = %v", ann.LastActive)
	}
	var projects []string
	for _, p := range ann.Projects {
		projects = append(projects, fmt.Sprintf("%s %d %d", p.Path, p.Sessions, p.Tokens.Total()))
	}
	if want := []string{"/work/web 1 330", "/work/api 2 115"}; !reflect.DeepEqual(projects, want) {
		t.Fatalf("projects = %v, want %v", projects, want)
	}
	if old := got[1]; old.Current || old.Sessions != 1 || old.Tokens != tok(40) {
		t.Fatalf("old = %+v", old)
	}
}

func TestSortAccounts(t *testing.T) {
	in := []AccountTotals{
		{Provider: "hermes", Label: "a", usage: usage{Tokens: tok(1000)}},
		{Provider: "claude", Label: "b", usage: usage{Tokens: tok(1)}},
		{Provider: "claude", Label: "c", usage: usage{Tokens: tok(50)}},
		{Provider: "claude", Label: "d", usage: usage{Tokens: tok(1)}, Current: true},
		{Provider: "claude", Label: "a", usage: usage{Tokens: tok(1)}},
		{Provider: "codex", Label: "x"},
	}
	SortAccounts(in)
	var got []string
	for _, a := range in {
		got = append(got, a.Provider+"/"+a.Label)
	}
	want := []string{"claude/d", "claude/c", "claude/a", "claude/b", "codex/x", "hermes/a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestTotalsWeighAndWindowTheHours: a project's burn weighs each session by
// its harness, only the hours the ledger kept at its last run count, and the
// sessions that spent in them are counted with their activity, their replies
// without a final count, and whether Claude Code counted over twice their
// transcripts' output.
func TestTotalsWeighAndWindowTheHours(t *testing.T) {
	h := logs.HourOf(t0)
	old := logs.HourOf(t0.Add(-state.Retention - 2*time.Hour))
	st := &state.State{LastRunAt: t0, Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
		state.Key("claude", "a4c2e917"): {Provider: "claude", Project: "/work/app", Updated: t0.Add(-time.Hour), Nulls: 3,
			Side: snapshot.Tokens{Output: 5000},
			By:   map[string]snapshot.Tokens{"ann@acme.dev": {Input: 100, Output: 7000, CacheRead: 10000}},
			U:    meter.Hours{old: {Input: 50}, h - 1: {Input: 50, Output: 7000, CacheRead: 10000}}},
		state.Key("codex", "c1"): {Provider: "codex", Project: "/work/app", Updated: t0,
			By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 200, Output: 100, CacheRead: 1000}},
			U:  meter.Hours{h: {Input: 200, Output: 100, CacheRead: 1000}}},
		// Only hours the ledger no longer keeps: not counted.
		state.Key("codex", "c0"): {Provider: "codex", Project: "/work/app", Updated: t0,
			By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 9}},
			U:  meter.Hours{old: {Input: 9}}},
	}}
	ps := Projects(st, nil)
	if len(ps) != 1 {
		t.Fatalf("projects = %+v", ps)
	}
	p := ps[0]
	// claude: 50 + 7000×5 + 10000×0.1; codex: 200 + 100×8 + 1000×0.1.
	if want := map[int64]int64{h - 1: 36050, h: 1100}; !reflect.DeepEqual(p.Burn, want) {
		t.Errorf("burn = %v, want %v", p.Burn, want)
	}
	if p.Sessions != 2 || p.Tokens != (snapshot.Tokens{Input: 250, Output: 7100, CacheRead: 11000}) {
		t.Errorf("project = %d sessions, %+v", p.Sessions, p.Tokens)
	}
	// Claude Code counted 5000 output beyond a transcript of 2000.
	if len(p.Active) != 2 || p.Nulls != 3 || p.OverTracked != 1 {
		t.Errorf("active %v, nulls %d, over-tracked %d", p.Active, p.Nulls, p.OverTracked)
	}
	if a := totalsFor(t, st, "claude", "ann@acme.dev"); a.OverTracked != 1 || a.Nulls != 3 || !a.Active[0].Equal(t0.Add(-time.Hour)) {
		t.Errorf("claude = %+v", a.usage)
	}
	// 5000 beyond a transcript of 7000 is not over twice it.
	st.Sessions[state.Key("claude", "a4c2e917")].By["ann@acme.dev"] = snapshot.Tokens{Input: 100, Output: 12000}
	if a := totalsFor(t, st, "claude", "ann@acme.dev"); a.OverTracked != 0 {
		t.Errorf("claude = %+v", a.usage)
	}
	if a := totalsFor(t, st, "codex", "ann@acme.dev"); a.Sessions != 1 || a.OverTracked != 0 || a.Burn[h] != 1100 {
		t.Errorf("codex = %+v", a.usage)
	}
	if got := Coverage(st); !got["claude"].Equal(logs.HourStart(h-1)) || !got["codex"].Equal(logs.HourStart(h)) {
		t.Errorf("coverage = %v", got)
	}
}

// TestCoverageAfterMigration: a ledger migrated from v0.3 covers from the
// first hour it kept.
func TestCoverageAfterMigration(t *testing.T) {
	dir := state.Dir(t.TempDir())
	legacy := map[string]any{
		"ledger":      0,
		"last_run_at": logs.HourStart(496000 + 30*24),
		"sessions": map[string]any{state.Key("claude", "a4c2e917"): map[string]any{
			"p": "claude", "proj": "/work/app", "at": logs.HourStart(496000 + 24),
			"by": map[string]snapshot.Tokens{"ann@acme.dev": {Input: 100, Output: 900, CacheRead: 5000}},
			"h":  map[int64]int64{496000: 300, 496024: 700},
		}},
	}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(string(dir), "state.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := dir.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got := Coverage(st)["claude"]; !got.Equal(logs.HourStart(496000)) {
		t.Errorf("covered since %v, want %v", got, logs.HourStart(496000))
	}
	if a := totalsFor(t, st, "claude", "ann@acme.dev"); a.Tokens != (snapshot.Tokens{Input: 100, Output: 900, CacheRead: 5000}) {
		t.Errorf("migrated totals = %+v", a.Tokens)
	}
}

// A session is active when it last grew, no later than its newest hour: a
// harness that dates a session by when it was read does not make it active
// in a period it spent nothing in.
func TestSessionIsActiveWithinItsHours(t *testing.T) {
	spent := logs.HourOf(t0.Add(-72 * time.Hour))
	st := &state.State{LastRunAt: t0, Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
		state.Key("grok", "g1"): {Provider: "grok", Project: "/work/app", Updated: t0,
			By:   map[string]snapshot.Tokens{"ann@acme.dev": {Input: 100}},
			Last: map[string]time.Time{"ann@acme.dev": t0},
			U:    meter.Hours{spent: {Input: 100}}},
	}}
	if a := totalsFor(t, st, "grok", "ann@acme.dev"); len(a.Active) != 1 || !a.Active[0].Equal(logs.HourStart(spent).Add(30*time.Minute)) {
		t.Fatalf("active = %v, want in the hour it spent in", a.Active)
	}
}
