package view

import (
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"
)

// statusRows are the device names of DEVICES' rows, in order, in either view.
func statusRows(t *testing.T, p Page) []string {
	t.Helper()
	lines := pageSection(p, "DEVICES")
	if len(lines) < 5 {
		t.Fatalf("no DEVICES rows:\n%s", strings.Join(lines, "\n"))
	}
	var names []string
	for _, l := range lines[3 : len(lines)-1] {
		_, rest, _ := strings.Cut(l, " ")
		names = append(names, strings.Fields(rest)[0])
	}
	return names
}

// statusHeads are the column headings of the status view.
func statusHeads(t *testing.T, p Page) []string {
	t.Helper()
	return strings.Fields(pageSection(p, "DEVICES")[2])
}

// statusLine is the status view's row of a device, by its name or label.
func statusLine(t *testing.T, p Page, device string) string {
	t.Helper()
	for _, l := range pageSection(p, "DEVICES")[3:] {
		_, rest, _ := strings.Cut(l, " ")
		name, _, _ := strings.Cut(strings.TrimLeft(rest, " "), " ")
		if _, label, _ := strings.Cut(name, "@"); name == device || label == device {
			return l
		}
	}
	t.Fatalf("no row of %s:\n%s", device, strings.Join(pageSection(p, "DEVICES"), "\n"))
	return ""
}

// troubled is the team fixture with bot-e silent since Monday, with the
// error it last reported, and bot-f failing on hermes.
func troubled(t *testing.T) Report {
	r := loadReport(t, "team")
	since := time.Date(2026, 9, 21, 11, 2, 0, 0, time.UTC)
	lost, bad := "codex: not logged in", "hermes: database is locked"
	for i := range r.Team.Devices {
		switch d := &r.Team.Devices[i]; d.Label {
		case "bot-e":
			d.Silent, d.Error, d.CollectedAt = true, &lost, since
		case "bot-f":
			d.Error = &bad
			d.Sources[3].Status, d.Sources[3].Error = "partial", &bad
		}
	}
	return r
}

// TestStatusOrder: the status view has the matrix's rows in the matrix's
// order, for every period, and as many lines, so a device keeps its line
// when the view changes.
func TestStatusOrder(t *testing.T) {
	for name, r := range map[string]Report{"team": troubled(t)} {
		for _, per := range Periods {
			for _, share := range []bool{false, true} {
				o := Options{Width: 120, Loc: sampleZone, Period: per, Share: share}
				grid, status := Render(r, o), Render(r, devices(o))
				if g, s := statusRows(t, grid), statusRows(t, status); !slices.Equal(g, s) {
					t.Errorf("%s by %s: matrix %v, status %v", name, per, g, s)
				}
				if g, s := len(pageSection(grid, "DEVICES")), len(pageSection(status, "DEVICES")); g != s || len(grid.Body) != len(status.Body) {
					t.Errorf("%s by %s: matrix %d lines, status %d", name, per, g, s)
				}
			}
		}
	}
}

// TestStatusTitle counts the devices that fail, are silent, or are behind,
// each in its state's color, and leaves out a count of none.
func TestStatusTitle(t *testing.T) {
	title := func(r Report, o Options) string { return pageSection(Render(r, devices(o)), "DEVICES")[0] }
	for _, c := range []struct {
		r     Report
		width int
		want  string
	}{
		{loadReport(t, "team"), 120, "DEVICES  15 · 1 error · 1 silent · 4 old · by 7d UTC · M weighted tokens"},
		{troubled(t), 120, "DEVICES  15 · 2 errors · 2 silent · 4 old · by 7d UTC · M weighted tokens"},
	} {
		got := title(c.r, Options{Width: c.width, Loc: sampleZone})
		if !strings.HasPrefix(got, c.want+"  ") || !strings.HasSuffix(got, "usage  ‹status›") {
			t.Errorf("title %q, want %q and the views", got, c.want)
		}
	}
	// A team with nothing wrong counts nothing.
	r := loadReport(t, "team")
	for i := range r.Team.Devices {
		r.Team.Devices[i].Error, r.Team.Devices[i].Old, r.Team.Devices[i].Silent = nil, false, false
	}
	if got := title(r, Options{Width: 120}); !strings.HasPrefix(got, "DEVICES  15 · by 7d UTC · M weighted tokens  ") {
		t.Errorf("a sound team: %q", got)
	}
	// Each count is in its state's color, and the views stay at 80.
	o := Options{Width: 80, Color: true, Dark: true, Period: Month}
	colored := Render(troubled(t), devices(o)).Body
	th := NewTheme(true)
	var line string
	for _, l := range colored {
		if strings.HasPrefix(sgr.ReplaceAllString(l, ""), "DEVICES") {
			line = l
		}
	}
	for _, want := range []string{
		(&page{o: o, th: th}).paint("2 errors", th.Out, false, false).st.Render("2 errors"),
		(&page{o: o, th: th}).paint("2 silent", th.Tight, false, false).st.Render("2 silent"),
		(&page{o: o, th: th}).muted("4 old").st.Render("4 old"),
	} {
		if !strings.Contains(line, want) {
			t.Errorf("title %q lacks %q", line, want)
		}
	}
	if plain := sgr.ReplaceAllString(line, ""); !strings.HasPrefix(plain, "DEVICES  15 · 2 errors · 2 silent · 4 old · by 30d UTC") || !strings.HasSuffix(plain, "usage  ‹status›") {
		t.Errorf("title at 80: %q", plain)
	}
}

