package view

import (
	"cmp"
	"encoding/json"
	"slices"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
)

// shareProjects gives each of this device's projects whose repository has
// an origin the team's other devices that work in it too: those whose
// snapshot holds an entry that opens with the repository's key. Entries no
// key of this device opens, another repository's or one sealed before
// v0.14.0 with the team key, stay unread.
func shareProjects(in Input, r *Report, now time.Time) {
	if in.Key == nil || in.Team.Team != in.Key.Fingerprint() || len(in.Repos) == 0 {
		return
	}
	// The projects of each repository, and its key: two clones of one
	// repository on this device share what the team spends in it.
	type repo struct {
		key      *team.Project
		projects []int
	}
	repos := map[string]*repo{}
	var ids []string
	for i, p := range r.Projects {
		id := in.Repos[p.Path]
		if id == "" {
			continue
		}
		if repos[id] == nil {
			k, err := in.Key.Project(id)
			if err != nil {
				continue
			}
			repos[id] = &repo{key: k}
			ids = append(ids, id)
		}
		repos[id].projects = append(repos[id].projects, i)
	}
	if len(ids) == 0 {
		return
	}
	names := map[string]string{}
	for _, d := range r.Team.Devices {
		if !d.This {
			names[d.Device] = d.Name
		}
	}
	for _, d := range in.Team.Docs {
		name, ok := names[d.Device]
		if !ok || d.Device == in.Doc.Device {
			continue
		}
		shift := dayShift(d.CollectedAt, now)
		got := map[string]*ProjectDevice{}
		for _, a := range d.Accounts {
			for _, sp := range a.Projects {
				for _, id := range ids {
					plain, ok := repos[id].key.Open(sp.Path)
					if !ok {
						continue
					}
					var u snapshot.ProjectUsage
					if json.Unmarshal(plain, &u) == nil {
						pd := got[id]
						if pd == nil {
							pd = &ProjectDevice{Device: d.Device, Name: name, Providers: []string{}}
							got[id] = pd
						}
						pd.add(a.Provider, u, shift)
					}
					break
				}
			}
		}
		for id, pd := range got {
			for _, i := range repos[id].projects {
				r.Projects[i].Team = append(r.Projects[i].Team, *pd)
			}
		}
	}
	for i := range r.Projects {
		if len(r.Projects[i].Team) > 0 {
			u := r.Projects[i].teamBurn()
			r.Projects[i].TeamBurn = &u
		}
		slices.SortFunc(r.Projects[i].Team, func(a, b ProjectDevice) int {
			return cmp.Or(cmp.Compare(b.Burn.Week, a.Burn.Week), cmp.Compare(b.Burn.Quarter, a.Burn.Quarter),
				cmp.Compare(a.Name, b.Name), cmp.Compare(a.Device, b.Device))
		})
	}
}

// add counts an account's use of the project, of provider, collected shift
// UTC days before the report's day.
func (pd *ProjectDevice) add(provider string, u snapshot.ProjectUsage, shift int) {
	week := usageOf(u.Days, shift)
	pd.Burn.Today += week.Today
	pd.Burn.Week += week.Week
	pd.Burn.Month += u.Month
	pd.Burn.Quarter += u.Quarter
	pd.Sessions += u.Sessions
	if !slices.Contains(pd.Providers, provider) {
		pd.Providers = append(pd.Providers, provider)
	}
	if u.LastActive > 0 {
		at := time.Unix(u.LastActive, 0).UTC()
		if pd.LastActiveAt == nil || at.After(*pd.LastActiveAt) {
			pd.LastActiveAt = &at
		}
	}
}

// teamBurn is what the other devices of the team burned in a project.
func (p Project) teamBurn() Usage {
	var u Usage
	for _, d := range p.Team {
		u.Today += d.Burn.Today
		u.Week += d.Burn.Week
		u.Month += d.Burn.Month
		u.Quarter += d.Burn.Quarter
	}
	return u
}
