// Package collect runs one sample: read local logs, ask each harness who is
// logged in and how full its windows are, attribute new tokens to that
// account, store the sample, and publish this device to the team relay.
package collect

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

// UnknownAccount labels usage the harness did not attribute to anyone.
const UnknownAccount = logs.UnknownAccount

// Bounds of a believable reset time: none before 2000, and none further
// ahead than the longest window plus a day of clock skew.
var minResetsAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

const maxResetsAhead = snapshot.MaxWindowMinutes*time.Minute + 24*time.Hour

// Options are the inputs of one run. Zero values use the real environment.
type Options struct {
	Dir      state.Dir
	Version  string
	Probe    probe.Env
	Getenv   func(string) string
	UserHome string
	Hostname string
	OSUser   string
	Now      func() time.Time
	// Relay is nil when no relay is configured or the run is offline.
	Relay *relay.Client
	// PullEvery, when set, skips the team read while the cached one is
	// younger. Every read returns the whole team, so a team's reads grow
	// with the square of its size; a scheduled run has no one to show the
	// team to, and a report is fine with a read from the last hour.
	PullEvery time.Duration
	// Readers and prober are replaced in tests. ReadLogs reads all homes of
	// one provider together and tags each session with its home.
	ReadLogs func(provider string, homes []string, since time.Time) logs.Result
	Ask      func(ctx context.Context, provider, home string) (probe.Reading, error)
	// After runs under the run lock before the state is saved. The command
	// layer uses it for scheduler registration and self-update bookkeeping.
	After func(ctx context.Context, cfg *state.Config, st *state.State)
	// Wait is how long a run waits for another, such as the scheduled one,
	// to finish; zero fails at once with state.ErrBusy. Waiting is called
	// when the wait starts.
	Wait    time.Duration
	Waiting func()

	// quotaFrom is Config.QuotaFrom keyed by resolved home, set by Run.
	quotaFrom map[string]map[string]string
	paths     paths
}

// Result is what a run leaves behind for the views.
type Result struct {
	Config state.Config
	State  *state.State
	Key    *team.Key
	Doc    snapshot.Doc
	Team   TeamCache
	// Waited is set when the result is the one the run waited for.
	Waited bool
}

func (o *Options) fill() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.UserHome == "" {
		o.UserHome, _ = os.UserHomeDir()
	}
	if o.Probe.HomeDir == "" {
		o.Probe.HomeDir = o.UserHome
	}
	if o.ReadLogs == nil {
		o.ReadLogs = logs.ReadHomes
	}
}

// askHarness asks the installed harnesses. homeEnv is the remembered
// variable values of the config, by provider and home.
func askHarness(env probe.Env, homeEnv map[string]map[string]string) func(ctx context.Context, provider, home string) (probe.Reading, error) {
	return func(ctx context.Context, provider, home string) (probe.Reading, error) {
		switch provider {
		case "claude":
			return probe.Claude(ctx, claudeEnv(env, home, homeEnv["claude"][home]), home)
		case "codex":
			return probe.Codex(ctx, env, home)
		case "grok":
			return probe.Grok(home)
		default:
			return probe.Reading{}, nil
		}
	}
}

// claudeEnv is env for probing the Claude home at home. Claude Code keeps its
// config inside a home named by CLAUDE_CONFIG_DIR, even the default ~/.claude,
// and names its login after the exact string. So a home once seen through the
// variable is probed with the exact string remembered for it whenever this
// run's environment does not name it, as under the system scheduler.
func claudeEnv(env probe.Env, home, remembered string) probe.Env {
	if remembered == "" {
		return env
	}
	if v := env.Getenv("CLAUDE_CONFIG_DIR"); v != "" && samePath(v, home) {
		return env
	}
	return env.WithEnv("CLAUDE_CONFIG_DIR", remembered)
}