// TestStatusColumns: a narrow page cuts NOTE, then drops VIA, and the
// periods but the chosen one and 90d, in that order; a wider one never shows
// less; and no line is wider than the page before it is cut.
func TestStatusColumns(t *testing.T) {
	r := loadReport(t, "team")
	for _, c := range []struct {
		width int
		per   Period
		want  string
	}{
		{80, Week, "DEVICE VERSION SEEN 7D 90D NOTE"},
		{100, Week, "DEVICE VERSION SEEN TODAY 7D 30D 90D NOTE"},
		{120, Week, "DEVICE VERSION SEEN VIA TODAY 7D 30D 90D NOTE"},
		{160, Today, "DEVICE VERSION SEEN VIA TODAY 7D 30D 90D NOTE"},
	} {
		if got := strings.Join(statusHeads(t, Render(r, devices(Options{Width: c.width, Period: c.per}))), " "); got != c.want {
			t.Errorf("at %d by %s: %q, want %q", c.width, c.per, got, c.want)
		}
	}

	// Long names and releases leave room at 80 for 2 of the 4 periods.
	long := troubled(t)
	for i := range long.Team.Devices {
		d := &long.Team.Devices[i]
		d.CollectorVersion = "v1.4.0-rc.12"
		d.Label += "-with-a-long-name"
	}
	for i := range long.Team.Matrix.Rows {
		long.Team.Matrix.Rows[i].Device += "-with-a-long-name"
	}
	for per, want := range map[Period]string{
		Today:   "DEVICE VERSION SEEN TODAY 90D NOTE",
		Week:    "DEVICE VERSION SEEN 7D 90D NOTE",
		Month:   "DEVICE VERSION SEEN 30D 90D NOTE",
		Quarter: "DEVICE VERSION SEEN 7D 90D NOTE",
	} {
		if got := strings.Join(statusHeads(t, Render(long, devices(Options{Width: 80, Period: per}))), " "); got != want {
			t.Errorf("long names by %s: %q, want %q", per, got, want)
		}
	}

	order := []string{"DEVICE", "VERSION", "SEEN", "VIA", "TODAY", "7D", "30D", "90D", "NOTE"}
	for name, r := range map[string]Report{"team": loadReport(t, "team"), "troubled": troubled(t), "long": long} {
		for _, per := range Periods {
			for _, ascii := range []bool{false, true} {
				var last []string
				for w := 80; w <= 160; w++ {
					o := Options{Width: w, Period: per, ASCII: ascii, Loc: sampleZone, DeviceStatus: true}
					p := newPage(&r, o)
					for _, l := range p.deviceStatus() {
						if l.width() > p.w {
							t.Errorf("%s at %d by %s: a line %d wide: %q", name, w, per, l.width(), l.String())
						}
					}
					heads := statusHeads(t, Render(r, o))
					if !slices.IsSortedFunc(heads, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) }) {
						t.Errorf("%s at %d: columns out of order: %v", name, w, heads)
					}
					for _, must := range []string{"DEVICE", "VERSION", "SEEN", strings.ToUpper(per.String()), "90D"} {
						if !slices.Contains(heads, must) {
							t.Errorf("%s at %d by %s: no %s in %v", name, w, per, must, heads)
						}
					}
					for _, h := range last {
						if !slices.Contains(heads, h) {
							t.Errorf("%s at %d by %s: %s shows at %d but not here: %v", name, w, per, h, w-1, heads)
						}
					}
					last = heads
				}
			}
		}
	}
}

