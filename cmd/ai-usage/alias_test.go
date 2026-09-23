package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

// aliasDevice is a device that has collected dev@example.com on Claude and on
// Codex: one label on two providers.
func aliasDevice(t *testing.T) *device {
	t.Helper()
	d := newDevice(t)
	d.claude("aaaa", "/work/app", 1)
	d.codex("c-a", "/work/api")
	d.ok("collect", "--quiet", "--offline")
	return d
}

// addAccount puts an account with a quota reading in the device's ledger, as
// a run would.
func (d *device) addAccount(provider, label string) {
	d.t.Helper()
	dir := state.Dir(d.dir)
	st, err := dir.LoadState()
	if err != nil {
		d.t.Fatal(err)
	}
	now := time.Now().UTC()
	q := &state.Quota{At: now, Source: "harness", Windows: []snapshot.Window{{Name: "7d", Percent: 10}}}
	st.Accounts[state.Key(provider, label)] = &state.Account{Provider: provider, Label: label, Quota: q, LastSeenAt: now}
	if err := dir.SaveState(st); err != nil {
		d.t.Fatal(err)
	}
}

func TestAliasSetListAndClear(t *testing.T) {
	hermetic(t)
	d := aliasDevice(t)
	if out := d.ok("alias"); !strings.HasPrefix(out, "no account has a name yet") {
		t.Fatalf("alias with none set = %q", out)
	}

	// The same label on two providers is one person: both get the name.
	t0 := time.Date(2026, 9, 23, 18, 0, 0, 0, time.UTC)
	clock = func() time.Time { return t0 }
	out := d.ok("alias", "dev", "ann")
	if !strings.Contains(out, "dev@example.com is now ann on claude and codex") || !strings.Contains(out, "codex:dev@example.com") {
		t.Fatalf("alias printed %q", out)
	}
	want := map[string]state.Alias{
		state.Key("claude", "dev@example.com"): {Name: "ann", At: t0},
		state.Key("codex", "dev@example.com"):  {Name: "ann", At: t0},
	}
	if got := d.config().Aliases; !equalAliases(got, want) {
		t.Fatalf("aliases = %+v", got)
	}
	list := d.ok("alias")
	for _, p := range []string{"claude", "codex"} {
		if !strings.Contains(list, p) || strings.Count(list, "dev@example.com  ann  set on this device") != 2 {
			t.Fatalf("alias list:\n%s", list)
		}
	}

	// A provider before the account names that one alone. An email matches
	// in any case.
	t1 := t0.Add(time.Minute)
	clock = func() time.Time { return t1 }
	if out := d.ok("alias", "codex:DEV@example.com", "ann-cx"); !strings.HasPrefix(out, "dev@example.com is now ann-cx on codex;") {
		t.Fatalf("alias with a provider printed %q", out)
	}
	want[state.Key("codex", "dev@example.com")] = state.Alias{Name: "ann-cx", At: t1}
	if got := d.config().Aliases; !equalAliases(got, want) {
		t.Fatalf("aliases = %+v", got)
	}

	// An account is found by the name it goes by, and clearing keeps an entry
	// with no name, newer than the name, so that it outranks the name on
	// other devices.
	t2 := t1.Add(time.Minute)
	clock = func() time.Time { return t2 }
	if out := d.ok("alias", "ann-cx", "--clear"); !strings.Contains(out, "no longer goes by ann-cx on codex") {
		t.Fatalf("alias --clear printed %q", out)
	}
	want[state.Key("codex", "dev@example.com")] = state.Alias{At: t2}
	if got := d.config().Aliases; !equalAliases(got, want) {
		t.Fatalf("aliases after clear = %+v", got)
	}
	d.ok("alias", "--clear", "claude:ann")
	if out := d.ok("alias"); !strings.HasPrefix(out, "no account has a name yet") {
		t.Fatalf("alias after clearing both = %q", out)
	}
}

func equalAliases(got, want map[string]state.Alias) bool {
	if len(got) != len(want) {
		return false
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok || g.Name != w.Name || !g.At.Equal(w.At) {
			return false
		}
	}
	return true
}

