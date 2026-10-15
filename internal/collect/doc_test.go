package collect

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

func mustKey(t *testing.T) *team.Key {
	t.Helper()
	k, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func ledger() *state.State {
	reset := t0.Add(3 * time.Hour)
	return &state.State{
		LastSuccessAt: t0,
		LastError:     "codex: read /Users/анна/.codex: permission denied",
		Sources: map[string]state.Source{
			"claude": {Status: "ok"},
			"codex":  {Status: "error", Error: "read /Users/анна/.codex: permission denied"},
			"grok":   {Status: "skipped"},
		},
		Current: map[string]string{state.Key("claude", "/h/.claude"): "ann@example.com"},
		Accounts: map[string]*state.Account{
			state.Key("claude", "ann@example.com"): {Provider: "claude", Label: "ann@example.com", Plan: "max",
				Quota: &state.Quota{At: t0, Source: "cache", Windows: []snapshot.Window{{Name: "5h", Percent: 61.5, ResetsAt: &reset, Minutes: 300}}}},
			state.Key("claude", "old@example.com"): {Provider: "claude", Label: "old@example.com"},
			state.Key("codex", "idle"):             {Provider: "codex", Label: "idle"},
		},
		Sessions: map[string]*state.Session{
			state.Key("claude", "s1"): {Provider: "claude", Project: "/work/api", Updated: t0.Add(-time.Hour),
				By: map[string]snapshot.Tokens{"ann@example.com": tok(100), "old@example.com": tok(40)}},
			state.Key("claude", "s2"): {Provider: "claude", Project: "/work/web", Updated: t0.Add(-2 * time.Hour),
				By: map[string]snapshot.Tokens{"ann@example.com": tok(300)}},
			state.Key("claude", "s3"): {Provider: "claude", Project: "/work/api", Updated: t0.Add(-3 * time.Hour),
				By: map[string]snapshot.Tokens{"ann@example.com": tok(5), "zero": {}}},
			state.Key("hermes", "h1"): {Provider: "hermes", Project: "/h", Updated: t0,
				By: map[string]snapshot.Tokens{"openrouter": tok(9)}},
		},
	}
}

func encodeDecode(t *testing.T, doc snapshot.Doc) (snapshot.Doc, []byte) {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	back, err := snapshot.Decode(body)
	if err != nil {
		t.Fatalf("Decode: %v (%d bytes)", err, len(body))
	}
	return back, body
}

func TestBuildDocDecodesAndOpens(t *testing.T) {
	key := mustKey(t)
	doc, body := encodeDecode(t, BuildDoc(ledger(), nil, key, state.Config{Device: "d-0123456789"}, "workbox", "sam", "v1.2.3", t0))
	open := func(s string) string {
		t.Helper()
		v, err := key.Open(s)
		if err != nil {
			t.Fatalf("open %q: %v", s, err)
		}
		return v
	}
	if doc.Team != key.Fingerprint() || doc.Device != "d-0123456789" || doc.CollectorVersion != "v1.2.3" || !doc.CollectedAt.Equal(t0) {
		t.Fatalf("header = %+v", doc)
	}
	if open(doc.DeviceLabel) != "workbox" || open(doc.OSUser) != "sam" || !strings.Contains(open(doc.LastError), "permission denied") {
		t.Fatal("sealed header fields do not open")
	}
	for _, plain := range []string{"workbox", "ann@example.com", "/work/api", "анна", "permission"} {
		if strings.Contains(string(body), plain) {
			t.Fatalf("%q is in the published body in plain text", plain)
		}
	}
	var sources []string
	for _, s := range doc.Sources {
		sources = append(sources, s.Provider+":"+s.Status)
	}
	if want := []string{"claude:ok", "codex:error", "grok:skipped"}; !reflect.DeepEqual(sources, want) {
		t.Fatalf("sources = %v", sources)
	}
	if open(doc.Sources[1].Error) == "" || doc.Sources[0].Error != "" {
		t.Fatalf("source errors = %+v", doc.Sources)
	}
	if len(doc.Accounts) != 3 {
		t.Fatalf("accounts = %+v", doc.Accounts)
	}
	ann := doc.Accounts[0]
	if open(ann.Label) != "ann@example.com" || ann.Provider != "claude" || !ann.Current || ann.Plan != "max" {
		t.Fatalf("ann = %+v", ann)
	}
	if ann.QuotaAt == nil || !ann.QuotaAt.Equal(t0) || len(ann.Windows) != 1 || ann.Windows[0].Percent != 61.5 {
		t.Fatalf("ann quota = %+v", ann)
	}
	if ann.Sessions != 3 || ann.Tokens != tok(405) || ann.LastActiveAt == nil {
		t.Fatalf("ann usage = %+v", ann)
	}
	if len(ann.Projects) != 2 || open(ann.Projects[0].Path) != "/work/web" || ann.Projects[0].Tokens != tok(300) {
		t.Fatalf("ann projects = %+v", ann.Projects)
	}
	if old := doc.Accounts[1]; old.QuotaAt != nil || len(old.Windows) != 0 || old.Current {
		t.Fatalf("old = %+v", old)
	}
}

func TestBuildDocWithEmptyState(t *testing.T) {
	st := state.NewState()
	doc, _ := encodeDecode(t, BuildDoc(st, nil, mustKey(t), state.Config{Device: "d-0123456789"}, "", "", "", t0))
	if doc.CollectorVersion != "unknown" || doc.LastError != "" {
		t.Fatalf("doc = %+v", doc)
	}
}

// A failed release check reaches the team as the last line of last_error,
// one line and cut short, and stays whole behind a long run error, while no
// newer release waits for the next run.
func TestLastErrorCarriesTheUpdateError(t *testing.T) {
	key := mustKey(t)
	lastError := func(st *state.State) (run, update string, found bool) {
		t.Helper()
		doc, _ := encodeDecode(t, BuildDoc(st, nil, key, state.Config{Device: "d-0123456789"}, "workbox", "sam", "v1.2.3", t0))
		if len(doc.LastError) > snapshot.MaxSealed {
			t.Fatalf("last_error is %d bytes sealed", len(doc.LastError))
		}
		plain, err := key.Open(doc.LastError)
		if err != nil {
			t.Fatal(err)
		}
		run, update = snapshot.SplitLastError(plain)
		return run, update, strings.Contains(plain, snapshot.UpdateLine)
	}
	st := ledger()
	st.LastError = strings.Repeat("codex: read /Users/анна/.codex: permission denied; ", 12) + "\nsecond line"
	st.Update.Error = "update check: HTTP 429 from github.com:\n" + strings.Repeat("slow down ", 30)
	run, update, _ := lastError(st)
	if strings.Contains(run, "\n") || !strings.HasSuffix(run, "second line") {
		t.Fatalf("run error = %q", run)
	}
	if !strings.HasPrefix(update, "update check: HTTP 429 from github.com: slow down") || strings.Contains(update, "\n") ||
		len(update) > maxUpdateError || !utf8.ValidString(update) {
		t.Fatalf("update error = %q", update)
	}

	st.Update.Installed = "v1.3.0"
	if run, _, found := lastError(st); found || !strings.HasSuffix(run, "second line") {
		t.Fatalf("a staged release still sends the update error, or loses the run's: %q", run)
	}
	st.Update.Installed, st.Update.Error = "", ""
	if _, _, found := lastError(st); found {
		t.Fatal("no update error, but an update line")
	}
	st.LastError, st.Update.Error = "", "cannot write beside /usr/local/bin/ai-usage: permission denied"
	if run, update, _ := lastError(st); run != "" || update != st.Update.Error {
		t.Fatalf("last_error = %q, %q", run, update)
	}
	// A network error names this device's own address.
	st.Update.Error = `update check: Get "https://github.com/neoromantic/ai-usage/releases/latest": read tcp 192.0.2.10:60512->198.51.100.4:443: read: connection reset by peer`
	if _, update, _ := lastError(st); strings.Contains(update, "192.0.2.10") || strings.Contains(update, "198.51.100.4") ||
		!strings.HasSuffix(update, "read tcp (IP address)->(IP address): read: connection reset by peer") {
		t.Fatalf("update error = %q", update)
	}
}

func TestBuildDocFitsTheSizeLimit(t *testing.T) {
	key := mustKey(t)
	st := state.NewState()
	// More accounts and projects than fit, each with a long path.
	for a := range snapshot.MaxAccounts + 4 {
		label := fmt.Sprintf("account-%02d@%s.example", a, strings.Repeat("x", 200))
		st.Accounts[state.Key("claude", label)] = &state.Account{Provider: "claude", Label: label}
		for p := range snapshot.MaxProjects + 5 {
			path := fmt.Sprintf("/Users/анна/%s/project-%02d", strings.Repeat("каталог/", 60), p)
			st.Sessions[state.Key("claude", fmt.Sprintf("%d-%d", a, p))] = &state.Session{
				Provider: "claude", Project: path, Updated: t0,
				By: map[string]snapshot.Tokens{label: tok(int64(1000*(a+1) + p))},
			}
		}
	}
	doc, body := encodeDecode(t, BuildDoc(st, nil, key, state.Config{Device: "d-0123456789"}, "workbox", "sam", "v1", t0))
	if len(body) > snapshot.MaxBytes {
		t.Fatalf("doc is %d bytes", len(body))
	}
	if len(doc.Accounts) == 0 {
		t.Fatal("every account was dropped")
	}
	// The largest projects survive: each account keeps a prefix of its
	// projects in token order.
	for _, a := range doc.Accounts {
		for i := 1; i < len(a.Projects); i++ {
			if a.Projects[i].Tokens.Total() > a.Projects[i-1].Tokens.Total() {
				t.Fatalf("a larger project was dropped before a smaller one: %+v", a.Projects)
			}
		}
		for _, p := range a.Projects {
			plain, err := key.Open(p.Path)
			if err != nil || !utf8.ValidString(plain) || !strings.HasSuffix(plain, "/project-"+plain[len(plain)-2:]) {
				t.Fatalf("path %q: %v", plain, err)
			}
		}
	}
}

func TestFitDocDropsAccountsWhenProjectsAreNotEnough(t *testing.T) {
	big := strings.Repeat("A", snapshot.MaxSealed)
	doc := snapshot.Doc{Accounts: []snapshot.Account{}}
	for range 200 {
		doc.Accounts = append(doc.Accounts, snapshot.Account{Provider: "claude", Label: big, Windows: []snapshot.Window{}, Projects: []snapshot.Project{}})
	}
	fitDoc(&doc)
	b, _ := json.Marshal(doc)
	if len(b) > snapshot.MaxBytes || len(doc.Accounts) == 0 {
		t.Fatalf("%d bytes, %d accounts", len(b), len(doc.Accounts))
	}
}

// A sealed path keeps its end, cut on a rune boundary. The projects that
// survive TestBuildDocFitsTheSizeLimit are all cut on one, so this checks
// the cuts inside a rune.
func TestClip(t *testing.T) {
	for _, in := range []string{
		"short",
		strings.Repeat("я", 200),          // 2-byte runes
		strings.Repeat("🙂", 100) + "/end", // 4-byte runes
	} {
		got := clip(in, 300)
		if len(in) <= 300 {
			if got != in {
				t.Fatalf("clip changed a short string: %q", got)
			}
			continue
		}
		rest, ok := strings.CutPrefix(got, "…")
		if !utf8.ValidString(got) || len(got) > 300 || !ok || !strings.HasSuffix(in, rest) || len(rest) < 300-len("…")-3 {
			t.Fatalf("clip(%d bytes) = %q", len(in), got)
		}
	}
}

// TestDocCarriesDaysRecentAndAliases: the snapshot has each account's days,
// its tokens since each window began, and the names this device set.
func TestDocCarriesDaysRecentAndAliases(t *testing.T) {
	key := mustKey(t)
	reset := t0.Add(24 * time.Hour)
	st := &state.State{
		Current: map[string]string{state.Key("codex", "/h/.codex"): "ann@example.com"},
		Accounts: map[string]*state.Account{state.Key("codex", "ann@example.com"): {Provider: "codex", Label: "ann@example.com",
			Quota: &state.Quota{At: t0, Windows: []snapshot.Window{
				{Name: "7d", Percent: 40, ResetsAt: &reset, Minutes: 10080},
				{Name: "5h", Percent: 10, ResetsAt: &t0, Minutes: 300},
			}}}},
		Sessions: map[string]*state.Session{state.Key("codex", "s1"): {Provider: "codex", Project: "/p", Updated: t0,
			By: map[string]snapshot.Tokens{"ann@example.com": {Input: 70, Output: 30}},
			U:  meter.Hours{t0.Add(-7*24*time.Hour).Unix() / 3600: {Input: 28, Output: 12}, t0.Unix()/3600 - 1: {Input: 42, Output: 18}}}},
	}
	cfg := state.Config{Device: "d-0123456789", Aliases: map[string]state.Alias{
		state.Key("codex", "ann@example.com"): {Name: "ann", At: t0},
		state.Key("claude", "bo@example.com"): {At: t0.Add(-time.Hour)},
	}}
	doc, _ := encodeDecode(t, BuildDoc(st, nil, key, cfg, "box", "ann", "v1", t0))
	a := doc.Accounts[0]
	if want := []int64{60, 0, 0, 0, 0, 0, 0, 40}; !reflect.DeepEqual(a.Days, want) {
		t.Errorf("days = %v, want %v", a.Days, want)
	}
	// The 5h window reset at t0, so only the weekly one is counted.
	if len(a.Recent) != 1 || a.Recent[0].Window != "7d" || a.Recent[0].Tokens != 60 || !a.Recent[0].Start.Equal(reset.Add(-7*24*time.Hour)) {
		t.Errorf("recent = %+v", a.Recent)
	}
	if len(doc.Aliases) != 2 {
		t.Fatalf("aliases = %+v", doc.Aliases)
	}
	name, err := key.Open(doc.Aliases[0].Name)
	label, lerr := key.Open(doc.Aliases[0].Label)
	if err != nil || lerr != nil || name != "ann" || label != "ann@example.com" || doc.Aliases[0].Provider != "codex" {
		t.Errorf("first alias = %q %q %v %v", label, name, err, lerr)
	}
	if doc.Aliases[1].Name != "" {
		t.Errorf("a cleared name travels without one: %+v", doc.Aliases[1])
	}
}

// extendedLedger is a Codex account with cache reads, a weekly reading, and
// a run error dated at.
func extendedLedger(errAt time.Time) *state.State {
	reset := t0.Add(24 * time.Hour)
	return &state.State{
		LastError:   "codex: rate limits: context deadline exceeded",
		LastErrorAt: errAt,
		Current:     map[string]string{state.Key("codex", "/h/.codex"): "ann@acme.dev"},
		Accounts: map[string]*state.Account{state.Key("codex", "ann@acme.dev"): {Provider: "codex", Label: "ann@acme.dev",
			Quota: &state.Quota{At: t0, Windows: []snapshot.Window{{Name: "7d", Percent: 40, ResetsAt: &reset, Minutes: 10080}}}}},
		Sessions: map[string]*state.Session{state.Key("codex", "a4c2e917"): {Provider: "codex", Project: "/p", Updated: t0,
			By: map[string]snapshot.Tokens{"ann@acme.dev": {Input: 70, Output: 30, CacheRead: 1100}},
			U: meter.Hours{
				t0.Add(-7*24*time.Hour).Unix() / 3600: {Input: 28, Output: 12, CacheRead: 100},
				t0.Unix()/3600 - 1:                    {Input: 42, Output: 18, CacheRead: 1000},
			}}},
	}
}

// The snapshot has each account's burn by day and since each window began,
// and when the last error happened; the body a release before v0.4.0 reads
// has none of it.
func TestBuildDocExtended(t *testing.T) {
	key := mustKey(t)
	at := t0.Add(-20 * time.Minute).In(time.FixedZone("east", 3*3600))
	doc, body := encodeDecode(t, BuildDoc(extendedLedger(at), nil, key, state.Config{Device: "d-0123456789"}, "annbook", "ann", "v0.4.0", t0))
	a := doc.Accounts[0]
	// Codex weighs output 8 and cache reads 0.1: 28+96+10 and 42+144+100.
	if want := []int64{286, 0, 0, 0, 0, 0, 0, 134}; !reflect.DeepEqual(a.BurnDays, want) {
		t.Errorf("burn days = %v, want %v", a.BurnDays, want)
	}
	if want := []int64{60, 0, 0, 0, 0, 0, 0, 40}; !reflect.DeepEqual(a.Days, want) {
		t.Errorf("days = %v, want %v", a.Days, want)
	}
	if len(a.Recent) != 1 || a.Recent[0].Tokens != 60 || a.Recent[0].Burn != 286 {
		t.Errorf("recent = %+v", a.Recent)
	}
	if doc.LastErrorAt == nil || !doc.LastErrorAt.Equal(at) || doc.LastErrorAt.Location() != time.UTC {
		t.Errorf("last error at = %v", doc.LastErrorAt)
	}
	if !doc.Extended() || !strings.Contains(string(body), `"last_error_at":"2026-09-01T11:40:00Z"`) {
		t.Errorf("body = %s", body)
	}

	legacy, err := json.Marshal(doc.Legacy())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"last_error_at", "burn_days", `"burn"`} {
		if strings.Contains(string(legacy), k) {
			t.Errorf("the legacy body has %s: %s", k, legacy)
		}
	}
	if _, err := snapshot.Decode(legacy); err != nil {
		t.Errorf("the legacy body does not decode: %v", err)
	}
}

