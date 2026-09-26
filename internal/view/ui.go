package view

import (
	"regexp"
	"strings"
	"time"
)

const (
	minWidth = 80
	maxWidth = 160
)

type ui struct {
	w     int // layout width: the terminal width minus one
	g     glyphs
	color bool
	loc   *time.Location
	now   time.Time
	home  string
	lines []line
	r     *Report
}

func newUI(r *Report, o Options) *ui {
	t := o.Width
	if t == 0 {
		t = minWidth
	}
	t = clamp(t, minWidth, maxWidth)
	u := &ui{w: t - 1, g: utf8Glyphs, color: o.Color, loc: o.Loc, now: r.GeneratedAt, r: r}
	if o.ASCII {
		u.g = asciiGlyphs
	}
	if u.loc == nil {
		u.loc = time.Local
	}
	u.home = homeOf(r)
	return u
}

// defaultHomes are the harness homes that sit directly in the user's home.
var defaultHomes = []string{".claude", ".codex", ".grok", ".hermes"}

// homeOf is the user's home folder, taken from a default harness home, so
// paths print with ~ without the renderer asking the OS.
func homeOf(r *Report) string {
	for _, p := range r.Providers {
		for _, h := range p.Homes {
			// A home the Claude app keeps for a session ends in .claude too.
			if claudeAppHome.MatchString(h) {
				continue
			}
			for _, d := range defaultHomes {
				for _, sep := range []string{"/", `\`} {
					if strings.HasSuffix(h, sep+d) {
						return strings.TrimSuffix(h, sep+d)
					}
				}
			}
		}
	}
	return ""
}

func (u *ui) emit(l line) { u.lines = append(u.lines, l) }
func (u *ui) blank()      { u.emit(nil) }

func (u *ui) String() string {
	var b strings.Builder
	for _, l := range u.lines {
		for _, s := range trimRight(l) {
			if u.color && s.st != plain && strings.TrimSpace(s.text) != "" {
				b.WriteString("\x1b[" + string(s.st) + "m" + s.text + "\x1b[0m")
			} else {
				b.WriteString(s.text)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func trimRight(l line) line {
	out := append(line(nil), l...)
	for len(out) > 0 {
		last := &out[len(out)-1]
		last.text = strings.TrimRight(last.text, " ")
		if last.text != "" {
			break
		}
		out = out[:len(out)-1]
	}
	return out
}

// path prints a path under the user's home with ~.
func (u *ui) path(p string) string {
	if u.home == "" {
		return p
	}
	if p == u.home {
		return "~"
	}
	for _, sep := range []string{"/", `\`} {
		if strings.HasPrefix(p, u.home+sep) {
			return "~" + strings.TrimPrefix(p, u.home)
		}
	}
	return p
}

// claudeAppHome is a home the Claude app keeps for one of its sessions. No
// one logs in to it: the app records the account each session ran under.
var claudeAppHome = regexp.MustCompile(`[/\\]local-agent-mode-sessions[/\\].+[/\\]local_(?:[^/\\]*_)?([^/\\_]+)[/\\]\.claude$`)

// appHomes are the homes an app keeps per account or per session, and the
// app's name.
var appHomes = []struct {
	re  *regexp.Regexp
	app string
}{
	{regexp.MustCompile(`[/\\]orca[/\\]codex-accounts[/\\]([^/\\]+)[/\\]home$`), "orca"},
	{claudeAppHome, "claude app"},
}

// homeName is how a harness home is printed: an app's home by the app and
// the account or session id, anything else as a path. An id that is a UUID
// is known by its first 8 hex digits.
func (u *ui) homeName(p string) string {
	for _, a := range appHomes {
		if m := a.re.FindStringSubmatch(p); m != nil {
			id := m[1]
			if uuidRe.MatchString(id) {
				id = id[:8]
			}
			return a.app + " " + id
		}
	}
	return u.path(p)
}

func (u *ui) clock(t time.Time, layout string) string { return t.In(u.loc).Format(layout) }

// spread puts right at the page's right edge after left, when both fit.
func (u *ui) spread(left, right line) line {
	left = left.cut(u.w, u.g.ell)
	gap := max(u.w-left.width()-right.width(), 2)
	if left.width()+gap+right.width() > u.w {
		return left
	}
	return append(append(left, seg{strings.Repeat(" ", gap), plain}), right...)
}

// flow joins items with sep and wraps them at the page width, indenting
// the continuation lines.
func (u *ui) flow(first line, items []line, sep string, indent int) {
	for _, l := range u.wrap(first, items, sep, indent, u.w) {
		u.emit(l)
	}
}

func (u *ui) wrap(first line, items []line, sep string, indent, limit int) []line {
	var out []line
	cur := first
	fresh := true
	for _, it := range items {
		add := it.width()
		if !fresh {
			add += width(sep)
		}
		if cur.width()+add > limit && !fresh {
			out = append(out, cur.cut(u.w, u.g.ell))
			cur = line{{strings.Repeat(" ", indent), plain}}
			fresh = true
		}
		if !fresh {
			cur = append(cur, seg{sep, plain})
		}
		cur = append(cur, it...)
		fresh = false
	}
	return append(out, cur.cut(u.w, u.g.ell))
}

// failed is whether the last run ended in an error: the error is newer than
// the last success.
func failed(c Collector) bool {
	return c.LastError != nil && c.LastErrorAt != nil && (c.LastSuccessAt == nil || c.LastErrorAt.After(*c.LastSuccessAt))
}
