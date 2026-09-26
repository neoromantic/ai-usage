package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/view"
)

func TestReportBeforeAnyRunWritesNothing(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	r := d.run("", "report", "--json")
	if r.code != 1 || !strings.Contains(r.stderr, "nothing collected yet") {
		t.Fatalf("report: exit %d, stderr %q", r.code, r.stderr)
	}
	if _, err := os.Stat(d.dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(d.dir)
		t.Fatalf("report created collector files: %v", entries)
	}
}

// TestReportFrom: a report saved with --json shows as it was saved, without
// reading or writing this device's state; one saved before health and limits
// shows as one saved after; one of another schema is refused.
func TestReportFrom(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	saved := filepath.Join("..", "..", "internal", "view", "testdata", "team.json")
	if out := d.ok("report", "--from", saved, "--width", "120"); !strings.Contains(out, "ai-usage · annbook · team qmvrtzpa") ||
		!strings.Contains(out, "\nDEVICES × SUBSCRIPTIONS  13 · 7d") {
		t.Fatalf("report --from:\n%s", out)
	}
	var r view.Report
	if err := json.Unmarshal([]byte(d.ok("report", "--from", saved, "--json")), &r); err != nil {
		t.Fatal(err)
	}
	if r.Collector.DeviceLabel != "annbook" || len(r.Team.Devices) != 13 {
		t.Fatalf("report --from --json: %s with %d devices", r.Collector.DeviceLabel, len(r.Team.Devices))
	}
	if _, err := os.Stat(d.dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(d.dir)
		t.Fatalf("report --from created collector files: %v", entries)
	}
	earlier := filepath.Join(t.TempDir(), "earlier.json")
	if err := os.WriteFile(earlier, withoutHealthAndLimits(t, saved), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--width", "120"}, {"--json"}} {
		want := d.ok(append([]string{"report", "--from", saved}, args...)...)
		if got := d.ok(append([]string{"report", "--from", earlier}, args...)...); got != want {
			t.Errorf("report --from an earlier report %v:\n%s\nwant\n%s", args, got, want)
		}
	}
	old := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(old, []byte(`{"schema_version": 3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := d.run("", "report", "--from", old); res.code != 1 || !strings.Contains(res.stderr, "schema_version 3, this release reads") {
		t.Fatalf("report --from an old report: exit %d, stderr %q", res.code, res.stderr)
	}
}

// withoutHealthAndLimits is a saved report as a release before
// collector.health and window limits saved it.
func withoutHealthAndLimits(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	delete(r["collector"].(map[string]any), "health")
	var strip func(any)
	strip = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			delete(v, "limits")
			for _, e := range v {
				strip(e)
			}
		case []any:
			for _, e := range v {
				strip(e)
			}
		}
	}
	strip(r)
	if b, err = json.Marshal(r); err != nil {
		t.Fatal(err)
	}
	return b
}
