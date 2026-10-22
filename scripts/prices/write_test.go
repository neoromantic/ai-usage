package main

import (
	"strings"
	"testing"
)

// A made-up list, written as prices.json is: one plan a line.
const writeList = `{"v":1,"updated":"2026-01-02","currency":"USD","plans":[
 {"provider":"claude","plan":"pro","name":"Pro","usd":20,"annual_usd":200,"source":"https://claude.com/pricing"},
 {"provider":"codex","plan":"plus","name":"Plus","usd":20,"source":"https://apps.apple.com/us/app/chatgpt/id6448311069"},
 {"provider":"grok","plan":"SuperGrok","name":"SuperGrok","usd":30,"annual_usd":300,"source":"https://apps.apple.com/us/app/grok/id6670324846"},
 {"provider":"grok","plan":"X Premium+","name":"X Premium+","usd":40,"was":["XPremiumPlus"],"source":"https://news.example.com/premium"}
]}
`

func diff(provider, plan string, annual bool, fix float64) difference {
	return difference{Plan: listPlan{Provider: provider, Plan: plan}, Annual: annual, Fix: fix}
}

// -write sets each fixed price and the day, and leaves every other byte:
// the lines, the order of the keys, and each source.
func TestRewrite(t *testing.T) {
	got, err := rewrite([]byte(writeList), []difference{
		diff("codex", "plus", false, 25),
		diff("grok", "SuperGrok", true, 349.5),
		diff("grok", "SuperGrok", false, 35),
	}, "2026-03-04")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer(
		`"updated":"2026-01-02"`, `"updated":"2026-03-04"`,
		`"plan":"plus","name":"Plus","usd":20,`, `"plan":"plus","name":"Plus","usd":25,`,
		`"usd":30,"annual_usd":300,`, `"usd":35,"annual_usd":349.5,`,
	).Replace(writeList)
	if string(got) != want {
		t.Errorf("rewrite =\n%s\nwant\n%s", got, want)
	}
}

// With nothing to fix, the list is left as it is, its day too.
func TestRewriteNothing(t *testing.T) {
	got, err := rewrite([]byte(writeList), nil, "2026-03-04")
	if err != nil || string(got) != writeList {
		t.Errorf("rewrite = %s, %v; want the list unchanged", got, err)
	}
}

// A fix that cannot be placed is an error, never a list written in part.
func TestRewriteRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		list string
		fix  difference
		day  string
	}{
		"plan not in the list":   {writeList, diff("codex", "pro", false, 210), "2026-03-04"},
		"year the plan lacks":    {writeList, diff("codex", "plus", true, 250), "2026-03-04"},
		"plan twice":             {strings.Replace(writeList, `"provider":"claude","plan":"pro"`, `"provider":"codex","plan":"plus"`, 1), diff("codex", "plus", false, 25), "2026-03-04"},
		"not a day":              {writeList, diff("codex", "plus", false, 25), "March 4"},
		"price out of range":     {writeList, diff("codex", "plus", false, 200001), "2026-03-04"},
		"list without a day":     {strings.Replace(writeList, `"updated":"2026-01-02",`, "", 1), diff("codex", "plus", false, 25), "2026-03-04"},
		"list that is not JSON":  {writeList[:40], diff("codex", "plus", false, 25), "2026-03-04"},
		"plans that are a value": {`{"v":1,"updated":"2026-01-02","currency":"USD","plans":1}`, diff("codex", "plus", false, 25), "2026-03-04"},
	} {
		if got, err := rewrite([]byte(c.list), []difference{c.fix}, c.day); err == nil {
			t.Errorf("%s: rewrite =\n%s\nwant an error", name, got)
		}
	}
}

// A difference gets a fix only when its page shows one price that could be
// the plan's, near the list's: $24.99 is $25, while a price over double the
// list's, or two prices that could be it, are left to a person.
func TestCompareFixes(t *testing.T) {
	l := testList()
	r := compare(l, map[string]page{
		store: {offers: []offer{
			{Name: "ChatGPT Plus", USD: 24.99},
			{Name: "ChatGPT Go", USD: 90, Period: annual},
			{Name: "ChatGPT Go", USD: 7, Period: annual},
		}},
		pricing: {offers: []offer{{Name: "Pro", USD: 200, Period: annual}, {Name: "Pro", USD: 220, Period: monthly}}},
	})
	fixes := map[string]float64{}
	for _, d := range r.Differs {
		k := d.Plan.Provider + "/" + d.Plan.Plan
		if d.Annual {
			k += "/year"
		}
		fixes[k] = d.Fix
	}
	want := map[string]float64{
		"claude/pro":    0, // $220 a month is over double $20
		"codex/plus":    25,
		"codex/go":      0, // no price a month
		"codex/go/year": 0, // two prices could be it
	}
	if len(fixes) != len(want) {
		t.Errorf("differs = %v, want %v", fixes, want)
	}
	for k, w := range want {
		if got, ok := fixes[k]; !ok || got != w {
			t.Errorf("fix of %s = %v (%v), want %v", k, got, ok, w)
		}
	}
	md := r.markdown()
	for _, s := range []string{"| Codex Plus | $20 a month | $24.99 | [apps.apple.com", "| $25 a month |", "| by hand |", "scripts/prices -write"} {
		if !strings.Contains(md, s) {
			t.Errorf("report has no %q:\n%s", s, md)
		}
	}
	if f := r.fixes(); len(f) != 1 || f[0].Plan.Plan != "plus" {
		t.Errorf("fixes = %+v, want Codex Plus", f)
	}
}
