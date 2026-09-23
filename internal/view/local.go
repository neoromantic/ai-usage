package view

import (
	"sort"
	"strconv"
	"strings"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Numeric columns of the token tables: sessions, input, output, cache read,
// cache write, each after one space.
var tokenCols = []struct {
	name string
	w    int
}{{"SESS", 5}, {"INPUT", 7}, {"OUTPUT", 7}, {"CACHE R", 8}, {"CACHE W", 8}}

const (
	projectIndent = 4
	// maxLocalLabel keeps the numbers near the names on a wide terminal.
	maxLocalLabel = 60
)

func tokenColsWidth() int {
	n := 0
	for _, col := range tokenCols {
		n += 1 + col.w
	}
	return n
}

// localNums is the width of THIS DEVICE's numbers, with LAST when wide.
func (u *ui) localNums() int {
	if u.wide {
		return tokenColsWidth() + 2 + 5
	}
	return tokenColsWidth()
}

func (u *ui) tokenHeader(labelW int, last bool) {
	hdr := padRight("", labelW)
	for _, col := range tokenCols {
		hdr += " " + padLeft(col.name, col.w)
	}
	if last {
		hdr += "  " + padLeft("LAST", 5)
	}
	u.emit(line{{hdr, gray}})
}

// thisDevice is this device's accounts with their 90-day tokens and top
// projects, or every project with allProjects.
func (u *ui) thisDevice(allProjects bool) {
	c := u.r.Collector
	labelW := min(u.w-u.localNums(), maxLocalLabel)

	title := "THIS DEVICE"
	if allProjects {
		title = "PROJECTS"
	}
	u.emit(line{{title, bold}, {"  " + c.DeviceLabel + " (" + c.OSUser + ")" + u.g.sep + "tokens in the last 90 days", gray}}.cut(u.w, u.g.ell))
	u.tokenHeader(labelW, u.wide)

	var idle []string
	for _, p := range collect.Providers {
		pv := u.provider(p)
		if pv == nil {
			continue
		}
		if len(pv.Accounts) == 0 {
			switch {
			case pv.Status == "skipped":
				idle = append(idle, p+" not installed")
			case pv.Error != nil:
				idle = append(idle, p+" "+pv.Status+": "+strings.TrimPrefix(*pv.Error, p+": "))
			default:
				idle = append(idle, p+" no usage yet")
			}
			continue
		}
		accts := append([]Account(nil), pv.Accounts...)
		sort.SliceStable(accts, func(i, j int) bool {
			if accts[i].Current != accts[j].Current {
				return accts[i].Current
			}
			return accts[i].Tokens.Total() > accts[j].Tokens.Total()
		})
		logins := 0
		for _, h := range pv.Homes {
			if !claudeAppHome.MatchString(h) {
				logins++
			}
		}
		for _, a := range accts {
			u.localAccount(p, a, labelW, allProjects, logins > 1)
		}
	}
	if len(idle) > 0 {
		u.emit(line{{truncEnd(strings.Join(idle, u.g.sep), u.w, u.g.ell), gray}})
	}
}

func (u *ui) localAccount(p string, a Account, labelW int, allProjects, homes bool) {
	g := u.g
	mark := seg{g.here + " ", cyan}
	u.legend["here"] = u.legend["here"] || a.Current
	if !a.Current {
		mark = seg{g.seen + " ", gray}
		u.legend["seen"] = true
	}
	head := line{{padRight(p, 7), bold}, mark}
	room := labelW - head.width()
	lbl := truncLabel(a.Label, room, g.ell)
	head = append(head, seg{lbl, bold})
	// The home tells apart two logins of one provider on this device; a
	// Hermes account names the login it is assumed to bill through.
	suffix := ""
	switch {
	case a.Link != nil:
		suffix = "via " + a.Link.Provider
	case homes && a.Home != "":
		suffix = u.homeName(a.Home)
	}
	if suffix != "" {
		if left := room - width(lbl) - 2; left >= 8 {
			head = append(head, seg{"  " + truncPath(suffix, left, g.ell), gray})
		}
	}
	head = append(head, seg{strings.Repeat(" ", max(0, labelW-head.width())), plain})
	head = append(head, u.counts(a.Sessions, a.Tokens)...)
	if u.wide {
		last := g.dash
		if a.LastActiveAt != nil {
			last = age(u.now.Sub(*a.LastActiveAt))
		} else {
			u.legend["dash"] = true
		}
		head = append(head, seg{"  " + padLeft(last, 5), gray})
	}
	u.emit(head)

	projects := a.Projects
	limit := len(projects)
	if !allProjects && limit > topProjects {
		limit = topProjects
	}
	room = labelW - projectIndent
	for _, pr := range projects[:limit] {
		l := line{{strings.Repeat(" ", projectIndent) + padRight(truncPath(u.path(pr.Path), room, g.ell), room), plain}}
		u.emit(append(l, u.counts(pr.Sessions, pr.Tokens)...))
	}
	if more := len(projects) - limit; more > 0 {
		word := "projects"
		if more == 1 {
			word = "project"
		}
		u.emit(line{{strings.Repeat(" ", projectIndent) + "+ " + strconv.Itoa(more) + " more " + word + g.sep + "ai-usage --projects", gray}})
	}
}

// counts is the numeric columns, zeros in gray.
func (u *ui) counts(sessions int, t snapshot.Tokens) line {
	vals := []int64{int64(sessions), t.Input, t.Output, t.CacheRead, t.CacheWrite}
	var l line
	for i, col := range tokenCols {
		st := plain
		if vals[i] == 0 {
			st = gray
		}
		s := human(vals[i])
		if i == 0 {
			s = strconv.Itoa(sessions)
		}
		l = append(l, seg{" " + padLeft(s, col.w), st})
	}
	return l
}

// teamTokens is the --tokens view: every team account's 90-day counts,
// summed across devices, with each device's share under it.
func (u *ui) teamTokens() {
	g := u.g
	labelW := u.w - tokenColsWidth()
	u.emit(line{{"TEAM TOKENS", bold}, {"  every device" + g.sep + "tokens in the last 90 days", gray}})
	u.tokenHeader(labelW, false)
	for _, tp := range u.r.Team.Providers {
		accts := append([]TeamAccount(nil), tp.Accounts...)
		sort.SliceStable(accts, func(i, j int) bool { return accts[i].Tokens.Total() > accts[j].Tokens.Total() })
		for _, a := range accts {
			head := line{{padRight(tp.Provider, 7), bold}, {"  ", plain}}
			room := labelW - head.width()
			head = append(head, seg{padRight(truncLabel(a.Label, room, g.ell), room), bold})
			u.emit(append(head, u.counts(a.Sessions, a.Tokens)...))
			ds := append([]DeviceUsage(nil), a.PerDevice...)
			sort.SliceStable(ds, func(i, j int) bool { return ds[i].Tokens.Total() > ds[j].Tokens.Total() })
			room = labelW - projectIndent
			for _, d := range ds {
				l := line{{strings.Repeat(" ", projectIndent) + padRight(truncEnd(d.Device, room, g.ell), room), plain}}
				u.emit(append(l, u.counts(d.Sessions, d.Tokens)...))
			}
			if len(ds) == 0 {
				u.emit(line{{strings.Repeat(" ", projectIndent) + truncEnd(strings.Join(a.Devices, ", "), room, g.ell), gray}})
			}
		}
	}
}
