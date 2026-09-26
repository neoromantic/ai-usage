package view

import (
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/state"
)

// A server reads dozens of Hermes homes; the status line names two and
// counts the rest.
func TestStatusFoldsManyHomes(t *testing.T) {
	st := emptyState()
	st.Sources["hermes"] = state.Source{Status: "ok", Homes: []string{"/srv/a/.hermes", "/srv/b/.hermes", "/srv/c/.hermes", "/srv/d/.hermes", "/srv/e/.hermes"}}
	st.Sources["codex"] = state.Source{Status: "ok", Homes: []string{"/srv/a/.codex", "/srv/b/.codex", "/srv/c/.codex"}}
	f := newFixture(t, st)
	status := StatusText(Build(f.in), "", Options{Width: 100, Loc: time.UTC})
	for _, want := range []string{
		"hermes  /srv/a/.hermes, /srv/b/.hermes, +3 more (ai-usage home) · no accounts yet\n",
		"codex   /srv/a/.codex, /srv/b/.codex, /srv/c/.codex · no accounts yet\n",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}
}

// A home the Claude app keeps for a session ends in .claude, but the user's
// home folder is not above it.
func TestClaudeAppHomeIsNotTheUsersHome(t *testing.T) {
	st := emptyState()
	st.Sources["claude"] = state.Source{Status: "ok", Homes: []string{"/Users/ann/Library/Application Support/Claude/local-agent-mode-sessions/a/b/local_c1/.claude"}}
	st.Sources["codex"] = state.Source{Status: "ok", Homes: []string{"/Users/ann/.codex"}}
	status := StatusText(Build(newFixture(t, st).in), "", Options{Width: 100, Loc: time.UTC})
	if want := "codex   ~/.codex · no accounts yet\n"; !strings.Contains(status, want) {
		t.Fatalf("status lacks %q:\n%s", want, status)
	}
}

// TestStatusUpdateLine: a release installed by hand that is newer than the
// last update check saw is the newest release, not an older one.
func TestStatusUpdateLine(t *testing.T) {
	for _, tc := range []struct{ latest, want string }{
		{"v1.2.3", "v1.2.3 is the newest release"},
		{"v1.2.0", "v1.2.3 is the newest release"},
		{"v1.4.0", "newest release v1.4.0"},
	} {
		st := emptyState()
		st.Update = state.Update{Latest: tc.latest}
		f := newFixture(t, st)
		status := StatusText(Build(f.in), "", Options{Width: 100, Loc: time.UTC})
		if !strings.Contains(status, tc.want) {
			t.Fatalf("latest %s: status lacks %q:\n%s", tc.latest, tc.want, status)
		}
	}
}

func TestCollectorSection(t *testing.T) {
	st := emptyState()
	st.LastError, st.LastErrorAt = "claude: 2 malformed lines", now.Add(-time.Hour)
	st.Relay = state.Relay{LastPushAt: now.Add(-20 * time.Minute), LastPullAt: now.Add(-20 * time.Minute), Pending: true, LastError: "relay unreachable: refused"}
	st.Schedule = state.Schedule{Registered: false, Error: "crontab: permission denied"}
	st.Update = state.Update{Latest: "v1.3.0", Installed: "v1.3.0"}
	st.Sources["grok"] = state.Source{Status: "skipped"}
	st.Sources["claude"] = state.Source{Status: "partial", Error: "2 malformed lines"}
	f := newFixture(t, st)
	r := Build(f.in)
	c := r.Collector
	if c.Team != f.key.Fingerprint() || c.Device != "d-this-device" || c.Version != "v1.2.3" || !c.Relay.Pending {
		t.Fatalf("collector = %+v", c)
	}
	if c.Update.Staged == nil || *c.Update.Staged != "v1.3.0" {
		t.Fatalf("staged = %v", c.Update.Staged)
	}
	// This device is on an older release than the one its check saw, and a
	// harness of it and its relay fail.
	if len(r.Attention) != 3 || r.Attention[0].Kind != AttentionError || r.Attention[0].Message != "claude: 2 malformed lines" ||
		r.Attention[1].Kind != AttentionError || r.Attention[1].Message != "relay unreachable: refused" || r.Attention[2].Kind != AttentionOld {
		t.Fatalf("attention = %+v", r.Attention)
	}
	status := StatusText(r, "/home/.config/ai-usage", Options{Width: 80, Loc: time.UTC})
	for _, want := range []string{
		"\n✕ 3 problems: relay failing, not scheduled, claude partial\n",
		"\ndirectory       ~/.config/ai-usage\n",
		"\nlast error      11:00 (1h ago): claude: 2 malformed lines\n",
		"\nrelay         ✕ https://relay.example\n                pushed 11:40 (20m ago) · pulled 11:40 (20m ago)\n" +
			"                the newest snapshot is not sent yet\n                relay unreachable: refused\n",
		"\nschedule      ✕ not registered: crontab: permission denied\n                register: ai-usage schedule install\n",
		"\nupdate        ↑ v1.3.0 is installed and runs next time\n                checked never\n",
		"\nsources       ◐ claude  no accounts yet\n                        2 malformed lines\n",
		"\n              · grok    not installed\n",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status lacks %q:\n%s", want, status)
		}
	}

	// After `schedule remove`, status still says how to register again.
	saved := f.in.State.Schedule.Error
	f.in.State.Schedule.Error = "removed by `ai-usage schedule remove`"
	if status := StatusText(Build(f.in), "", Options{Width: 80, Loc: time.UTC}); !strings.Contains(status, "register: ai-usage schedule install") {
		t.Fatalf("status after schedule remove lacks the register hint:\n%s", status)
	}
	f.in.State.Schedule.Error = saved

	// Once the new version runs, the staged note goes away.
	f.in.Version = "v1.3.0"
	if r := Build(f.in); r.Collector.Update.Staged != nil {
		t.Fatalf("staged after it ran: %v", r.Collector.Update.Staged)
	}

	f.in.RelayURL = ""
	r = Build(f.in)
	if status := StatusText(r, "", Options{Loc: time.UTC}); !strings.Contains(status, "\nrelay         · not configured; the team view shows this device only\n                set one: ai-usage relay set URL\n") {
		t.Fatalf("unconfigured relay not shown:\n%s", status)
	}
}
