package tui

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/view"
)

// prompt is the line over the key bar that asks a person one thing at a
// time: a question of the report's, or, after `e`, a subscription's price,
// cadence, and next charge. What it asks is in steps; the answers become
// the arguments of one `ai-usage cost` command.
type prompt struct {
	// question is the question it asks, nil while it edits.
	question *view.Question
	provider string
	label    string
	steps    []step
	at       int
	input    []rune
	// args are what the steps answered so far, after `set ACCOUNT`.
	args []string
	// problem is what was wrong with the answer Enter last took, shown
	// until the next key.
	problem string
}

// step is one thing the prompt asks: its words, the answer Enter takes on
// an empty line, if any, which keys it takes, and the arguments an answer
// gives the command. An empty answer with no suggestion gives none. check,
// when set, says what is wrong with an answer, and then, when set, changes
// the steps after it by the answer.
type step struct {
	ask        string
	suggestion string
	keys       func(r rune) bool
	answer     func(s string) []string
	check      func(s string) string
	then       func(pr *prompt, s string)
}

// costDoneMsg ends a cost command the prompt ran.
type costDoneMsg struct {
	text string
	err  error
}

func digits(r rune) bool { return unicode.IsDigit(r) || r == '.' || r == '-' }

// dayCheck says what is wrong with a next charge typed: a YYYY-MM-DD day,
// or, unless yearly, a day of the month.
func dayCheck(yearly bool) func(s string) string {
	return func(s string) string {
		if n, err := strconv.Atoi(s); err == nil {
			switch {
			case yearly:
				return "a yearly subscription charges on a date: YYYY-MM-DD"
			case n < 1 || n > 31:
				return "a day of the month is from 1 to 31"
			}
			return ""
		}
		if _, err := time.Parse(time.DateOnly, s); err != nil {
			return "a day is YYYY-MM-DD"
		}
		return ""
	}
}

// renewsStep asks the next charge, of a subscription billed yearly or not,
// offering suggestion.
func renewsStep(yearly bool, suggestion *string) step {
	s := step{ask: "next charge, YYYY-MM-DD or a day of the month", keys: digits, check: dayCheck(yearly),
		answer: func(s string) []string { return []string{"--renews", s} }}
	if yearly {
		s.ask = "next charge, YYYY-MM-DD"
	}
	if suggestion != nil {
		s.suggestion = *suggestion
	}
	return s
}

// account is the subscription as `ai-usage cost` takes it.
func (pr *prompt) account() string { return pr.provider + ":" + pr.label }

// questionPrompt asks q.
func questionPrompt(q view.Question) *prompt {
	pr := &prompt{question: &q, provider: q.Provider, label: q.Account}
	switch q.Kind {
	case view.AskRenewal:
		// The cadence first: a yearly subscription charges on a date of
		// its own, and is offered its own.
		if len(q.Choices) > 1 {
			c := cadenceStep("monthly")
			// Monthly is how it counts unless a person says otherwise.
			c.answer = func(s string) []string {
				if s == "y" {
					return []string{"--yearly"}
				}
				return nil
			}
			c.then = func(pr *prompt, s string) {
				if s == "y" {
					pr.steps[pr.at+1] = renewsStep(true, q.SuggestionYearly)
				}
			}
			pr.steps = append(pr.steps, c)
		}
		pr.steps = append(pr.steps, renewsStep(false, q.Suggestion))
	case view.AskPrice:
		ask := "price a month, in USD"
		if len(q.Choices) > 0 {
			// A plan read without its tier: its tiers' prices.
			ask += ", as " + strings.Join(q.Choices, " or ")
		}
		pr.steps = []step{{ask: ask, keys: digits, answer: func(s string) []string { return []string{"--price", s} }}}
	case view.AskStillPaid:
		pr.steps = []step{{ask: "y still paid, n cancelled"}}
	}
	return pr
}

