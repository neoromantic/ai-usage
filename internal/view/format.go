package view

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// style is an SGR parameter string. Only the 16 base colors are used, so the
// palette follows the terminal's light or dark theme.
type style string

const (
	plain   style = ""
	bold    style = "1"
	red     style = "31"
	redBold style = "1;31"
	yellow  style = "33"
	green   style = "32"
	magenta style = "35"
	cyan    style = "36"
	gray    style = "90"
)

type seg struct {
	text string
	st   style
}

type line []seg

func (l line) width() int {
	n := 0
	for _, s := range l {
		n += width(s.text)
	}
	return n
}

func (l line) text() string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.text)
	}
	return b.String()
}

// cut keeps the start of l within w columns.
func (l line) cut(w int, ell string) line {
	if l.width() <= w {
		return l
	}
	var out line
	left := w - width(ell)
	for _, s := range l {
		if width(s.text) <= left {
			out = append(out, s)
			left -= width(s.text)
			continue
		}
		return append(out, seg{prefix(s.text, max(left, 0)) + ell, s.st})
	}
	return out
}

type glyphs struct {
	eighths                  []string
	full, track, none        string
	ell, rule, branch        string
	here, seen, pace, stale  string
	ok, partial, fail, skip  string
	warn, old, staged, dash  string
	question, crit, warnMark string
	sep, ge                  string
}

var utf8Glyphs = glyphs{
	eighths: []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"},
	full:    "█", track: "░", none: "·",
	ell: "…", rule: "─", branch: "└",
	here: "●", seen: "○", pace: "▲", stale: "~",
	ok: "✓", partial: "◐", fail: "✕", skip: "·",
	warn: "!", old: "↓", staged: "↑", dash: "—",
	question: "?", crit: "!!", warnMark: "!",
	sep: " · ", ge: "≥",
}

var asciiGlyphs = glyphs{
	full: "#", track: ".", none: ".",
	ell: "...", rule: "-", branch: "-",
	here: "*", seen: "o", pace: "^", stale: "~",
	ok: "+", partial: "/", fail: "x", skip: ".",
	warn: "!", old: "v", staged: "^", dash: "-",
	question: "?", crit: "!!", warnMark: "!",
	sep: " - ", ge: ">=",
}

// width is the display width. Everything the console draws itself is one
// column wide; labels and paths may carry wide East Asian runes and emoji.
func width(s string) int {
	n := 0
	for _, r := range s {
		n += runeWidth(r)
	}
	return n
}

func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, r >= 0x2E80 && r <= 0xA4CF, r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF, r >= 0xFE30 && r <= 0xFE4F, r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6, r >= 0x1F300 && r <= 0x1FAFF, r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	return 1
}

