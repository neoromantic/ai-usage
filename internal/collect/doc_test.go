package collect

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/logs"
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

func TestTotals(t *testing.T) {
	st := ledger()
	got := Totals(st)
	var order []string
	for _, a := range got {
		order = append(order, a.Provider+"/"+a.Label)
	}
	// Providers in display order, the logged-in account first; accounts with
	// no usage, no quota, and no login are left out.
	want := []string{"claude/ann@example.com", "claude/old@example.com", "hermes/openrouter"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	ann := got[0]
	if !ann.Current || ann.Plan != "max" || ann.Quota == nil || ann.Sessions != 3 || ann.Tokens != tok(405) {
		t.Fatalf("ann = %+v", ann)
	}
	if !ann.LastActive.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("last active = %v", ann.LastActive)
	}
	var projects []string
	for _, p := range ann.Projects {
		projects = append(projects, fmt.Sprintf("%s %d %d", p.Path, p.Sessions, p.Tokens.Total()))
	}
	if want := []string{"/work/web 1 330", "/work/api 2 115"}; !reflect.DeepEqual(projects, want) {
		t.Fatalf("projects = %v, want %v", projects, want)
	}
	if old := got[1]; old.Current || old.Sessions != 1 || old.Tokens != tok(40) {
		t.Fatalf("old = %+v", old)
	}
}

func TestSortAccounts(t *testing.T) {
	in := []AccountTotals{
		{Provider: "hermes", Label: "a", Tokens: tok(1000)},
		{Provider: "claude", Label: "b", Tokens: tok(1)},
		{Provider: "claude", Label: "c", Tokens: tok(50)},
		{Provider: "claude", Label: "d", Tokens: tok(1), Current: true},
		{Provider: "claude", Label: "a", Tokens: tok(1)},
		{Provider: "codex", Label: "x"},
	}
	SortAccounts(in)
	var got []string
	for _, a := range in {
		got = append(got, a.Provider+"/"+a.Label)
	}
	want := []string{"claude/d", "claude/c", "claude/a", "claude/b", "codex/x", "hermes/a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
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
	doc, body := encodeDecode(t, BuildDoc(ledger(), key, state.Config{Device: "d-0123456789"}, "workbox", "sam", "v1.2.3", t0))
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
	doc, _ := encodeDecode(t, BuildDoc(st, mustKey(t), state.Config{Device: "d-0123456789"}, "", "", "", t0))
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
		doc, _ := encodeDecode(t, BuildDoc(st, key, state.Config{Device: "d-0123456789"}, "workbox", "sam", "v1.2.3", t0))
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
	doc, body := encodeDecode(t, BuildDoc(st, key, state.Config{Device: "d-0123456789"}, "workbox", "sam", "v1", t0))
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

// TestHoursFollowTheSession: in a ledger from before accounts kept their own
// hours, each account's part of a session is spread over the session's hours
// by its share, a session without hours puts it at the hour the share last
// grew, and the days and window counts come from them.
func TestHoursFollowTheSession(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	day := 24 * time.Hour
	st := &state.State{
		Accounts: map[string]*state.Account{},
		Sessions: map[string]*state.Session{
			// 400 tokens in, 0 out: 3 parts to ann, 1 to bo, over two hours.
			state.Key("codex", "s1"): {Provider: "codex", Project: "/p", Updated: t0,
				By:    map[string]snapshot.Tokens{"ann": {Input: 300}, "bo": {Input: 100}},
				Hours: map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0): 200}},
			// No hours: at the hour ann's share last grew.
			state.Key("claude", "s2"): {Provider: "claude", Project: "/p", Updated: t0,
				By:   map[string]snapshot.Tokens{"ann": {Input: 50, Output: 10, CacheRead: 1000}},
				Last: map[string]time.Time{"ann": t0.Add(-day)}},
		},
	}
	byLabel := map[string]AccountTotals{}
	for _, a := range Totals(st) {
		byLabel[a.Provider+"/"+a.Label] = a
	}
	ann, bo := byLabel["codex/ann"], byLabel["codex/bo"]
	if want := map[int64]int64{h(t0.Add(-2 * day)): 150, h(t0): 150}; !reflect.DeepEqual(ann.Hours, want) {
		t.Errorf("ann's hours = %v, want %v", ann.Hours, want)
	}
	if want := map[int64]int64{h(t0.Add(-2 * day)): 50, h(t0): 50}; !reflect.DeepEqual(bo.Hours, want) {
		t.Errorf("bo's hours = %v, want %v", bo.Hours, want)
	}
	if got, want := DaysOf(ann.Hours, t0), []int64{150, 0, 150}; !reflect.DeepEqual(got, want) {
		t.Errorf("days = %v, want %v", got, want)
	}
	if got := DaysOf(byLabel["claude/ann"].Hours, t0); !reflect.DeepEqual(got, []int64{0, 60}) {
		t.Errorf("claude days = %v, want [0 60]: cache is not counted", got)
	}
	if got := SinceStart(ann.Hours, t0.Add(-day)); got != 150 {
		t.Errorf("since a day ago = %d, want 150", got)
	}
	ps := Projects(st)
	if len(ps) != 1 || ps[0].Sessions != 2 || !reflect.DeepEqual(ps[0].Providers, []string{"claude", "codex"}) || DaysOf(ps[0].Hours, t0)[2] != 200 {
		t.Errorf("projects = %+v", ps)
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
			By:    map[string]snapshot.Tokens{"ann@example.com": {Input: 70, Output: 30}},
			Hours: map[int64]int64{t0.Add(-7*24*time.Hour).Unix() / 3600: 40, t0.Unix()/3600 - 1: 60}}},
	}
	cfg := state.Config{Device: "d-0123456789", Aliases: map[string]state.Alias{
		state.Key("codex", "ann@example.com"): {Name: "ann", At: t0},
		state.Key("claude", "bo@example.com"): {At: t0.Add(-time.Hour)},
	}}
	doc, _ := encodeDecode(t, BuildDoc(st, key, cfg, "box", "ann", "v1", t0))
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

