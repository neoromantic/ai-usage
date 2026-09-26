package view

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// style is an SGR parameter string. Only the 16 base colors are used, so the
// palette follows the terminal's light or dark theme.
type style string

const (
	plain  style = ""
	bold   style = "1"
	red    style = "31"
	yellow style = "33"
	green  style = "32"
	cyan   style = "36"
	gray   style = "90"
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
	ell, sep                string
	ok, partial, fail, skip string
	warn, staged            string
}

var utf8Glyphs = glyphs{
	ell: "…", sep: " · ",
	ok: "✓", partial: "◐", fail: "✕", skip: "·",
	warn: "!", staged: "↑",
}

var asciiGlyphs = glyphs{
	ell: "...", sep: " - ",
	ok: "+", partial: "/", fail: "x", skip: ".",
	warn: "!", staged: "^",
}

// width is the display width, as a terminal draws s. Everything the console
// draws itself is one column wide; labels and paths may carry wide East Asian
// runes and emoji, which take two.
func width(s string) int { return ansi.StringWidth(s) }

// chars are the characters of s as a terminal draws them: a letter with its
// accents, or an emoji with its modifiers, is one character.
func chars(s string) []string {
	var out []string
	for s != "" {
		c, _ := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if c == "" {
			c = s[:1]
		}
		out = append(out, c)
		s = s[len(c):]
	}
	return out
}

func padRight(s string, w int) string {
	if d := w - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
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

// prefix is the start of s within w columns, and suffix its end; neither
// cuts a character in two.
func prefix(s string, w int) string {
	var b strings.Builder
	n := 0
	for _, c := range chars(s) {
		if n+width(c) > w {
			break
		}
		b.WriteString(c)
		n += width(c)
	}
	return b.String()
}

func suffix(s string, w int) string {
	cs := chars(s)
	n, i := 0, len(cs)
	for i > 0 && n+width(cs[i-1]) <= w {
		i--
		n += width(cs[i])
	}
	return strings.Join(cs[i:], "")
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
// When not even the first fits, it cuts that one with an ellipsis.
func nameList(names []string, w int, ell string) string {
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
	tail := more(len(names) - 1)
	return truncEnd(names[0], w-width(tail), ell) + tail
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

// ago is the long form for status: "2h 5m ago".
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

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
