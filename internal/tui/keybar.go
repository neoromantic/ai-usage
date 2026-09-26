package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// kind is what a piece of the view's own text is, for its style.
type kind int

const (
	styleKey    kind = iota // a key: bold, in the accent color
	styleAction             // what a key does: dim
	stylePill               // the chosen option: reverse accent
	styleFaint              // separators and the rule
	styleTitle              // a heading in the help: bold
	styleError              // a failed refresh or reload
)

// style colors s with the theme. Without color, keys and headings are still
// bold, and pills keep their brackets.
func (m Model) style(s string, k kind) string {
	st := lipgloss.NewStyle()
	if !m.opts.Color {
		if k == styleKey || k == styleTitle {
			return st.Bold(true).Render(s)
		}
		return s
	}
	t := m.theme
	switch k {
	case styleKey:
		st = st.Bold(true).Foreground(t.Accent)
	case styleAction:
		st = st.Foreground(t.Muted)
	case stylePill:
		st = st.Foreground(t.Accent).Reverse(true)
	case styleFaint:
		st = st.Foreground(t.Faint)
	case styleTitle:
		st = st.Bold(true)
	case styleError:
		st = st.Foreground(t.Out)
	}
	return st.Render(s)
}

// pill is the chosen option: in reverse accent with color, else in the
// chosen-option marks. Both are as wide.
func (m Model) pill(s string) string {
	switch {
	case m.opts.Color:
		return m.style(" "+s+" ", stylePill)
	case m.opts.ASCII:
		return "[" + s + "]"
	default:
		return "‹" + s + "›"
	}
}

// item is one key in the key bar.
type item struct {
	key string
	// action is what the key does, and pill the option chosen now.
	action, pill string
	// choices are the options the key steps between, pill among them, for
	// a key with no action.
	choices []string
	// drop is the order a narrow terminal drops it in, the least used
	// first; 0 is never.
	drop int
	// short is the key in fewer columns, which a narrow terminal takes
	// before it drops any key; nil when it has none.
	short *item
}

// options are the options the key bar shows after the key and its action:
// its choices, or the pill alone.
func (it item) options() []string {
	if len(it.choices) > 0 {
		return it.choices
	}
	if it.pill != "" {
		return []string{it.pill}
	}
	return nil
}

// items are the keys that do something now, in the key bar's order, with
// `? help` and `q quit` last.
func (m Model) items() []item {
	vertical, sideways := "↑↓", "←→"
	if m.opts.ASCII {
		vertical, sideways = "j k", "h l"
	}
	var its []item
	if m.help {
		if m.maxHelpTop() > 0 {
			its = append(its, item{key: vertical, action: "scroll", drop: 1})
		}
		return append(its, item{key: "esc", action: "close"}, item{key: "q", action: "quit"})
	}
	if m.maxTop() > 0 {
		its = append(its, item{key: vertical, action: "scroll", drop: 6})
	}
	if m.matrixCut() {
		its = append(its, item{key: sideways, action: "matrix", drop: 4})
	}
	if m.page.DeviceViews {
		// Short, it names the status view alone, as `%` names share:
		// `s status`, or `s ‹status›` when it shows.
		views := item{key: "s", choices: []string{"usage", "status"}, pill: "usage", drop: 3}
		short := item{key: "s", action: "status", drop: 3}
		if m.opts.DeviceStatus {
			views.pill = "status"
			short.action, short.pill = "", "status"
		}
		views.short = &short
		its = append(its, views)
	}
	its = append(its, item{key: "p", action: "period", pill: m.opts.Period.String(), drop: 5})
	if m.shareKey() {
		share := item{key: "%", action: "share", drop: 1}
		if m.opts.Share {
			share.action, share.pill = "", "share"
		}
		its = append(its, share)
	}
	if m.cfg.Refresh != nil && !m.busy {
		its = append(its, item{key: "r", action: "refresh", drop: 2})
	}
	return append(its, item{key: "?", action: "help"}, item{key: "q", action: "quit"})
}

// keyBar is the bottom line. On a narrow terminal it shortens the keys that
// have a short form, then drops keys, the least used first, down to `? help`
// and `q quit`.
func (m Model) keyBar() string {
	its := m.items()
	if ansi.StringWidth(m.drawBar(its)) > m.width {
		for i, it := range its {
			if it.short != nil {
				its[i] = *it.short
			}
		}
	}
	for ansi.StringWidth(m.drawBar(its)) > m.width {
		drop := -1
		for i, it := range its {
			if it.drop > 0 && (drop < 0 || it.drop < its[drop].drop) {
				drop = i
			}
		}
		if drop < 0 {
			break
		}
		its = slices.Delete(its, drop, drop+1)
	}
	return fit(m.drawBar(its), m.width)
}

// drawBar draws its keys: a leading space, and ` · ` between keys.
func (m Model) drawBar(its []item) string {
	sep := "·"
	if m.opts.ASCII {
		sep = "."
	}
	var b strings.Builder
	b.WriteString(" ")
	for i, it := range its {
		if i > 0 {
			b.WriteString(" " + m.style(sep, styleFaint) + " ")
		}
		b.WriteString(m.style(it.key, styleKey))
		if it.action != "" {
			b.WriteString(" " + m.style(it.action, styleAction))
		}
		for _, c := range it.options() {
			if c == it.pill {
				b.WriteString(" " + m.pill(c))
			} else {
				b.WriteString(" " + m.style(c, styleAction))
			}
		}
	}
	return b.String()
}
