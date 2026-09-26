package logs

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCodexLimits(t *testing.T) {
	main := func(pct float64) string {
		return fmt.Sprintf(`{"limit_id":"codex","limit_name":null,"primary":{"used_percent":%v,"window_minutes":300,"resets_at":1790000000},"secondary":{"used_percent":7.5,"window_minutes":10080,"resets_at":1790500000},"credits":null,"plan_type":"plus"}`, pct)
	}
	spark := `{"limit_id":"codex_bengalfox","limit_name":"GPT-5.3-Codex-Spark","primary":{"used_percent":64.0,"window_minutes":300,"resets_at":1790001000},"secondary":null,"plan_type":"plus"}`
	premium := `{"limit_id":"premium","primary":null,"secondary":null,"plan_type":"plus"}`
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	reset := func(sec int64) time.Time { return time.Unix(sec, 0).UTC() }
	type window struct {
		name  string
		pct   float64
		reset time.Time
	}
	cases := []struct {
		name     string
		files    map[string][]string // leaf/day -> lines
		observed time.Time
		plan     string
		want     []window
	}{
		{
			name: "resets_in_seconds",
			files: map[string][]string{"sessions/20": {
				cxLimits("2026-09-20T10:00:00Z", `{"primary":{"used_percent":55.5,"window_minutes":300,"resets_in_seconds":3600},"secondary":{"used_percent":1,"window_minutes":10080}}`),
			}},
			observed: at("2026-09-20T10:00:00Z"),
			want:     []window{{"5h", 55.5, at("2026-09-20T11:00:00Z")}, {"7d", 1, time.Time{}}},
		},
		{
			// The archived file is walked last but read earlier.
			name: "newest across files",
			files: map[string][]string{
				"sessions/20":          {cxLimits("2026-09-20T12:00:00Z", main(60))},
				"archived_sessions/20": {cxLimits("2026-09-20T09:00:00Z", main(30))},
			},
			observed: at("2026-09-20T12:00:00Z"),
			plan:     "plus",
			want:     []window{{"5h", 60, reset(1790000000)}, {"7d", 7.5, reset(1790500000)}},
		},
		{
			name: "last line in a file wins",
			files: map[string][]string{"sessions/20": {
				cxLimits("2026-09-20T10:00:00Z", main(10)),
				cxLimits("2026-09-20T10:05:00Z", main(20)),
			}},
			observed: at("2026-09-20T10:05:00Z"),
			plan:     "plus",
			want:     []window{{"5h", 20, reset(1790000000)}, {"7d", 7.5, reset(1790500000)}},
		},
		{
			name: "a later side limit does not hide the main one",
			files: map[string][]string{"sessions/20": {
				cxLimits("2026-09-20T10:00:00Z", main(40)),
				cxLimits("2026-09-20T12:00:00Z", spark),
				cxLimits("2026-09-20T13:00:00Z", premium),
			}},
			observed: at("2026-09-20T10:00:00Z"),
			plan:     "plus",
			want:     []window{{"5h", 40, reset(1790000000)}, {"7d", 7.5, reset(1790500000)}, {"codex_bengalfox 5h", 64, reset(1790001000)}},
		},
		{
			name: "a side limit read long before is left out",
			files: map[string][]string{"sessions/20": {
				cxLimits("2026-09-20T08:00:00Z", spark),
				cxLimits("2026-09-20T10:00:00Z", main(40)),
			}},
			observed: at("2026-09-20T10:00:00Z"),
			plan:     "plus",
			want:     []window{{"5h", 40, reset(1790000000)}, {"7d", 7.5, reset(1790500000)}},
		},
		{
			name: "only a side limit",
			files: map[string][]string{"sessions/20": {
				cxLimits("2026-09-20T08:00:00Z", spark),
			}},
			observed: at("2026-09-20T08:00:00Z"),
			plan:     "plus",
			want:     []window{{"codex_bengalfox 5h", 64, reset(1790001000)}},
		},
		{
			name: "no window and no time are no reading",
			files: map[string][]string{"sessions/20": {
				cxLimits("2026-09-20T08:00:00Z", premium),
				cxLimits("not a time", main(90)),
				cxLimits("2026-09-20T09:00:00Z", "null"),
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			for where, lines := range c.files {
				leaf, day, _ := strings.Cut(where, "/")
				mustWrite(t, rollout(home, leaf, day, rootID), append([]string{cxMeta{at: "2026-09-20T07:00:00Z", id: rootID, cwd: "/w"}.String()}, lines...)...)
			}
			res := mustRead(t, "codex", home, since)
			if res.Malformed != 0 {
				t.Fatalf("malformed = %d", res.Malformed)
			}
			if c.want == nil {
				if res.Limits != nil {
					t.Fatalf("limits = %+v", res.Limits)
				}
				return
			}
			l := res.Limits
			if l == nil {
				t.Fatal("no limits")
			}
			if !l.ObservedAt.Equal(c.observed) || l.Plan != c.plan {
				t.Fatalf("observed %v plan %q", l.ObservedAt, l.Plan)
			}
			if len(l.Windows) != len(c.want) {
				t.Fatalf("windows = %+v", l.Windows)
			}
			for i, w := range c.want {
				got := l.Windows[i]
				gotReset := time.Time{}
				if got.ResetsAt != nil {
					gotReset = *got.ResetsAt
				}
				if got.Name != w.name || got.Percent != w.pct || !gotReset.Equal(w.reset) {
					t.Fatalf("window %d = %+v reset %v, want %+v", i, got, gotReset, w)
				}
			}
		})
	}
}

// TestCodexLimits names the main and side windows; snapshot's tests cover
// durations and plain labels. A side window's whole name is a plain label.
func TestCodexWindowName(t *testing.T) {
	if got := CodexWindowName("a/b#c", 60); got != "a/b-c 1h" {
		t.Errorf("CodexWindowName = %q", got)
	}
}
