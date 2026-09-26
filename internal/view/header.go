package view

import (
	"image/color"
	"time"
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

// health is the collector's health in the header, as the report has it:
// the collection, the relay, and the update. While the page collects, the
// collection is that. A status this release does not know is left out.
func (p *page) health() []healthItem {
	var out []healthItem
	if p.o.Busy != "" {
		out = append(out, healthItem{busy: true, long: "collecting", short: "collecting"})
	}
	for _, h := range p.r.Collector.Health {
		if p.o.Busy != "" && h.Item == HealthCollection {
			continue
		}
		if long, short := p.healthText(h); long != "" {
			out = append(out, healthItem{dot: p.level(h.State), long: long, short: short})
		}
	}
	return out
}

// healthText is a health item in words, in a long and a short form; empty
// for a status this release does not know.
func (p *page) healthText(h Health) (long, short string) {
	// since is "7m ago", or "just now"; ago is "7m", or "now".
	ago := func(at *time.Time) string {
		if at == nil {
			return "never"
		}
		return age(p.now.Sub(*at))
	}
	since := func(at *time.Time) string {
		if s := ago(at); s != "now" && s != "never" {
			return s + " ago"
		}
		if at == nil {
			return "never"
		}
		return "just now"
	}
	v := "a release"
	if h.Release != nil {
		v = p.txt(*h.Release)
	}
	switch h.Item {
	case HealthCollection:
		switch h.Status {
		case CollectionNever:
			return "never collected", "never collected"
		case CollectionFailed:
			return "last run failed " + since(h.At), "run failed"
		case CollectionUnscheduled:
			return "collected " + since(h.At) + ", not scheduled", "not scheduled"
		case HealthOK:
			return "collected " + since(h.At), "collected " + ago(h.At)
		}
	case HealthRelay:
		switch h.Status {
		case RelayNone:
			return "no relay", "no relay"
		case RelayFailing:
			return "relay failing", "relay failing"
		case RelayPending:
			return "relay pending", "relay pending"
		case HealthOK:
			return "relay " + since(h.At), "relay " + ago(h.At)
		}
	case HealthUpdate:
		switch h.Status {
		case UpdateDev:
			return "dev build", "dev build"
		case UpdateStaged:
			return v + " runs next time", v + " next run"
		case UpdateFailed:
			return "update check failed", "update failed"
		case UpdateUnchecked:
			return "update not checked", "not checked"
		case UpdateAvailable:
			return v + " available", v + " available"
		case HealthOK:
			return "up to date", "up to date"
		}
	}
	return "", ""
}

// level is the color of a health state's dot.
func (p *page) level(state string) color.Color {
	t := p.th
	switch state {
	case LevelOK:
		return t.OK
	case LevelWarn:
		return t.Tight
	case LevelError:
		return t.Out
	case LevelInfo:
		return t.Accent
	}
	return t.Faint
}
