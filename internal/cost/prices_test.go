package cost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/state"
)

// Every plan of the built-in list says where its price was checked, and the
// list prices no team, business, enterprise or education plan.
func TestBuiltinPrices(t *testing.T) {
	p := Builtin()
	if p.Updated == "" || p.Currency != "USD" || len(p.Plans) == 0 {
		t.Fatalf("Builtin = %+v", p)
	}
	var raw struct {
		Plans []map[string]any `json:"plans"`
	}
	if err := json.Unmarshal(PricesJSON, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Plans) != len(p.Plans) {
		t.Fatalf("%d plans in the file, %d read", len(raw.Plans), len(p.Plans))
	}
	for _, pl := range raw.Plans {
		if src, _ := pl["source"].(string); !strings.HasPrefix(src, "https://") {
			t.Errorf("%v has no source", pl["plan"])
		}
	}
	for _, pl := range p.Plans {
		for _, out := range []string{"team", "business", "enterprise", "edu"} {
			if strings.Contains(Norm(pl.Plan), out) {
				t.Errorf("%s %s is out of scope", pl.Provider, pl.Plan)
			}
		}
		if pl.USD <= 0 {
			t.Errorf("%s %s has no price", pl.Provider, pl.Plan)
		}
	}
	for _, c := range []struct {
		provider, plan string
		usd            float64
	}{
		{"claude", "pro", 20}, {"claude", "max 5x", 100}, {"claude", "max 20x", 200},
		{"codex", "go", 8}, {"codex", "plus", 20}, {"codex", "prolite", 100}, {"codex", "pro", 200}, {"codex", "promax", 500},
		{"grok", "SuperGrok Lite", 10}, {"grok", "SuperGrok", 30}, {"grok", "SuperGrok Plus", 100},
		{"grok", "SuperGrok Heavy", 300}, {"grok", "X Premium+", 40}, {"grok", "XPremiumPlus", 40},
	} {
		if pl, ok := p.Find(c.provider, c.plan); !ok || pl.USD != c.usd {
			t.Errorf("Find(%s, %s) = %+v, %v", c.provider, c.plan, pl, ok)
		}
	}
	if pl, _ := p.Find("claude", "pro"); pl.Monthly(Yearly) != 200.0/12 || pl.Monthly(Monthly) != 20 {
		t.Errorf("Claude Pro a month: %v yearly, %v monthly", pl.Monthly(Yearly), pl.Monthly(Monthly))
	}
	if pl, _ := p.Find("claude", "max 20x"); pl.Monthly(Yearly) != 200 {
		t.Error("a plan billed only monthly costs its monthly price at a yearly cadence")
	}
}

func TestParsePrices(t *testing.T) {
	ok := `{"v":1,"updated":"2026-11-01","currency":"USD","extra":1,"plans":[
		{"provider":"claude","plan":"pro","usd":21,"source":"x"},
		{"provider":"gemini","plan":"ultra","usd":-5},
		{"provider":"hermes","plan":"x","usd":1}]}`
	p, err := ParsePrices([]byte(ok))
	if err != nil || len(p.Plans) != 1 || p.Plans[0].USD != 21 {
		t.Fatalf("ParsePrices = %+v, %v", p, err)
	}
	for name, body := range map[string]string{
		"not JSON":       `{"v":1`,
		"version":        `{"v":2,"updated":"2026-11-01","currency":"USD","plans":[]}`,
		"no day":         `{"v":1,"currency":"USD","plans":[]}`,
		"bad day":        `{"v":1,"updated":"Nov 1","currency":"USD","plans":[]}`,
		"currency":       `{"v":1,"updated":"2026-11-01","currency":"EUR","plans":[]}`,
		"negative":       `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"codex","plan":"plus","usd":-1}]}`,
		"too dear":       `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"codex","plan":"plus","usd":1e9}]}`,
		"no plan":        `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"codex","plan":"","usd":1}]}`,
		"control":        `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"codex","plan":"a\tb","usd":1}]}`,
		"empty was":      `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"codex","plan":"a","usd":1,"was":["--"]}]}`,
		"escape in name": `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"claude","plan":"pro","name":"\u001b[2JPro","usd":20}]}`,
		"long name":      `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"claude","plan":"pro","name":"` + strings.Repeat("P", 41) + `","usd":20}]}`,
		"escape in was":  `{"v":1,"updated":"2026-11-01","currency":"USD","plans":[{"provider":"claude","plan":"pro","usd":20,"was":["\u001b[31mX"]}]}`,
		"plans as text":  `{"v":1,"updated":"2026-11-01","currency":"USD","plans":"none"}`,
	} {
		if _, err := ParsePrices([]byte(body)); err == nil {
			t.Errorf("%s: ParsePrices took it", name)
		}
	}
	if _, err := ParsePrices([]byte(`{"v":1,"updated":"2026-11-01","currency":"USD","plans":[],"pad":"` + strings.Repeat("x", MaxPricesBytes) + `"}`)); err == nil {
		t.Error("ParsePrices took a list over the limit")
	}
}

