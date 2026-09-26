// Command demo writes the made-up reports that docs/demo shows, as
// `ai-usage --json` would print them. Every machine, person, account, and
// number in them is invented. Each is built by the code that builds a real
// report, so its forecasts, attention, and matrix agree with its readings
// and tokens. `ai-usage report --from FILE` shows one.
//
//	go run ./scripts/demo docs/demo    # write the reports
//	go run ./scripts/demo guide 110     # print the solo page as a first run does
//	go run ./scripts/demo snapshot      # print what the solo device sends the relay
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/view"
)

// now is every report's time: a Thursday afternoon in Berlin.
var now = time.Date(2026, 9, 24, 13, 40, 0, 0, time.UTC)

const (
	latest = "v0.2.4"
	relay  = "https://relay.example.com"
	week   = 7 * 24 * time.Hour
)

func main() {
	out := "docs/demo"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	switch out {
	case "guide":
		firstRun()
		return
	case "snapshot":
		b, err := json.MarshalIndent(soloDevice(now.Add(-4*time.Minute)).doc(demoKey()), "", "  ")
		if err != nil {
			panic(err)
		}
		fmt.Println(string(b))
		return
	}
	for name, build := range map[string]func() view.Report{"solo": solo, "team": teamReport} {
		b, err := json.MarshalIndent(build(), "", "  ")
		if err != nil {
			panic(err)
		}
		path := filepath.Join(out, name+".json")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			panic(err)
		}
		fmt.Println(path)
	}
}

// solo is one developer on one laptop with three subscriptions: Claude
// runs out before its reset, Codex has room, and Grok sits nearly unused.
func solo() view.Report {
	return soloDevice(now.Add(-4 * time.Minute)).report(nil)
}

// firstRun prints the solo page as the installer's first run does: the
// report as of the run, then the guide. The width is the argument after
// guide, and COLORFGBG says whether the terminal is light.
func firstRun() {
	width := 110
	if len(os.Args) > 2 {
		if _, err := fmt.Sscan(os.Args[2], &width); err != nil {
			panic(err)
		}
	}
	d := soloDevice(now)
	d.st.Update.Latest = ""
	o := view.Options{Width: width, Color: true, Dark: !strings.HasSuffix(os.Getenv("COLORFGBG"), ";15")}
	r := d.report(nil)
	fmt.Print(view.Text(r, o) + "\n" + view.Guide(r, "launchd", o))
}

