package view

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

func TestAttention(t *testing.T) {
	start := now.Add(-4 * 24 * time.Hour) // 4/7 of the week gone
	weekly := func(pct float64) snapshot.Window { return week7(pct, start.Add(week)) }
	st := emptyState()
	addAccount(st, "codex", "sam@mail.test", true, &state.Quota{At: now, Windows: []snapshot.Window{weekly(100)}}, 10)
	addAccount(st, "claude", "sam@mail.test", true, &state.Quota{At: now.Add(-30 * time.Hour), Windows: []snapshot.Window{
		weekly(20), {Name: "7d Fable", Percent: 100, Minutes: 10080, ResetsAt: tp(start.Add(week))},
	}}, 10)
	addAccount(st, "codex", "kim@mail.test", false, &state.Quota{At: now, Windows: []snapshot.Window{weekly(80)}}, 10)
	addAccount(st, "codex", "bots@corp.test", false, &state.Quota{At: now, Windows: []snapshot.Window{weekly(20)}}, 10)
	addAccount(st, "codex", "fine@a.io", false, &state.Quota{At: now, Windows: []snapshot.Window{weekly(50)}}, 10)
	f := newFixture(t, st)
	f.in.State.Update.Latest = "v1.2.3"

	broken := emptyState()
	broken.Sources["codex"] = state.Source{Status: "error", Error: "app-server exited without answering"}
	// A silent device's error is the one it last reported.
	quiet := emptyState()
	quiet.Sources["codex"] = state.Source{Status: "partial", Error: "codex: not logged in"}
	r := withTeam(t, f,
		otherDoc(t, f.key, "d-broken-device", "Mac.localdomain", now.Add(-10*time.Minute), broken),
		otherDoc(t, f.key, "d-quiet-device", "leebook", now.Add(-50*time.Hour), quiet),
	)
	var got []string
	for _, a := range r.Attention {
		s := a.Kind + " " + a.Provider + " " + a.Name
		if a.Window != "" {
			s += " " + a.Window
		}
		if len(a.Devices) > 0 {
			s += " " + strings.Join(a.Devices, ",")
		}
		got = append(got, strings.TrimSpace(strings.Join(strings.Fields(s), " ")))
	}
	want := []string{
		"out claude sam 7d Fable",
		"out codex sam",
		"over codex kim",
		"error Mac.localdomain",
		"silent leebook",
		"old Mac.localdomain,leebook",
		"under codex bots",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A full window stays full until it resets, so its age is no news.
	fable := r.Attention[0]
	if fable.At == nil || !fable.At.Equal(start.Add(week)) || fable.ReadingAge != 0 || fable.Account != "sam@mail.test" {
		t.Fatalf("fable = %+v", fable)
	}
	over := r.Attention[2]
	if over.Percent == nil || *over.Percent != 140 || over.At == nil || over.ResetsAt == nil || !over.At.Before(*over.ResetsAt) || over.ReadingAge != 0 {
		t.Fatalf("over = %+v", over)
	}
	if e := r.Attention[3]; e.Message != "codex: app-server exited without answering" {
		t.Fatalf("error = %+v", e)
	}
	if s := r.Attention[4]; s.At == nil || !s.At.Equal(now.Add(-50*time.Hour)) || s.Message != "codex: not logged in" {
		t.Fatalf("silent = %+v", s)
	}
	if o := r.Attention[5]; o.Message != "v1.2.3" {
		t.Fatalf("old = %+v", o)
	}
	if u := r.Attention[6]; u.Percent == nil || *u.Percent != 35 || u.ResetsAt == nil {
		t.Fatalf("under = %+v", u)
	}
	// Nothing wrong, nothing to say.
	if r := Build(newFixture(t, emptyState()).in); len(r.Attention) != 0 || r.Attention == nil {
		t.Fatalf("attention = %#v", r.Attention)
	}
}

// An OVER window at exactly 100% runs out at its reset, and comes before one
// that runs out after that reset, although it has no time of its own to run
// out.
func TestAttentionOverAtResetOrder(t *testing.T) {
	st := emptyState()
	// At 140%, with a day of its week gone: it runs out in 4 days, 2 days
	// before its own reset.
	addAccount(st, "codex", "ann@acme.dev", false, &state.Quota{At: now, Windows: []snapshot.Window{week7(20, now.Add(6*24*time.Hour))}}, 10)
	// At 100%, with half its week gone: it runs out at its reset, in 3.5
	// days.
	reset := now.Add(week / 2)
	addAccount(st, "codex", "kim@mail.test", false, &state.Quota{At: now, Windows: []snapshot.Window{week7(50, reset)}}, 10)
	r := Build(newFixture(t, st).in)
	var got []string
	for _, a := range r.Attention {
		got = append(got, a.Kind+" "+a.Account)
	}
	if want := []string{"over kim@mail.test", "over ann@acme.dev"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("attention = %v, want %v", got, want)
	}
	kim, ann := r.Attention[0], r.Attention[1]
	if kim.At != nil || kim.ResetsAt == nil || !kim.ResetsAt.Equal(reset) || *kim.Percent != 100 {
		t.Fatalf("kim = %+v", kim)
	}
	if ann.At == nil || !ann.At.Equal(now.Add(4*24*time.Hour)) || *ann.Percent != 140 {
		t.Fatalf("ann = %+v", ann)
	}
	lines := pageSection(Render(r, Options{Width: 120, Loc: time.UTC}), "ATTENTION")
	want := []string{
		" OVER  codex kim@mail.test  runs out ~Sat 00:00 at this week's pace, at its reset",
		" OVER  codex ann@acme.dev   runs out ~Sat 12:00 at this week's pace, 2d before reset",
	}
	if len(lines) != 3 || !reflect.DeepEqual(lines[1:], want) {
		t.Fatalf("ATTENTION =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// The header's failing relay and update check have their errors in
// ATTENTION, as this device's, although the run that met them read every
// source and counts as a success.
func TestHeaderFailuresAreInAttention(t *testing.T) {
	st := emptyState()
	st.Relay = state.Relay{LastPushAt: now.Add(-time.Hour), Pending: true, LastError: "relay: service unavailable (HTTP 503)", LastErrorAt: now}
	st.LastError, st.LastErrorAt = st.Relay.LastError, now
	st.Update = state.Update{CheckedAt: now, Error: "cannot write beside /opt/bin/ai-usage: permission denied"}
	f := newFixture(t, st)
	r := Build(f.in)
	got := attentionList(r)
	want := []string{
		"error thisbox relay: service unavailable (HTTP 503)",
		"error thisbox update: cannot write beside /opt/bin/ai-usage: permission denied",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	page := plainText(r, Options{Width: 120, Loc: time.UTC})
	for _, s := range []string{
		"● relay failing  ● update check failed\n",
		"\n ERROR  thisbox  relay: service unavailable (HTTP 503)\n",
		"\n ERROR  thisbox  update: cannot write beside /opt/bin/ai-usage: permission denied\n",
	} {
		if !strings.Contains(page, s) {
			t.Errorf("the page lacks %q:\n%s", s, page)
		}
	}
	// Each failure turns its dot red, the color of its ERROR.
	th := NewTheme(true)
	dot := lipgloss.NewStyle().Foreground(th.Out).Render("●")
	h := Render(r, Options{Width: 120, Loc: time.UTC, Color: true, Dark: true}).Header
	for _, s := range []string{"relay failing", "update check failed"} {
		if want := dot + " " + lipgloss.NewStyle().Foreground(th.Muted).Render(s); !strings.Contains(h, want) {
			t.Errorf("the dot before %q is not red: %q", s, h)
		}
	}

	// Without a relay, or on a build that does not update, the header shows
	// no failure, and there is no error.
	f.in.RelayURL, f.in.Version = "", "dev"
	if r := Build(f.in); len(r.Attention) != 0 {
		t.Fatalf("attention = %+v", r.Attention)
	}
}

// A run a bug stopped leaves no report, so its error is not the device's; it
// is in ATTENTION as this device's all the same, while the device is
// silent too. A run that failed and reported it is named once.
func TestFailedRunIsInAttention(t *testing.T) {
	bug := "collection stopped by a bug: runtime error: invalid memory address or nil pointer dereference"
	for _, c := range []struct {
		name string
		ran  time.Duration
		want []string
		line string
	}{
		{"a bug", 15 * time.Minute, []string{"error thisbox " + bug}, " ERROR  thisbox  " + bug},
		{"a bug every run", 30 * time.Hour, []string{"error thisbox " + bug, "silent thisbox "}, " ERROR   thisbox  " + bug},
	} {
		st := emptyState()
		f := newFixture(t, st)
		ran := now.Add(-c.ran)
		st.LastRunAt, st.LastSuccessAt = ran, ran
		st.LastError, st.LastErrorAt = bug, now.Add(-time.Minute)
		f.seal(ran)
		r := Build(f.in)
		got := attentionList(r)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: attention =\n%s\nwant\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
		page := plainText(r, Options{Width: 120, Loc: time.UTC})
		for _, s := range []string{"● last run failed 1m ago", "\n" + c.line + "\n"} {
			if !strings.Contains(page, s) {
				t.Errorf("%s: the page lacks %q:\n%s", c.name, s, page)
			}
		}
	}

	st := emptyState()
	st.Sources["codex"] = state.Source{Status: "error", Error: "app-server exited without answering"}
	f := newFixture(t, st)
	st.LastSuccessAt = now.Add(-time.Hour)
	st.LastError, st.LastErrorAt = "codex: app-server exited without answering", now
	f.seal(now)
	r := Build(f.in)
	if len(r.Attention) != 1 || r.Attention[0].Message != st.LastError {
		t.Fatalf("attention = %+v", r.Attention)
	}
}