// TestLedgerPlacesHours: a log with times gives the hours of a session read
// for the first time, a later read adds its growth where the log grew, even
// when it is partial, and a log without times has each run's growth at the
// session's last activity.
func TestLedgerPlacesHours(t *testing.T) {
	h := t0.Unix() / 3600
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	timed := logs.Session{ID: "s1", Tokens: snapshot.Tokens{Input: 90, Output: 10}, Updated: t0, Hours: map[int64]int64{h - 1: 40, h: 60}}
	attribute(st, "codex", timed, "ann", false, t0, growth)
	if got := st.Sessions[state.Key("codex", "s1")].Hours; !reflect.DeepEqual(got, map[int64]int64{h - 1: 40, h: 60}) {
		t.Errorf("hours = %v", got)
	}
	// A partial read shows 110 tokens, all in the last hour: 10 more.
	timed.Tokens.Input, timed.Hours = 100, map[int64]int64{h: 110}
	attribute(st, "codex", timed, "ann", true, t0, growth)
	if got := st.Sessions[state.Key("codex", "s1")].Hours; !reflect.DeepEqual(got, map[int64]int64{h - 1: 40, h: 70}) {
		t.Errorf("hours after a partial read = %v, want the 10 grown added", got)
	}

	untimed := logs.Session{ID: "s2", Tokens: snapshot.Tokens{Input: 30}, Updated: t0.Add(-2 * time.Hour)}
	attribute(st, "hermes", untimed, "openrouter", false, t0, growth)
	untimed.Tokens.Input, untimed.Updated = 50, t0
	attribute(st, "hermes", untimed, "openrouter", false, t0, growth)
	if got := st.Sessions[state.Key("hermes", "s2")].Hours; !reflect.DeepEqual(got, map[int64]int64{h - 2: 30, h: 20}) {
		t.Errorf("untimed hours = %v", got)
	}
}

// TestSwitchedSessionKeepsEachAccountsHours: a session continued under
// another login gives each account the hours it spent in, not a share of
// every hour of the session, and so does one continued under the first
// login again.
func TestSwitchedSessionKeepsEachAccountsHours(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	day := 24 * time.Hour
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	s := logs.Session{ID: "s1", Project: "/p", Tokens: snapshot.Tokens{Input: 100}, Updated: t0.Add(-3 * day), Hours: map[int64]int64{h(t0.Add(-3 * day)): 100}}
	attribute(st, "claude", s, "ann@acme.dev", false, t0.Add(-3*day), growth)
	// ann's weekly window is full: bo logs in and continues s1 today.
	s.Tokens.Input, s.Updated = 120, t0.Add(-2*time.Hour)
	s.Hours = map[int64]int64{h(t0.Add(-3 * day)): 100, h(s.Updated): 20}
	attribute(st, "claude", s, "bo@acme.dev", false, s.Updated, growth)
	days := func(label string) []int64 { return DaysOf(totalsFor(t, st, "claude", label).Hours, t0) }
	if got, want := days("ann@acme.dev"), []int64{0, 0, 0, 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("ann's days = %v, want %v", got, want)
	}
	if got, want := days("bo@acme.dev"), []int64{20}; !reflect.DeepEqual(got, want) {
		t.Errorf("bo's days = %v, want %v", got, want)
	}
	if got := SinceStart(totalsFor(t, st, "claude", "ann@acme.dev").Hours, t0.Add(-day)); got != 0 {
		t.Errorf("ann since yesterday = %d, want 0", got)
	}
	// ann logs in again and continues it for 30 more.
	s.Tokens.Input, s.Updated = 150, t0
	s.Hours = map[int64]int64{h(t0.Add(-3 * day)): 100, h(t0.Add(-2 * time.Hour)): 20, h(t0): 30}
	attribute(st, "claude", s, "ann@acme.dev", false, t0, growth)
	if got, want := days("ann@acme.dev"), []int64{30, 0, 0, 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("ann's days = %v, want %v", got, want)
	}
	if got, want := days("bo@acme.dev"), []int64{20}; !reflect.DeepEqual(got, want) {
		t.Errorf("bo's days = %v, want %v", got, want)
	}
	if ps := Projects(st); len(ps) != 1 || !reflect.DeepEqual(DaysOf(ps[0].Hours, t0), []int64{50, 0, 0, 100}) {
		t.Errorf("projects = %+v", ps)
	}
}

