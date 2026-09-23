// Package collect runs one sample: read local logs, ask each harness who is
// logged in and how full its windows are, attribute new tokens to that
// account, store the sample, and publish this device to the team relay.
package collect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/relay"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
)

// UnknownAccount labels usage the harness did not attribute to anyone.
const UnknownAccount = "unknown"

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
	// Readers and prober are replaced in tests. ReadLogs reads all homes of
	// one provider together and tags each session with its home.
	ReadLogs func(provider string, homes []string, since time.Time) logs.Result
	Ask      func(ctx context.Context, provider, home string) (probe.Reading, error)
	// After runs under the run lock before the state is saved. The command
	// layer uses it for scheduler registration and self-update bookkeeping.
	After func(ctx context.Context, cfg *state.Config, st *state.State)
}

// Result is what a run leaves behind for the views.
type Result struct {
	Config state.Config
	State  *state.State
	Key    *team.Key
	Doc    snapshot.Doc
	Team   TeamCache
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
	if err := k.Save(dir.KeyFile()); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// Run takes one sample. It returns an error only when the collector's own
// state cannot be read or written; source and relay failures are recorded in
// the state and do not stop the run.
func Run(ctx context.Context, o Options) (*Result, error) {
	o.fill()
	unlock, err := o.Dir.Lock(10 * time.Minute)
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
	remembered, changed := Remember(cfg.Homes, o.UserHome, homes)
	cfg.Homes = remembered
	if homeEnv, envChanged := RememberEnv(cfg.HomeEnv, o.Getenv, homes); envChanged {
		cfg.HomeEnv, changed = homeEnv, true
	}
	if changed {
		if err := o.Dir.SaveConfig(cfg); err != nil {
			return nil, err
		}
	}
	if o.Ask == nil {
		o.Ask = askHarness(o.Probe, cfg.HomeEnv)
	}

	sample := state.Sample{At: now}
	growth := map[string]snapshot.Tokens{}
	var problems []string
	if st.Damage != "" {
		problems = append(problems, st.Damage)
	}
	failed := false
	for _, p := range Providers {
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
	if err := o.Dir.AppendSample(sample); err != nil {
		problems = append(problems, "samples: "+err.Error())
	}
	if err := o.Dir.PruneSamples(now); err != nil {
		problems = append(problems, "samples: "+err.Error())
	}

	// A run succeeds when every installed source could be read. A run where
	// nothing is installed has nothing to fail and still counts.
	st.LastRunAt = now
	if !failed {
		st.LastSuccessAt = now
	}
	noteProblems := func() {
		if len(problems) > 0 {
			st.LastError = truncate(strings.Join(problems, "; "), 600)
			st.LastErrorAt = now
		}
	}
	// Record this run's problems before the snapshot is built, so the team
	// sees them now rather than one run late.
	noteProblems()
	res := &Result{Config: cfg, State: st, Key: key}
	res.Doc = BuildDoc(st, key, cfg.Device, o.Hostname, o.OSUser, o.Version, now)

	cache, _ := LoadTeamCache(o.Dir)
	res.Team = cache
	if o.Relay != nil {
		// Publish with the key this run sealed and signed the snapshot for.
		client := *o.Relay
		client.Key = key
		o.Relay = &client
		syncTeam(ctx, o, st, cfg.Device, &res.Doc, &res.Team, now)
		if st.Relay.LastError != "" && st.Relay.LastErrorAt.Equal(now) {
			problems = append(problems, st.Relay.LastError)
			noteProblems()
		}
	}
	if o.After != nil {
		o.After(ctx, &res.Config, st)
	}
	if err := o.Dir.SaveState(st); err != nil {
		return res, err
	}
	return res, nil
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
			src = state.Source{Status: "error", Homes: homes, Error: truncate(fmt.Sprintf("stopped by a bug: %v", v), 300)}
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
	labels := map[string]string{}
	// partial means some home's read was incomplete, so a lower count than
	// last time is a missing file rather than a real drop.
	partial := false
	for _, home := range homes {
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
		if p != "hermes" {
			reading, perr := o.Ask(ctx, p, home)
			if perr != nil {
				errs = append(errs, shortErr(perr))
			}
			labels[home] = applyReading(st, p, home, reading, isLoggedOut(perr), hr.Limits, now, prevRun)
		}
	}
	var read []readSession
	for _, s := range res.Sessions {
		label, ok := labels[s.Home]
		if p == "hermes" {
			label = s.Account
		} else if !ok {
			// A session from a home that was not probed has no account.
			label = UnknownAccount
		}
		if label == "" {
			label = UnknownAccount
		}
		read = append(read, readSession{s: s, label: label})
	}
	for _, r := range read {
		if p == "hermes" {
			touchAccount(st, p, r.label)
		}
		attribute(st, p, r.s, r.label, partial, now, growth)
	}
	probed := homes
	if p == "hermes" {
		markHermesCurrent(st, read)
		probed = nil
		if len(homes) > 0 {
			probed = []string{""}
		}
	} else if len(homes) == 0 {
		// Installed binary, no data directory yet: still ask who is logged in.
		home := filepath.Join(o.UserHome, "."+p)
		probed = []string{home}
		reading, perr := o.Ask(ctx, p, home)
		if perr != nil {
			errs = append(errs, shortErr(perr))
		}
		if strings.TrimSpace(reading.Account) != "" || isLoggedOut(perr) {
			applyReading(st, p, home, reading, isLoggedOut(perr), nil, now, prevRun)
		}
	}
	keepCurrent(st, p, probed)
	switch {
	case len(errs) == 0:
		src.Status = "ok"
	case readOK == 0 && len(homes) > 0:
		src.Status = "error"
	default:
		src.Status = "partial"
	}
	src.Error = truncate(strings.Join(dedupe(errs), "; "), 300)
	return src
}

// isLoggedOut reports whether a probe error is the harness answering that
// nobody is logged in, rather than not answering at all.
func isLoggedOut(err error) bool {
	var lo interface{ LoggedOut() bool }
	return errors.As(err, &lo) && lo.LoggedOut()
}

// applyReading records who is logged in at home and their quota, and returns
// the label new tokens from that home belong to.
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
	if q == nil || len(q.Windows) == 0 {
		return label
	}
	// A reading stamped ahead of the clock is from now at the latest. One
	// older than the retention window is not kept, whatever produced it.
	at := q.At.UTC()
	if at.After(now) {
		at = now
	}
	if at.Before(now.Add(-state.Retention)) {
		return label
	}
	// A stored reading from a clock that has since gone back would otherwise
	// block every real reading until the clock caught up.
	if acct.Quota == nil || acct.Quota.At.After(now) || !at.Before(acct.Quota.At) {
		acct.Quota = &state.Quota{At: at, Source: q.Source, Windows: clampWindows(q.Windows, now)}
	}
	return label
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

func touchAccount(st *state.State, p, label string) *state.Account {
	k := state.Key(p, label)
	acct := st.Accounts[k]
	if acct == nil {
		acct = &state.Account{Provider: p, Label: label}
		st.Accounts[k] = acct
	}
	return acct
}

// attribute moves a session's growth since the last run onto label.
func attribute(st *state.State, p string, s logs.Session, label string, partial bool, now time.Time, growth map[string]snapshot.Tokens) {
	k := state.Key(p, s.ID)
	e := st.Sessions[k]
	if e == nil {
		e = &state.Session{Provider: p, By: map[string]snapshot.Tokens{}}
		st.Sessions[k] = e
	}
	if e.By == nil {
		e.By = map[string]snapshot.Tokens{}
	}
	g := s.Tokens.Growth(e.Seen)
	if partial {
		// Keep the higher count, or the part that could not be read would be
		// counted again when it reads next time.
		e.Seen = e.Seen.Add(g)
	} else {
		e.Seen = s.Tokens
	}
	e.Project = s.Project
	updated := s.Updated
	if updated.IsZero() || updated.After(now) {
		updated = now
	}
	if updated.After(e.Updated) {
		e.Updated = updated.UTC()
	}
	if g.Zero() {
		return
	}
	e.By[label] = e.By[label].Add(g)
	if e.Last == nil {
		e.Last = map[string]time.Time{}
	}
	if updated.After(e.Last[label]) {
		e.Last[label] = updated.UTC()
	}
	ak := state.Key(p, label)
	growth[ak] = growth[ak].Add(g)
}

// markHermesCurrent marks the billing provider of the newest Hermes session
// read this run as current. It is the account the session bills now, not
// whichever account earlier growth went to. With no sessions read, the last
// known one stays.
func markHermesCurrent(st *state.State, read []readSession) {
	var newest *readSession
	for i := range read {
		r := &read[i]
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
// see, so an account on a removed home is no longer shown as logged in.
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
		for l := range s.By {
			// An account's share of a session another account continued
			// ages out on its own. The session stays for its counts.
			if last, ok := s.Last[l]; ok && last.Before(cutoff) {
				delete(s.By, l)
				delete(s.Last, l)
				continue
			}
			used[state.Key(s.Provider, l)] = true
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

func shortErr(err error) string { return truncate(err.Error(), 200) }

// truncate keeps the start of s in at most n bytes, cut on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
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
