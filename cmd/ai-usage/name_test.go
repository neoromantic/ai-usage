package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neoromantic/ai-usage/internal/view"
	"github.com/neoromantic/ai-usage/relay"
)

// A device goes by the name it is given instead of its host name, in its own
// report and in the team's, and AI_USAGE_NAME overrides both.
func TestDeviceName(t *testing.T) {
	hermetic(t)
	srv := httptest.NewServer(relay.NewServer(relay.NewMemory()))
	defer srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)
	t.Setenv("AI_USAGE_NAME", "")

	a, b := newDevice(t), newDevice(t)
	a.claude("aaaa", "/work/app", 1)
	if out := a.ok("name"); !strings.HasPrefix(out, "test-host") {
		t.Fatalf("name = %q", out)
	}
	for _, bad := range []string{"  ", "a\tb", strings.Repeat("я", maxName+1)} {
		if r := a.run("", "name", "set", bad); r.code != 2 {
			t.Fatalf("name set %q: exit %d, %s", bad, r.code, r.stderr)
		}
	}
	a.ok("name", "set", "  Build bot  ")
	if got := a.config().Name; got != "Build bot" {
		t.Fatalf("config name = %q", got)
	}
	if out := a.ok("name"); out != "Build bot\n" {
		t.Fatalf("name = %q", out)
	}
	a.ok("collect", "--quiet")
	b.ok("team", "join", strings.TrimSpace(a.ok("team", "key")))
	b.ok("collect", "--json")

	label := func(r view.Report, device string) string {
		for _, dev := range r.Team.Devices {
			if dev.Device == device {
				return dev.Label
			}
		}
		return ""
	}
	if r := a.report(); r.Collector.DeviceLabel != "Build bot" || label(r, a.config().Device) != "Build bot" {
		t.Fatalf("own label = %q, in the team %q", r.Collector.DeviceLabel, label(r, a.config().Device))
	}
	if got := label(b.report(), a.config().Device); got != "Build bot" {
		t.Fatalf("the team sees %q", got)
	}

	// The scheduler's runs do not see a shell's AI_USAGE_NAME, and name says
	// what they use instead.
	t.Setenv("AI_USAGE_NAME", "from-env")
	if out := a.ok("name"); !strings.HasPrefix(out, "from-env") || !strings.Contains(out, "Build bot") {
		t.Fatalf("name with AI_USAGE_NAME = %q", out)
	}
	if out := a.ok("name", "set", "Build bot"); !strings.Contains(out, "from-env") {
		t.Fatalf("name set with AI_USAGE_NAME = %q", out)
	}
	a.ok("collect", "--quiet")
	t.Setenv("AI_USAGE_NAME", "")
	b.ok("collect", "--json")
	if got := label(b.report(), a.config().Device); got != "from-env" {
		t.Fatalf("the team sees %q after AI_USAGE_NAME", got)
	}

	// A name set would refuse is not used from the environment either.
	t.Setenv("AI_USAGE_NAME", "evil\nFAKE LINE\x1b[2J")
	if out := a.ok("name"); !strings.Contains(out, "ignored") || !strings.HasSuffix(out, "\nBuild bot\n") {
		t.Fatalf("name with a bad AI_USAGE_NAME = %q", out)
	}
	if r := a.report(); r.Collector.DeviceLabel != "Build bot" {
		t.Fatalf("own label with a bad AI_USAGE_NAME = %q", r.Collector.DeviceLabel)
	}
	t.Setenv("AI_USAGE_NAME", "")

	if out := a.ok("name", "clear"); !strings.Contains(out, "test-host") {
		t.Fatalf("name clear = %q", out)
	}
	a.ok("collect", "--quiet")
	b.ok("collect", "--json")
	if got := label(b.report(), a.config().Device); got != "test-host" {
		t.Fatalf("the team sees %q after name clear", got)
	}
}
