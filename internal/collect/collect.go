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
	"os"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

// UnknownAccount labels usage the harness did not attribute to anyone.
const UnknownAccount = logs.UnknownAccount

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
	Ask      func(ctx context.Context, provider, home string, lastUse time.Time) (probe.Reading, error)
	// After runs under the run lock before the state is saved. The command
	// layer uses it for scheduler registration and self-update bookkeeping.
	After func(ctx context.Context, cfg *state.Config, st *state.State)
	// Wait is how long a run waits for another, such as the scheduled one,
	// to finish; zero fails at once with state.ErrBusy. Waiting is called
	// when the wait starts.
	Wait    time.Duration
	Waiting func()
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

// Run takes one sample. It returns an error only when the collector's own
// state cannot be read or written, or when ctx ends before the sample is
// written; source and relay failures are recorded in the state and do not
// stop the run.
func Run(ctx context.Context, o Options) (*Result, error) {
	o.fill()
	unlock, waited, lastRun, err := o.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	cfg, err := o.Dir.LoadConfig()
	if err != nil {
		return nil, err
	}
	key, err := LoadKey(o.Dir)
	if err != nil {
		return nil, err
	}
	st, err := o.Dir.LoadState()
	if err != nil {
		return nil, err
	}
	now := o.Now().UTC().Truncate(time.Second)

	homes := Discover(o.UserHome, o.Getenv, cfg.Homes)
	if cfg, err = rememberHomes(o, cfg, homes); err != nil {
		return nil, err
	}
	inputs := runInputs(o, cfg, homes)
	if waited {
		if res := reuseWaited(o, cfg, key, st, inputs, lastRun); res != nil {
			return res, nil
		}
	}
	s := newSampler(o, cfg, st, now)
	problems, failed := s.collect(ctx, homes)
	sample := sampleOf(st, s.growth, now)
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
	// Record this run's problems before the snapshot is built, so the team
	// sees them now rather than one run late.
	noteProblems(st, problems, now)
	res := &Result{Config: cfg, State: st, Key: key}
	res.Doc = BuildDoc(st, key, cfg, o.Hostname, o.OSUser, o.Version, now)
	res.Team, _ = LoadTeamCache(o.Dir)
	if problem := s.publish(ctx, key, cfg.Device, res); problem != "" {
		problems = append(problems, problem)
		noteProblems(st, problems, now)
	}
	// A stopped run leaves its housekeeping to the next one.
	if o.After != nil && ctx.Err() == nil {
		o.After(ctx, &res.Config, st)
	}
	return res, o.Dir.SaveState(st)
}

// lock takes the run lock. When another run holds it and o.Wait is set, it
// waits up to o.Wait for that run to finish, and lastRun is the state's
// LastRunAt from before the wait.
func (o *Options) lock(ctx context.Context) (unlock func(), waited bool, lastRun time.Time, err error) {
	unlock, err = o.Dir.Lock()
	waited = errors.Is(err, state.ErrBusy) && o.Wait > 0
	if waited {
		if st, err := o.Dir.LoadState(); err == nil {
			lastRun = st.LastRunAt
		}
		unlock, err = o.Dir.LockWait(ctx, o.Wait, o.Waiting)
	}
	return unlock, waited, lastRun, err
}

// reuseWaited is the result of the run this one waited for, when that run
// has just collected what this one would: the same release, relay, and
// homes. It is nil when this run must collect.
func reuseWaited(o Options, cfg state.Config, key *team.Key, st *state.State, inputs string, lastRun time.Time) *Result {
	if !st.LastRunAt.After(lastRun) || st.LastRunInputs != inputs {
		return nil
	}
	// A scheduled run may have skipped the team read this one would make.
	cache, _ := LoadTeamCache(o.Dir)
	read := !cache.PulledAt.Before(st.LastRunAt) || (o.PullEvery > 0 && st.LastRunAt.Sub(cache.PulledAt) < o.PullEvery)
	if o.Relay != nil && !read {
		return nil
	}
	doc := BuildDoc(st, key, cfg, o.Hostname, o.OSUser, o.Version, st.LastRunAt)
	return &Result{Config: cfg, State: st, Key: key, Doc: doc, Team: cache, Waited: true}
}

