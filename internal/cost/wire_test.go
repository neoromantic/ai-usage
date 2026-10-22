package cost

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
)

func TestAnswerOnTheWire(t *testing.T) {
	price := 180.5
	full := Answer{
		Price: &price, PriceFor: "max 20x", PriceAt: t0.Add(-3 * time.Hour),
		Cadence: Yearly, CadenceAt: t0.Add(-2 * time.Hour),
		Renews: "2026-11-14", RenewsAt: t0.Add(-time.Hour),
		CancelledAt: t0.Add(-30 * time.Minute), ConfirmedAt: t0.Add(-20 * time.Minute),
	}
	cleared := Answer{PriceAt: t0, CadenceAt: t0, RenewsAt: t0}
	for name, a := range map[string]Answer{
		"full":      full,
		"cleared":   cleared,
		"confirmed": {ConfirmedAt: t0},
		"monthly":   {Cadence: Monthly, CadenceAt: t0},
		"untyped":   {Price: &price, PriceAt: t0},
	} {
		s, at, ok := EncodeAnswer(a)
		if !ok || !at.Equal(a.At()) {
			t.Fatalf("%s: EncodeAnswer = %q, %v, %v", name, s, at, ok)
		}
		if got := DecodeAnswer(s, t0); !reflect.DeepEqual(got, a) {
			t.Errorf("%s: %s reads back as %+v", name, s, got)
		}
	}
	s, _, _ := EncodeAnswer(full)
	if want := `{"p":180.5,"pl":"max 20x","pt":1792659600,"c":"y","ct":1792663200,"r":"2026-11-14","rt":1792666800,"x":"2026-10-22T11:30:00Z","ok":"2026-10-22T11:40:00Z"}`; s != want {
		t.Errorf("the data is\n%s\nwant\n%s", s, want)
	}
	if s, _, _ := EncodeAnswer(cleared); s != `{"p":null,"pt":1792670400,"c":null,"ct":1792670400,"r":null,"rt":1792670400}` {
		t.Errorf("cleared: %s", s)
	}
	if _, _, ok := EncodeAnswer(Answer{}); ok {
		t.Error("an empty answer was encoded")
	}
	long := strings.Repeat("y", snapshot.MaxPlain)
	if s, _, ok := EncodeAnswer(Answer{Price: &price, PriceFor: long, PriceAt: t0, Cadence: Yearly, CadenceAt: t0, Renews: "2026-11-14", RenewsAt: t0, CancelledAt: t0, ConfirmedAt: t0}); !ok || len(s) > MaxData {
		t.Errorf("the longest answer is %d bytes", len(s))
	}
}

// Each part of an answer is read on its own: what is not what this build
// writes, or is dated after the snapshot, is left out.
func TestDecodeAnswerLeniently(t *testing.T) {
	pt := `"pt":1792670400`
	for name, c := range map[string]struct {
		data string
		want Answer
	}{
		"not JSON":        {`{"p":`, Answer{}},
		"an array":        {`[1]`, Answer{}},
		"unknown keys":    {`{"zz":1,"ok":"2026-10-22T12:00:00Z"}`, Answer{ConfirmedAt: t0}},
		"price as text":   {`{"p":"20",` + pt + `}`, Answer{}},
		"negative price":  {`{"p":-5,` + pt + `}`, Answer{}},
		"price too high":  {`{"p":1e9,` + pt + `}`, Answer{}},
		"price no time":   {`{"p":20}`, Answer{}},
		"price bad plan":  {`{"p":20,"pl":"a\tb",` + pt + `}`, Answer{Price: ptr(20.0), PriceAt: t0}},
		"cadence weekly":  {`{"c":"w","ct":1792670400}`, Answer{}},
		"day not a day":   {`{"r":"2026-13-01","rt":1792670400}`, Answer{}},
		"day as a number": {`{"r":14,"rt":1792670400}`, Answer{}},
		"time negative":   {`{"p":null,"pt":-1}`, Answer{}},
		"time in future":  {`{"p":null,"pt":1892670400,"x":"2036-01-01T00:00:00Z"}`, Answer{}},
		"stamp not RFC":   {`{"x":"yesterday","ok":1}`, Answer{}},
		"within the skew": {`{"ok":"2026-10-22T12:05:00Z"}`, Answer{ConfirmedAt: t0.Add(5 * time.Minute)}},
	} {
		if got := DecodeAnswer(c.data, t0); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// Each answer is the newest any device gave, and of two given at once, the
// one of the smaller device id; a cleared answer outranks an older one.
func TestMerge(t *testing.T) {
	p1, p2 := 100.0, 150.0
	got := Merge(map[string]Answer{
		"d-bbbb": {Price: &p1, PriceFor: "plus", PriceAt: t0, Renews: "2026-11-01", RenewsAt: t0.Add(time.Hour), CancelledAt: t0},
		"d-aaaa": {Price: &p2, PriceAt: t0, RenewsAt: t0.Add(2 * time.Hour), ConfirmedAt: t0.Add(time.Minute)},
		"d-cccc": {Cadence: Yearly, CadenceAt: t0.Add(-time.Hour)},
		"d-dddd": {},
	})
	want := Answer{Price: &p2, PriceAt: t0, Cadence: Yearly, CadenceAt: t0.Add(-time.Hour), RenewsAt: t0.Add(2 * time.Hour),
		CancelledAt: t0, ConfirmedAt: t0.Add(time.Minute)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Merge = %+v", got)
	}
	if got.Cancelled() {
		t.Fatal("a newer confirm did not outrank the cancel")
	}
	if !Merge(nil).IsZero() {
		t.Fatal("Merge of nothing")
	}
}

// Heard opens each device's costs, leaves out what does not open or is of
// Hermes, and keeps a device's newest answer of a subscription.
func TestHeard(t *testing.T) {
	key, err := team.Generate()
	if err != nil {
		t.Fatal(err)
	}
	other, _ := team.Generate()
	entry := func(provider, label string, a Answer, k *team.Key) snapshot.Cost {
		s, at, _ := EncodeAnswer(a)
		return snapshot.Cost{Provider: provider, Label: k.Seal(label), Data: k.Seal(s), At: at}
	}
	price := 30.0
	docs := []snapshot.Doc{
		{Device: "d-one", CollectedAt: t0, Costs: []snapshot.Cost{
			entry("grok", "Ann@acme.dev", Answer{Price: &price, PriceAt: t0.Add(-time.Hour)}, key),
			entry("grok", "ann@acme.dev", Answer{ConfirmedAt: t0.Add(-2 * time.Hour)}, key),
			entry("hermes", "bot", Answer{ConfirmedAt: t0}, key),
			entry("codex", "kim@acme.dev", Answer{ConfirmedAt: t0}, other),
			{Provider: "codex", Label: key.Seal("kim@acme.dev"), Data: "not-sealed", At: t0},
		}},
		{Device: "d-two", CollectedAt: t0, Costs: []snapshot.Cost{entry("grok", "ann@acme.dev", Answer{ConfirmedAt: t0}, key)}},
	}
	got := Heard(docs, key.Open)
	k := Key("grok", "ann@acme.dev")
	if len(got) != 1 || len(got[k]) != 2 || *got[k]["d-one"].Price != 30 || !got[k]["d-two"].ConfirmedAt.Equal(t0) {
		t.Fatalf("Heard = %+v", got)
	}
}
