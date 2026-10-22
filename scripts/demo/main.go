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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/cost"
	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/view"
)

// now is every report's time: a Thursday afternoon in Berlin.
var now = time.Date(2026, 9, 24, 13, 40, 0, 0, time.UTC)

// began is when the reporting device's cost store began, two months
// before: it has known the subscriptions found then since, and the ones
// found later are news.
var began = now.Add(-60 * 24 * time.Hour)

// berlin is the time zone the reports' words give their clocks in, as the
// pictures show them.
var berlin = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		panic(err)
	}
	return loc
}()

const (
	latest = "v0.5.0"
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
	for name, build := range map[string]func() view.Report{"solo": solo, "team": teamReport, "homebrew": homebrew} {
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

// homebrew is solo's laptop where Homebrew installed ai-usage and a release
// is out that brew has not installed yet, for the menu bar app's checks.
func homebrew() view.Report {
	d := soloDevice(now.Add(-4 * time.Minute))
	d.brew = true
	d.st.Update.Latest = "v0.5.1"
	return d.report(nil)
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
	fmt.Print(view.Text(r, o) + "\n" + view.Guide(r, "launchd", false, o))
}

// soloDevice is solo's laptop, last collected at at.
func soloDevice(at time.Time) *device {
	rng := rand.New(rand.NewPCG(1, 1))
	d := newDevice(rng, "d-demo-solo", "mira-mbp", "mira", "/Users/mira", latest, at, "claude", "codex", "grok")
	d.account("claude", "mira@studio.dev", "max 20x", true, at,
		window("5h", 35, 5*time.Hour, 2*time.Hour+10*time.Minute),
		window("7d", 71, week, 2*24*time.Hour+9*time.Hour+20*time.Minute),
		window("7d Fable", 93, week, 2*24*time.Hour+9*time.Hour+20*time.Minute))
	d.account("codex", "mira@studio.dev", "pro", true, at,
		window("5h", 6, 5*time.Hour, 3*time.Hour+45*time.Minute),
		window("7d", 22, week, 4*24*time.Hour+19*time.Hour))
	d.account("grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "SuperGrok", true, at,
		window("7d", 9, week, 1*24*time.Hour+22*time.Hour))
	miraHistory(d)
	d.webWorktrees()
	d.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/web", 16, 90)
	d.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/api", 9, 60)
	d.work("codex", "mira@studio.dev", "/Users/mira/src/orbit/web", 11, 90)
	d.workTo(now.Add(-2*time.Hour), "codex", "mira@studio.dev", "/Users/mira/src/orbit/infra", 4, 45)
	d.workTo(now.Add(-5*time.Hour), "claude", "mira@studio.dev", "/Users/mira/notes", 3, 90)
	d.workTo(now.Add(-26*time.Hour), "codex", "mira@studio.dev", "/Users/mira/src/ml-playground", 2, 20)
	d.workTo(now.Add(-3*24*time.Hour), "grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "/Users/mira/src/ml-playground", 0.6, 30)
	d.workTo(now.Add(-4*24*time.Hour), "claude", "mira@studio.dev", "/Users/mira/dotfiles", 0.4, 90)
	soloCost(d)
	return d
}

// soloCost is what mira told her collector of her subscriptions: when
// Claude and Codex charge. She has not said when SuperGrok does, so the
// report asks, monthly or yearly.
func soloCost(d *device) {
	d.cost = newCosts()
	claude := d.subscription("claude", "mira@studio.dev", "max 20x")
	must(claude.Answer.SetRenews("2026-07-26", began.Add(26*time.Hour)))
	codex := d.subscription("codex", "mira@studio.dev", "pro")
	must(codex.Answer.SetRenews("2026-08-08", began.Add(26*time.Hour)))
	d.subscription("grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "SuperGrok")
	d.observe(nil, nil)
}

// teamReport is a small studio: two laptops, a Linux server, and nine
// Hermes bots, each in its own container, that all bill through one Codex
// login on the server.
func teamReport() view.Report {
	rng := rand.New(rand.NewPCG(2, 2))
	key := demoKey()

	mira := newDevice(rng, "d-demo-mira", "mira-mbp", "mira", "/Users/mira", latest, now.Add(-3*time.Minute), "claude", "codex", "grok")
	mira.account("claude", "mira@studio.dev", "max 20x", true, now.Add(-3*time.Minute),
		window("5h", 100, 5*time.Hour, time.Hour+25*time.Minute),
		window("7d", 58, week, 2*24*time.Hour+9*time.Hour+20*time.Minute))
	// Her Claude week filled faster over the last day than before: at that
	// pace it runs out, though at the week's it would not.
	mira.runs("claude", "mira@studio.dev", "7d",
		run{26 * time.Hour, 33}, run{22 * time.Hour, 36}, run{16 * time.Hour, 42}, run{10 * time.Hour, 47}, run{4 * time.Hour, 53})
	mira.account("codex", "mira@studio.dev", "pro", true, now.Add(-3*time.Minute),
		window("5h", 9, 5*time.Hour, 4*time.Hour+5*time.Minute),
		window("7d", 47, week, 3*24*time.Hour+7*time.Hour))
	// She stopped using Grok nine days ago, and still pays for it.
	mira.account("grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "SuperGrok", true, now.Add(-3*time.Minute),
		window("7d", 0, week, 5*24*time.Hour+2*time.Hour))
	miraHistory(mira)
	mira.webWorktrees()
	mira.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/web", 17, 90)
	mira.work("claude", "mira@studio.dev", "/Users/mira/src/orbit/api", 8, 60)
	mira.work("codex", "mira@studio.dev", "/Users/mira/src/orbit/web", 10, 90)
	mira.workTo(now.Add(-3*time.Hour), "codex", "mira@studio.dev", "/Users/mira/src/orbit/infra", 3, 45)
	mira.workTo(now.Add(-6*time.Hour), "claude", "mira@studio.dev", "/Users/mira/notes", 2.5, 90)
	mira.workTo(now.Add(-9*24*time.Hour), "grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "/Users/mira/src/ml-playground", 0.7, 30)
	mira.workTo(now.Add(-4*24*time.Hour), "claude", "mira@studio.dev", "/Users/mira/dotfiles", 0.3, 90)

	// leo runs a release from before weighted tokens, so his snapshot has
	// only input plus output and the team estimates his burn from them. His
	// Codex app fails to answer.
	leo := newDevice(rng, "d-demo-leo", "leo-air", "leo", "/Users/leo", "v0.3.4", now.Add(-11*time.Minute), "claude", "codex")
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
	// login the bots share. Its triage job runs Claude Code in a home of its
	// own that nobody logged in to, so its tokens go to no login.
	srv := newDevice(rng, "d-demo-build", "build-01", "root", "/root", latest, now.Add(-6*time.Minute), "claude", "codex", "hermes")
	srv.account("claude", "mira@studio.dev", "max 20x", false, time.Time{})
	srv.loggedOut("claude", "/root/triage/.claude")
	srv.work("claude", collect.UnknownAccount, "/srv/orbit/triage", 1.5, 20)
	srv.account("codex", "bots@studio.dev", "pro", true, now.Add(-6*time.Minute),
		window("5h", 30, 5*time.Hour, 2*time.Hour+40*time.Minute),
		window("7d", 83, week, 3*24*time.Hour+11*time.Hour))
	srv.work("codex", "bots@studio.dev", "/srv/orbit/nightly", 24, 90)
	srv.work("claude", "mira@studio.dev", "/srv/orbit/review", 5, 60)
	srv.hermes("bots@studio.dev", 6, 60)

	// leo's release reads none of the cycles, so no device sends them: the
	// team sees mira's alone.
	docs := []snapshot.Doc{leo.doc(key).Legacy(), srv.doc(key).WithoutCycles()}
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
		docs = append(docs, bot.doc(key).WithoutCycles())
	}
	// The reads found each device on the release its snapshot names, since
	// the first read of the quarter.
	known := map[string]collect.Known{}
	for _, d := range docs {
		known[d.Device] = collect.Known{Version: d.CollectorVersion, First: now.Add(-90 * 24 * time.Hour), Last: d.CollectedAt}
	}
	studioCost(mira, docs, key)
	return mira.report(&collect.TeamCache{PulledAt: now.Add(-3 * time.Minute), Team: key.Fingerprint(), Docs: docs, Known: known})
}

// studioCost is what mira's collector recorded of the studio's
// subscriptions since its cost store began, and what she typed. She types
// prices with VAT, as the studio pays them: the bots' Codex Pro, and her
// Claude Max 5x, which she moved to Max 20x two days ago, so that price no
// longer applies and the list's does. She said when her Codex charges, and
// that she pays SuperGrok yearly; when her Claude charges, the report asks,
// with the day its tool says it began. Leo's Claude has no tier, as his
// release reads none, so it has no price. Jun left the studio in August,
// and nobody has said whether his Claude Pro is still paid; she cancelled
// the CI's ChatGPT Plus ten days ago. The team's other devices run releases
// from before cost, so nothing she typed reaches them.
func studioCost(mira *device, docs []snapshot.Doc, key *team.Key) {
	mira.cost = newCosts()
	claude := mira.subscription("claude", "mira@studio.dev", "max 5x", cost.PlanAt{At: now.Add(-2*24*time.Hour - 5*time.Hour), Plan: "max 20x"})
	must(claude.Answer.SetPrice(new(119.0), "max 5x", began.Add(26*time.Hour)))
	mira.st.Accounts[state.Key("claude", "mira@studio.dev")].SubscribedAt = time.Date(2026, 5, 11, 9, 12, 0, 0, time.UTC)
	codex := mira.subscription("codex", "mira@studio.dev", "pro")
	must(codex.Answer.SetRenews("2026-08-27", began.Add(26*time.Hour)))
	grok := mira.subscription("grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "SuperGrok")
	must(grok.Answer.SetCadence(cost.Yearly, began.Add(26*time.Hour)))
	must(grok.Answer.SetRenews("2026-03-02", began.Add(26*time.Hour)))
	mira.subscription("claude", "leo@studio.dev", "max")
	mira.subscription("codex", "leo@studio.dev", "plus")
	bots := mira.subscription("codex", "bots@studio.dev", "pro")
	must(bots.Answer.SetPrice(new(238.0), "pro", began.Add(4*24*time.Hour)))
	jun := mira.subscription("claude", "jun@studio.dev", "pro")
	jun.LastSeen = time.Date(2026, 8, 21, 16, 5, 0, 0, time.UTC)
	jun.PlanConfirmedAt = jun.LastSeen
	ci := mira.subscription("codex", "ci@studio.dev", "plus")
	ci.LastSeen = time.Date(2026, 9, 13, 23, 50, 0, 0, time.UTC)
	ci.PlanConfirmedAt = ci.LastSeen
	ci.Answer.Cancel(time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC))
	mira.observe(docs, key)
}

// newCosts is an empty cost store that began at began.
func newCosts() *cost.Store {
	return &cost.Store{V: cost.StoreVersion, Since: began, Subs: map[string]*cost.Sub{}}
}

// subscription records in the device's cost store a subscription found
// at began on plan, which then changed to each of later, at its time.
func (d *device) subscription(provider, label, plan string, later ...cost.PlanAt) *cost.Sub {
	sub := d.cost.Sub(provider, label, began)
	sub.Plans = append([]cost.PlanAt{{At: began, Plan: plan}}, later...)
	return sub
}

// observe records in the device's cost store what its last run found, as
// a collection does: its own accounts, and those of the team's snapshots
// docs, opened with key.
func (d *device) observe(docs []snapshot.Doc, key *team.Key) {
	var seen []cost.Sighting
	for _, a := range d.st.Accounts {
		g := cost.Sighting{Provider: a.Provider, Label: a.Label, Plan: a.Plan, At: a.LastSeenAt, Current: collect.IsCurrent(d.st, a.Provider, a.Label)}
		if a.Quota != nil {
			g.QuotaAt = a.Quota.At
		}
		seen = append(seen, g)
	}
	for _, doc := range docs {
		for _, a := range doc.Accounts {
			label, err := key.Open(a.Label)
			if err != nil {
				panic(err)
			}
			if label == collect.UnknownAccount {
				continue
			}
			g := cost.Sighting{Provider: a.Provider, Label: label, Plan: a.Plan, At: doc.CollectedAt, Current: a.Current}
			if a.QuotaAt != nil && a.QuotaFrom == "" {
				g.QuotaAt = *a.QuotaAt
			}
			seen = append(seen, g)
		}
	}
	d.cost.Observe(d.id, seen, nil, cost.Builtin(), d.at)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// miraHistory is how full mira's subscriptions got before each reset over
// the quarter: her Claude week runs out most weeks, its 5 hours now and
// then, and her Codex and Grok have room.
func miraHistory(d *device) {
	d.history("claude", "mira@studio.dev", "7d", week, "100 94 ~71 100 88 100 82 97 100 76 91 100 85")
	d.history("claude", "mira@studio.dev", "5h", 7*time.Hour, "100 64 38 100 92 ~55 71 100 47 83 100 59 66 90")
	d.history("codex", "mira@studio.dev", "7d", week, "52 41 ~29 63 48 37 55 44 71 39 46 58 33")
	d.history("codex", "mira@studio.dev", "5h", 7*time.Hour, "34 12 27 8 41 19 22 15 30 9 26 18 11 24")
	d.history("grok", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64", "7d", week, "12 7 15 4 9 11 6 14 3 8 10 5 13")
}

// device is one made-up machine and the ledger its collector keeps. Its
// folders are the projects of the worktrees and subfolders its sessions
// ran in, which spread holds by harness and project. Its past is the
// samples of the quarter before that read its quota windows.
type device struct {
	rng            *rand.Rand
	id, host, user string
	home, version  string
	at             time.Time
	st             *state.State
	folders        collect.Folders
	spread         map[string][]string
	samples, past  []state.Sample
	brew           bool
	// cost is the device's cost store, nil for none.
	cost *cost.Store
}

func newDevice(rng *rand.Rand, id, host, user, home, version string, at time.Time, providers ...string) *device {
	st := state.NewState()
	st.LastRunAt, st.LastSuccessAt = at, at
	for _, p := range snapshot.Providers {
		st.Sources[p] = state.Source{Status: "skipped"}
	}
	for _, p := range providers {
		st.Sources[p] = state.Source{Status: "ok", Homes: []string{home + "/." + p}}
	}
	st.Relay.LastPushAt, st.Relay.LastPullAt = at, at
	st.Update.CheckedAt, st.Update.Latest = at.Add(-2*time.Hour), version
	st.Schedule.Registered = true
	return &device{rng: rng, id: id, host: host, user: user, home: home, version: version, at: at, st: st,
		folders: collect.Folders{}, spread: map[string][]string{}}
}

// worktrees spreads the days of provider's sessions in project over the
// project's folder and dirs, its worktrees and subfolders, in turn. The
// tokens stay as they were, and so does the project.
func (d *device) worktrees(provider, project string, dirs ...string) {
	d.spread[state.Key(provider, project)] = append([]string{project}, dirs...)
	for _, dir := range dirs {
		d.folders[dir] = collect.Folder{Path: dir, Project: project}
	}
}

// webWorktrees has mira work on orbit/web in a Claude Code worktree, a
// subfolder, and two Codex worktrees too.
func (d *device) webWorktrees() {
	const web = "/Users/mira/src/orbit/web"
	d.worktrees("claude", web, web+"/.claude/worktrees/checkout", web+"/packages/ui")
	d.worktrees("codex", web, "/Users/mira/.codex/worktrees/5e1c/web", "/Users/mira/.codex/worktrees/b07a/web")
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

// run is a reading of a window, ago before now.
type run struct {
	ago     time.Duration
	percent float64
}

// runs adds the device's runs that read window name of provider's account
// label before its last reading, oldest first: what the forecast's pace of
// the last day goes by.
func (d *device) runs(provider, label, name string, runs ...run) {
	a := d.st.Accounts[state.Key(provider, label)]
	i := slices.IndexFunc(a.Quota.Windows, func(w snapshot.Window) bool { return w.Name == name })
	for _, r := range runs {
		at := now.Add(-r.ago)
		w := a.Quota.Windows[i]
		w.Percent = r.percent
		d.samples = append(d.samples, state.Sample{At: at, Accounts: []state.SampleAccount{
			{Provider: provider, Label: label, QuotaAt: &at, Windows: []snapshot.Window{w}},
		}})
	}
}

// history adds the periods of window name of provider's account label
// before the one now to the device's past, newest first, one every as long
// before the next: how full each got, as UTILIZATION shows it. Each was
// last read a quarter hour before its reset, or, marked ~, as the laptop
// slept, halfway through.
func (d *device) history(provider, label, name string, every time.Duration, periods string) {
	a := d.st.Accounts[state.Key(provider, label)]
	i := slices.IndexFunc(a.Quota.Windows, func(w snapshot.Window) bool { return w.Name == name })
	w := a.Quota.Windows[i]
	for k, p := range strings.Fields(periods) {
		p, asleep := strings.CutPrefix(p, "~")
		pct, err := strconv.ParseFloat(p, 64)
		if err != nil {
			panic(err)
		}
		reset := w.ResetsAt.Add(-time.Duration(k+1) * every)
		at := reset.Add(-15 * time.Minute)
		if asleep {
			at = reset.Add(-w.Length() / 2)
		}
		was := w
		was.Percent, was.ResetsAt = pct, &reset
		d.past = append(d.past, state.Sample{At: at, Accounts: []state.SampleAccount{
			{Provider: provider, Label: label, QuotaAt: &at, Windows: []snapshot.Window{was}},
		}})
	}
}

// loggedOut adds a home of provider's on the device that nobody is logged
// in to, as the harness answers there.
func (d *device) loggedOut(provider, home string) {
	src := d.st.Sources[provider]
	src.Status, src.Homes = "partial", append(src.Homes, home)
	src.Error = strings.Replace(home, d.home, "~", 1) + ": " + provider + ": not logged in"
	d.st.Sources[provider] = src
}

// fail makes a harness fail on the device.
func (d *device) fail(provider, msg string) {
	src := d.st.Sources[provider]
	src.Status, src.Error = "error", msg
	d.st.Sources[provider] = src
}

// mixes is how each harness's tokens split by kind, in proportion. Claude
// Code sends little fresh input, reads about 25 times its output back from
// cache, and writes 1.5 times its output to cache, three fifths of it kept an
// hour. Codex reads about 30 times its fresh input from cache and counts no
// cache writes. Grok reads less from cache. Hermes bills to Codex here.
var mixes = map[string]meter.Use{
	"claude": {Input: 3, Output: 120, CacheRead: 3000, CacheWrite: 180, CacheWrite1h: 108},
	"codex":  {Input: 30, Output: 10, CacheRead: 900},
	"grok":   {Input: 60, Output: 15, CacheRead: 600},
	"hermes": {Input: 30, Output: 10, CacheRead: 900},
}

// use is an hour of provider's tokens, billed to label, that burns about n
// weighted tokens.
func use(provider, label string, n int64) meter.Use {
	mix := mixes[provider]
	k := float64(n) / float64(mix.Burn(meter.WeightsOf(provider, label)))
	part := func(m int64) int64 { return int64(float64(m) * k) }
	return meter.Use{Input: part(mix.Input), Output: part(mix.Output), CacheRead: part(mix.CacheRead),
		CacheWrite: part(mix.CacheWrite), CacheWrite1h: part(mix.CacheWrite1h)}
}

// work burns about perDay million weighted tokens a day on project over the
// last days days, up to the device's last run: in working hours, less at
// weekends, with a day off now and then, one session a day.
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
		folder := project
		if all := d.spread[state.Key(provider, project)]; len(all) > 0 {
			folder = all[i%len(all)]
		}
		s := &state.Session{Provider: provider, Project: folder}
		for h := 7; h < 19; h++ {
			at := day.Add(time.Duration(h)*time.Hour + 20*time.Minute)
			if at.After(until) {
				break
			}
			n := int64(perDay * 1e6 / 12 * weight * (0.3 + 1.4*d.rng.Float64()))
			s.U.Add(at, use(provider, label, n))
			s.Updated = at
		}
		if len(s.U) == 0 {
			continue
		}
		tok := s.U.Sum().Tokens()
		s.Seen = tok
		s.By = map[string]snapshot.Tokens{label: tok}
		if via != nil {
			s.Via = map[string]snapshot.Tokens{state.Key(via.Provider, via.Label): tok}
		}
		d.st.Sessions[state.Key(provider, label, folder, day.Format(time.DateOnly))] = s
	}
}

// cycles are the quota cycles the device's samples hold: its past, its
// runs, and its last run's readings.
func (d *device) cycles() collect.Cycles {
	last := state.Sample{At: d.at}
	for _, a := range d.st.Accounts {
		if a.Quota != nil {
			last.Accounts = append(last.Accounts, state.SampleAccount{Provider: a.Provider, Label: a.Label, QuotaAt: &a.Quota.At, Windows: a.Quota.Windows})
		}
	}
	return collect.FindCycles(slices.Concat(d.past, d.samples, []state.Sample{last}))
}

// doc is the device's snapshot as another device pulls it.
func (d *device) doc(key *team.Key) snapshot.Doc {
	body, err := json.Marshal(collect.BuildDoc(d.st, d.cycles(), key, state.Config{Device: d.id}, d.host, d.user, d.version, d.at))
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
		Doc:      collect.BuildDoc(d.st, d.cycles(), key, cfg, d.host, d.user, d.version, d.at),
		Hostname: d.host, OSUser: d.user, Now: now, Folders: d.folders, Samples: d.samples, Loc: berlin,
		Cost: d.cost,
	}
	if d.brew {
		in.Homebrew, in.BrewCommand = true, fsutil.HomebrewUpgrade
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
