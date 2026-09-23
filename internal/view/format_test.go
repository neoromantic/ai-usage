package view

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSlots(t *testing.T) {
	names := func(ws []Window) []string {
		var out []string
		for _, w := range ws {
			out = append(out, w.Name)
		}
		return out
	}
	name := func(w *Window) string {
		if w == nil {
			return ""
		}
		return w.Name
	}
	for _, c := range []struct {
		in          []Window
		short, long string
		extra       []string
	}{
		{[]Window{{Name: "5h", Minutes: 300}, {Name: "7d", Minutes: 10080}, {Name: "7d Opus", Minutes: 10080}}, "5h", "7d", []string{"7d Opus"}},
		// A 5h or 7d window takes the slot from another name, and the extras
		// keep the report's order.
		{[]Window{{Name: "7d Opus", Minutes: 10080}, {Name: "3h", Minutes: 180}, {Name: "7d", Minutes: 10080}, {Name: "5h", Minutes: 300}}, "5h", "7d", []string{"7d Opus", "3h"}},
		// Without minutes, a name in hours is short.
		{[]Window{{Name: "7d credits"}, {Name: "8h burst"}}, "8h burst", "7d credits", nil},
		{[]Window{{Name: "monthly", Minutes: 43200}}, "", "monthly", nil},
		{nil, "", "", nil},
	} {
		s, l, x := slots(c.in)
		if name(s) != c.short || name(l) != c.long || !reflect.DeepEqual(names(x), c.extra) {
			t.Errorf("slots(%v) = %q %q %v", names(c.in), name(s), name(l), names(x))
		}
	}
}

func TestBar(t *testing.T) {
	u, a := newUI(&Report{}, Options{}), newUI(&Report{}, Options{ASCII: true})
	for _, c := range []struct {
		p           float64
		utf8, ascii string
	}{
		{0, "░░░░░░", "......"},
		{0.1, "▏░░░░░", "#....."},
		{50, "███░░░", "###..."},
		{99.9, "█████▉", "#####."},
		{100, "██████", "######"},
		{150, "██████", "######"},
	} {
		f, tr := u.bar(c.p, 6)
		af, atr := a.bar(c.p, 6)
		if f+tr != c.utf8 || af+atr != c.ascii {
			t.Errorf("bar(%v) = %q %q, want %q %q", c.p, f+tr, af+atr, c.utf8, c.ascii)
		}
	}
}

func TestNumbers(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{{0, "0"}, {601, "601"}, {2900, "2.9K"}, {51_300_000, "51.3M"}, {640_000_000, "640M"}, {16_842_296_448, "16.8G"}, {3e12, "3.0T"}} {
		if got := human(c.n); got != c.want {
			t.Errorf("human(%d) = %q, want %q", c.n, got, c.want)
		}
	}
	for _, c := range []struct {
		p    float64
		want string
	}{{0, "0%"}, {12.5, "12%"}, {99.9, "99%"}, {100, "100%"}, {1500, "999%"}} {
		if got := pctText(c.p); got != c.want {
			t.Errorf("pct(%v) = %q, want %q", c.p, got, c.want)
		}
	}
}

func TestDurations(t *testing.T) {
	m, h, d := time.Minute, time.Hour, 24*time.Hour
	for _, c := range []struct {
		d               time.Duration
		span, age, ago_ string
	}{
		{30 * time.Second, "<1m", "now", "just now"},
		{53 * m, "53m", "53m", "53m ago"},
		{5*h + 53*m, "5h53m", "5h", "5h 53m ago"},
		{5 * h, "5h", "5h", "5h ago"},
		{14*h + 20*m, "14h", "14h", "14h ago"},
		{3*d + 4*h + 10*m, "3d4h", "3d", "3d 4h ago"},
		{3 * d, "3d", "3d", "3d ago"},
		{12*d + 5*h, "12d", "12d", "12d ago"},
	} {
		if got := span(c.d); got != c.span {
			t.Errorf("span(%v) = %q, want %q", c.d, got, c.span)
		}
		if got := age(c.d); got != c.age {
			t.Errorf("age(%v) = %q, want %q", c.d, got, c.age)
		}
		if got := ago(c.d); got != c.ago_ {
			t.Errorf("ago(%v) = %q, want %q", c.d, got, c.ago_)
		}
	}
}