// last_error_at goes only with a dated last_error, and never after the
// snapshot, so the body always validates.
func TestBuildDocLastErrorAt(t *testing.T) {
	key := mustKey(t)
	build := func(st *state.State) snapshot.Doc {
		t.Helper()
		doc, _ := encodeDecode(t, BuildDoc(st, nil, key, state.Config{Device: "d-0123456789"}, "annbook", "ann", "v0.4.0", t0))
		return doc
	}
	if doc := build(ledger()); doc.LastError == "" || doc.LastErrorAt != nil {
		t.Errorf("an undated last_error: %v", doc.LastErrorAt)
	}
	if doc := build(extendedLedger(t0.Add(10 * time.Minute))); doc.LastErrorAt == nil || !doc.LastErrorAt.Equal(t0) {
		t.Errorf("a last error 10 minutes ahead: %v", doc.LastErrorAt)
	}
	st := extendedLedger(t0)
	st.LastError = ""
	if doc := build(st); doc.LastErrorAt != nil {
		t.Errorf("no last error, but a time: %v", doc.LastErrorAt)
	}
}

// fitDoc fits the body a release before v0.4.0 reads as that release did,
// then cuts only the burn days: all of them when the legacy body had to
// lose days, and the oldest when only the burn days do not fit.
func TestFitDocKeepsTheLegacyDays(t *testing.T) {
	sealed := func(c byte) string { return strings.Repeat(string(c), snapshot.MaxSealed) }
	for _, c := range []struct {
		aliases   int
		legacyCut bool
		burnKept  bool
	}{
		{20, true, false},
		{4, false, true},
	} {
		doc := snapshot.Doc{V: snapshot.Version, Accounts: []snapshot.Account{}, Sources: []snapshot.Source{}}
		for i := range snapshot.MaxAccounts {
			a := snapshot.Account{Provider: "claude", Label: sealed('A' + byte(i%26)), Windows: []snapshot.Window{},
				Projects: []snapshot.Project{{Path: sealed('p'), Sessions: 1, Tokens: tok(9)}}}
			for d := range snapshot.MaxDays {
				a.Days = append(a.Days, 100_000_000_000_000+int64(d))
				a.BurnDays = append(a.BurnDays, 700_000_000_000_000+int64(d))
			}
			doc.Accounts = append(doc.Accounts, a)
		}
		for i := range c.aliases {
			doc.Aliases = append(doc.Aliases, snapshot.Alias{Provider: "claude", Label: sealed('a' + byte(i%26)), Name: sealed('n'), At: t0})
		}
		legacy := doc.Legacy()
		fitLegacy(&legacy)
		fitDoc(&doc)

		if !reflect.DeepEqual(doc.Legacy(), legacy) {
			t.Fatalf("%d aliases: the legacy body differs from the one v0.3.4 fits", c.aliases)
		}
		b, _ := json.Marshal(doc)
		if len(b) > snapshot.MaxBytes || len(doc.Accounts) != snapshot.MaxAccounts {
			t.Fatalf("%d aliases: %d bytes, %d accounts", c.aliases, len(b), len(doc.Accounts))
		}
		kept := 0
		for _, a := range doc.Accounts {
			if cut := len(a.Days) < snapshot.MaxDays; cut != c.legacyCut {
				t.Fatalf("%d aliases: the legacy fit kept %d days", c.aliases, len(a.Days))
			}
			if len(a.BurnDays) > len(a.Days) || len(a.BurnDays) == snapshot.MaxDays {
				t.Fatalf("%d aliases: %d burn days for %d days", c.aliases, len(a.BurnDays), len(a.Days))
			}
			kept += len(a.BurnDays)
		}
		if (kept > 0) != c.burnKept {
			t.Fatalf("%d aliases: %d burn days kept", c.aliases, kept)
		}
	}
}

