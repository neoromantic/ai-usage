package collect

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/state"
)

// claudeAppHome makes the home the Claude app keeps for session id, in dir
// of one account's folder in its sessions folder, with the app's record of
// the session beside it unless record is "".
func claudeAppHome(t *testing.T, userHome, dir, id, record string) string {
	t.Helper()
	s := filepath.Join(defaultAppData("Claude", userHome), claudeAppSessions, "org", "user", dir, "local_"+id)
	h := filepath.Join(s, ".claude")
	mkdirs(t, h)
	if record != "" {
		if err := os.WriteFile(s+".json", []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// The Claude app's sessions are found by path and go to the account the
// app recorded for each, with the folder the person gave it as the project.
// Nothing asks about them, and nothing about them is a problem.
func TestClaudeAppSessionsGoToTheirRecordedAccount(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "claude")
	w.login("claude", def, "ann@x", quota(t0, 40))
	w.sessions("claude", def, sess("cli", "/src/api", 100, t0))
	a := claudeAppHome(t, w.userHome, "", "a1", `{"emailAddress":"ann@x","userSelectedFolders":["/src/site","/src/docs"],"cwd":"/sessions/brave-owl","title":"t"}`)
	b := claudeAppHome(t, w.userHome, "agent", "ditto_b2", `{"emailAddress":"bea@x","userSelectedFolders":[]}`)
	c := claudeAppHome(t, w.userHome, "", "c3", "")
	w.sessions("claude", a, sess("app-a", "/sessions/brave-owl", 200, t0))
	w.sessions("claude", b, sess("app-b", "/sessions/calm-fox", 300, t0))
	w.sessions("claude", c, sess("app-c", "/sessions/dry-elk", 400, t0))
	res := run(t, o)

	if got := res.State.Sources["claude"]; got.Status != "ok" || got.Error != "" || !reflect.DeepEqual(got.Homes, []string{def, b, a, c}) {
		t.Fatalf("claude source = %+v", got)
	}
	if want := []string{state.Key("claude", def)}; !reflect.DeepEqual(w.asked, want) {
		t.Fatalf("asked %v, want %v", w.asked, want)
	}
	if len(res.Config.Homes) != 0 {
		t.Fatalf("remembered %v", res.Config.Homes)
	}
	ann := totalsFor(t, res.State, "claude", "ann@x")
	if !ann.Current || ann.Tokens != tok(300) || ann.Quota == nil || ann.Quota.Windows[0].Percent != 40 {
		t.Fatalf("ann = %+v", ann)
	}
	if bea := totalsFor(t, res.State, "claude", "bea@x"); bea.Current || bea.Tokens != tok(300) || bea.Quota != nil {
		t.Fatalf("bea = %+v", bea)
	}
	if u := totalsFor(t, res.State, "claude", UnknownAccount); u.Tokens != tok(400) {
		t.Fatalf("unknown = %+v", u)
	}
	for id, want := range map[string]string{"app-a": "/src/site", "app-b": claudeAppProject, "app-c": claudeAppProject} {
		if got := res.State.Sessions[state.Key("claude", id)].Project; got != want {
			t.Fatalf("%s project = %q, want %q", id, got, want)
		}
	}
}

// An account only the app's records name leaves a home that never answered
// its claim on the history counted there.
func TestClaudeAppAccountDoesNotStopAClaim(t *testing.T) {
	w, o := newWorld(t)
	def := w.home(t, "claude")
	k := state.Key("claude", def)
	w.askErr[k] = errors.New("no answer")
	w.sessions("claude", def, sess("cli", "/src/api", 100, t0))
	a := claudeAppHome(t, w.userHome, "", "a1", `{"emailAddress":"bea@x"}`)
	w.sessions("claude", a, sess("app-a", "/src/site", 200, t0))
	run(t, o)

	w.now = t0.Add(15 * time.Minute)
	delete(w.askErr, k)
	w.login("claude", def, "ann@x", nil)
	res := run(t, o)
	if ann := totalsFor(t, res.State, "claude", "ann@x"); ann.Tokens != tok(100) {
		t.Fatalf("ann = %+v", ann)
	}
	if hasTotals(res.State, "claude", UnknownAccount) {
		t.Fatal("unknown account kept history")
	}
}