// TestStatusNote: a silent device says since when, and the error it last
// reported where it fits; a failing one its error; one behind the latest
// release; a sound one nothing.
func TestStatusNote(t *testing.T) {
	r := troubled(t)
	o := Options{Width: 160, Loc: sampleZone}
	note := func(label string, w int) string {
		t.Helper()
		for _, d := range r.Team.Devices {
			if d.Label == label {
				return newPage(&r, o).deviceNote(d, w).String()
			}
		}
		t.Fatalf("no device %s", label)
		return ""
	}
	for _, c := range []struct {
		label string
		w     int
		want  string
	}{
		{"bot-e", -1, "silent since Mon 14:02 · last error: codex: not logged in"},
		{"bot-e", 52, "silent since Mon 14:02 · last error: codex: not log…"},
		{"bot-e", 48, "silent since Mon 14:02"},
		// Short of room, the note keeps the time whole: since when, then
		// the day, then only that it is silent.
		{"bot-e", 21, "since Mon 14:02"},
		{"bot-e", 16, "since Mon 14:02"},
		{"bot-e", 14, "since Mon"},
		{"bot-e", 8, "silent"},
		{"bot-e", 4, "sil…"},
		{"bot-f", -1, "hermes: database is locked"},
		{"bot-f", 16, "hermes: databas…"},
		// A device that fails and is behind says what fails.
		{"Mac.localdomain", -1, "codex: app-server exited without answering"},
		{"MacBook-Pro-Kim", -1, "update: latest v1.4.2"},
		{"MacBook-Pro-Kim", 20, "latest v1.4.2"},
		{"MacBook-Pro-Kim", 10, "latest v1…"},
		{"srv1", -1, ""},
		{"annbook", -1, ""},
	} {
		if got := note(c.label, c.w); got != c.want {
			t.Errorf("%s in %d: %q, want %q", c.label, c.w, got, c.want)
		}
	}
	// A silent device with no error says only since when. Only an error is
	// in color.
	botE := &r.Team.Devices[slices.IndexFunc(r.Team.Devices, func(d TeamDevice) bool { return d.Label == "bot-e" })]
	botE.Error = nil
	if got := note("bot-e", -1); got != "silent since Mon 14:02" {
		t.Errorf("silent, no error: %q", got)
	}
	if got := note("bot-e", 16); got != "since Mon 14:02" {
		t.Errorf("silent, no error, in 16: %q", got)
	}
	// Silent for more than six days, it says the date, and short of room
	// the date alone.
	botE.CollectedAt = time.Date(2026, 9, 14, 11, 2, 0, 0, time.UTC)
	for w, want := range map[int]string{-1: "silent since 14 Sep 14:02", 18: "since 14 Sep 14:02", 16: "since 14 Sep"} {
		if got := note("bot-e", w); got != want {
			t.Errorf("silent since a date, in %d: %q, want %q", w, got, want)
		}
	}
	o.Color = true
	th := NewTheme(false)
	if got := note("bot-f", -1); !strings.Contains(got, sgr.FindString(lipglossFg(th.Out))) {
		t.Errorf("an error not in its color: %q", got)
	}
	if got := note("MacBook-Pro-Kim", -1); !strings.Contains(got, sgr.FindString(lipglossFg(th.Muted))) {
		t.Errorf("an old release not dim: %q", got)
	}

	// On the page, bot-e's row is silent, and its age in the silent color.
	p := Render(troubled(t), devices(Options{Width: 160, Loc: sampleZone}))
	if l := statusLine(t, p, "bot-e"); !strings.HasPrefix(l, "~ hermes@bot-e ") || !strings.Contains(l, " 2d 3h ") || !strings.HasSuffix(l, "silent since Mon 14:02 · last error: codex: not logged in") {
		t.Errorf("bot-e: %q", l)
	}
	p = Render(troubled(t), devices(Options{Width: 160, Loc: sampleZone, Color: true, Dark: true}))
	if !strings.Contains(strings.Join(p.Body, "\n"), sgr.FindString(lipglossFg(NewTheme(true).Tight))+"2d 3h") {
		t.Error("a silent device's age is not in the silent color")
	}
	// At 80 columns bot-e's NOTE says since when, whole.
	for _, ascii := range []bool{false, true} {
		p := Render(troubled(t), devices(Options{Width: 80, Loc: sampleZone, ASCII: ascii}))
		if l := statusLine(t, p, "bot-e"); !strings.HasSuffix(l, " since Mon 14:02") {
			t.Errorf("bot-e at 80, ascii %v: %q", ascii, l)
		}
	}
}

