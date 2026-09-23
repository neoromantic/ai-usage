package collect

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	wantProjects := []ProjectTotals{{Path: "/work/web", Sessions: 1, Tokens: tok(300)}, {Path: "/work/api", Sessions: 2, Tokens: tok(105)}}
	if !reflect.DeepEqual(ann.Projects, wantProjects) {
		t.Fatalf("projects = %+v", ann.Projects)
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
	doc, body := encodeDecode(t, BuildDoc(ledger(), key, "d-0123456789", "workbox", "sam", "v1.2.3", t0))
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
	st := &state.State{Sources: map[string]state.Source{}, Current: map[string]string{}, Accounts: map[string]*state.Account{}, Sessions: map[string]*state.Session{}}
	doc, _ := encodeDecode(t, BuildDoc(st, mustKey(t), "d-0123456789", "", "", "", t0))
	if doc.CollectorVersion != "unknown" || doc.LastError != "" {
		t.Fatalf("doc = %+v", doc)
	}
}

func TestBuildDocFitsTheSizeLimit(t *testing.T) {
	key := mustKey(t)
	st := &state.State{
		Sources:  map[string]state.Source{},
		Current:  map[string]string{},
		Accounts: map[string]*state.Account{},
		Sessions: map[string]*state.Session{},
	}
	// More accounts and projects than fit, each with a long path.
	for a := 0; a < snapshot.MaxAccounts+4; a++ {
		label := fmt.Sprintf("account-%02d@%s.example", a, strings.Repeat("x", 200))
		st.Accounts[state.Key("claude", label)] = &state.Account{Provider: "claude", Label: label}
		for p := 0; p < snapshot.MaxProjects+5; p++ {
			path := fmt.Sprintf("/Users/анна/%s/project-%02d", strings.Repeat("каталог/", 60), p)
			st.Sessions[state.Key("claude", fmt.Sprintf("%d-%d", a, p))] = &state.Session{
				Provider: "claude", Project: path, Updated: t0,
				By: map[string]snapshot.Tokens{label: tok(int64(1000*(a+1) + p))},
			}
		}
	}
	doc, body := encodeDecode(t, BuildDoc(st, key, "d-0123456789", "workbox", "sam", "v1", t0))
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
	for i := 0; i < 200; i++ {
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