// TestSwitchedSessionFromALedgerWithoutAccountsHours: a ledger from before
// accounts kept their own hours shares a session's hours by the accounts'
// shares of its tokens. From its next growth, each account keeps its own:
// what they had stays as it was shown, and the growth is its spender's.
func TestSwitchedSessionFromALedgerWithoutAccountsHours(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	day := 24 * time.Hour
	st := &state.State{Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
		state.Key("codex", "s1"): {Provider: "codex", Project: "/p", Seen: snapshot.Tokens{Input: 400}, Updated: t0.Add(-time.Hour),
			By:    map[string]snapshot.Tokens{"ann@acme.dev": {Input: 300}, "bo@acme.dev": {Input: 100}},
			Hours: map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0.Add(-time.Hour)): 200}},
	}}
	s := logs.Session{ID: "s1", Project: "/p", Tokens: snapshot.Tokens{Input: 440}, Updated: t0,
		Hours: map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0.Add(-time.Hour)): 200, h(t0): 40}}
	attribute(st, "codex", s, "bo@acme.dev", false, t0, map[string]snapshot.Tokens{})
	e := st.Sessions[state.Key("codex", "s1")]
	want := map[string]map[int64]int64{
		"ann@acme.dev": {h(t0.Add(-2 * day)): 150, h(t0.Add(-time.Hour)): 150},
		"bo@acme.dev":  {h(t0.Add(-2 * day)): 50, h(t0.Add(-time.Hour)): 50, h(t0): 40},
	}
	if !reflect.DeepEqual(e.ByHours, want) {
		t.Errorf("accounts' hours = %v, want %v", e.ByHours, want)
	}
	if got, want := e.Hours, map[int64]int64{h(t0.Add(-2 * day)): 200, h(t0.Add(-time.Hour)): 200, h(t0): 40}; !reflect.DeepEqual(got, want) {
		t.Errorf("session's hours = %v, want %v", got, want)
	}
}

// TestUntimedHistoryFromBeforeHoursStays: a session the ledger kept from
// before it recorded hours has its tokens at the hour its share last grew.
// When its log, which records no times, shows it grow, that history stays in
// its day beside the growth.
func TestUntimedHistoryFromBeforeHoursStays(t *testing.T) {
	day := 24 * time.Hour
	for _, parts := range []map[string]snapshot.Tokens{nil, {"openrouter": {Input: 510}}} {
		st := &state.State{Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
			state.Key("hermes", "s2"): {Provider: "hermes", Project: "/srv1", Seen: snapshot.Tokens{Input: 500}, Updated: t0.Add(-2 * day),
				By: map[string]snapshot.Tokens{"openrouter": {Input: 500}}, Last: map[string]time.Time{"openrouter": t0.Add(-2 * day)}},
		}}
		if got := DaysOf(totalsFor(t, st, "hermes", "openrouter").Hours, t0); !reflect.DeepEqual(got, []int64{0, 0, 500}) {
			t.Fatalf("days before = %v", got)
		}
		s := logs.Session{ID: "s2", Project: "/srv1", Tokens: snapshot.Tokens{Input: 510}, Updated: t0, Account: "openrouter", Parts: parts}
		attribute(st, "hermes", s, "openrouter", false, t0, map[string]snapshot.Tokens{})
		a := totalsFor(t, st, "hermes", "openrouter")
		if got := DaysOf(a.Hours, t0); a.Tokens.Input != 510 || !reflect.DeepEqual(got, []int64{10, 0, 500}) {
			t.Errorf("parts %v: days after = %v for %d tokens, want [10 0 500]", parts, got, a.Tokens.Input)
		}
	}

	// A session on two routes keeps each route's history in its own day.
	st := &state.State{Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{
		state.Key("hermes", "s3"): {Provider: "hermes", Project: "/srv1", Seen: snapshot.Tokens{Input: 700}, Updated: t0.Add(-day),
			By:   map[string]snapshot.Tokens{"openrouter": {Input: 500}, "nous": {Input: 200}},
			Last: map[string]time.Time{"openrouter": t0.Add(-2 * day), "nous": t0.Add(-day)}},
	}}
	s := logs.Session{ID: "s3", Project: "/srv1", Tokens: snapshot.Tokens{Input: 710}, Updated: t0, Account: "openrouter",
		Parts: map[string]snapshot.Tokens{"openrouter": {Input: 510}, "nous": {Input: 200}}}
	attribute(st, "hermes", s, "openrouter", false, t0, map[string]snapshot.Tokens{})
	if got := DaysOf(totalsFor(t, st, "hermes", "openrouter").Hours, t0); !reflect.DeepEqual(got, []int64{10, 0, 500}) {
		t.Errorf("openrouter's days = %v, want [10 0 500]", got)
	}
	if got := DaysOf(totalsFor(t, st, "hermes", "nous").Hours, t0); !reflect.DeepEqual(got, []int64{0, 200}) {
		t.Errorf("nous's days = %v, want [0 200]", got)
	}
}