// TestStatusNoteUpdate: an old device whose release check fails says why,
// in the error's color, and is an error in ATTENTION; one that has run its
// release for longer than updating takes says for how long it has not
// updated, in the tight color, and so does OLD, but not one that has not
// run since, or is silent; a failing check on a device that is not old is a
// dim note alone. A short NOTE keeps some of why a check fails.
func TestStatusNoteUpdate(t *testing.T) {
	r := withoutReplaced(loadReport(t, "team"))
	kim := slices.IndexFunc(r.Team.Devices, func(d TeamDevice) bool { return d.Label == "MacBook-Pro-Kim" })
	srv := slices.IndexFunc(r.Team.Devices, func(d TeamDevice) bool { return d.Label == "srv1" })
	behind, timeout := r.GeneratedAt.Add(-26*time.Hour), "update check: github.com did not answer in 30s"
	r.Team.Devices[kim].BehindSince = &behind
	r.Team.Devices[srv].UpdateError = &timeout
	o := Options{Width: 160, Loc: sampleZone}
	note := func(i, w int) string { return newPage(&r, o).deviceNote(r.Team.Devices[i], w).String() }
	for w, want := range map[int]string{-1: "not updated for 1d · latest v1.4.2", 20: "not updated for 1d", 16: "not updated 1d"} {
		if got := note(kim, w); got != want {
			t.Errorf("behind, in %d: %q, want %q", w, got, want)
		}
	}
	for w, want := range map[int]string{-1: "update check failing: github.com did not answer in 30s", 30: "check failing: github.com did…", 19: "check failing"} {
		if got := note(srv, w); got != want {
			t.Errorf("a failing check on a current device, in %d: %q, want %q", w, got, want)
		}
	}
	if l := statusLine(t, Render(r, devices(Options{Width: 160, Loc: sampleZone})), "srv1"); !strings.Contains(l, "failing: github.com") {
		t.Errorf("srv1 at 160: %q", l)
	}
	old := func() string {
		t.Helper()
		r.Attention = attention(r.Team, r.Collector, r.GeneratedAt)
		got := pageSection(Render(r, Options{Width: 120, Interactive: true}), "ATTENTION")
		if i := slices.IndexFunc(got, func(l string) bool { return strings.HasPrefix(l, " OLD ") }); i >= 0 {
			return got[i]
		}
		t.Fatalf("no OLD in ATTENTION:\n%s", strings.Join(got, "\n"))
		return ""
	}
	if got := old(); got != " OLD         3 devices on old releases  ci@ci-runner-eu-west-build-pool-… +2 · latest v1.4.2 · not updated for up to 1d" {
		t.Errorf("OLD: %q", got)
	}
	// A silent device is silent: OLD does not count its days.
	mac := slices.IndexFunc(r.Team.Devices, func(d TeamDevice) bool { return d.Label == "Mac.localdomain" })
	was, macSince := r.Team.Devices[mac], r.GeneratedAt.Add(-72*time.Hour)
	r.Team.Devices[mac].Silent, r.Team.Devices[mac].CollectedAt, r.Team.Devices[mac].BehindSince = true, r.GeneratedAt.Add(-48*time.Hour), &macSince
	if got := old(); !strings.HasSuffix(got, " · not updated for up to 1d") {
		t.Errorf("OLD with a silent device: %q", got)
	}
	r.Team.Devices[mac] = was
	// Found behind 10 hours ago, but last run 9 hours ago, as a laptop
	// asleep, it has had no run to update in.
	collected := r.Team.Devices[kim].CollectedAt
	r.Team.Devices[kim].CollectedAt, behind = r.GeneratedAt.Add(-9*time.Hour), r.GeneratedAt.Add(-10*time.Hour)
	if got := note(kim, -1); got != "update: latest v1.4.2" {
		t.Errorf("not run since: %q", got)
	}
	if got := old(); strings.Contains(got, "not updated") {
		t.Errorf("OLD, not run since: %q", got)
	}
	r.Team.Devices[kim].CollectedAt, behind = collected, r.GeneratedAt.Add(-26*time.Hour)

	rateLimited := "update check: HTTP 429 from github.com: Too Many Requests"
	r.Team.Devices[kim].UpdateError = &rateLimited
	for w, want := range map[int]string{-1: "update failing: HTTP 429 from github.com: Too Many Requests", 30: "update failing: HTTP 429 from…", 20: "update: HTTP 429 fr…", 14: "update failing"} {
		if got := note(kim, w); got != want {
			t.Errorf("failing, in %d: %q, want %q", w, got, want)
		}
	}
	var errs []string
	for _, a := range attention(r.Team, r.Collector, r.GeneratedAt) {
		if a.Kind == AttentionError {
			errs = append(errs, a.Devices[0]+": "+a.Message)
		}
	}
	if want := []string{"kim@Mac.localdomain: codex: app-server exited without answering", "sam@MacBook-Pro-Kim: " + rateLimited}; !slices.Equal(errs, want) {
		t.Errorf("errors %q, want %q", errs, want)
	}
	// Behind for less than updating takes, it only says the latest.
	behind = r.GeneratedAt.Add(-3 * time.Hour)
	r.Team.Devices[kim].UpdateError = nil
	if got := note(kim, -1); got != "update: latest v1.4.2" {
		t.Errorf("behind for 3h: %q", got)
	}

	o.Color = true
	th := NewTheme(false)
	r.Team.Devices[kim].UpdateError = &rateLimited
	for i, c := range map[int]color.Color{kim: th.Out, srv: th.Muted} {
		if got := note(i, -1); !strings.HasPrefix(got, sgr.FindString(lipglossFg(c))) {
			t.Errorf("%s not in its color: %q", r.Team.Devices[i].Label, got)
		}
	}
	behind, r.Team.Devices[kim].UpdateError = r.GeneratedAt.Add(-26*time.Hour), nil
	if got := note(kim, -1); !strings.HasPrefix(got, sgr.FindString(lipglossFg(th.Tight))) {
		t.Errorf("not updated, not in the tight color: %q", got)
	}
}

