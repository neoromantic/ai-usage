package view

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

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

func TestTruncPath(t *testing.T) {
	for _, c := range []struct {
		p    string
		w    int
		want string
	}{
		{"~/src/acme/api", 20, "~/src/acme/api"},
		{"~/orca/workspaces/monorepo/faster-dashboard-queries", 40, "~/orca/…/faster-dashboard-queries"},
		{"~/orca/workspaces/monorepo/fix-x", 30, "~/orca/…/monorepo/fix-x"},
		{"/opt/build/agents/workspace/project", 26, "/opt/…/workspace/project"},
		// Without the first folder, the root and the last one still fit.
		{"/Users/someone/deep/project-name-that-is-long", 28, "/…/project-name-that-is-long"},
		{"~/Developer/acme/worktrees/checkout-flow-migration", 36, "~/…/checkout-flow-migration"},
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

// TestWidth measures text as a terminal draws it: emoji two columns wide,
// in the Basic Multilingual Plane too, and a character with its modifiers
// as one.
func TestWidth(t *testing.T) {
	for _, c := range []struct {
		s    string
		want int
	}{
		{"annbook", 7},
		{"⚡✅✨❌⭐", 10},
		{"⚡️ci-runner✅", 13},
		{"日本語", 6},
		{"café", 4},
		{"━─┃╋┈●×↓·‹›—…", 13},
	} {
		if got := width(c.s); got != c.want {
			t.Errorf("width(%q) = %d, want %d", c.s, got, c.want)
		}
	}
	for _, c := range []struct {
		s    string
		w    int
		pre  string
		suf  string
		cutE string
	}{
		{"⚡️ci✅", 3, "⚡️c", "i✅", "⚡️…"},
		{"⚡️ci✅", 2, "⚡️", "✅", "…"},
		{"cafés", 4, "café", "afés", "caf…"},
	} {
		if got := prefix(c.s, c.w); got != c.pre {
			t.Errorf("prefix(%q, %d) = %q, want %q", c.s, c.w, got, c.pre)
		}
		if got := suffix(c.s, c.w); got != c.suf {
			t.Errorf("suffix(%q, %d) = %q, want %q", c.s, c.w, got, c.suf)
		}
		if got := truncEnd(c.s, c.w, "…"); got != c.cutE || width(got) > c.w {
			t.Errorf("truncEnd(%q, %d) = %q, want %q", c.s, c.w, got, c.cutE)
		}
	}
}

// TestPageWideCharacters draws a page whose device, label, and path carry
// emoji, and measures each line as a terminal does: none is too wide, and
// the matrix keeps its columns.
func TestPageWideCharacters(t *testing.T) {
	r := loadReport(t, "team")
	r.Collector.DeviceLabel = "⚡✅✨❌⭐box"
	r.Projects[0].Path = "/Users/ann/src/⚡✅✨❌⭐-app"
	for i := range r.Team.Matrix.Rows {
		if r.Team.Matrix.Rows[i].Device == "srv1" {
			r.Team.Matrix.Rows[i].Device = "⚡️ci-runner✅"
		}
	}
	p := Render(r, Options{Width: 80, Loc: sampleZone})
	for i, l := range append([]string{p.Header}, p.Body...) {
		if w := ansi.StringWidth(l); w > 79 {
			t.Errorf("line %d is %d wide: %q", i, w, sgr.ReplaceAllString(l, ""))
		}
	}
	m := pageSection(p, "DEVICES")
	for _, l := range m[2:] {
		if w, want := ansi.StringWidth(l), ansi.StringWidth(m[2]); w != want {
			t.Errorf("a matrix row is %d wide, its header %d:\n%s", w, want, strings.Join(m, "\n"))
			break
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
		if got := nameList(names, c.w, "…"); got != c.want || width(got) > c.w {
			t.Errorf("nameList(%d) = %q, want %q", c.w, got, c.want)
		}
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
