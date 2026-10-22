package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/view"
)

// costRec records the cost commands the view runs.
type costRec struct{ runs [][]string }

func (c *costRec) run(args []string) (string, error) {
	c.runs = append(c.runs, args)
	return "done: " + strings.Join(args, " "), nil
}

// press sends keys one at a time and runs the command the last returns,
// feeding its message back, as Bubble Tea would.
func pressRun(t *testing.T, m Model, ks ...string) Model {
	t.Helper()
	for _, k := range ks {
		next, cmd := m.Update(press(t, k))
		m = next.(Model)
		for _, msg := range immediate(cmd) {
			m = update(t, m, msg)
		}
	}
	return m
}

func costModel(t *testing.T, rec *costRec) Model {
	t.Helper()
	return model(t, 120, 50, Config{Report: teamReport(t), Render: view.Render, Cost: rec.run})
}

// TestCostMode: $ shows the matrix in dollars, % goes from there to share,
// and $ again back to tokens.
func TestCostMode(t *testing.T) {
	m := model(t, 120, 50, Config{Report: teamReport(t), Render: view.Render})
	m = keys(t, m, "$")
	if !m.opts.Cost || !strings.Contains(strings.Join(m.page.Body, "\n"), "$ used, unused, and price a month") {
		t.Fatal("$ did not show dollars")
	}
	if b := bar(m); !strings.Contains(b, "$ ‹cost›") {
		t.Fatalf("bar %q", b)
	}
	m = keys(t, m, "%")
	if m.opts.Cost || !m.opts.Share {
		t.Fatalf("%% from dollars: cost %v, share %v", m.opts.Cost, m.opts.Share)
	}
	m = keys(t, m, "$", "$")
	if m.opts.Cost {
		t.Fatal("$ twice stays in dollars")
	}
	// A report with no cost has no $.
	if m = model(t, 120, 50, Config{}); m.costKey() {
		t.Fatal("$ without a cost")
	}
}

// TestQuestions: the report's questions show one at a time over the key
// bar; Enter takes the suggestion, s skips, S asks nothing more, y says
// still paid, and Esc puts one away until the view opens again.
func TestQuestions(t *testing.T) {
	rec := &costRec{}
	m := costModel(t, rec)
	rows := screen(m)
	if m.prompt == nil || !strings.Contains(rows[len(rows)-3], "grok a4c2e917: next charge") || !strings.Contains(rows[len(rows)-3], "Enter takes 2026-10-04") {
		t.Fatalf("no question:\n%s", strings.Join(rows[len(rows)-4:], "\n"))
	}
	if b := bar(m); !strings.Contains(b, "↵ answer · s skip · S never ask · esc later") {
		t.Fatalf("bar %q", b)
	}
	// Keys the step does not take do what they do: q still quits.
	if _, cmd := m.Update(press(t, "q")); !quits(cmd) {
		t.Fatal("q does not quit while a question shows")
	}
	m = pressRun(t, m, "enter")
	want := [][]string{{"set", "grok:a4c2e917-6b3d-4f81-9c2a-5e7d1b8f0a36", "--renews", "2026-10-04"}}
	if !reflect.DeepEqual(rec.runs, want) || m.note == "" {
		t.Fatalf("runs %v, note %q", rec.runs, m.note)
	}
	// The next question, still paid?, answers with a key.
	if m.prompt == nil || m.prompt.question.Kind != view.AskStillPaid {
		t.Fatalf("next = %+v", m.prompt)
	}
	if b := bar(m); !strings.Contains(b, "y still paid · n cancelled · s skip") {
		t.Fatalf("bar %q", b)
	}
	m = pressRun(t, m, "y")
	if got := rec.runs[len(rec.runs)-1]; !reflect.DeepEqual(got, []string{"confirm", "claude:old@acme.dev"}) || m.prompt != nil {
		t.Fatalf("y ran %v, prompt %+v", got, m.prompt)
	}

	rec = &costRec{}
	m = pressRun(t, costModel(t, rec), "s", "S")
	if !reflect.DeepEqual(rec.runs, [][]string{{"skip", "grok:a4c2e917-6b3d-4f81-9c2a-5e7d1b8f0a36", "renewal"}, {"never-ask"}}) {
		t.Fatalf("s, S ran %v", rec.runs)
	}

	// Typed: digits only, Backspace takes one back.
	rec = &costRec{}
	m = pressRun(t, costModel(t, rec), "2", "x", "9", "backspace", "8", "enter")
	if !reflect.DeepEqual(rec.runs, [][]string{{"set", "grok:a4c2e917-6b3d-4f81-9c2a-5e7d1b8f0a36", "--renews", "28"}}) {
		t.Fatalf("typed ran %v", rec.runs)
	}

	// Esc: later, and the next one shows; a reload does not bring it back.
	rec = &costRec{}
	m = pressRun(t, costModel(t, rec), "esc")
	if len(rec.runs) != 0 || m.prompt == nil || m.prompt.question.Kind != view.AskStillPaid {
		t.Fatalf("esc: runs %v, prompt %+v", rec.runs, m.prompt)
	}
	m = pressRun(t, m, "esc")
	m = update(t, m, loadedMsg{report: teamReport(t)})
	if m.prompt != nil {
		t.Fatalf("a question put away came back: %+v", m.prompt.question)
	}
	// Without a way to answer, nothing is asked.
	if m = model(t, 120, 50, Config{Report: teamReport(t), Render: view.Render}); m.prompt != nil {
		t.Fatal("asked with no Cost")
	}
}

