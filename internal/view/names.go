package view

import (
	"strings"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// addAliases keeps the newest alias of each account a device doc names.
func (m *merger) addAliases(d snapshot.Doc) {
	for _, a := range d.Aliases {
		if a.Name != "" {
			// A name the alias command would refuse is not one.
			if a.Name = m.open(a.Name); snapshot.CheckAlias(a.Name) != nil {
				continue
			}
		}
		k := aliasKey(a.Provider, m.open(a.Label))
		if cur, ok := m.aliases[k]; !ok || snapshot.AliasWins(a.At, d.Device, cur.At, m.aliasBy[k]) {
			m.aliases[k], m.aliasBy[k] = a, d.Device
		}
	}
}

// names gives each account of a provider its short name: the alias the
// team gave it, else the part of an email before the @, else the first 8
// characters of an id. When two accounts go by the same name, in any case,
// as the alias command compares names, both go by the full label. A device
// can give an account a name before it reads another device's account that
// goes by it.
func names(provider string, m map[string]*teamAccount, aliases map[string]snapshot.Alias) {
	for _, x := range m {
		x.ta.Name = ShortName(x.ta.Label)
		if a, ok := aliases[aliasKey(provider, x.ta.Label)]; ok && a.Name != "" {
			x.ta.Name, x.ta.Alias = a.Name, strPtr(a.Name)
		}
	}
	// A full label can be another account's name too, so this goes on until
	// no name is taken twice. A full label stays, so it ends.
	for {
		count := map[string]int{}
		for _, x := range m {
			count[strings.ToLower(x.ta.Name)]++
		}
		same := false
		for _, x := range m {
			if count[strings.ToLower(x.ta.Name)] > 1 && x.ta.Name != x.ta.Label {
				x.ta.Name, same = x.ta.Label, true
			}
		}
		if !same {
			return
		}
	}
}

// aliasKey keys an alias by provider and label, with an email in any case.
func aliasKey(provider, label string) string {
	return state.Key(provider, snapshot.LabelKey(label))
}

// ShortName is a label's short name when the team gave it none, before
// collisions: the part of an email before the @, the first 8 characters of
// an id, else the whole label.
func ShortName(label string) string {
	if i := strings.Index(label, "@"); i > 0 {
		return label[:i]
	}
	if idLike(label) {
		return label[:8]
	}
	return label
}

// idLike is a label that is an opaque id: a UUID, or a long run of letters
// and digits with digits in it.
func idLike(s string) bool {
	if uuidRe.MatchString(s) {
		return true
	}
	if len(s) < 16 {
		return false
	}
	digits := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
		default:
			return false
		}
	}
	return digits
}

// teamName is an account's short name in the team, or its default name
// when the team view does not hold it.
func teamName(t Team, provider, label string) string {
	for _, p := range t.Providers {
		if p.Provider != provider {
			continue
		}
		for _, a := range p.Accounts {
			if a.Label == label {
				return a.Name
			}
		}
	}
	return ShortName(label)
}
