package collect

import "github.com/neoromantic/ai-usage/internal/logs"

// hermes labels each Hermes session read with the billing provider its row
// names, as its account, attributes the sessions' growth, and links the
// accounts to the logins they bill through. It returns the sessions, and the
// homes whose current account stays: Hermes keeps its current account under
// no home, and forgets it when no Hermes home is found.
func (s *sampler) hermes(homes []string, res logs.Result, partial bool) (read []readSession, probed []string) {
	for _, rs := range res.Sessions {
		label := rs.Account
		if label == "" {
			label = UnknownAccount
		}
		read = append(read, readSession{s: rs, label: label})
	}
	for _, r := range read {
		touchAccount(s.st, "hermes", r.label)
		grown := attribute(s.st, "hermes", r.s, r.label, partial, s.now, s.growth)
		for l := range grown {
			touchAccount(s.st, "hermes", l)
		}
		s.linkGrowth(r.s, grown)
	}
	markHermesCurrent(s.st, read)
	s.linkHermes(read, partial)
	if len(homes) > 0 {
		probed = []string{""}
	}
	return read, probed
}