// TestHoursNeverExceedTheTokens: a Claude log spreads the tokens it records
// no time for over its timed hours, anew as the session grows, and a file
// that cannot be read keeps every read partial. A session's hours still add
// up to its tokens, and its account's days count no more than it spent.
func TestHoursNeverExceedTheTokens(t *testing.T) {
	h := t0.Unix() / 3600
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	// 100 used at h-5, and side calls of 100 spread over that hour.
	s1 := logs.Session{ID: "s1", Tokens: snapshot.Tokens{Input: 200}, Updated: t0.Add(-5 * time.Hour), Hours: map[int64]int64{h - 5: 200}}
	attribute(st, "claude", s1, "ann@acme.dev", true, t0, growth)
	// Resumed for 100 more at h, with the side calls not read this time.
	s1.Updated, s1.Hours = t0, map[int64]int64{h - 5: 100, h: 100}
	attribute(st, "claude", s1, "ann@acme.dev", true, t0, growth)
	if got, want := st.Sessions[state.Key("claude", "s1")].Hours, map[int64]int64{h - 5: 200}; !reflect.DeepEqual(got, want) {
		t.Errorf("hours of a session that did not grow = %v, want %v", got, want)
	}
	// The same with the side calls read: 100 more, spread anew.
	s2 := logs.Session{ID: "s2", Tokens: snapshot.Tokens{Input: 200}, Updated: t0.Add(-5 * time.Hour), Hours: map[int64]int64{h - 5: 200}}
	attribute(st, "claude", s2, "ann@acme.dev", true, t0, growth)
	s2.Tokens.Input, s2.Updated, s2.Hours = 300, t0, map[int64]int64{h - 5: 150, h: 150}
	attribute(st, "claude", s2, "ann@acme.dev", true, t0, growth)
	if got, want := st.Sessions[state.Key("claude", "s2")].Hours, map[int64]int64{h - 5: 200, h: 100}; !reflect.DeepEqual(got, want) {
		t.Errorf("hours = %v, want %v", got, want)
	}
	ann := totalsFor(t, st, "claude", "ann@acme.dev")
	if got := DaysOf(ann.Hours, t0); len(got) != 1 || got[0] != logs.InOut(ann.Tokens) {
		t.Errorf("days = %v for %d tokens", got, logs.InOut(ann.Tokens))
	}
}

// TestGrowthIsNotPlacedInHoursTheLedgerDropped: the log of a session that
// runs longer than the retention window still shows the hours the ledger
// dropped. What the session grows by goes to the hours it was spent in.
func TestGrowthIsNotPlacedInHoursTheLedgerDropped(t *testing.T) {
	h := func(at time.Time) int64 { return at.Unix() / 3600 }
	old, yesterday := t0.Add(-state.Retention-24*time.Hour), t0.Add(-24*time.Hour)
	st := state.NewState()
	growth := map[string]snapshot.Tokens{}
	s := logs.Session{ID: "s1", Tokens: snapshot.Tokens{Input: 1100}, Updated: yesterday, Hours: map[int64]int64{h(old): 1000, h(yesterday): 100}}
	attribute(st, "codex", s, "ann@acme.dev", false, yesterday, growth)
	prune(st, t0)
	s.Tokens.Input, s.Updated, s.Hours = 1150, t0, map[int64]int64{h(old): 1000, h(yesterday): 100, h(t0): 50}
	attribute(st, "codex", s, "ann@acme.dev", false, t0, growth)
	if got, want := st.Sessions[state.Key("codex", "s1")].Hours, map[int64]int64{h(yesterday): 100, h(t0): 50}; !reflect.DeepEqual(got, want) {
		t.Errorf("hours = %v, want %v", got, want)
	}
}
