package view

import (
	"image/color"
	"time"

	"github.com/neoromantic/ai-usage/internal/selfupdate"
)

// healthItem is one item on the header's right: a colored dot and a few
// words, in a long and a short form.
type healthItem struct {
	dot         color.Color
	long, short string
	busy        bool
}

// header is `ai-usage`, this device, and the team on the left, and the
// collector's health on the right, ending where the page does. A failure
// turns its dot red; the error itself is in ATTENTION.
func (p *page) header(pageWidth int) chunks {
	c := p.r.Collector
	g := p.g
	device := p.txt(c.DeviceLabel)
	left := func(dev string, team bool) chunks {
		out := chunks{p.bold("ai-usage"), p.faint(g.sep), p.plain(dev)}
		if team && c.Team != "" {
			out = append(out, p.faint(g.sep), p.muted("team "+p.txt(prefix(c.Team, 8))))
		}
		return out
	}
	right := func(short bool) chunks {
		var out chunks
		for i, h := range p.health() {
			if i > 0 {
				out = append(out, p.space(2))
			}
			text := h.long
			if short {
				text = h.short
			}
			if h.busy {
				out = append(out, p.accent(p.txt(p.o.Busy)), p.plain(" "), p.accent(text))
				continue
			}
			out = append(out, p.paint(g.here, h.dot, false, false), p.plain(" "), p.muted(text))
		}
		return out
	}
	// The health words shorten first, then the team goes, then the device
	// is cut.
	l, r := left(device, true), right(false)
	fits := func() bool { return l.width()+2+r.width() <= p.w }
	if !fits() {
		r = right(true)
	}
	if !fits() {
		l = left(device, false)
	}
	if over := l.width() + 2 + r.width() - p.w; over > 0 {
		l = left(truncEnd(device, max(width(device)-over, 6), g.ell), false)
	}
	edge := min(max(pageWidth, l.width()+2+r.width()), p.w)
	return append(l, append(chunks{p.space(max(edge-l.width()-r.width(), 2))}, r...)...).cut(p.w, g.ell)
}

// health is the collection, the relay, and the update, in the header.
func (p *page) health() []healthItem {
	c := p.r.Collector
	t := p.th
	// since is "7m ago", or "just now"; short is "7m", or "now".
	short := func(at *time.Time) string {
		if at == nil {
			return "never"
		}
		return age(p.now.Sub(*at))
	}
	since := func(at *time.Time) string {
		if s := short(at); s != "now" && s != "never" {
			return s + " ago"
		}
		if at == nil {
			return "never"
		}
		return "just now"
	}
	var out []healthItem
	switch {
	case p.o.Busy != "":
		out = append(out, healthItem{busy: true, long: "collecting", short: "collecting"})
	case c.LastRunAt == nil:
		out = append(out, healthItem{t.Out, "never collected", "never collected", false})
	case failed(c):
		out = append(out, healthItem{t.Out, "last run failed " + since(c.LastErrorAt), "run failed", false})
	case !c.Schedule.Registered:
		out = append(out, healthItem{t.Tight, "collected " + since(c.LastSuccessAt) + ", not scheduled", "not scheduled", false})
	default:
		out = append(out, healthItem{t.OK, "collected " + since(c.LastSuccessAt), "collected " + short(c.LastSuccessAt), false})
	}
	rl := c.Relay
	switch {
	case rl.URL == nil:
		out = append(out, healthItem{t.Faint, "no relay", "no relay", false})
	case rl.LastError != nil:
		out = append(out, healthItem{t.Out, "relay failing", "relay failing", false})
	case rl.Pending || rl.LastPushAt == nil:
		out = append(out, healthItem{t.Tight, "relay pending", "relay pending", false})
	default:
		out = append(out, healthItem{t.OK, "relay " + since(rl.LastPushAt), "relay " + short(rl.LastPushAt), false})
	}
	up := c.Update
	switch {
	case selfupdate.Dev(c.Version):
		out = append(out, healthItem{t.Faint, "dev build", "dev build", false})
	case up.Staged != nil:
		v := p.txt(*up.Staged)
		out = append(out, healthItem{t.Accent, v + " runs next time", v + " next run", false})
	case up.Error != nil:
		out = append(out, healthItem{t.Out, "update check failed", "update failed", false})
	case up.CheckedAt == nil:
		out = append(out, healthItem{t.Faint, "update not checked", "not checked", false})
	case up.Latest != nil && selfupdate.Newer(*up.Latest, c.Version):
		v := p.txt(*up.Latest)
		out = append(out, healthItem{t.Tight, v + " available", v + " available", false})
	default:
		out = append(out, healthItem{t.OK, "up to date", "up to date", false})
	}
	return out
}