// cadenceStep asks how a subscription is billed, now as cur.
func cadenceStep(cur string) step {
	return step{ask: "billed m monthly or y yearly", suggestion: cur[:1], keys: func(r rune) bool { return r == 'm' || r == 'y' },
		answer: func(s string) []string {
			if s == "y" {
				return []string{"--yearly"}
			}
			return []string{"--monthly"}
		}}
}

// editPrompt edits s's cadence, price, and next charge, each kept as it
// is on an empty line. The price is of each charge: a year's when it is
// billed yearly. A typed price kept through a change of cadence is turned
// into the new cadence's, as the price step says.
func editPrompt(s view.CostSubscription) *prompt {
	priceStep := func(yearly bool) step {
		st := step{ask: "price of each charge in USD, a year's when yearly, empty keeps it", keys: digits,
			answer: func(v string) []string { return []string{"--price", v} }}
		if s.PriceSource == view.PriceTyped && s.PriceUSD != nil {
			now := *s.PriceUSD
			if yearly {
				now *= 12
			}
			st.ask = "price of each charge in USD, a year's when yearly, now " + strconv.FormatFloat(math.Round(now*100)/100, 'f', -1, 64) + ", empty keeps it"
		}
		return st
	}
	chargeStep := func(yearly bool) step {
		st := step{ask: "next charge, YYYY-MM-DD or a day of the month, empty keeps it", keys: digits, check: dayCheck(yearly),
			answer: func(v string) []string { return []string{"--renews", v} }}
		if yearly {
			st.ask = "next charge, YYYY-MM-DD, empty keeps it"
		}
		if s.RenewsAt != nil {
			st.ask = "next charge, now " + s.RenewsAt.Local().Format("2006-01-02") + ", empty keeps it"
		}
		return st
	}
	yearly := s.Cadence == "yearly"
	cadence := cadenceStep(s.Cadence)
	cadence.suggestion = ""
	cadence.ask += ", now " + s.Cadence + ", empty keeps it"
	cadence.then = func(pr *prompt, v string) {
		if v != "" {
			pr.steps[pr.at+1], pr.steps[pr.at+2] = priceStep(v == "y"), chargeStep(v == "y")
		}
	}
	return &prompt{provider: s.Provider, label: s.Account, steps: []step{cadence, priceStep(yearly), chargeStep(yearly)}}
}

// promptKey is a key while the prompt shows. It takes the keys its step
// takes, Enter, Backspace, and Esc, and for a question s and S on an empty
// line; any other key does what it does without it, and handled is false.
func (m Model) promptKey(k string) (tea.Model, tea.Cmd, bool) {
	pr := m.prompt
	st := pr.steps[pr.at]
	pr.problem = ""
	switch k {
	case "esc":
		// Later: the question comes back the next time the view opens.
		m.endPrompt()
		return m, nil, true
	case "backspace":
		if n := len(pr.input); n > 0 {
			pr.input = pr.input[:n-1]
		}
		return m, nil, true
	case "enter":
		v := strings.TrimSpace(string(pr.input))
		if v == "" {
			v = st.suggestion
		}
		// A question with nothing to take waits for an answer, or Esc.
		if v == "" && pr.question != nil && st.answer != nil {
			return m, nil, true
		}
		if v != "" && st.check != nil {
			if pr.problem = st.check(v); pr.problem != "" {
				return m, nil, true
			}
		}
		if v != "" && st.answer != nil {
			pr.args = append(pr.args, st.answer(v)...)
		}
		if st.then != nil {
			st.then(pr, v)
		}
		pr.at, pr.input = pr.at+1, nil
		if pr.at < len(pr.steps) {
			return m, nil, true
		}
		return m.runPrompt(append([]string{"set", pr.account()}, pr.args...))
	}
	if pr.question != nil && len(pr.input) == 0 {
		switch k {
		case "s":
			return m.runPrompt([]string{"skip", pr.account(), pr.question.Kind})
		case "S":
			return m.runPrompt([]string{"never-ask"})
		}
		if pr.question.Kind == view.AskStillPaid {
			switch k {
			case "y":
				return m.runPrompt([]string{"confirm", pr.account()})
			case "n":
				return m.runPrompt([]string{"cancel", pr.account()})
			}
		}
	}
	if r := []rune(k); len(r) == 1 && st.keys != nil && st.keys(r[0]) {
		pr.input = append(pr.input, r[0])
		return m, nil, true
	}
	if k == "space" {
		return m, nil, true
	}
	return m, nil, false
}

