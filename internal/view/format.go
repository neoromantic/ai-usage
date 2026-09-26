package view

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

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

// wrapItems joins items with sep into lines, the first line first columns
// wide and the rest rest wide. An item that does not fit starts the next
// line, and every line holds at least one item.
func wrapItems(items []string, sep string, first, rest int) []string {
	var out []string
	cur, limit := "", first
	for _, it := range items {
		switch {
		case cur == "":
			cur = it
		case width(cur)+width(sep)+width(it) <= limit:
			cur += sep + it
		default:
			out = append(out, cur)
			cur, limit = it, rest
		}
	}
	return append(out, cur)
}

// dur prints a duration in at most two units: 34m, 7h 5m, 1d 23h, 12d.
func dur(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	m := int(d / time.Minute)
	days, hours, mins := m/(60*24), m/60%24, m%60
	switch {
	case days > 0 && hours > 0:
		return strconv.Itoa(days) + "d " + strconv.Itoa(hours) + "h"
	case days > 0:
		return strconv.Itoa(days) + "d"
	case hours > 0 && mins > 0:
		return strconv.Itoa(hours) + "h " + strconv.Itoa(mins) + "m"
	case hours > 0:
		return strconv.Itoa(hours) + "h"
	default:
		return strconv.Itoa(mins) + "m"
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

// ago is the long form for status: "2h 5m ago". From 10 hours it drops
// the minutes, and from 10 days the hours.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d >= 10*24*time.Hour:
		d = d.Truncate(24 * time.Hour)
	case d >= 10*time.Hour:
		d = d.Truncate(time.Hour)
	}
	return dur(d) + " ago"
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