// TestEdit: e on a subscription picked edits its cadence, price, and next
// charge, an empty line keeping each.
func TestEdit(t *testing.T) {
	rec := &costRec{}
	m := costModel(t, rec)
	m = keys(t, m, "esc", "esc")
	m.picking, m.picked = true, view.Pick{Provider: "claude", Label: "ann@acme.dev"}
	if b := bar(m); !strings.Contains(b, "e edit cost") {
		t.Fatalf("bar %q", b)
	}
	m = pressRun(t, m, "e")
	if m.prompt == nil || m.prompt.question != nil || !strings.Contains(m.promptLine(), "edit claude ann@acme.dev") {
		t.Fatalf("no edit: %+v", m.prompt)
	}
	m = pressRun(t, m, "y", "enter", "2", "4", "0", "0", "enter", "enter")
	if !reflect.DeepEqual(rec.runs, [][]string{{"set", "claude:ann@acme.dev", "--yearly", "--price", "2400"}}) {
		t.Fatalf("edit ran %v", rec.runs)
	}
	// Nothing typed runs nothing.
	rec.runs = nil
	m.picking = true
	m = pressRun(t, m, "e", "enter", "enter", "enter")
	if len(rec.runs) != 0 {
		t.Fatalf("empty edit ran %v", rec.runs)
	}
	// A device picked has no cost to edit.
	m.picking, m.picked = true, view.Pick{Device: "d-2f8a6c4e0b9d7f1a3c5e7b9d"}
	if m = keys(t, m, "e"); m.prompt != nil {
		t.Fatal("e edits a device")
	}
}

// TestRenewalPrompt: a plan billed yearly too is asked its cadence first,
// and yearly is offered its own date and takes no day of the month; with
// no suggestion, Enter waits for an answer, and a day that is no day keeps
// the prompt open with what is wrong.
func TestRenewalPrompt(t *testing.T) {
	rec := &costRec{}
	m := costModel(t, rec)
	month, year := "2026-11-15", "2027-03-15"
	m.prompt = questionPrompt(view.Question{ID: "renewal-1", Kind: view.AskRenewal, Provider: "claude", Account: "ann@acme.dev", Text: "When?",
		Suggestion: &month, SuggestionYearly: &year, Choices: []string{"monthly", "yearly"}})
	if l := m.promptLine(); !strings.Contains(l, "billed m monthly or y yearly") {
		t.Fatalf("first %q", l)
	}
	m = pressRun(t, m, "y", "enter")
	if l := m.promptLine(); !strings.Contains(l, "next charge, YYYY-MM-DD, Enter takes "+year) {
		t.Fatalf("yearly %q", l)
	}
	m = pressRun(t, m, "1", "5", "enter")
	if l := m.promptLine(); m.prompt == nil || !strings.Contains(l, "charges on a date") || len(rec.runs) != 0 {
		t.Fatalf("a day of the month, yearly: %q, runs %v", l, rec.runs)
	}
	m = pressRun(t, m, "backspace", "backspace", "enter")
	if want := [][]string{{"set", "claude:ann@acme.dev", "--yearly", "--renews", year}}; !reflect.DeepEqual(rec.runs, want) {
		t.Fatalf("yearly ran %v", rec.runs)
	}

	rec.runs = nil
	m.prompt = questionPrompt(view.Question{ID: "renewal-2", Kind: view.AskRenewal, Provider: "claude", Account: "cat@acme.dev", Text: "When?"})
	m = pressRun(t, m, "enter", "enter")
	if m.prompt == nil || m.prompt.at != 0 || len(rec.runs) != 0 {
		t.Fatalf("Enter with nothing to take: %+v, runs %v", m.prompt, rec.runs)
	}
	m = pressRun(t, m, "9", "9", "enter")
	if l := m.promptLine(); m.prompt == nil || !strings.Contains(l, "from 1 to 31") || len(rec.runs) != 0 {
		t.Fatalf("day 99: %q, runs %v", l, rec.runs)
	}
}

// TestTierPrompt: the price of a plan read without its tier offers its
// tiers' prices.
func TestTierPrompt(t *testing.T) {
	rec := &costRec{}
	m := costModel(t, rec)
	m.prompt = questionPrompt(view.Question{ID: "price-1", Kind: view.AskPrice, Provider: "claude", Account: "ann@acme.dev", Text: "What?",
		Choices: []string{"Max 5x $100", "Max 20x $200"}})
	if l := m.promptLine(); !strings.Contains(l, "price a month, in USD, as Max 5x $100 or Max 20x $200") {
		t.Fatalf("prompt %q", l)
	}
	m = pressRun(t, m, "2", "0", "0", "enter")
	if want := [][]string{{"set", "claude:ann@acme.dev", "--price", "200"}}; !reflect.DeepEqual(rec.runs, want) {
		t.Fatalf("ran %v", rec.runs)
	}
}