// fitDoc fits the body a release before v0.5.0 reads as that release did,
// then puts back the cycles of the accounts it kept, cutting the oldest of
// the window with the most first while the whole is too big.
func TestFitDocKeepsCyclesLast(t *testing.T) {
	sealed := func(c byte) string { return strings.Repeat(string(c), snapshot.MaxSealed/2) }
	for _, c := range []struct {
		name    string
		days    int
		cut     bool
		without bool // the fields v0.4.0 added still fit
	}{
		{"room for all", 7, false, true},
		{"cycles cut", 45, true, true},
	} {
		doc := snapshot.Doc{V: snapshot.Version, Accounts: []snapshot.Account{}, Sources: []snapshot.Source{}}
		for i := range snapshot.MaxAccounts {
			a := snapshot.Account{Provider: "claude", Label: sealed('A' + byte(i%26)), Windows: []snapshot.Window{}, Projects: []snapshot.Project{}}
			for d := range c.days {
				a.Days = append(a.Days, 100_000_000_000_000+int64(d))
				a.BurnDays = append(a.BurnDays, 700_000_000_000_000+int64(d))
			}
			if i < 6 {
				for w, name := range []string{"5h", "7d", "7d sonnet"} {
					for n := range []int{12, 13, 13}[w] {
						reset := t0.Add(-time.Duration(n) * 7 * 24 * time.Hour).Add(time.Duration(w) * time.Hour)
						a.Cycles = append(a.Cycles, cycle(name, reset, float64(50+n), reset.Add(-time.Hour)))
					}
				}
			}
			doc.Accounts = append(doc.Accounts, a)
		}
		without := doc.WithoutCycles()
		fitDoc(&without)
		full := doc.WithoutCycles()
		for i := range full.Accounts {
			full.Accounts[i].Cycles = doc.Accounts[i].Cycles
		}
		fitDoc(&doc)

		if !reflect.DeepEqual(doc.WithoutCycles(), without) {
			t.Fatalf("%s: the body without cycles differs from the one v0.4.0 fits", c.name)
		}
		if without.Extended() != c.without {
			t.Fatalf("%s: the fields v0.4.0 added were dropped", c.name)
		}
		b, _ := json.Marshal(doc)
		if len(b) > snapshot.MaxBytes {
			t.Fatalf("%s: %d bytes", c.name, len(b))
		}
		total, most := 0, 0
		for i, a := range doc.Accounts {
			total += len(a.Cycles)
			was := full.Accounts[i].Cycles
			// What is kept of each window is its newest.
			n := map[string]int{}
			for _, cy := range a.Cycles {
				n[cy.Window]++
			}
			for _, cy := range was {
				if n[cy.Window] > 0 {
					if !slices.Contains(a.Cycles, cy) {
						t.Fatalf("%s: account %d kept an older cycle than %+v", c.name, i, cy)
					}
					n[cy.Window]--
				}
			}
			most = max(most, len(a.Cycles))
		}
		if cut := total < 6*38; cut != c.cut {
			t.Fatalf("%s: %d cycles kept", c.name, total)
		}
		// The accounts with the most lose them first, so none keeps many
		// more than another.
		for i, a := range doc.Accounts[:6] {
			if most-len(a.Cycles) > 1 {
				t.Fatalf("%s: account %d kept %d cycles, another %d", c.name, i, len(a.Cycles), most)
			}
		}
	}
}

