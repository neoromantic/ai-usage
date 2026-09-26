package collect

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

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
		{Provider: "hermes", Label: "a", usage: usage{Tokens: tok(1000)}},
		{Provider: "claude", Label: "b", usage: usage{Tokens: tok(1)}},
		{Provider: "claude", Label: "c", usage: usage{Tokens: tok(50)}},
		{Provider: "claude", Label: "d", usage: usage{Tokens: tok(1)}, Current: true},
		{Provider: "claude", Label: "a", usage: usage{Tokens: tok(1)}},
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