// LoadKey reads the team key, generating one on the first run.
func LoadKey(dir state.Dir) (*team.Key, bool, error) {
	k, err := team.Load(dir.KeyFile())
	if err == nil {
		return k, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, fmt.Errorf("team key: %w", err)
	}
	k, err = team.Generate()
	if err != nil {
		return nil, false, err
	}
	if err := fsutil.WriteFile(dir.KeyFile(), []byte(k.Export()+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// Run takes one sample. It returns an error only when the collector's own
// state cannot be read or written, or when ctx ends before the sample is
// written; source and relay failures are recorded in the state and do not
// stop the run.
func Run(ctx context.Context, o Options) (*Result, error) {
	o.fill()
	unlock, err := o.Dir.Lock()
	waited := errors.Is(err, state.ErrBusy) && o.Wait > 0
	var lastRun time.Time
	if waited {
		if st, err := o.Dir.LoadState(); err == nil {
			lastRun = st.LastRunAt
		}
		unlock, err = o.Dir.LockWait(ctx, o.Wait, o.Waiting)
	}
	if err != nil {
		return nil, err
	}
	defer unlock()

	cfg, err := o.Dir.LoadConfig()
	if err != nil {
		return nil, err
	}
	key, _, err := LoadKey(o.Dir)
	if err != nil {
		return nil, err
	}
	st, err := o.Dir.LoadState()
	if err != nil {
		return nil, err
	}
	now := o.Now().UTC().Truncate(time.Second)
	since := now.Add(-state.Retention)
	prevRun := st.LastRunAt

	homes := Discover(o.UserHome, o.Getenv, cfg.Homes)
	// Only the folders this run found beyond the remembered ones are
	// recorded, into the config as it is now, so a folder a person added or
	// removed since it was read stays that way.
	found := unremembered(homes, o.UserHome, cfg.Homes)
	remember := func(c *state.Config) bool {
		remembered, changed := Remember(c.Homes, o.UserHome, found)
		c.Homes = remembered
		if homeEnv, envChanged := RememberEnv(c.HomeEnv, o.Getenv, homes); envChanged {
			c.HomeEnv, changed = homeEnv, true
		}
		return changed
	}
	if remember(&cfg) {
		cfg, err = o.Dir.EditConfig(func(c *state.Config) error {
			remember(c)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	inputs := runInputs(o, cfg, homes)
	if waited && st.LastRunAt.After(lastRun) && st.LastRunInputs == inputs {
		// The run waited for has just collected what this one would: the
		// same release, relay, and homes. A scheduled run may have skipped
		// the team read this one would make.
		cache, _ := LoadTeamCache(o.Dir)
		read := !cache.PulledAt.Before(st.LastRunAt) || (o.PullEvery > 0 && st.LastRunAt.Sub(cache.PulledAt) < o.PullEvery)
		if o.Relay == nil || read {
			doc := BuildDoc(st, key, cfg, o.Hostname, o.OSUser, o.Version, st.LastRunAt)
			return &Result{Config: cfg, State: st, Key: key, Doc: doc, Team: cache, Waited: true}, nil
		}
	}
	if o.Ask == nil {
		o.Ask = askHarness(o.Probe, cfg.HomeEnv)
	}
	o.paths = paths{}
	o.quotaFrom = quotaLinks(cfg.QuotaFrom, o.paths)

	sample := state.Sample{At: now}
	growth := map[string]snapshot.Tokens{}
	var problems []string
	if st.Damage != "" {
		problems = append(problems, st.Damage)
	}
	failed := false
	for _, p := range snapshot.Providers {
		src := collectSource(ctx, o, st, p, homes[p], now, since, prevRun, growth)
		st.Sources[p] = src
		if src.Error != "" {
			problems = append(problems, p+": "+src.Error)
		}
		if src.Status == "error" {
			failed = true
		}
	}
	prune(st, now)

	for k, acct := range st.Accounts {
		g := growth[k]
		quotaNow := acct.Quota != nil && !acct.Quota.At.Before(now.Add(-24*time.Hour))
		if g.Zero() && !quotaNow {
			continue
		}
		sa := state.SampleAccount{Provider: acct.Provider, Label: acct.Label, Growth: g}
		if acct.Quota != nil {
			at := acct.Quota.At
			sa.QuotaAt = &at
			sa.Windows = acct.Quota.Windows
		}
		sample.Accounts = append(sample.Accounts, sa)
	}
	sort.Slice(sample.Accounts, func(i, j int) bool {
		a, b := sample.Accounts[i], sample.Accounts[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Label < b.Label
	})
	// A run stopped before it writes, as when the view that started it
	// closes, saves nothing: what failed in it failed because it stopped,
	// and the next run collects what it would have.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := o.Dir.AppendSample(sample); err != nil {
		problems = append(problems, "samples: "+err.Error())
	}
	if err := o.Dir.PruneSamples(now); err != nil {
		problems = append(problems, "samples: "+err.Error())
	}

	// A run succeeds when every installed source could be read. A run where
	// nothing is installed has nothing to fail and still counts.
	st.LastRunAt = now
	st.LastRunInputs = inputs
	if !failed {
		st.LastSuccessAt = now
	}
	noteProblems := func() {
		if len(problems) > 0 {
			st.LastError = snapshot.Truncate(strings.Join(problems, "; "), 600)
			st.LastErrorAt = now
		}
	}
	// Record this run's problems before the snapshot is built, so the team
	// sees them now rather than one run late.
	noteProblems()
	res := &Result{Config: cfg, State: st, Key: key}
	res.Doc = BuildDoc(st, key, cfg, o.Hostname, o.OSUser, o.Version, now)

	cache, _ := LoadTeamCache(o.Dir)
	res.Team = cache
	if o.Relay != nil {
		// Publish with the key this run sealed and signed the snapshot for.
		client := *o.Relay
		client.Key = key
		o.Relay = &client
		last := st.Relay
		syncTeam(ctx, o, st, cfg.Device, &res.Doc, &res.Team, now)
		if ctx.Err() != nil && st.Relay.LastErrorAt.Equal(now) && !st.Relay.LastPullAt.Equal(now) {
			// A stop that cuts the exchange short before its read is no
			// failure of the relay's. A snapshot it did not push stays
			// pending, with the last run's error; once the snapshot is
			// pushed, the cached read's error stands, as when a run skips
			// the read.
			st.Relay.LastError, st.Relay.LastErrorAt = last.LastError, last.LastErrorAt
			if st.Relay.LastPushAt.Equal(now) {
				st.Relay.LastError = res.Team.ReadError
				if res.Team.ReadError != "" {
					st.Relay.LastErrorAt = res.Team.PulledAt
				}
			}
		}
		if st.Relay.LastError != "" && st.Relay.LastErrorAt.Equal(now) {
			problems = append(problems, st.Relay.LastError)
			noteProblems()
		}
	}
	// A stopped run leaves its housekeeping to the next one.
	if o.After != nil && ctx.Err() == nil {
		o.After(ctx, &res.Config, st)
	}
	if err := o.Dir.SaveState(st); err != nil {
		return res, err
	}
	return res, nil
}

// runInputs fingerprints what a run collects with: the release, the relay it
// syncs with, the homes it reads, and how it reads them.
func runInputs(o Options, cfg state.Config, homes map[string][]string) string {
	in := struct {
		Version   string
		Relay     string
		Homes     map[string][]string
		HomeEnv   map[string]map[string]string
		QuotaFrom map[string]map[string]string
	}{o.Version, "", homes, cfg.HomeEnv, cfg.QuotaFrom}
	if o.Relay != nil {
		in.Relay = o.Relay.BaseURL
	}
	b, _ := json.Marshal(in)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// sharedClaudeLogs is the Claude homes whose projects folder is another
// home's too, through a symlink.
func sharedClaudeLogs(p string, homes []string) map[string]bool {
	shared := map[string]bool{}
	if p != "claude" {
		return shared
	}
	by := map[string][]string{}
	for _, h := range homes {
		if dir, err := filepath.EvalSymlinks(filepath.Join(h, "projects")); err == nil {
			by[dir] = append(by[dir], h)
		}
	}
	for _, hs := range by {
		if len(hs) > 1 {
			for _, h := range hs {
				shared[h] = true
			}
		}
	}
	return shared
}

// readSession is one session as read this run, with the account its growth
// belongs to.
type readSession struct {
	s     logs.Session
	label string
}

// collectSource collects one provider. A bug that panics while reading or
// probing it becomes that source's error, and the other sources still run.
func collectSource(ctx context.Context, o Options, st *state.State, p string, homes []string, now, since, prevRun time.Time, growth map[string]snapshot.Tokens) (src state.Source) {
	defer func() {
		if v := recover(); v != nil {
			src = state.Source{Status: "error", Homes: homes, Error: snapshot.Truncate(fmt.Sprintf("stopped by a bug: %v", v), 300)}
		}
	}()
	return collectProvider(ctx, o, st, p, homes, now, since, prevRun, growth)
}

func collectProvider(ctx context.Context, o Options, st *state.State, p string, homes []string, now, since, prevRun time.Time, growth map[string]snapshot.Tokens) state.Source {
	if len(homes) == 0 && !o.Probe.Find(p) {
		keepCurrent(st, p, nil)
		return state.Source{Status: "skipped"}
	}
	src := state.Source{Homes: homes}
	var errs []string
	readOK := 0
	// All homes are read together, so a session kept in two homes, or a
	// sub-agent or fork in one home of a session in another, counts once.
	var res logs.Result
	if len(homes) > 0 {
		res = o.ReadLogs(p, homes, since)
	}
	// The Claude app's session homes have no login to ask about: the app
	// recorded the account each session ran under.
	apps := map[string]claudeAppSession{}
	for _, h := range homes {
		if rec := claudeAppRecord(p, h); rec != "" {
			apps[h] = readClaudeAppSession(rec)
		}
	}
	// Which homes have usage, and when each was last used, as the logs
	// show.
	used := map[string]bool{}
	lastUse := map[string]time.Time{}
	for _, s := range res.Sessions {
		for _, h := range append([]string{s.Home}, s.Homes...) {
			used[h] = true
			if s.Updated.After(lastUse[h]) {
				lastUse[h] = s.Updated
			}
		}
	}
	// Claude homes that share their logs go unused: the logs give their
	// sessions to one of them, and cannot say which login ran them.
	for h := range sharedClaudeLogs(p, homes) {
		delete(lastUse, h)
	}
	labels := map[string]string{}
	var answers []answer
	if p != "hermes" {
		answers = askAll(ctx, func(ctx context.Context, p, home string) (probe.Reading, error) {
			if _, ok := apps[home]; ok {
				return probe.Reading{}, nil
			}
			return o.Ask(probe.WithLastUse(ctx, lastUse[home]), p, home)
		}, p, homes)
	}
	// partial means some home's read was incomplete, so a lower count than
	// last time is a missing file rather than a real drop.
	partial := false
	var probeErrs [][2]string
	for i, home := range homes {
		hr := res.Homes[home]
		if hr.Err != nil {
			errs = append(errs, "read "+home+": "+shortErr(hr.Err))
			partial = true
		} else {
			readOK++
		}
		if hr.Malformed > 0 {
			errs = append(errs, fmt.Sprintf("%d malformed lines", hr.Malformed))
		}
		if hr.Unreadable > 0 {
			errs = append(errs, fmt.Sprintf("%d unreadable files", hr.Unreadable))
			partial = true
		}
		if app, ok := apps[home]; ok {
			labels[home] = touchAccount(st, p, app.account()).Label
		} else if p != "hermes" {
			a := answers[i]
			// An app's per-account home with nobody logged in is an account
			// removed or not added yet, and a home with no usage is a tool
			// installed but never used, as in a bot's image, which Claude
			// Code makes its home for as soon as it is asked who is logged
			// in. Neither is a problem.
			if a.err != nil && !(isLoggedOut(a.err) && (isManaged(p, home) || !used[home])) {
				probeErrs = append(probeErrs, [2]string{home, shortErr(a.err)})
			}
			labels[home] = applyReading(st, p, home, a.reading, isLoggedOut(a.err), hr.Limits, now, prevRun)
		}
	}
	errs = append(errs, homeErrors(probeErrs, len(homes), o.UserHome)...)
	if p != "hermes" {
		claimUnknown(st, p, homes, answers, res, labels)
	}
	var read []readSession
	for _, s := range res.Sessions {
		label, ok := labels[s.Home]
		if l := servedBy(st, p, s, labels); l != "" {
			label, ok = l, true
		}
		if p == "hermes" {
			label = s.Account
		} else if !ok {
			// A session from a home that was not probed has no account.
			label = UnknownAccount
		}
		if label == "" {
			label = UnknownAccount
		}
		if app, ok := apps[s.Home]; ok {
			s.Project = app.project()
		}
		read = append(read, readSession{s: s, label: label})
	}
	for _, r := range read {
		if p == "hermes" {
			touchAccount(st, p, r.label)
		}
		grown := attribute(st, p, r.s, r.label, partial, now, growth)
		if p == "hermes" {
			for l := range grown {
				touchAccount(st, p, l)
			}
			linkGrowth(st, o, r.s, grown)
		}
	}
	applyRejected(st, p, read, now, prevRun)
	probed := homes
	if p == "hermes" {
		markHermesCurrent(st, read)
		linkHermes(st, o, read, partial)
		probed = nil
		if len(homes) > 0 {
			probed = []string{""}
		}
	}
	// An installed tool without its data directory has never been used, and
	// nobody is logged in to it: logging in makes the directory. It is not
	// asked, since asking would start it and it would make the directory
	// itself, in the home of someone who may only have the app that bundles it.
	keepCurrent(st, p, probed)
	switch {
	case len(errs) == 0:
		src.Status = "ok"
	case readOK == 0 && len(homes) > 0:
		src.Status = "error"
	default:
		src.Status = "partial"
	}
	src.Error = snapshot.Truncate(strings.Join(dedupe(errs), "; "), 300)
	return src
}

// answer is one home's reply to a probe.
type answer struct {
	reading probe.Reading
	err     error
	// panicked is what the probe panicked with, if it did.
	panicked any
}

// askAll asks about every home of p at once, so that a harness that does not
// answer in one home costs its timeout once, not once per home. A probe that
// panics panics here, where the source's own recovery handles it.
func askAll(ctx context.Context, ask func(context.Context, string, string) (probe.Reading, error), p string, homes []string) []answer {
	out := make([]answer, len(homes))
	var wg sync.WaitGroup
	for i, home := range homes {
		wg.Go(func() {
			defer func() { out[i].panicked = recover() }()
			out[i].reading, out[i].err = ask(ctx, p, home)
		})
	}
	wg.Wait()
	for _, a := range out {
		if a.panicked != nil {
			panic(a.panicked)
		}
	}
	return out
}

// isLoggedOut reports whether a probe error is the harness answering that
// nobody is logged in, rather than not answering at all.
func isLoggedOut(err error) bool {
	var lo interface{ LoggedOut() bool }
	return errors.As(err, &lo) && lo.LoggedOut()
}

// applyReading records who is logged in at home, when that changed, and
// their quota, and returns the label new tokens from that home belong to.
func applyReading(st *state.State, p, home string, r probe.Reading, loggedOut bool, logLimits *logs.Limits, now, prevRun time.Time) string {
	cur := state.Key(p, home)
	prev := st.Current[cur]
	label := strings.TrimSpace(r.Account)
	answered := label != ""
	switch {
	case answered:
		st.Current[cur] = label
	case loggedOut:
		// The previous account keeps its tokens and quota, but it is no
		// longer logged in, and what still grows here is not its.
		delete(st.Current, cur)
		label = UnknownAccount
	case prev != "":
		// No answer: whoever was logged in still is, as far as anyone knows.
		label = prev
	default:
		label = UnknownAccount
	}
	if prev != "" && label != prev {
		if st.Switched == nil {
			st.Switched = map[string]time.Time{}
		}
		st.Switched[cur] = now
	}
	acct := touchAccount(st, p, label)
	if answered {
		acct.LastSeenAt = now
		if r.Plan != "" {
			acct.Plan = snapshot.PlainLabel(r.Plan)
		}
	}
	q := r.Quota
	// The logs keep the last limits the harness saw, whoever was logged in
	// then. A reading older than the previous run belongs to whichever
	// account was current at that run, and that run already recorded it, so
	// only a newer one is credited here. A home with no earlier account has
	// no one else to credit, like the first run's tokens.
	if q == nil && logLimits != nil && label != UnknownAccount && (prev == "" || !logLimits.ObservedAt.Before(prevRun)) {
		q = &probe.Quota{At: logLimits.ObservedAt, Source: "log", Windows: logLimits.Windows}
		if acct.Plan == "" && logLimits.Plan != "" {
			acct.Plan = snapshot.PlainLabel(logLimits.Plan)
		}
	}
	setQuota(acct, q, now)
	return label
}

// RejectionSource is the source of a reading taken from a refused request.
const RejectionSource = "rejection"

// setQuota makes q the account's reading unless the account has a newer one.
func setQuota(acct *state.Account, q *probe.Quota, now time.Time) {
	if q == nil || len(q.Windows) == 0 {
		return
	}
	// A reading stamped ahead of the clock is from now at the latest. One
	// older than the retention window is not kept, whatever produced it.
	at := q.At.UTC()
	if at.After(now) {
		at = now
	}
	if at.Before(now.Add(-state.Retention)) {
		return
	}
	// A stored reading from a clock that has since gone back would otherwise
	// block every real reading until the clock caught up. One from refused
	// requests holds only until its windows reset; after that it would hide
	// an older reading of the windows it says nothing about. Each run gathers
	// every refusal still in force, so a newer gathering replaces it whole.
	old := acct.Quota
	refused := old != nil && old.Source == RejectionSource && (q.Source == RejectionSource || allReset(old.Windows, now))
	if old == nil || old.At.After(now) || !at.Before(old.At) || refused {
		acct.Quota = &state.Quota{At: at, Source: q.Source, Windows: clampWindows(q.Windows, now)}
	}
}

// applyRejected gives each account the requests refused for a full window,
// among the sessions attributed to it, whose window has not reset: the newest
// of each window, as one reading as of the newest. A refusal says that window
// was at 100% then and stays so until it resets, and nothing about the other
// windows, so the reading is those windows alone: carrying the older
// reading's windows along would make them look newer than they are. It
// counts when it is newer than the account's reading. A refusal since the previous run goes where this run
// puts the session's growth. One from before it belongs to whoever used the
// session then, and the ledger keeps only each account's share, so it counts
// only when the whole session is this account's, as a session read for the
// first time is. Neither counts when it is older than the run that found the
// home's login changed: a full window is the usual reason to switch, so it
// is as likely the earlier account's, even in a session the ledger gives
// wholly to the new one.
func applyRejected(st *state.State, p string, read []readSession, now, prevRun time.Time) {
	held := map[string][]*logs.Limits{}
	for _, r := range read {
		if r.label == UnknownAccount {
			continue
		}
		switched := st.Switched[state.Key(p, r.s.Home)]
		for _, x := range r.s.Rejected {
			if allReset(x.Windows, now) || x.ObservedAt.Before(switched) {
				continue
			}
			if x.ObservedAt.Before(prevRun) && !soleAccount(st.Sessions[state.Key(p, r.s.ID)], r.label) {
				continue
			}
			held[r.label] = logs.AddRejected(held[r.label], x)
		}
	}
	for label, xs := range held {
		q := &probe.Quota{Source: RejectionSource}
		for _, x := range xs {
			if x.ObservedAt.After(q.At) {
				q.At = x.ObservedAt
			}
			q.Windows = append(q.Windows, x.Windows...)
		}
		slices.SortFunc(q.Windows, func(a, b snapshot.Window) int {
			return cmp.Or(cmp.Compare(a.Minutes, b.Minutes), strings.Compare(a.Name, b.Name))
		})
		setQuota(touchAccount(st, p, label), q, now)
	}
}

// soleAccount reports whether the ledger gives all of session e to label.
func soleAccount(e *state.Session, label string) bool {
	if e == nil || len(e.By) != 1 {
		return false
	}
	_, ok := e.By[label]
	return ok
}

// allReset reports whether every window has passed its reset time.
func allReset(ws []snapshot.Window, now time.Time) bool {
	for _, w := range ws {
		if w.ResetsAt == nil || w.ResetsAt.After(now) {
			return false
		}
	}
	return true
}

// clampWindows keeps windows within what the state and the snapshot can hold.
func clampWindows(ws []snapshot.Window, now time.Time) []snapshot.Window {
	out := make([]snapshot.Window, 0, len(ws))
	for _, w := range ws {
		if len(out) == snapshot.MaxWindows {
			break
		}
		w.Name = snapshot.PlainLabel(w.Name)
		if w.Name == "" {
			w.Name = "window"
		}
		// NaN would make the state and the snapshot unencodable.
		if !(w.Percent >= 0) {
			w.Percent = 0
		}
		if w.Percent > 1000 {
			w.Percent = 1000
		}
		if w.Minutes < 0 || w.Minutes > snapshot.MaxWindowMinutes {
			w.Minutes = 0
		}
		// No window resets further out than its length, and a time JSON
		// cannot hold would stop the state from saving. Such a value is a
		// unit mix-up upstream: no reset time rather than a wrong one.
		if w.ResetsAt != nil && (w.ResetsAt.Before(minResetsAt) || w.ResetsAt.After(now.Add(maxResetsAhead))) {
			w.ResetsAt = nil
		}
		out = append(out, w)
	}
	return out
}

// homeErrors are the probe errors of a provider's homes, each after the home
// it came from, unless every home had the same one.
func homeErrors(errs [][2]string, homes int, userHome string) []string {
	var out []string
	same := len(errs) == homes
	for _, e := range errs {
		same = same && e[1] == errs[0][1]
	}
	for _, e := range errs {
		if same || homes == 1 {
			out = append(out, e[1])
			continue
		}
		out = append(out, fsutil.Tilde(e[0], userHome)+": "+e[1])
	}
	return out
}

// claimUnknown gives the account a home first names the usage counted there
// while that home had never answered, as the first run gives an account the
// history before it. Until then, as when an old binary cannot answer, that
// usage is unknown rather than someone else's. A home that said nobody is
// logged in has answered: what grew there then is no one's, and stays so.
// Each session read this run is claimed by the home it was read from, or,
// for a file several homes hold, by the account servedBy names when a home
// logged in to it claims this run. A home whose logs were not all read this
// run claims at a later one, since the sessions it missed would stay unknown.
// labels are this run's accounts by home.
func claimUnknown(st *state.State, p string, homes []string, answers []answer, res logs.Result, labels map[string]string) {
	claim := map[string]string{}
	for i, home := range homes {
		label := strings.TrimSpace(answers[i].reading.Account)
		if label == "" && !isLoggedOut(answers[i].err) {
			continue
		}
		k := state.Key(p, home)
		if st.Answered == nil {
			st.Answered = map[string]bool{}
		}
		if st.Answered[k] {
			continue
		}
		if label != "" && label != UnknownAccount {
			if hr := res.Homes[home]; hr.Err != nil || hr.Unreadable > 0 {
				continue // It claims at a run that reads them all.
			}
			claim[home] = label
		}
		st.Answered[k] = true
	}
	if len(claim) == 0 {
		return
	}
	for _, rs := range res.Sessions {
		label, ok := claim[rs.Home]
		if l := servedBy(st, p, rs, labels); l != "" {
			label, ok = l, slices.ContainsFunc(rs.Homes, func(h string) bool { return claim[h] == l })
		}
		if !ok {
			continue
		}
		s := st.Sessions[state.Key(p, rs.ID)]
		if s == nil {
			continue
		}
		t, ok := s.By[UnknownAccount]
		if !ok {
			continue
		}
		s.By[label] = s.By[label].Add(t)
		delete(s.By, UnknownAccount)
		if h, ok := s.ByHours[UnknownAccount]; ok {
			s.ByHours[label] = addHours(s.ByHours[label], h)
			delete(s.ByHours, UnknownAccount)
		}
		if last, ok := s.Last[UnknownAccount]; ok {
			if last.After(s.Last[label]) {
				s.Last[label] = last
			}
			delete(s.Last, UnknownAccount)
		}
	}
}

func touchAccount(st *state.State, p, label string) *state.Account {
	k := state.Key(p, label)
	acct := st.Accounts[k]
	if acct == nil {
		acct = &state.Account{Provider: p, Label: label}
		st.Accounts[k] = acct
	}
	return acct
}

// attribute moves a session's growth since the last run onto label, or onto
// each part's account when the log splits the session by account. It returns
// the growth by account.
func attribute(st *state.State, p string, s logs.Session, label string, partial bool, now time.Time, growth map[string]snapshot.Tokens) map[string]snapshot.Tokens {
	k := state.Key(p, s.ID)
	e := st.Sessions[k]
	if e == nil {
		e = &state.Session{Provider: p, By: map[string]snapshot.Tokens{}}
		st.Sessions[k] = e
	}
	if e.By == nil {
		e.By = map[string]snapshot.Tokens{}
	}
	grown := map[string]snapshot.Tokens{}
	if s.Parts == nil {
		g := s.Tokens.Growth(e.Seen)
		e.Seen = seenNow(e.Seen, s.Tokens, g, partial)
		grown[label] = g
	} else {
		parts := map[string]snapshot.Tokens{}
		for acct, t := range s.Parts {
			// A part that names no account, as a row from before Hermes
			// recorded billing, is the session's.
			l := strings.TrimSpace(acct)
			if l == "" {
				l = label
			}
			parts[l] = parts[l].Add(t)
		}
		if e.Parts == nil {
			e.Parts = map[string]snapshot.Tokens{}
		}
		for l, t := range parts {
			g := t.Growth(e.Parts[l])
			e.Parts[l] = seenNow(e.Parts[l], t, g, partial)
			grown[l] = g
		}
		e.Seen = snapshot.Tokens{}
		for _, t := range e.Parts {
			e.Seen = e.Seen.Add(t)
		}
	}
	e.Project = s.Project
	updated := s.Updated
	if updated.IsZero() || updated.After(now) {
		updated = now
	}
	for l, g := range grown {
		if g.Zero() {
			delete(grown, l)
		}
	}
	placeHours(e, s, grown, updated, now)
	if updated.After(e.Updated) {
		e.Updated = updated.UTC()
	}
	for l, g := range grown {
		e.By[l] = e.By[l].Add(g)
		if e.Last == nil {
			e.Last = map[string]time.Time{}
		}
		if updated.After(e.Last[l]) {
			e.Last[l] = updated.UTC()
		}
		ak := state.Key(p, l)
		growth[ak] = growth[ak].Add(g)
	}
	return grown
}

// placeHours adds the hours a session's growth this run went to, before the
// ledger adds the growth: to the session's hours and, once more than one
// account spends in it, to the hours of the account each part grew for. A
// log that records times has it in the hours where the log shows more than
// the ledger placed, in proportion. Taking the log's hours as they are would
// count some tokens twice: a partial read misses some, and the tokens a log
// records no time for are spread anew as the session grows. A log without
// times has it at the session's last activity, which is within the last
// run's interval. So a session's hours, and each account's, add up to their
// input plus output.
func placeHours(e *state.Session, s logs.Session, grown map[string]snapshot.Tokens, updated, now time.Time) {
	var had, spent int64
	for _, t := range e.By {
		had += logs.InOut(t)
	}
	for _, g := range grown {
		spent += logs.InOut(g)
	}
	if len(e.Hours) == 0 && len(s.Hours) > 0 {
		// A session read for the first time, or kept from before the ledger
		// recorded hours: the log's times cover its whole history, which its
		// accounts share as they share its tokens.
		e.Hours = maps.Clone(s.Hours)
		logs.ScaleHours(e.Hours, had+spent)
		e.ByHours = nil
		return
	}
	if spent <= 0 {
		return
	}
	many := spenders(e, grown) > 1
	if e.ByHours == nil && (many || len(e.Hours) == 0) {
		// Each account's hours start where the report put them. A session
		// kept from before the ledger recorded hours, whose log records no
		// times, has each account's share at the hour the share last grew.
		by := accountHours(e)
		e.Hours = nil
		for _, h := range by {
			e.Hours = addHours(e.Hours, h)
		}
		if many {
			e.ByHours = by
		}
	}
	add := grownHours(e.Hours, s.Hours, spent, updated, now)
	for l, g := range grown {
		n := logs.InOut(g)
		if n <= 0 {
			continue
		}
		part := maps.Clone(add)
		logs.ScaleHours(part, n)
		e.Hours = addHours(e.Hours, part)
		if e.ByHours != nil {
			e.ByHours[l] = addHours(e.ByHours[l], part)
		}
	}
}

// spenders is how many accounts spent input or output tokens in a session,
// with this run's growth.
func spenders(e *state.Session, grown map[string]snapshot.Tokens) int {
	n := 0
	for l, t := range e.By {
		if logs.InOut(t)+logs.InOut(grown[l]) > 0 {
			n++
		}
	}
	for l, g := range grown {
		if _, ok := e.By[l]; !ok && logs.InOut(g) > 0 {
			n++
		}
	}
	return n
}

// grownHours is where a session's growth of spent tokens went: the hours in
// which its log shows more than the ledger placed, in proportion, leaving out
// those the ledger no longer keeps. A log without times, or one that shows no
// such hour, has it all at updated.
func grownHours(placed, log map[int64]int64, spent int64, updated, now time.Time) map[int64]int64 {
	cutoff := now.Add(-state.Retention)
	out := map[int64]int64{}
	for h, n := range log {
		if d := n - placed[h]; d > 0 && hourKept(h, cutoff) {
			out[h] = d
		}
	}
	if len(out) == 0 {
		return map[int64]int64{logs.HourOf(updated): spent}
	}
	logs.ScaleHours(out, spent)
	return out
}

// hourKept reports whether the ledger keeps hour h, which it does while the
// hour ended at cutoff or later.
func hourKept(h int64, cutoff time.Time) bool {
	return !logs.HourStart(h + 1).Before(cutoff)
}

// dropHours drops the hours the ledger no longer keeps.
func dropHours(hours map[int64]int64, cutoff time.Time) {
	for h := range hours {
		if !hourKept(h, cutoff) {
			delete(hours, h)
		}
	}
}

// seenNow is the count to remember after a read that found cur, g past prev.
// A partial read keeps the higher count, or the part that could not be read
// would be counted again when it reads next time.
func seenNow(prev, cur, g snapshot.Tokens, partial bool) snapshot.Tokens {
	if partial {
		return prev.Add(g)
	}
	return cur
}

// servedBy is the account that served a session whose log file several homes
// hold, as Orca links each Codex rollout into every account's home. The
// file's newest rate-limit reading is the serving account's: a weekly window
// resets at a time fixed per account, so the one account logged in at those
// homes whose quota resets at the same time served it. With none or more
// than one such account, it returns "" and the session stays with its first
// home, the default home when it holds the file.
func servedBy(st *state.State, p string, s logs.Session, labels map[string]string) string {
	if len(s.Homes) < 2 || s.Limits == nil {
		return ""
	}
	found := ""
	for _, h := range s.Homes {
		l := labels[h]
		if l == "" || l == UnknownAccount || l == found {
			continue
		}
		acct := st.Accounts[state.Key(p, l)]
		if acct == nil || acct.Quota == nil || !sameReset(acct.Quota.Windows, s.Limits.Windows) {
			continue
		}
		if found != "" {
			return ""
		}
		found = l
	}
	return found
}

// resetSkew is how far apart two readings of one window's reset time may be.
const resetSkew = time.Minute

// sameReset reports whether the longest window of the log reading resets when
// the window of the same length in the quota does.
func sameReset(quota, log []snapshot.Window) bool {
	var longest *snapshot.Window
	for i, w := range log {
		if w.ResetsAt != nil && w.Minutes > 0 && (longest == nil || w.Minutes > longest.Minutes) {
			longest = &log[i]
		}
	}
	if longest == nil {
		return false
	}
	for _, w := range quota {
		if w.Minutes == longest.Minutes && w.ResetsAt != nil {
			d := w.ResetsAt.Sub(*longest.ResetsAt)
			return d <= resetSkew && d >= -resetSkew
		}
	}
	return false
}

// hermesLinks are the Hermes billing providers that log in to the same
// subscription as another harness, and that harness. Hermes keeps its own
// login for them, so the account is assumed, not read: the one logged in to
// the home Config.QuotaFrom names for the Hermes home, else to the harness's
// default home. Hermes never marks Anthropic usage as billed to a
// subscription, so it is not linked.
var hermesLinks = map[string]string{
	"openai-codex": "codex",
	"xai-oauth":    "grok",
}

// BillsThrough reports whether Hermes can bill a subscription through the
// login of harness p.
func BillsThrough(p string) bool {
	return HermesBilling(p) != ""
}

// HermesBilling is the Hermes billing provider that bills through the login
// of harness p, or "".
func HermesBilling(p string) string {
	for b, v := range hermesLinks {
		if v == p {
			return b
		}
	}
	return ""
}

// paths resolves symlinks in paths, once per path and run, so a home named
// by one path still matches when it is found by another.
type paths map[string]string

func (r paths) resolve(p string) string {
	if p == "" || r == nil {
		return p
	}
	if v, ok := r[p]; ok {
		return v
	}
	v := fsutil.RealPath(filepath.Clean(p))
	r[p] = v
	return v
}

// quotaLinks is Config.QuotaFrom keyed by resolved Hermes home.
func quotaLinks(named map[string]map[string]string, r paths) map[string]map[string]string {
	// Two spellings of one home merge in a fixed order, so the same entry
	// wins every run.
	out := map[string]map[string]string{}
	for _, h := range slices.Sorted(maps.Keys(named)) {
		k := r.resolve(h)
		for p, at := range named[h] {
			if out[k] == nil {
				out[k] = map[string]string{}
			}
			out[k][p] = at
		}
	}
	return out
}

// quotaHome is the home of harness p named for a Hermes home, or for the
// home a profile is kept in, or "". named is keyed by resolved home.
func quotaHome(named map[string]map[string]string, r paths, hermesHome, p string) string {
	if at := named[r.resolve(hermesHome)][p]; at != "" {
		return at
	}
	// A profile is in its home's profiles folder even when it is a link to
	// somewhere else, so the parent is taken from the path it was found by.
	if parent := filepath.Dir(filepath.Clean(hermesHome)); filepath.Base(parent) == profiles["hermes"].dir {
		return named[r.resolve(filepath.Dir(parent))][p]
	}
	return ""
}

// QuotaHomesOf is, by harness, the home named for a Hermes home or for the
// home a profile is kept in.
func QuotaHomesOf(named map[string]map[string]string, hermesHome string) map[string]string {
	r := paths{}
	links := quotaLinks(named, r)
	out := map[string]string{}
	for _, p := range snapshot.Providers {
		if at := quotaHome(links, r, hermesHome, p); at != "" {
			out[p] = at
		}
	}
	return out
}

// linkedAccount is the account a Hermes billing provider in the Hermes home
// is assumed to bill through: the one logged in now to the home the person
// named for it, or to the linked harness's default home.
func linkedAccount(st *state.State, o Options, hermesHome, billing string) *state.Link {
	p, ok := hermesLinks[billing]
	if !ok {
		return nil
	}
	at := quotaHome(o.quotaFrom, o.paths, hermesHome, p)
	if at == "" {
		if o.UserHome == "" {
			return nil
		}
		at = filepath.Join(o.UserHome, "."+p)
	}
	label := st.Current[state.Key(p, at)]
	if label == "" {
		// The home may be found under another path than it was named by.
		want := o.paths.resolve(at)
		for k, l := range st.Current {
			if parts := state.SplitKey(k); len(parts) == 2 && parts[0] == p && o.paths.resolve(parts[1]) == want {
				label = l
				break
			}
		}
	}
	if label == "" {
		return nil
	}
	return &state.Link{Provider: p, Label: label}
}

// linkHermes points each Hermes account billed through a subscription at the
// login most of its tokens read this run went through, or at none when
// nobody is logged in there. Homes can bill one route through different
// logins, as bots on a shared login beside the person's own Hermes; the
// account shows the quota of the login it uses most, which holds from run to
// run. An account with no session read this run keeps its link, and so does
// one whose homes were not all read: the rest would not be the majority.
func linkHermes(st *state.State, o Options, read []readSession, partial bool) {
	type use struct {
		tokens int64
		last   time.Time
	}
	seen := map[string]bool{}
	uses := map[string]map[state.Link]*use{}
	for _, r := range read {
		for billing, t := range hermesParts(r.s) {
			seen[billing] = true
			link := linkedAccount(st, o, r.s.Home, billing)
			if link == nil {
				continue
			}
			if uses[billing] == nil {
				uses[billing] = map[state.Link]*use{}
			}
			u := uses[billing][*link]
			if u == nil {
				u = &use{}
				uses[billing][*link] = u
			}
			u.tokens += t.Total()
			if r.s.Updated.After(u.last) {
				u.last = r.s.Updated
			}
		}
	}
	for _, acct := range st.Accounts {
		if acct.Provider != "hermes" {
			continue
		}
		if _, ok := hermesLinks[acct.Label]; !ok {
			acct.Link = nil
			continue
		}
		if !seen[acct.Label] || (partial && acct.Link != nil) {
			continue
		}
		var best *state.Link
		var top *use
		for l, u := range uses[acct.Label] {
			if top == nil || u.tokens > top.tokens ||
				(u.tokens == top.tokens && (u.last.After(top.last) || (u.last.Equal(top.last) && l.Label < best.Label))) {
				best, top = &l, u
			}
		}
		acct.Link = best
	}
}

// hermesParts is a Hermes session's tokens by billing provider, with a part
// that names none credited to the session's own, as attribute does.
func hermesParts(s logs.Session) map[string]snapshot.Tokens {
	own := strings.TrimSpace(s.Account)
	if s.Parts == nil {
		return map[string]snapshot.Tokens{own: s.Tokens}
	}
	out := map[string]snapshot.Tokens{}
	for b, t := range s.Parts {
		if t.Zero() {
			continue
		}
		if b = strings.TrimSpace(b); b == "" {
			b = own
		}
		out[b] = out[b].Add(t)
	}
	return out
}

// linkGrowth records a Hermes session's growth on each subscription against
// the account it is assumed to have used, so that account can show what
// Hermes spent on it.
func linkGrowth(st *state.State, o Options, s logs.Session, grown map[string]snapshot.Tokens) {
	for billing, g := range grown {
		link := linkedAccount(st, o, s.Home, billing)
		if link == nil {
			continue
		}
		e := st.Sessions[state.Key("hermes", s.ID)]
		if e.Via == nil {
			e.Via = map[string]snapshot.Tokens{}
		}
		k := state.Key(link.Provider, link.Label)
		e.Via[k] = e.Via[k].Add(g)
	}
}

// markHermesCurrent marks the billing provider of the newest Hermes session
// read this run as current. It is the account the session bills now, not
// whichever account earlier growth went to. A session whose row names no
// provider and holds no tokens of its own, as one Hermes made for auxiliary
// calls alone, does not say. With no sessions read, the last known one stays.
func markHermesCurrent(st *state.State, read []readSession) {
	var newest *readSession
	for i := range read {
		r := &read[i]
		if strings.TrimSpace(r.s.Account) == "" && r.s.Parts[r.s.Account].Zero() {
			continue
		}
		if newest == nil || r.s.Updated.After(newest.s.Updated) ||
			(r.s.Updated.Equal(newest.s.Updated) && r.s.ID > newest.s.ID) {
			newest = r
		}
	}
	if newest != nil {
		st.Current[state.Key("hermes", "")] = newest.label
	}
}

// keepCurrent forgets who was logged in at homes of p that this run did not
// see, and when that changed, so an account on a removed home is no longer
// shown as logged in.
func keepCurrent(st *state.State, p string, homes []string) {
	keep := map[string]bool{}
	for _, h := range homes {
		keep[state.Key(p, h)] = true
	}
	for k := range st.Current {
		if state.SplitKey(k)[0] == p && !keep[k] {
			delete(st.Current, k)
		}
	}
	for k := range st.Switched {
		if state.SplitKey(k)[0] == p && !keep[k] {
			delete(st.Switched, k)
		}
	}
}

// prune drops sessions and idle accounts older than the retention window.
func prune(st *state.State, now time.Time) {
	cutoff := now.Add(-state.Retention)
	used := map[string]bool{}
	for k, s := range st.Sessions {
		if s.Updated.Before(cutoff) {
			delete(st.Sessions, k)
			continue
		}
		dropHours(s.Hours, cutoff)
		for _, hours := range s.ByHours {
			dropHours(hours, cutoff)
		}
		for l := range s.By {
			// An account's share of a session another account continued
			// ages out on its own. The session stays for its counts.
			if last, ok := s.Last[l]; ok && last.Before(cutoff) {
				delete(s.By, l)
				delete(s.Last, l)
				for h, n := range s.ByHours[l] {
					if s.Hours[h] > n {
						s.Hours[h] -= n
					} else {
						delete(s.Hours, h)
					}
				}
				delete(s.ByHours, l)
				continue
			}
			used[state.Key(s.Provider, l)] = true
		}
		for via := range s.Via {
			used[via] = true
		}
	}
	current := map[string]bool{}
	for k, l := range st.Current {
		current[state.Key(state.SplitKey(k)[0], l)] = true
	}
	for k, a := range st.Accounts {
		if used[k] || current[k] {
			continue
		}
		last := a.LastSeenAt
		if a.Quota != nil && a.Quota.At.After(last) {
			last = a.Quota.At
		}
		if last.Before(cutoff) {
			delete(st.Accounts, k)
		}
	}
}

// IsCurrent reports whether an account is logged in on any home now.
func IsCurrent(st *state.State, provider, label string) bool {
	for k, l := range st.Current {
		if l == label && state.SplitKey(k)[0] == provider {
			return true
		}
	}
	return false
}

// shortErr is an error on one line, cut short. Errors can quote a server's
// reply, line breaks and all.
func shortErr(err error) string {
	return snapshot.Truncate(strings.Join(strings.Fields(snapshot.Printable(err.Error())), " "), 200)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