func TestTrimDays(t *testing.T) {
	for _, c := range []struct {
		in   []int64
		n    int
		want []int64
	}{
		{[]int64{1, 2, 3}, 5, []int64{1, 2, 3}},
		{[]int64{1, 0, 3}, 2, []int64{1}},
		{[]int64{0, 0, 3}, 2, nil},
		{nil, 3, nil},
	} {
		if got := trimDays(c.in, c.n); !reflect.DeepEqual(got, c.want) {
			t.Errorf("trimDays(%v, %d) = %v, want %v", c.in, c.n, got, c.want)
		}
	}
}

// HoursOf lays out the last 48 UTC hours, newest first, without the zeros
// that would end them.
func TestHoursOf(t *testing.T) {
	cur := t0.Unix() / 3600
	got := HoursOf(map[int64]int64{cur: 5, cur - 2: 7, cur - 47: 1, cur - 48: 9, cur + 1: 3, cur - 3: 0}, t0)
	want := make([]int64, 48)
	want[0], want[2], want[47] = 5, 7, 1
	if !slices.Equal(got, want) {
		t.Fatalf("HoursOf = %v", got)
	}
	if got := HoursOf(map[int64]int64{cur - 1: 4, cur - 60: 2}, t0); !slices.Equal(got, []int64{0, 4}) {
		t.Fatalf("HoursOf = %v", got)
	}
}

