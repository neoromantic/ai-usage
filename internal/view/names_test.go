package view

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func TestShortNames(t *testing.T) {
	st := emptyState()
	addAccount(st, "claude", "sam@mail.test", true, nil, 10)
	addAccount(st, "claude", "lee@corp.test", false, nil, 10)
	addAccount(st, "codex", "sam@a.io", true, nil, 10)
	addAccount(st, "codex", "sam@b.io", false, nil, 10)
	addAccount(st, "codex", "unknown", false, nil, 10)
	addAccount(st, "grok", "a4c2e917-5e4f-4a3b-8c2d-1e0f9a8b7c6d", true, nil, 10)
	r := Build(newFixture(t, st).in)
	for _, c := range []struct{ provider, label, name string }{
		{"claude", "sam@mail.test", "sam"},
		{"claude", "lee@corp.test", "lee"},
		// The same name within a provider: both keep the full label.
		{"codex", "sam@a.io", "sam@a.io"},
		{"codex", "sam@b.io", "sam@b.io"},
		{"codex", "unknown", "unknown"},
		{"grok", "a4c2e917-5e4f-4a3b-8c2d-1e0f9a8b7c6d", "a4c2e917"},
	} {
		if a := findTeamAccount(t, r, c.provider, c.label); a.Name != c.name || a.Alias != nil {
			t.Errorf("%s %s: name %q alias %v, want %q", c.provider, c.label, a.Name, a.Alias, c.name)
		}
		if a := findAccount(t, r, c.provider, c.label); a.Name != c.name {
			t.Errorf("%s %s: local name %q, want %q", c.provider, c.label, a.Name, c.name)
		}
	}
	for in, want := range map[string]bool{"0f1e2d3c4b5a49688776655443322110": true, "openai-codex": false, "abcdefghijklmnopq": false, "a1b2c3": false} {
		if idLike(in) != want {
			t.Errorf("idLike(%q) = %v", in, !want)
		}
	}
}

func TestAliasesTravelAndTheNewestWins(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "bots@corp.test", true, nil, 10)
	addAccount(st, "codex", "sam@mail.test", false, nil, 10)
	addAccount(st, "claude", "sam@mail.test", false, nil, 10)
	f := newFixture(t, st)
	f.in.Config.Aliases = map[string]state.Alias{
		state.Key("codex", "bots@corp.test"): {Name: "bots-old", At: now.Add(-2 * time.Hour)},
		state.Key("codex", "sam@mail.test"):  {Name: "sp", At: now.Add(-3 * time.Hour)},
	}
	f.seal(now)

	other := emptyState()
	addAccount(other, "codex", "bots@corp.test", true, nil, 10)
	addAccount(other, "codex", "sam@mail.test", true, nil, 10)
	cfg := state.Config{Device: "d-other-device", Aliases: map[string]state.Alias{
		// Newer than this device's: it wins.
		state.Key("codex", "bots@corp.test"): {Name: "build", At: now.Add(-time.Hour)},
		// A newer clearing removes the name.
		state.Key("codex", "sam@mail.test"): {At: now.Add(-time.Hour)},
	}}
	r := withTeam(t, f, docWith(t, f.key, cfg, "otherbox", now, other))
	if a := findTeamAccount(t, r, "codex", "bots@corp.test"); a.Name != "build" || a.Alias == nil || *a.Alias != "build" {
		t.Fatalf("bots = %q %v", a.Name, a.Alias)
	}
	if a := findTeamAccount(t, r, "codex", "sam@mail.test"); a.Name != "sam" || a.Alias != nil {
		t.Fatalf("cleared = %q %v", a.Name, a.Alias)
	}
	// An alias names one provider's account.
	if a := findTeamAccount(t, r, "claude", "sam@mail.test"); a.Name != "sam" {
		t.Fatalf("claude = %q", a.Name)
	}
	if a := findAccount(t, r, "codex", "bots@corp.test"); a.Name != "build" {
		t.Fatalf("local name = %q", a.Name)
	}
	var cols []string
	for _, c := range r.Team.Matrix.Columns {
		cols = append(cols, c.Name)
	}
	if !reflect.DeepEqual(cols, []string{"sam", "build", "sam"}) {
		t.Fatalf("columns = %v", cols)
	}
}