func TestPlanKeys(t *testing.T) {
	for in, want := range map[string]string{
		"X Premium+": "xpremiumplus", "XPremiumPlus": "xpremiumplus", "max 20x": "max20x", "SuperGrok Lite": "supergroklite",
		"self_serve_business_prolite": "selfservebusinessprolite", " Free ": "free", "": "", "--": "",
	} {
		if got := Norm(in); got != want {
			t.Errorf("Norm(%q) = %q, want %q", in, got, want)
		}
	}
	list := Builtin()
	for _, c := range []struct {
		provider, a, b string
		same           bool
	}{
		{"claude", "max", "max 20x", true},
		{"claude", "max 20x", "max", true},
		{"claude", "max 5x", "max 20x", false},
		{"claude", "pro", "max", false},
		{"grok", "XPremiumPlus", "X Premium+", true},
		{"grok", "SuperGrok", "SuperGrok Plus", false},
		{"codex", "pro", "prolite", false},
		{"codex", "PRO", "pro", true},
	} {
		if got := list.Same(c.provider, c.a, c.b); got != c.same {
			t.Errorf("Same(%s, %q, %q) = %v", c.provider, c.a, c.b, got)
		}
	}
	for _, c := range [][3]string{{"max", "max 20x", "max 20x"}, {"max 20x", "max", "max 20x"}, {"pro", "plus", "plus"}} {
		if got := Finer(c[0], c[1]); got != c[2] {
			t.Errorf("Finer(%q, %q) = %q", c[0], c[1], got)
		}
	}
	if !Refines("max", "max 5x") || Refines("max", "maximum") || Refines("", "20x") || Refines("max 5x", "max 5x") {
		t.Error("Refines")
	}
	if !Free("free") || !Free("FREE") || Free("freelance") {
		t.Error("Free")
	}
	if list.Name("codex", "prolite") != "Pro 100" || list.Name("codex", "team") != "team" {
		t.Error("Name")
	}
}

// The relay's list is used once it was updated after the built-in one, and
// a fetch is due a day after the last ask, whether or not it got a list.
func TestFetchedPrices(t *testing.T) {
	dir := state.Dir(t.TempDir())
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	if p, src := Current(dir); src != SourceBuiltin || p.Updated != Builtin().Updated {
		t.Fatalf("Current with no cache = %s %s", p.Updated, src)
	}
	if !FetchDue(dir, now) {
		t.Fatal("a fetch is due with no cache")
	}
	newer := `{"v":1,"updated":"2099-01-01","currency":"USD","plans":[{"provider":"codex","plan":"plus","usd":25}]}`
	if err := SaveFetched(dir, []byte(newer), nil, now); err != nil {
		t.Fatal(err)
	}
	p, src := Current(dir)
	if pl, _ := p.Find("codex", "plus"); src != SourceRelay || pl.USD != 25 {
		t.Fatalf("Current = %s, %+v", src, pl)
	}
	if FetchDue(dir, now.Add(23*time.Hour)) || !FetchDue(dir, now.Add(FetchEvery)) || !FetchDue(dir, now.Add(-time.Minute)) {
		t.Fatal("FetchDue")
	}
	// A failed ask and a list this build cannot read keep the list.
	if err := SaveFetched(dir, nil, errFake, now.Add(FetchEvery)); err != nil {
		t.Fatal(err)
	}
	if err := SaveFetched(dir, []byte(`{"v":9}`), nil, now.Add(2*FetchEvery)); err != nil {
		t.Fatal(err)
	}
	f := LoadFetched(dir)
	if !f.FetchedAt.Equal(now) || !f.TriedAt.Equal(now.Add(2*FetchEvery)) {
		t.Fatalf("fetched %v, tried %v", f.FetchedAt, f.TriedAt)
	}
	if _, src := Current(dir); src != SourceRelay {
		t.Fatal("a failed ask dropped the relay's list")
	}
	// A relay on an older release cannot take prices back.
	older := `{"v":1,"updated":"2020-01-01","currency":"USD","plans":[{"provider":"codex","plan":"plus","usd":1}]}`
	if _, src := Choose(Builtin(), Fetched{List: json.RawMessage(older)}); src != SourceBuiltin {
		t.Fatal("an older relay list was used")
	}
	if _, src := Choose(Builtin(), Fetched{List: json.RawMessage(`{"v":1,`)}); src != SourceBuiltin {
		t.Fatal("a damaged relay list was used")
	}
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

const errFake = fakeErr("relay unreachable")