// fitDoc fits the body a release before v0.7.0 reads as that release did,
// then puts back the hours of the accounts it kept, and drops an account's
// hours whole, both kinds, the longest first, while the whole is too big.
func TestFitDocKeepsHoursLast(t *testing.T) {
	sealed := func(c byte) string { return strings.Repeat(string(c), snapshot.MaxSealed/2) }
	for _, c := range []struct {
		name string
		days int
		cut  bool
	}{
		{"room for all", 7, false},
		{"hours cut", 60, true},
	} {
		doc := snapshot.Doc{V: snapshot.Version, Accounts: []snapshot.Account{}, Sources: []snapshot.Source{}}
		for i := range snapshot.MaxAccounts {
			a := snapshot.Account{Provider: "claude", Label: sealed('A' + byte(i%26)), Windows: []snapshot.Window{}, Projects: []snapshot.Project{}}
			for d := range c.days {
				a.Days = append(a.Days, 100_000_000_000_000+int64(d))
				a.BurnDays = append(a.BurnDays, 700_000_000_000_000+int64(d))
			}
			for h := range snapshot.MaxHours - i {
				a.Hours = append(a.Hours, 1_000_000_000+int64(h))
				a.BurnHours = append(a.BurnHours, 7_000_000_000+int64(h))
			}
			doc.Accounts = append(doc.Accounts, a)
		}
		without := doc.WithoutHours()
		fitDoc(&without)
		full := doc
		fitDoc(&doc)

		if !reflect.DeepEqual(doc.WithoutHours(), without) {
			t.Fatalf("%s: the body without hours differs from the one v0.6.2 fits", c.name)
		}
		b, _ := json.Marshal(doc)
		if len(b) > snapshot.MaxBytes {
			t.Fatalf("%s: %d bytes", c.name, len(b))
		}
		cut, last := false, -1
		for i, a := range doc.Accounts {
			if len(a.Hours) == 0 && len(a.BurnHours) == 0 {
				cut = true
				// The longest go first.
				if last >= 0 && i > last {
					t.Fatalf("%s: account %d lost its hours after account %d kept them", c.name, i, last)
				}
				continue
			}
			if last < 0 {
				last = i
			}
			if !slices.Equal(a.Hours, full.Accounts[i].Hours) || !slices.Equal(a.BurnHours, full.Accounts[i].BurnHours) {
				t.Fatalf("%s: account %d kept part of its hours", c.name, i)
			}
		}
		if cut != c.cut {
			t.Fatalf("%s: hours cut = %v", c.name, cut)
		}
	}
}