// A name another account of the provider goes by, set before this device
// read that account, does not tell the two apart: both go by the full label.
func TestAliasThatAnotherAccountGoesBy(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "ann@a.io", true, nil, 10)
	addAccount(st, "codex", "ann@b.io", false, nil, 10)
	addAccount(st, "claude", "lee@corp.test", true, nil, 10)
	f := newFixture(t, st)
	f.in.Config.Aliases = map[string]state.Alias{
		state.Key("codex", "ann@a.io"):       {Name: "kim", At: now.Add(-time.Hour)},
		state.Key("claude", "lee@corp.test"): {Name: "Sam", At: now.Add(-time.Hour)},
	}
	f.seal(now)
	other := emptyState()
	addAccount(other, "codex", "kim@corp.test", true, nil, 10)
	addAccount(other, "claude", "sam@mail.test", true, nil, 10)
	r := withTeam(t, f, otherDoc(t, f.key, "d-other-device", "otherbox", now, other))

	for _, c := range []struct{ provider, label, name string }{
		{"codex", "ann@a.io", "ann@a.io"},
		{"codex", "kim@corp.test", "kim@corp.test"},
		// The alias leaves ann@b.io the only ann.
		{"codex", "ann@b.io", "ann"},
		// Names differ only in case: the alias command refuses that too.
		{"claude", "lee@corp.test", "lee@corp.test"},
		{"claude", "sam@mail.test", "sam@mail.test"},
	} {
		if a := findTeamAccount(t, r, c.provider, c.label); a.Name != c.name {
			t.Errorf("%s %s: name %q, want %q", c.provider, c.label, a.Name, c.name)
		}
	}
	if a := findTeamAccount(t, r, "codex", "ann@a.io"); a.Alias == nil || *a.Alias != "kim" {
		t.Fatalf("alias = %v", a.Alias)
	}
	seen := map[string]bool{}
	for _, c := range r.Team.Matrix.Columns {
		k := c.Provider + ":" + strings.ToLower(c.Name)
		if seen[k] {
			t.Fatalf("two columns go by %s: %+v", k, r.Team.Matrix.Columns)
		}
		seen[k] = true
	}
}

// Two accounts the team gave one name go by their full labels on the page
// too: in SUBSCRIPTIONS, in ATTENTION, and in USAGE on a team of one.
func TestAliasThatTwoAccountsGoByOnThePage(t *testing.T) {
	out := func() *state.Quota {
		return &state.Quota{At: now, Source: "harness", Windows: []snapshot.Window{week7(100, now.Add(48*time.Hour))}}
	}
	st := emptyState()
	addAccount(st, "codex", "ann@acme.dev", true, out(), 10)
	f := newFixture(t, st)
	f.in.Config.Aliases = map[string]state.Alias{state.Key("codex", "ann@acme.dev"): {Name: "kim", At: now.Add(-time.Hour)}}
	f.seal(now)
	other := emptyState()
	addAccount(other, "codex", "sam@mail.test", true, out(), 10)
	cfg := state.Config{Device: "d-other-device", Aliases: map[string]state.Alias{
		state.Key("codex", "sam@mail.test"): {Name: "kim", At: now.Add(-2 * time.Hour)},
	}}
	two := withTeam(t, f, docWith(t, f.key, cfg, "otherbox", now, other))

	// On one device, the alias came before the account that goes by it.
	addAccount(st, "codex", "kim@corp.test", false, nil, 10)
	f.seal(now)
	one := withTeam(t, f)

	for _, c := range []struct {
		r      Report
		title  string
		labels []string
	}{
		{two, "ATTENTION", []string{"ann@acme.dev", "sam@mail.test"}},
		{two, "SUBSCRIPTIONS", []string{"ann@acme.dev", "sam@mail.test"}},
		{one, "USAGE", []string{"ann@acme.dev", "kim@corp.test"}},
	} {
		// A section goes on to the next title, the next line that starts
		// with a capital.
		var lines []string
		in := false
		for _, l := range Render(c.r, Options{Width: 160, Loc: sampleZone}).Body {
			l = sgr.ReplaceAllString(l, "")
			if l != "" && l[0] >= 'A' && l[0] <= 'Z' {
				in = strings.HasPrefix(l, c.title)
			}
			if in {
				lines = append(lines, l)
			}
		}
		text := strings.Join(lines, "\n")
		for _, l := range c.labels {
			if !strings.Contains(text, l) {
				t.Errorf("%s does not name %s:\n%s", c.title, l, text)
			}
		}
		if slices.Contains(strings.Fields(text), "kim") {
			t.Errorf("%s names an account kim:\n%s", c.title, text)
		}
	}
}

// A name the alias command would refuse, which only another build can seal,
// is not shown. Of two names set at once, the smaller device id's wins, as
// the alias command picks, and an email matches in any case.
func TestAliasesTheReportRefuses(t *testing.T) {
	st := emptyState()
	addAccount(st, "codex", "bots@corp.test", true, nil, 10)
	addAccount(st, "claude", "sam@mail.test", true, nil, 10)
	f := newFixture(t, st)
	at := now.Add(-time.Hour)
	doc := func(device string, aliases map[string]state.Alias) snapshot.Doc {
		return docWith(t, f.key, state.Config{Device: device, Aliases: aliases}, device, now, emptyState())
	}
	r := withTeam(t, f,
		doc("d-device-2", map[string]state.Alias{state.Key("codex", "bots@corp.test"): {Name: "second", At: at}}),
		doc("d-device-1", map[string]state.Alias{state.Key("codex", "bots@corp.test"): {Name: "first", At: at}}),
		doc("d-device-0", map[string]state.Alias{
			state.Key("codex", "bots@corp.test"): {Name: "bad name", At: now},
			state.Key("claude", "Sam@Mail.test"): {Name: "sammy", At: at},
		}),
	)
	if a := findTeamAccount(t, r, "codex", "bots@corp.test"); a.Name != "first" {
		t.Fatalf("bots = %q", a.Name)
	}
	if a := findTeamAccount(t, r, "claude", "sam@mail.test"); a.Name != "sammy" {
		t.Fatalf("sam = %q", a.Name)
	}
}