func TestTruncation(t *testing.T) {
	const uuid = "a4c2e917-3b5d-4e6f-8a7b-9c0d1e2f3a4b"
	for _, c := range []struct {
		s    string
		w    int
		want string
	}{
		{"ann@acme.io", 16, "ann@acme.io"},
		{"a-very-long-label@acme.io", 16, "a-very-long-lab…"},
		{uuid, 16, "a4c2e917…"},
		{uuid, 36, uuid},
		{"日本語のラベル", 9, "日本語の…"},
	} {
		if got := truncLabel(c.s, c.w, "…"); got != c.want {
			t.Errorf("truncLabel(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
	if got := truncLabel(uuid, 16, "..."); got != "a4c2e917..." {
		t.Errorf("ASCII uuid = %q", got)
	}
	for _, c := range []struct {
		p    string
		w    int
		want string
	}{
		{"~/src/acme/api", 20, "~/src/acme/api"},
		{"~/orca/workspaces/monorepo/optimize-data-connection", 40, "~/orca/…/optimize-data-connection"},
		{"~/orca/workspaces/monorepo/fix-x", 30, "~/orca/…/monorepo/fix-x"},
		{"/opt/build/agents/workspace/project", 26, "/opt/…/workspace/project"},
		// Without the first folder, the root and the last one still fit.
		{"/Users/someone/deep/project-name-that-is-long", 28, "/…/project-name-that-is-long"},
		{"~/Developer/orbit/worktrees/flight-skill-evaluation", 36, "~/…/flight-skill-evaluation"},
		// Not even the root: the last folder alone.
		{"/Users/someone/deep/project-name-that-is-long", 27, "…/project-name-that-is-long"},
		// Not even that: the middle goes.
		{"~/orca/workspaces/a-very-long-project-folder-name", 30, "~/orca/workspa…ect-folder-name"},
	} {
		if got := truncPath(c.p, c.w, "…"); got != c.want || width(got) > c.w {
			t.Errorf("truncPath(%q, %d) = %q, want %q", c.p, c.w, got, c.want)
		}
	}
}

func TestNameList(t *testing.T) {
	names := []string{"ann-mbp", "bo-laptop", "cy-desk", "dee-air"}
	for _, c := range []struct {
		w    int
		want string
	}{
		{80, "ann-mbp, bo-laptop, cy-desk, dee-air"},
		{25, "ann-mbp, bo-laptop +2"},
		{12, "ann-mbp +3"},
		{8, "ann-… +3"},
	} {
		if got := nameList(names, nil, c.w, "…"); got != c.want || width(got) > c.w {
			t.Errorf("nameList(%d) = %q, want %q", c.w, got, c.want)
		}
	}
	if got := nameList([]string{"box/ann", "box/bo"}, []string{"box", "box"}, 8, "…"); got != "box +1" {
		t.Errorf("nameList of host/user = %q", got)
	}
	// A name of its own with a slash is not a host and a user.
	if got := nameList([]string{"team/support-bot", "box"}, []string{"team/support-bot", "box"}, 12, "…"); got != "team/sup… +1" {
		t.Errorf("nameList of a name with a slash = %q", got)
	}
}

func TestWrapWords(t *testing.T) {
	const msg = "relay: Post https://relay.example/v1/teams/abcdefghijklmnopqrstuvwxyz/devices/d-1: refused"
	got := wrapWords(msg, 30)
	for _, l := range got {
		if width(l) > 30 {
			t.Errorf("line %q is wider than 30", l)
		}
	}
	// A long word breaks after a slash and loses nothing.
	if want := []string{"relay: Post", "https://relay.example/v1/", "teams/", "abcdefghijklmnopqrstuvwxyz/", "devices/d-1: refused"}; !reflect.DeepEqual(got, want) {
		t.Errorf("wrapWords = %q", got)
	}
	if got := wrapWords("a b c", 30); !reflect.DeepEqual(got, []string{"a b c"}) {
		t.Errorf("short text = %q", got)
	}
}

func TestTitleWrap(t *testing.T) {
	u := newUI(&Report{}, Options{Width: 80})
	g := u.g
	title := line{{"ACCOUNTS", bold}, {"  12", plain}}
	for _, s := range []string{"13 critical", "14 warning", "12 fills before reset", "15 stale", "11 unknown"} {
		title = append(title, seg{g.sep, gray}, seg{s, plain})
	}
	u.titleWrap(title, width("ACCOUNTS  "))
	var got []string
	for _, l := range u.lines {
		got = append(got, l.text())
	}
	want := []string{
		"ACCOUNTS  12 · 13 critical · 14 warning · 12 fills before reset · 15 stale",
		"          11 unknown",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("title = %q", got)
	}
}

// fleet is a team of n healthy devices, this one first.
func fleet(n int) Report {
	r := Report{GeneratedAt: now, Collector: Collector{Version: "v1.0.0", DeviceLabel: "host00", OSUser: "u"}}
	pulled := now
	r.Team.PulledAt = &pulled
	for i := 0; i < n; i++ {
		r.Team.Devices = append(r.Team.Devices, TeamDevice{
			Device: "d-" + strconv.Itoa(i), Label: fmt.Sprintf("host%02d", i), OSUser: "u",
			This: i == 0, CollectorVersion: "v1.0.0", CollectedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	return r
}

func TestDevicesFoldPastTwelve(t *testing.T) {
	out := text(fleet(12))
	if strings.Contains(out, "more ok") || !strings.Contains(out, "\n  host11 (u)  v1.0.0 ") {
		t.Fatalf("12 devices folded:\n%s", out)
	}
	out = text(fleet(13))
	if !strings.Contains(out, "\n● host00 (u)  v1.0.0 ") || strings.Contains(out, "\n  host01 (u)") ||
		!strings.Contains(out, "\n  ✓ 12 more ok, seen within 12m: host01, host02, host03,") {
		t.Fatalf("13 devices not folded:\n%s", out)
	}
	if all := Text(fleet(13), Options{Mode: Devices, Loc: time.UTC}); strings.Contains(all, "more ok") {
		t.Fatalf("--devices folded:\n%s", all)
	}
}

func TestLegendLinesAreEven(t *testing.T) {
	u := newUI(&Report{}, Options{Width: 80})
	var items []line
	for _, s := range []string{"● logged in here", "!! ≥90%", "~ old: reading 6h+", "? reset since reading", "— no window"} {
		items = append(items, line{{s, gray}})
	}
	u.flowEven(items, "  ")
	var got []string
	for _, l := range u.lines {
		got = append(got, l.text())
	}
	want := []string{"● logged in here  !! ≥90%  ~ old: reading 6h+", "? reset since reading  — no window"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legend = %q", got)
	}
}