// TestStatusNoteReplacedLimitGap: a silent device reinstalled as another
// says so, before since when; a release that cannot read the Claude quota,
// or report its update errors, says what it cannot, after being behind; and
// a device that missed runs says its longest gap, last.
func TestStatusNoteReplacedLimitGap(t *testing.T) {
	r := loadReport(t, "team")
	o := Options{Width: 160, Loc: sampleZone}
	find := func(pred func(TeamDevice) bool) int {
		t.Helper()
		i := slices.IndexFunc(r.Team.Devices, pred)
		if i < 0 {
			t.Fatal("no such device")
		}
		return i
	}
	note := func(i, w int) string { return newPage(&r, o).deviceNote(r.Team.Devices[i], w).String() }
	old := find(func(d TeamDevice) bool { return d.ReplacedBy != nil })
	botC := find(func(d TeamDevice) bool { return d.Label == "bot-c" })
	botD := find(func(d TeamDevice) bool { return d.Label == "bot-d" })
	for _, c := range []struct {
		i, w int
		want string
	}{
		{old, -1, "silent since Sun 12:12 · reinstalled as kim@Mac.localdomain"},
		{old, 40, "reinstalled as kim@Mac.localdomain"},
		{old, 20, "reinstalled"},
		{old, 10, "since Sun"},
		{botC, -1, "gap 12h Sep 22"},
		{botC, 10, "gap 12h"},
	} {
		if got := note(c.i, c.w); got != c.want {
			t.Errorf("%s in %d: %q, want %q", r.Team.Devices[c.i].Label, c.w, got, c.want)
		}
	}
	r.Team.Devices[botD].Limitations = []string{LimitNoUpdateErrors}
	r.Team.Devices[botC].Limitations = []string{LimitNoClaudeQuota, LimitNoUpdateErrors}
	for _, c := range []struct {
		i, w int
		want string
	}{
		{botC, -1, "cannot read Claude quota"},
		{botC, 20, "no Claude quota"},
		{botD, -1, "does not report update errors"},
		{botD, 20, "no update errors"},
	} {
		if got := note(c.i, c.w); got != c.want {
			t.Errorf("%s in %d: %q, want %q", r.Team.Devices[c.i].Label, c.w, got, c.want)
		}
	}
	// Behind, it says so first.
	r.Team.Devices[botC].Old = true
	if got := note(botC, -1); got != "update: latest v1.4.2" {
		t.Errorf("old and limited: %q", got)
	}
}