// runPrompt ends the prompt and runs the cost command it answered, if it
// answered anything.
func (m Model) runPrompt(args []string) (tea.Model, tea.Cmd, bool) {
	m.endPrompt()
	if len(args) == 2 && args[0] == "set" {
		return m, nil, true
	}
	run := m.cfg.Cost
	return m, func() tea.Msg {
		text, err := run(args)
		return costDoneMsg{text: text, err: err}
	}, true
}

// endPrompt puts the prompt away, and asks the next question: the one it
// asked does not come back while the view stays open.
func (m *Model) endPrompt() {
	if m.prompt != nil && m.prompt.question != nil {
		if m.dismissed == nil {
			m.dismissed = map[string]bool{}
		}
		m.dismissed[m.prompt.question.ID] = true
	}
	m.prompt = nil
	m.draw()
	m.nextQuestion()
}

// nextQuestion shows the report's first question not put away yet, when
// nothing else is asked.
func (m *Model) nextQuestion() {
	if m.prompt != nil || m.cfg.Cost == nil || m.report.Cost == nil {
		return
	}
	for _, q := range m.report.Cost.Questions {
		if !m.dismissed[q.ID] && len(questionPrompt(q).steps) > 0 {
			m.prompt = questionPrompt(q)
			m.draw()
			return
		}
	}
}

// edit opens the prompt on the subscription picked, or open in detail.
func (m *Model) edit() {
	if m.cfg.Cost == nil || m.report.Cost == nil {
		return
	}
	k, ok := m.editable()
	if !ok {
		return
	}
	if s := m.report.Cost.Subscription(k.Provider, k.Label); s != nil {
		m.prompt = editPrompt(*s)
		m.draw()
	}
}

// editable is the subscription `e` edits: the one picked, or open in
// detail.
func (m Model) editable() (view.Pick, bool) {
	var k view.Pick
	switch {
	case m.help || m.cfg.Cost == nil || m.report.Cost == nil:
		return k, false
	case m.open != nil:
		k = *m.open
	case m.choosing():
		k = m.picked
	default:
		return k, false
	}
	return k, k.Provider != "" && m.report.Cost.Subscription(k.Provider, k.Label) != nil
}

// promptLine is the prompt as its row draws it: what it asks, of which
// subscription, the answer typed so far, and the suggestion, in the first
// of its forms that fits the terminal's width: the question in full, then
// the subscription's short name, then the answer alone.
func (m Model) promptLine() string {
	pr := m.prompt
	st := pr.steps[pr.at]
	who := pr.provider + " " + snapshot.Printable(pr.label)
	if s := m.report.Cost.Subscription(pr.provider, pr.label); s != nil {
		who = pr.provider + " " + snapshot.Printable(s.Name)
	}
	var heads []string
	if pr.question != nil {
		heads = append(heads, pr.question.Text+" ")
	} else {
		heads = append(heads, "edit "+pr.provider+" "+snapshot.Printable(pr.label)+": ")
	}
	heads = append(heads, who+": ", "")
	ask := st.ask
	if st.suggestion != "" {
		ask += ", Enter takes " + st.suggestion
	}
	if pr.problem != "" {
		ask = pr.problem + "; " + ask
	}
	input := m.style(string(pr.input)+"_", styleKey)
	var line string
	for _, h := range heads {
		line = " " + h + m.style(ask+" › ", styleAction) + input
		if ansi.StringWidth(line) <= m.width {
			break
		}
	}
	return fit(line, m.width)
}
