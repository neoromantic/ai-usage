package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// sharedFixture is this device in /src/api and /src/notes, of
// github.com/acme/api and of no origin, and two others: leo in a clone of
// api and in github.com/acme/web, and an old release's, whose projects are
// sealed with the team key.
func sharedFixture(t *testing.T) *fixture {
	t.Helper()
	st := emptyState()
	addAccount(st, "claude", "sam@acme.dev", true, nil, 0)
	spend(st, "claude", "sam@acme.dev", "/src/api", 5_000_000, now.Add(-2*time.Hour))
	spend(st, "claude", "sam@acme.dev", "/src/notes", 1_000_000, now.Add(-time.Hour))
	f := newFixture(t, st)
	f.in.Repos = map[string]string{"/src/api": "github.com/acme/api"}
	f.in.Doc = collect.BuildDoc(f.in.State, nil, f.key, f.in.Config, collect.Repos{IDs: f.in.Repos}, "thisbox", "sam", "v1.2.3", now)

	leo := emptyState()
	leo.LastRunAt = now
	addAccount(leo, "claude", "leo@acme.dev", true, nil, 0)
	addAccount(leo, "codex", "leo@acme.dev", true, nil, 0)
	spend(leo, "claude", "leo@acme.dev", "/home/leo/api", 3_000_000, now.Add(-3*time.Hour), now.Add(-50*time.Hour))
	spend(leo, "codex", "leo@acme.dev", "/home/leo/api", 1_000_000, now.Add(-30*24*time.Hour))
	spend(leo, "claude", "leo@acme.dev", "/home/leo/web", 9_000_000, now.Add(-time.Hour))
	leoRepos := collect.Repos{IDs: map[string]string{"/home/leo/api": "github.com/acme/api", "/home/leo/web": "github.com/acme/web"}}
	body, _ := json.Marshal(collect.BuildDoc(leo, nil, f.key, state.Config{Device: "d-leo-new-release"}, leoRepos, "leo-air", "leo", "v0.14.0", now))
	leoDoc, err := snapshot.Decode(body)
	if err != nil {
		t.Fatal(err)
	}

	old := emptyState()
	addAccount(old, "claude", "kim@acme.dev", true, nil, 0)
	spend(old, "claude", "kim@acme.dev", "/src/api", 7_000_000, now.Add(-time.Hour))
	oldDoc := otherDoc(t, f.key, "d-kim-old-release", "kim-mbp", now, old)
	oldDoc.Accounts[0].Projects = []snapshot.Project{{Path: f.key.Seal("/src/api"), Sessions: 1, Tokens: snapshot.Tokens{Input: 7_000_000}}}

	f.in.Team = collect.TeamCache{PulledAt: now, Team: f.key.Fingerprint(), Docs: []snapshot.Doc{f.in.Doc, leoDoc, oldDoc}}
	return f
}

// A project another device works in too, by its repository's origin, shows
// that device's burn over its accounts; one of no origin, another
// repository, and a project an old release sealed with the team key show
// none.
func TestShareProjects(t *testing.T) {
	r := Build(sharedFixture(t).in)
	var api, notes *Project
	for i := range r.Projects {
		switch r.Projects[i].Path {
		case "/src/api":
			api = &r.Projects[i]
		case "/src/notes":
			notes = &r.Projects[i]
		}
	}
	if api == nil || notes == nil {
		t.Fatalf("projects = %+v", r.Projects)
	}
	if len(notes.Team) != 0 {
		t.Errorf("notes has a team: %+v", notes.Team)
	}
	if len(api.Team) != 1 {
		t.Fatalf("api's team = %+v", api.Team)
	}
	leo := api.Team[0]
	if leo.Device != "d-leo-new-release" || leo.Name != "leo@leo-air" || leo.Sessions != 2 || leo.LastActiveAt == nil {
		t.Errorf("leo = %+v", leo)
	}
	if len(leo.Providers) != 2 || leo.Providers[0] != "claude" {
		t.Errorf("leo's providers = %v", leo.Providers)
	}
	// Claude's input weighs 1: 3M an hour, twice; Codex's 1M a month ago.
	if leo.Burn.Today != 3_000_000 || leo.Burn.Week != 6_000_000 || leo.Burn.Month != 6_000_000 || leo.Burn.Quarter != 7_000_000 {
		t.Errorf("leo's burn = %+v", leo.Burn)
	}
	if api.TeamBurn == nil || *api.TeamBurn != leo.Burn || notes.TeamBurn != nil {
		t.Errorf("team burn = %+v, notes %+v", api.TeamBurn, notes.TeamBurn)
	}
	b, _ := json.Marshal(r.Projects)
	if strings.Contains(string(b), "web") || strings.Contains(string(b), "d-kim-old-release") {
		t.Errorf("another repository or the old release shows: %s", b)
	}
}

// PROJECTS has TEAM when another device works in a project, and a device's
// detail lists the projects it shares with this one.
func TestSharedProjectsText(t *testing.T) {
	r := Build(sharedFixture(t).in)
	text := plainText(r, Options{Width: 120, Loc: time.UTC})
	head, row := "", ""
	for _, l := range strings.Split(text, "\n") {
		switch f := strings.Fields(l); {
		case len(f) > 0 && f[0] == "PROJECT":
			head = l
		case len(f) > 0 && f[0] == "/src/api":
			row = l
		}
	}
	if !strings.Contains(head, "90D  TEAM  SESS") || len(strings.Fields(row)) < 4 || strings.Fields(row)[3] != "6" {
		t.Errorf("PROJECTS:\n%s\n%s", head, row)
	}
	d := sgr.ReplaceAllString(detailText(t, r, Options{Width: 120, Loc: time.UTC}, Pick{Device: "d-leo-new-release"}), "")
	if !strings.Contains(d, "SHARED PROJECTS  leo@leo-air · by 7d UTC") || !strings.Contains(d, "/src/api") || strings.Contains(d, "SESS") {
		t.Errorf("leo's detail:\n%s", d)
	}
	if d := detailText(t, r, Options{Width: 120, Loc: time.UTC}, Pick{Device: "d-kim-old-release"}); strings.Contains(d, "SHARED PROJECTS") {
		t.Errorf("kim's detail:\n%s", d)
	}
}