// withoutReplaced is r without the devices reinstalled as another, whose
// silence and empty periods other tests do not expect.
func withoutReplaced(r Report) Report {
	var gone []string
	r.Team.Devices = slices.DeleteFunc(r.Team.Devices, func(d TeamDevice) bool {
		if d.ReplacedBy != nil {
			gone = append(gone, d.Device)
		}
		return d.ReplacedBy != nil
	})
	r.Team.Matrix.Rows = slices.DeleteFunc(r.Team.Matrix.Rows, func(row Row) bool { return slices.Contains(gone, row.DeviceID) })
	return r
}

// lipglossFg is the escape a text in color c starts with.
func lipglossFg(c color.Color) string {
	p := &page{o: Options{Color: true}}
	return p.paint("x", c, false, false).st.Render("x")
}

// TestStatusVersion: an old release is dim and marked ↓, so it reads
// without color, and puts ↓ in the legend when the row's mark is another.
func TestStatusVersion(t *testing.T) {
	r := loadReport(t, "team")
	for i := range r.Team.Devices {
		if d := &r.Team.Devices[i]; d.Label == "MacBook-Pro-Kim" {
			// Now only the failing device is behind, and its mark is ×.
			d.Old, d.CollectorVersion = false, "v1.4.2"
		}
	}
	for _, ascii := range []bool{false, true} {
		o := devices(Options{Width: 120, Loc: sampleZone, ASCII: ascii})
		p := Render(r, o)
		old, fail, mark := "v1.4.0 ↓", "× kim@Mac.localdomain", "↓ old"
		if ascii {
			old, fail, mark = "v1.4.0 v", "x kim@Mac.localdomain", "v old"
		}
		if l := statusLine(t, p, "Mac.localdomain"); !strings.HasPrefix(l, fail) || !strings.Contains(l, "  "+old+"  ") {
			t.Errorf("ascii %v: %q", ascii, l)
		}
		if l := statusLine(t, p, "MacBook-Pro-Kim"); !strings.Contains(l, "  v1.4.2  ") || strings.Contains(l, old) {
			t.Errorf("ascii %v: %q", ascii, l)
		}
		if !strings.Contains(p.Legend, mark) {
			t.Errorf("ascii %v: no %q in the legend %q", ascii, mark, p.Legend)
		}
	}
	// In color the old release is dim.
	p := Render(r, devices(Options{Width: 120, Color: true, Dark: true}))
	if !strings.Contains(strings.Join(p.Body, "\n"), sgr.FindString(lipglossFg(NewTheme(true).Muted))+"v1.4.0 ↓") {
		t.Error("an old release is not dim")
	}
}