func TestAliasRefusals(t *testing.T) {
	hermetic(t)
	// A device that has collected nothing knows no account to name.
	if r := newDevice(t).run("", "alias", "dev", "ann"); r.code != 1 || !strings.Contains(r.stderr, "knows no accounts yet") {
		t.Fatalf("alias before a run: %+v", r)
	}

	d := aliasDevice(t)
	d.addAccount("codex", "dev@other.org")
	d.addAccount("codex", "unknown")

	for _, args := range [][]string{
		{"alias", "dev"},
		{"alias", "dev", "ann", "extra"},
		{"alias", "dev", "ann", "--clear"},
		{"alias", "--clear"},
		{"alias", "dev", "--clear", "extra"},
		{"alias", "dev", "--bogus"},
		{"alias", "claude:", "ann"},
	} {
		if r := d.run("", args...); r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
		}
	}
	for _, bad := range []string{"", "  ", "an n", "a\tb", "thirteen-long", "日本語日本語日", "evil\x1b[2J", "a\u202eb", "a\u200db"} {
		r := d.run("", "alias", "claude:dev", bad)
		if r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Fatalf("name %q: exit %d, stderr %q", bad, r.code, r.stderr)
		}
	}
	if len(d.config().Aliases) != 0 {
		t.Fatalf("a refused name was saved: %+v", d.config().Aliases)
	}

	// An account nothing names, or a short name of two people, lists the
	// accounts instead of the usage. "unknown" is no one's account.
	for _, args := range [][]string{{"alias", "nobody", "ann"}, {"alias", "unknown", "ann"}} {
		r := d.run("", args...)
		if r.code != 2 || !strings.Contains(r.stderr, "no account this device knows goes by \""+args[1]+"\"") || strings.Contains(r.stderr, "Usage:") ||
			!strings.Contains(r.stderr, "dev@other.org") {
			t.Fatalf("%v: exit %d, stderr %q", args, r.code, r.stderr)
		}
		for _, line := range strings.Split(r.stderr, "\n")[1:] {
			if f := strings.Fields(line); len(f) > 1 && f[1] == "unknown" {
				t.Fatalf("%v lists the unknown account:\n%s", args, r.stderr)
			}
		}
	}
	r := d.run("", "alias", "dev", "ann")
	if r.code != 2 || !strings.Contains(r.stderr, `"dev" names more than one account`) || strings.Contains(r.stderr, "Usage:") ||
		!strings.Contains(r.stderr, "dev@example.com") || !strings.Contains(r.stderr, "dev@other.org") {
		t.Fatalf("ambiguous: exit %d, stderr %q", r.code, r.stderr)
	}
	// A name another account of the provider goes by would tell them apart
	// no more.
	r = d.run("", "alias", "dev@example.com", "dev")
	if r.code != 2 || !strings.Contains(r.stderr, "codex dev@other.org already goes by dev") {
		t.Fatalf("taken name: exit %d, stderr %q", r.code, r.stderr)
	}
	if len(d.config().Aliases) != 0 {
		t.Fatalf("a refused alias was saved: %+v", d.config().Aliases)
	}

	// Narrowed to one provider, the short name is one account again. A name
	// may be twelve columns wide, East Asian characters taking two.
	d.ok("alias", "claude:dev", "日本語日本語")
	d.ok("alias", "dev@other.org", "other")
	if got := d.config().Aliases; len(got) != 2 || got[state.Key("claude", "dev@example.com")].Name != "日本語日本語" || got[state.Key("codex", "dev@other.org")].Name != "other" {
		t.Fatalf("aliases = %+v", got)
	}
}

// The name travels sealed in the snapshot the next run publishes. Another
// device sees it after it reads the team, and can clear it there.
func TestAliasTravelsSealed(t *testing.T) {
	hermetic(t)
	var mu sync.Mutex
	pushed := map[string][]byte{}
	store := relay.NewServer(relay.NewMemory(), relay.Limits{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			pushed[path.Base(r.URL.Path)] = body
			mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		store.ServeHTTP(w, r)
	}))
	defer srv.Close()
	t.Setenv("AI_USAGE_RELAY", srv.URL)

	a := aliasDevice(t)
	a.ok("alias", "claude:dev@example.com", "ann")
	a.ok("collect", "--quiet")
	if st := a.state(); st.Relay.Pending || st.Relay.LastError != "" {
		t.Fatalf("the relay refused the snapshot: %+v", st.Relay)
	}
	mu.Lock()
	body := pushed[a.config().Device]
	mu.Unlock()
	if bytes.Contains(body, []byte("dev@example.com")) || bytes.Contains(body, []byte(`"ann"`)) {
		t.Fatalf("the snapshot carries the alias in the clear:\n%s", body)
	}
	var doc snapshot.Doc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	key, err := team.Load(filepath.Join(a.dir, "team.key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Aliases) != 1 || doc.Aliases[0].Provider != "claude" || doc.Aliases[0].At.IsZero() {
		t.Fatalf("aliases = %+v", doc.Aliases)
	}
	label, _ := key.Open(doc.Aliases[0].Label)
	name, _ := key.Open(doc.Aliases[0].Name)
	if label != "dev@example.com" || name != "ann" {
		t.Fatalf("alias opens as %q %q", label, name)
	}

	// Another device lists it after it reads the team, knows the account from
	// the snapshot alone, and a newer clearing there outranks the name.
	b := newDevice(t)
	b.ok("team", "join", strings.TrimSpace(a.ok("team", "key")))
	b.ok("collect", "--json")
	if out := b.ok("alias"); !strings.Contains(out, "claude  dev@example.com  ann  set on test-host") {
		t.Fatalf("b lists:\n%s", out)
	}
	if out := b.ok("alias", "ann", "--clear"); !strings.Contains(out, "no longer goes by ann on claude") {
		t.Fatalf("b clears: %q", out)
	}
	if out := b.ok("alias"); !strings.HasPrefix(out, "no account has a name yet") {
		t.Fatalf("b lists after clearing:\n%s", out)
	}
	b.ok("collect", "--quiet")
	a.ok("collect", "--json")
	if out := a.ok("alias"); !strings.HasPrefix(out, "no account has a name yet") {
		t.Fatalf("a lists after b cleared:\n%s", out)
	}
}