// soloDevice is solo's laptop, last collected at at.
func soloDevice(at time.Time) *device {
	rng := rand.New(rand.NewPCG(1, 1))
	d := newDevice(rng, "d-demo-solo", "mira-mbp", "mira", "/Users/mira", latest, at, "claude", "codex", "grok")
	d.account("claude", "mira@studio.dev", "max", true, at,
		window("5h", 35, 5*time.Hour, 2*time.Hour+10*time.Minute),
		window("7d", 71, week, 2*24*time.Hour+9*time.Hour+20*time.Minute),
		window("7d Fable", 93, week, 2*24*time.Hour+9*time.Hour+20*time.Minute))
	d.account("codex", "mira@studio.dev", "pro", true, at,
		window("5h", 6, 5*time.Hour, 3*time.Hour+45*time.Minute),
		window("7d", 22, week, 4*24*time.Hour+19*time.Hour))
	d.account("grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "SuperGrok", true, at,
		window("7d", 9, week, 1*24*time.Hour+22*time.Hour))
	d.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/web", 16, 90)
	d.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/api", 9, 60)
	d.work("codex", "mira@studio.dev", "/Users/mira/src/orbit/web", 11, 90)
	d.workTo(now.Add(-2*time.Hour), "codex", "mira@studio.dev", "/Users/mira/src/orbit/infra", 4, 45)
	d.workTo(now.Add(-5*time.Hour), "claude", "mira@studio.dev", "/Users/mira/notes", 3, 90)
	d.workTo(now.Add(-26*time.Hour), "codex", "mira@studio.dev", "/Users/mira/src/ml-playground", 2, 20)
	d.workTo(now.Add(-3*24*time.Hour), "grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "/Users/mira/src/ml-playground", 0.6, 30)
	d.workTo(now.Add(-4*24*time.Hour), "claude", "mira@studio.dev", "/Users/mira/dotfiles", 0.4, 90)
	return d
}

// teamReport is a small studio: two laptops, a Linux server, and nine
// Hermes bots, each in its own container, that all bill through one Codex
// login on the server.
func teamReport() view.Report {
	rng := rand.New(rand.NewPCG(2, 2))
	key := demoKey()

	mira := newDevice(rng, "d-demo-mira", "mira-mbp", "mira", "/Users/mira", latest, now.Add(-3*time.Minute), "claude", "codex", "grok")
	mira.account("claude", "mira@studio.dev", "max", true, now.Add(-3*time.Minute),
		window("5h", 100, 5*time.Hour, time.Hour+25*time.Minute),
		window("7d", 58, week, 2*24*time.Hour+9*time.Hour+20*time.Minute))
	mira.account("codex", "mira@studio.dev", "pro", true, now.Add(-3*time.Minute),
		window("5h", 9, 5*time.Hour, 4*time.Hour+5*time.Minute),
		window("7d", 41, week, 3*24*time.Hour+7*time.Hour))
	mira.account("grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "SuperGrok", true, now.Add(-3*time.Minute),
		window("7d", 6, week, 5*24*time.Hour+2*time.Hour))
	mira.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/web", 17, 90)
	mira.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/api", 8, 60)
	mira.work("codex", "mira@studio.dev", "/Users/mira/src/orbit/web", 10, 90)
	mira.workTo(now.Add(-3*time.Hour), "codex", "mira@studio.dev", "/Users/mira/src/orbit/infra", 3, 45)
	mira.workTo(now.Add(-6*time.Hour), "claude", "mira@studio.dev", "/Users/mira/notes", 2.5, 90)
	mira.workTo(now.Add(-2*24*time.Hour), "grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "/Users/mira/src/ml-playground", 0.7, 30)
	mira.workTo(now.Add(-4*24*time.Hour), "claude", "mira@studio.dev", "/Users/mira/dotfiles", 0.3, 90)

	// leo runs an older release, and his Codex app fails to answer.
	leo := newDevice(rng, "d-demo-leo", "leo-air", "leo", "/Users/leo", "v0.2.2", now.Add(-11*time.Minute), "claude", "codex")
	leo.account("claude", "leo@studio.dev", "max", true, now.Add(-11*time.Minute),
		window("5h", 18, 5*time.Hour, 3*time.Hour+50*time.Minute),
		window("7d", 26, week, 4*24*time.Hour+2*time.Hour))
	leo.account("codex", "leo@studio.dev", "plus", true, now.Add(-5*time.Hour),
		window("5h", 40, 5*time.Hour, 0),
		window("7d", 100, week, 19*time.Hour+32*time.Minute))
	leo.fail("codex", "app-server exited without answering")
	leo.work("claude", "leo@studio.dev", "/Users/leo/src/orbit/mobile", 12, 90)
	leo.workTo(now.Add(-5*time.Hour), "codex", "leo@studio.dev", "/Users/leo/src/orbit/mobile", 6, 60)
	leo.work("claude", "leo@studio.dev", "/Users/leo/src/orbit/api", 3, 40)

	// The server runs Claude Code and Codex jobs, and Hermes on the Codex
	// login the bots share.
	srv := newDevice(rng, "d-demo-build", "build-01", "root", "/root", latest, now.Add(-6*time.Minute), "claude", "codex", "hermes")
	srv.account("claude", "mira@studio.dev", "max", false, time.Time{})
	srv.account("codex", "bots@studio.dev", "pro", true, now.Add(-6*time.Minute),
		window("5h", 30, 5*time.Hour, 2*time.Hour+40*time.Minute),
		window("7d", 83, week, 3*24*time.Hour+11*time.Hour))
	srv.work("codex", "bots@studio.dev", "/srv/orbit/nightly", 24, 90)
	srv.work("claude", "mira@studio.dev", "/srv/orbit/review", 5, 60)
	srv.hermes("bots@studio.dev", 6, 60)

	var docs []snapshot.Doc
	for _, d := range []*device{leo, srv} {
		docs = append(docs, d.doc(key))
	}
	bots := []struct {
		name   string
		perDay float64
	}{
		{"scout", 9}, {"editor", 7}, {"herald", 5.5}, {"sentry", 4},
		{"archivist", 3}, {"courier", 2.5}, {"tutor", 1.6}, {"critic", 1}, {"concierge", 0.4},
	}
	for i, b := range bots {
		at := now.Add(-time.Duration(4+i) * time.Minute)
		if b.name == "courier" {
			// Its container stopped yesterday morning.
			at = now.Add(-29*time.Hour - 12*time.Minute)
		}
		bot := newDevice(rng, "d-demo-bot-"+b.name, b.name, "hermes", "/home/hermes", latest, at, "codex", "hermes")
		bot.account("codex", "bots@studio.dev", "pro", true, at,
			window("5h", 30, 5*time.Hour, 2*time.Hour+40*time.Minute),
			window("7d", 83, week, 3*24*time.Hour+11*time.Hour))
		bot.hermes("bots@studio.dev", b.perDay, 60)
		docs = append(docs, bot.doc(key))
	}
	return mira.report(&collect.TeamCache{PulledAt: now.Add(-3 * time.Minute), Team: key.Fingerprint(), Docs: docs})
}

// device is one made-up machine and the ledger its collector keeps.
type device struct {
	rng            *rand.Rand
	id, host, user string
	home, version  string
	at             time.Time
	st             *state.State
}

func newDevice(rng *rand.Rand, id, host, user, home, version string, at time.Time, providers ...string) *device {
	st := &state.State{
		LastRunAt:     at,
		LastSuccessAt: at,
		Sources:       map[string]state.Source{},
		Current:       map[string]string{},
		Accounts:      map[string]*state.Account{},
		Sessions:      map[string]*state.Session{},
	}
	for _, p := range collect.Providers {
		st.Sources[p] = state.Source{Status: "skipped"}
	}
	for _, p := range providers {
		st.Sources[p] = state.Source{Status: "ok", Homes: []string{home + "/." + p}}
	}
	st.Relay.LastPushAt, st.Relay.LastPullAt = at, at
	st.Update.CheckedAt, st.Update.Latest = at.Add(-2*time.Hour), version
	st.Schedule.Registered = true
	return &device{rng: rng, id: id, host: host, user: user, home: home, version: version, at: at, st: st}
}

// window is a quota window of length that resets in resetsIn.
func window(name string, pct float64, length, resetsIn time.Duration) snapshot.Window {
	reset := now.Add(resetsIn)
	if resetsIn == 0 {
		reset = now.Add(-time.Hour)
	}
	return snapshot.Window{Name: name, Percent: pct, Minutes: int(length / time.Minute), ResetsAt: &reset}
}

// account adds a login, read at readAt; a zero readAt has no reading.
func (d *device) account(provider, label, plan string, current bool, readAt time.Time, windows ...snapshot.Window) {
	a := &state.Account{Provider: provider, Label: label, Plan: plan, LastSeenAt: d.at}
	if !readAt.IsZero() {
		a.Quota = &state.Quota{At: readAt, Source: "harness", Windows: windows}
	}
	d.st.Accounts[state.Key(provider, label)] = a
	if current {
		d.st.Current[state.Key(provider, d.home+"/."+provider)] = label
	}
}

// fail makes a harness fail on the device.
func (d *device) fail(provider, msg string) {
	src := d.st.Sources[provider]
	src.Status, src.Error = "error", msg
	d.st.Sources[provider] = src
}

// work spends about perDay million input and output tokens a day on project
// over the last days days, up to the device's last run: in working hours,
// less at weekends, with a day off now and then, one session a day.
func (d *device) work(provider, label, project string, perDay float64, days int) {
	d.spend(d.at, provider, label, project, perDay, days, nil)
}

// workTo is work that stopped at until.
func (d *device) workTo(until time.Time, provider, label, project string, perDay float64, days int) {
	d.spend(until, provider, label, project, perDay, days, nil)
}

// hermes spends through a Codex login, as a Hermes gateway does.
func (d *device) hermes(codex string, perDay float64, days int) {
	d.st.Accounts[state.Key("hermes", "openai-codex")] = &state.Account{
		Provider: "hermes", Label: "openai-codex", LastSeenAt: d.at,
		Link: &state.Link{Provider: "codex", Label: codex},
	}
	d.spend(d.at, "hermes", "openai-codex", d.home, perDay, days, &state.Link{Provider: "codex", Label: codex})
}

func (d *device) spend(until time.Time, provider, label, project string, perDay float64, days int, via *state.Link) {
	today := d.at.Truncate(24 * time.Hour)
	for i := range days {
		day := today.Add(-time.Duration(i) * 24 * time.Hour)
		weight := 0.6 + 0.8*d.rng.Float64()
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			weight *= 0.3
		}
		if i > 0 && d.rng.Float64() < 0.12 {
			continue
		}
		s := &state.Session{Provider: provider, Project: project, Hours: map[int64]int64{}}
		var total int64
		for h := 7; h < 19; h++ {
			at := day.Add(time.Duration(h)*time.Hour + 20*time.Minute)
			if at.After(until) {
				break
			}
			n := int64(perDay * 1e6 / 12 * weight * (0.3 + 1.4*d.rng.Float64()))
			s.Hours[logs.HourOf(at)] += n
			total += n
			s.Updated = at
		}
		if total == 0 {
			continue
		}
		tok := snapshot.Tokens{Input: total * 93 / 100, Output: total * 7 / 100, CacheRead: total * 11, CacheWrite: total / 3}
		s.Seen = tok
		s.By = map[string]snapshot.Tokens{label: tok}
		if via != nil {
			s.Via = map[string]snapshot.Tokens{state.Key(via.Provider, via.Label): tok}
		}
		d.st.Sessions[state.Key(provider, label, project, day.Format(time.DateOnly))] = s
	}
}

// doc is the device's snapshot as another device pulls it.
func (d *device) doc(key *team.Key) snapshot.Doc {
	body, err := json.Marshal(collect.BuildDoc(d.st, key, state.Config{Device: d.id}, d.host, d.user, d.version, d.at))
	if err != nil {
		panic(err)
	}
	doc, err := snapshot.Decode(body)
	if err != nil {
		panic(err)
	}
	return doc
}

// report is the page this device shows, alone or with its team.
func (d *device) report(tc *collect.TeamCache) view.Report {
	key := demoKey()
	cfg := state.Config{Device: d.id}
	in := view.Input{
		Version: d.version, RelayURL: relay, Config: cfg, State: d.st, Key: key,
		Doc:      collect.BuildDoc(d.st, key, cfg, d.host, d.user, d.version, d.at),
		Hostname: d.host, OSUser: d.user, Now: now,
	}
	if tc != nil {
		in.Team = *tc
	}
	return view.Build(in)
}

// demoKey is a team key with a fixed seed, so the team's name is the same
// on every run.
func demoKey() *team.Key {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(0xa0 + i)
	}
	k, err := team.Import("aiu-team-1:" + base64.RawURLEncoding.EncodeToString(seed))
	if err != nil {
		panic(err)
	}
	return k
}