func padRight(s string, w int) string {
	if d := w - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

func padLeft(s string, w int) string {
	if d := w - width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// truncEnd keeps the start of s, for names, versions, errors and notes.
func truncEnd(s string, w int, ell string) string {
	if width(s) <= w {
		return s
	}
	if w <= width(ell) {
		return prefix(s, w)
	}
	return prefix(s, w-width(ell)) + ell
}

func prefix(s string, w int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n+runeWidth(r) > w {
			break
		}
		b.WriteRune(r)
		n += runeWidth(r)
	}
	return b.String()
}

func suffix(s string, w int) string {
	rs := []rune(s)
	n, i := 0, len(rs)
	for i > 0 && n+runeWidth(rs[i-1]) <= w {
		i--
		n += runeWidth(rs[i])
	}
	return string(rs[i:])
}

// truncMid keeps both ends of s.
func truncMid(s string, w int, ell string) string {
	if width(s) <= w {
		return s
	}
	room := w - width(ell)
	if room <= 0 {
		return prefix(s, w)
	}
	head := room / 2
	return prefix(s, head) + ell + suffix(s, room-head)
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// truncLabel shortens an account label. A UUID is known by its first 8 hex
// digits, like a short git hash; anything else keeps its start.
func truncLabel(s string, w int, ell string) string {
	if width(s) <= w {
		return s
	}
	if uuidRe.MatchString(s) && w >= 8+width(ell) {
		return s[:8] + ell
	}
	return truncEnd(s, w, ell)
}

// truncPath keeps the first folder and as many trailing folders as fit, so
// "~/orca/workspaces/monorepo/fix-x" becomes "~/orca/…/monorepo/fix-x".
// When the first folder leaves no room, the path keeps just its root: "~/…/x".
func truncPath(p string, w int, ell string) string {
	if width(p) <= w {
		return p
	}
	parts := strings.Split(p, "/")
	if len(parts) >= 3 {
		first := parts[0] + "/" + parts[1]
		if parts[0] == "" && len(parts) > 3 {
			first = "/" + parts[1]
		}
		for _, head := range []string{first, parts[0]} {
			tail := ""
			for i := len(parts) - 1; i >= 2; i-- {
				cand := parts[i]
				if tail != "" {
					cand += "/" + tail
				}
				if width(head)+1+width(ell)+1+width(cand) > w {
					break
				}
				tail = cand
			}
			if tail != "" {
				return head + "/" + ell + "/" + tail
			}
		}
	}
	// The last folder names the project; keep it whole when it fits.
	if last := parts[len(parts)-1]; len(parts) > 1 && width(ell)+1+width(last) <= w {
		return ell + "/" + last
	}
	return truncMid(p, w, ell)
}

// nameList fits as many whole names as it can, then says how many more.
// When not even the first fits, it takes short[0], the first name's shorter
// form if there is one, such as a device's host without its user, and cuts
// that with an ellipsis if it still does not fit.
func nameList(names, short []string, w int, ell string) string {
	if len(names) == 0 {
		return ""
	}
	more := func(n int) string {
		if n == 0 {
			return ""
		}
		return " +" + strconv.Itoa(n)
	}
	out, shown := "", 0
	for i, n := range names {
		cand := n
		if out != "" {
			cand = out + ", " + n
		}
		if width(cand+more(len(names)-i-1)) > w {
			break
		}
		out, shown = cand, i+1
	}
	if shown > 0 {
		return out + more(len(names)-shown)
	}
	first := names[0]
	if len(short) > 0 && short[0] != "" {
		first = short[0]
	}
	tail := more(len(names) - 1)
	if width(first+tail) <= w {
		return first + tail
	}
	return truncEnd(first, w-width(tail), ell) + tail
}

// human prints 1234567 as 1.2M: K, M, G, T with one decimal below 100.
func human(n int64) string {
	f := float64(n)
	for _, u := range []struct {
		v float64
		s string
	}{{1e12, "T"}, {1e9, "G"}, {1e6, "M"}, {1e3, "K"}} {
		if f >= u.v {
			x := f / u.v
			if x >= 100 {
				return strconv.FormatFloat(x, 'f', 0, 64) + u.s
			}
			return strconv.FormatFloat(x, 'f', 1, 64) + u.s
		}
	}
	return strconv.FormatInt(n, 10)
}

// pctText floors, so a window never shows full before it is and no mark
// sits next to a number below its threshold. It fits four columns.
func pctText(p float64) string {
	return strconv.Itoa(min(int(math.Floor(p)), 999)) + "%"
}

// span prints a duration in at most five columns: 3d22h, 12d, 14h, 5h53m, 53m.
func span(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	m := int(d.Minutes())
	days, hours, mins := m/(60*24), (m/60)%24, m%60
	switch {
	case days >= 10:
		return fmt.Sprintf("%dd", days)
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours >= 10:
		return fmt.Sprintf("%dh", hours)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh%dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// age prints how old something is in one unit: now, 12m, 9h, 3d.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ago is the long form for the header, status and notes: "2h 5m ago".
func ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	s := strings.Replace(span(d), "d", "d ", 1)
	if !strings.Contains(s, "d") && strings.HasSuffix(s, "m") {
		s = strings.Replace(s, "h", "h ", 1)
	}
	return strings.TrimSpace(s) + " ago"
}

func shortFP(fp string, n int, ell string) string {
	if len(fp) <= n {
		return fp
	}
	return fp[:n] + ell
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