// sampleOf is the run's sample: the accounts that grew this run or whose
// quota was read in the last day, with their growth and their last quota.
func sampleOf(st *state.State, growth map[string]snapshot.Tokens, now time.Time) state.Sample {
	sample := state.Sample{At: now}
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
	slices.SortFunc(sample.Accounts, func(a, b state.SampleAccount) int {
		return cmp.Or(strings.Compare(a.Provider, b.Provider), strings.Compare(a.Label, b.Label))
	})
	return sample
}

// noteProblems records the run's problems, when it has any, as the state's
// last error.
func noteProblems(st *state.State, problems []string, now time.Time) {
	if len(problems) > 0 {
		st.LastError = snapshot.Truncate(strings.Join(problems, "; "), 600)
		st.LastErrorAt = now
	}
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

// readSession is one session as read this run, with the account its growth
// belongs to.
type readSession struct {
	s     logs.Session
	label string
}

// sampler is one run's reading of the sources: the run's options, the state
// it updates, its times, and the growth by account it has found so far.
type sampler struct {
	Options
	st                  *state.State
	now, since, prevRun time.Time
	growth              map[string]snapshot.Tokens
	paths               paths
	// quotaFrom is Config.QuotaFrom keyed by resolved home.
	quotaFrom map[string]map[string]string
}

// newSampler starts a run's reading of the sources at now. Unless o.Ask is
// set, it asks the harnesses with the variable values cfg remembers.
func newSampler(o Options, cfg state.Config, st *state.State, now time.Time) *sampler {
	if o.Ask == nil {
		o.Ask = askHarness(o.Probe, cfg.HomeEnv)
	}
	s := &sampler{
		Options: o,
		st:      st,
		now:     now,
		since:   now.Add(-state.Retention),
		prevRun: st.LastRunAt,
		growth:  map[string]snapshot.Tokens{},
		paths:   paths{},
	}
	s.quotaFrom = quotaLinks(cfg.QuotaFrom, s.paths)
	return s
}

// collect reads every provider into the state and prunes it. It returns the
// run's problems so far, and whether an installed source failed.
func (s *sampler) collect(ctx context.Context, homes map[string][]string) (problems []string, failed bool) {
	if s.st.Damage != "" {
		problems = append(problems, s.st.Damage)
	}
	for _, p := range snapshot.Providers {
		src := s.source(ctx, p, homes[p])
		s.st.Sources[p] = src
		if src.Error != "" {
			problems = append(problems, p+": "+src.Error)
		}
		if src.Status == "error" {
			failed = true
		}
	}
	prune(s.st, s.now)
	return problems, failed
}

// source collects one provider. A bug that panics while reading or probing
// it becomes that source's error, and the other sources still run.
func (s *sampler) source(ctx context.Context, p string, homes []string) (src state.Source) {
	defer func() {
		if v := recover(); v != nil {
			src = state.Source{Status: "error", Homes: homes, Error: snapshot.Truncate(fmt.Sprintf("stopped by a bug: %v", v), 300)}
		}
	}()
	return s.provider(ctx, p, homes)
}

func (s *sampler) provider(ctx context.Context, p string, homes []string) state.Source {
	if len(homes) == 0 && !s.Probe.Find(p) {
		keepCurrent(s.st, p, nil)
		return state.Source{Status: "skipped"}
	}
	src := state.Source{Homes: homes}
	var errs []string
	readOK := 0
	// All homes are read together, so a session kept in two homes, or a
	// sub-agent or fork in one home of a session in another, counts once.
	var res logs.Result
	if len(homes) > 0 {
		res = s.ReadLogs(p, homes, s.since)
	}
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
	}
	var read []readSession
	probed := homes
	if p == "hermes" {
		read, probed = s.hermes(homes, res, partial)
	} else {
		var probeErrs []string
		read, probeErrs = s.harness(ctx, p, homes, res, partial)
		errs = append(errs, probeErrs...)
	}
	applyRejected(s.st, p, read, s.now, s.prevRun)
	// An installed tool without its data directory has never been used, and
	// nobody is logged in to it: logging in makes the directory. It is not
	// asked, since asking would start it and it would make the directory
	// itself, in the home of someone who may only have the app that bundles it.
	keepCurrent(s.st, p, probed)
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