// TestStatusVia: VIA lists the harnesses a device reads, marks one that
// fails or reads in part with ×, and shows · for none; a mark in VIA is in
// the legend only when VIA shows.
func TestStatusVia(t *testing.T) {
	r := withoutReplaced(loadReport(t, "team"))
	for i := range r.Team.Devices {
		d := &r.Team.Devices[i]
		d.Error = nil
		switch d.Label {
		case "bot-a":
			d.Sources[3].Status = "partial"
		case "bot-b":
			for k := range d.Sources {
				d.Sources[k].Status = "skipped"
			}
		case "Mac.localdomain":
			d.Old = false
		}
	}
	for _, c := range []struct {
		ascii           bool
		mac, botA, botB string
		fail, none      string
	}{
		{false, "claude, codex ×", "codex, hermes ×", "·", "× error", "· none"},
		{true, "claude, codex x", "codex, hermes x", ".", "x error", ". none"},
	} {
		p := Render(r, devices(Options{Width: 120, Loc: sampleZone, ASCII: c.ascii}))
		for label, want := range map[string]string{"Mac.localdomain": c.mac, "bot-a": c.botA, "bot-b": c.botB} {
			if l := statusLine(t, p, label); !strings.Contains(l, "  "+want+"  ") {
				t.Errorf("ascii %v, %s: %q, want VIA %q", c.ascii, label, l, want)
			}
		}
		if !strings.Contains(p.Legend, c.fail) || !strings.Contains(p.Legend, c.none) {
			t.Errorf("ascii %v: legend %q", c.ascii, p.Legend)
		}
	}
	// At 80 VIA goes, and its marks with it. UTILIZATION, which has a · of
	// its own, is left out.
	for i := range r.Team.Providers {
		for j := range r.Team.Providers[i].Accounts {
			r.Team.Providers[i].Accounts[j].Utilization = nil
		}
	}
	p := Render(r, devices(Options{Width: 80, Loc: sampleZone}))
	if slices.Contains(statusHeads(t, p), "VIA") || strings.Contains(p.Legend, "× error") || strings.Contains(p.Legend, "· none") {
		t.Errorf("at 80: %v, legend %q", statusHeads(t, p), p.Legend)
	}
}

// TestStatusViews: a team of more than one device has both views, and one
// device has USAGE whichever is asked for.
func TestStatusViews(t *testing.T) {
	team, single := loadReport(t, "team"), loadReport(t, "single")
	o := Options{Width: 120, Loc: sampleZone}
	if p := Render(team, o); !p.DeviceViews || p.MatrixColumns == 0 {
		t.Errorf("team: views %v, matrix %d", p.DeviceViews, p.MatrixColumns)
	}
	if p := Render(team, devices(o)); !p.DeviceViews || p.MatrixColumns != 0 || p.MatrixShown != 0 {
		t.Errorf("team status: views %v, matrix %d of %d", p.DeviceViews, p.MatrixShown, p.MatrixColumns)
	}
	for _, w := range []int{80, 120} {
		o.Width = w
		if a, b := Text(single, o), Text(single, devices(o)); a != b || Render(single, o).DeviceViews {
			t.Errorf("one device at %d: the status view changes the page", w)
		}
	}
	// The matrix names both views where they fit beside its modes.
	for w, want := range map[int]string{80: "15 · 7d UTC · M weighted tokens       ‹tokens›  share", 120: "‹usage›  status   ‹tokens›  share"} {
		if got := pageSection(Render(team, Options{Width: w}), "DEVICES")[0]; !strings.HasSuffix(got, want) {
			t.Errorf("matrix title at %d: %q", w, got)
		}
	}
}

// The team's tables say UTC after every period but a today that counts
// from local midnight, in the matrix and in the status view alike.
func TestTeamTitlesSayWhichDaysTheyCount(t *testing.T) {
	for _, c := range []struct {
		period        Period
		local         bool
		matrix, state string
	}{
		{Today, true, "15 · today · M weighted tokens", "by today · M weighted tokens"},
		{Today, false, "15 · today UTC · M weighted tokens", "by today UTC · M weighted tokens"},
		{Week, true, "15 · 7d UTC · M weighted tokens", "by 7d UTC · M weighted tokens"},
	} {
		r := loadReport(t, "team")
		r.Team.TodayLocal = c.local
		o := Options{Width: 160, Loc: sampleZone, Period: c.period}
		if got := pageSection(Render(r, o), "DEVICES")[0]; !strings.Contains(got, c.matrix) {
			t.Errorf("%v, local %v: matrix title %q, want %q", c.period, c.local, got, c.matrix)
		}
		if got := pageSection(Render(r, devices(o)), "DEVICES")[0]; !strings.Contains(got, c.state) {
			t.Errorf("%v, local %v: status title %q, want %q", c.period, c.local, got, c.state)
		}
	}
	// The guide says today counts from local midnight only when it does.
	r := loadReport(t, "team")
	if notes := strings.Join(guideNotes(r), "\n"); strings.Contains(notes, "local midnight") {
		t.Errorf("guide notes %q", notes)
	}
	r.Team.TodayLocal = true
	if notes := strings.Join(guideNotes(r), "\n"); !strings.Contains(notes, "team tables count today from your local midnight") {
		t.Errorf("guide notes %q", notes)
	}
}
