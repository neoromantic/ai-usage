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
// reading or writing this device's state; one of another schema is refused.
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
	old := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(old, []byte(`{"schema_version": 3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := d.run("", "report", "--from", old); res.code != 1 || !strings.Contains(res.stderr, "schema_version 3, this release reads") {
		t.Fatalf("report --from an old report: exit %d, stderr %q", res.code, res.stderr)
	}
}
