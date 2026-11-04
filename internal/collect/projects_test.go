package collect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// repoSessions makes n repositories with an origin under root, and a
// session in each, the first the largest.
func repoSessions(t *testing.T, root string, n int, at time.Time) []logs.Session {
	t.Helper()
	var out []logs.Session
	for i := range n {
		dir := filepath.Join(root, fmt.Sprintf("r%03d", i))
		mkdirs(t, filepath.Join(dir, ".git"))
		for name, body := range map[string]string{"HEAD": "ref: refs/heads/main\n",
			"config": fmt.Sprintf("[remote \"origin\"]\n\turl = git@github.com:acme/r%03d.git\n", i)} {
			if err := os.WriteFile(filepath.Join(dir, ".git", name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, sess(fmt.Sprintf("s%03d", i), dir, int64(1000*(n-i)), at))
	}
	return out
}

// A device sends more than 12 projects an account once a read shows every
// teammate accepts them, and goes without them for a day when the relay
// refuses them.
func TestPublishSendsManyProjectsBehindTheGate(t *testing.T) {
	for _, c := range []struct {
		name           string
		teammate       string
		rejectProjects bool
		second         []string // what the second run put
	}{
		{"a teammate on v0.13.4", "v0.13.4", false, []string{"hours"}},
		{"everyone on v0.14.0", "v0.14.0", false, []string{"projects"}},
		{"a relay on v0.13.4", "v0.14.0", true, []string{"projects", "hours"}},
	} {
		w, o := newWorld(t)
		o.Version = "v0.14.0"
		h := w.home(t, "claude")
		w.login("claude", h, "ann@acme.dev", quota(t0, 10))
		w.sessions("claude", h, repoSessions(t, t.TempDir(), 20, t0)...)
		r, rec := newRecordingRelay(t, w)
		o.Relay = r.client(w)
		key, _ := LoadKey(o.Dir)
		publishRelease(t, r, w, key, "d-teammate-device", c.teammate, ledger(), t0)
		rec.mu.Lock()
		rec.rejectProjects = c.rejectProjects
		rec.mu.Unlock()

		res := run(t, o)
		self := res.Config.Device
		if got := rec.wire(self, 0); !slices.Equal(got, []string{"legacy"}) {
			t.Fatalf("%s: the first run put %v", c.name, got)
		}
		if n := len(res.Doc.Accounts[0].Projects); n != 20 {
			t.Fatalf("%s: the run's own snapshot has %d projects", c.name, n)
		}

		w.now = t0.Add(15 * time.Minute)
		from := rec.count()
		res = run(t, o)
		if got := rec.wire(self, from); !slices.Equal(got, c.second) {
			t.Fatalf("%s: the second run put %v, want %v", c.name, got, c.second)
		}
		rs := res.State.Relay
		if rs.LastError != "" || rs.Pending {
			t.Fatalf("%s: relay state = %+v", c.name, rs)
		}
		if want := (time.Time{}); c.rejectProjects {
			if want = w.now.Add(24 * time.Hour); !rs.NoProjectsUntil.Equal(want) {
				t.Fatalf("%s: no projects until %v", c.name, rs.NoProjectsUntil)
			}
		} else if !rs.NoProjectsUntil.Equal(want) {
			t.Fatalf("%s: no projects until %v", c.name, rs.NoProjectsUntil)
		}
		rec.mu.Lock()
		last := rec.puts[len(rec.puts)-1]
		rec.mu.Unlock()
		if n, want := len(last.Accounts[0].Projects), map[bool]int{true: 20, false: snapshot.LegacyProjects}[c.second[len(c.second)-1] == "projects"]; n != want {
			t.Fatalf("%s: the relay keeps %d projects, want %d", c.name, n, want)
		}
	}
}

// Of 128 projects an account, those that do not fit go, the smallest
// first, before anything else; an account's 12 largest are what a release
// before v0.14.0 reads, fitted as it fits them.
func TestFitDocKeepsManyProjectsLast(t *testing.T) {
	key := mustKey(t)
	st := state.NewState()
	st.LastRunAt = t0
	repos := Repos{IDs: map[string]string{}}
	for a := range 4 {
		label := fmt.Sprintf("u%d@acme.dev", a)
		st.Accounts[state.Key("claude", label)] = &state.Account{Provider: "claude", Label: label}
		for i := range 160 {
			dir := fmt.Sprintf("/src/a%d/r%03d", a, i)
			repos.IDs[dir] = "github.com/acme" + dir
			st.Sessions[state.Key("claude", fmt.Sprintf("%d-%d", a, i))] = &state.Session{Provider: "claude", Project: dir,
				Updated: t0, By: map[string]snapshot.Tokens{label: tok(int64(10000 * (160 - i)))}}
		}
	}
	doc, body := encodeDecode(t, BuildDoc(st, nil, key, state.Config{Device: "d-0123456789"}, repos, "box", "ann", "v0.14.0", t0))
	if len(body) > snapshot.MaxBytes {
		t.Fatalf("%d bytes", len(body))
	}
	total := 0
	for _, a := range doc.Accounts {
		if len(a.Projects) > snapshot.MaxProjects || len(a.Projects) < snapshot.LegacyProjects {
			t.Fatalf("an account has %d projects", len(a.Projects))
		}
		total += len(a.Projects)
	}
	if len(doc.Accounts) != 4 || total <= 4*snapshot.LegacyProjects {
		t.Fatalf("%d accounts, %d projects", len(doc.Accounts), total)
	}
	t.Logf("%d projects in %d bytes", total, len(body))
	// What a release before v0.14.0 reads: 12 an account, the largest.
	few := doc.WithoutManyProjects()
	if few.HasManyProjects() {
		t.Fatal("WithoutManyProjects keeps more than 12")
	}
	for i, a := range few.Accounts {
		if !slices.Equal(a.Projects, doc.Accounts[i].Projects[:snapshot.LegacyProjects]) {
			t.Fatalf("account %d keeps other projects", i)
		}
	}
	if b, _ := json.Marshal(few); len(b) >= len(body) {
		t.Fatal("the legacy body is no smaller")
	}
	// One account's 128 fit with room to spare.
	one := Repos{IDs: map[string]string{}}
	for i := range 128 {
		one.IDs[fmt.Sprintf("/src/a0/r%03d", i)] = repos.IDs[fmt.Sprintf("/src/a0/r%03d", i)]
	}
	doc, body = encodeDecode(t, BuildDoc(st, nil, key, state.Config{Device: "d-0123456789"}, one, "box", "ann", "v0.14.0", t0))
	if n := len(doc.Accounts[0].Projects); n != snapshot.MaxProjects {
		t.Fatalf("one account keeps %d of 128 projects (%d bytes)", n, len(body))
	}
	t.Logf("one account's 128 projects: %d bytes", len(body))
}
