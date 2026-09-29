package collect

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// askHarness asks the installed harnesses. homeEnv is the remembered
// variable values of the config, by provider and home.
func askHarness(env probe.Env, homeEnv map[string]map[string]string) func(ctx context.Context, provider, home string, lastUse time.Time) (probe.Reading, error) {
	return func(ctx context.Context, provider, home string, lastUse time.Time) (probe.Reading, error) {
		switch provider {
		case "claude":
			return probe.Claude(ctx, env, home, homeEnv["claude"][home], lastUse)
		case "codex":
			return probe.Codex(ctx, env, home)
		case "grok":
			return probe.Grok(home)
		default:
			return probe.Reading{}, nil
		}
	}
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

// harness asks each home of harness p who is logged in, labels each session
// read with its account, and attributes the sessions' growth. It returns the
// sessions, the probes' problems, and what keeps a home from a quota
// reading. That is no problem of the run: the tokens are read all the same,
// and the person has to act, not wait.
func (s *sampler) harness(ctx context.Context, p string, homes []string, res logs.Result, partial bool) ([]readSession, []string, []string) {
	// The Claude app's session homes have no login to ask about: the app
	// recorded the account each session ran under.
	apps := claudeApps(p, homes)
	used, lastUse := lastUses(p, homes, res)
	lendAppUse(s.st, p, homes, apps, lastUse)
	answers := askAll(ctx, func(ctx context.Context, p, home string) (probe.Reading, error) {
		if _, ok := apps[home]; ok {
			return probe.Reading{}, nil
		}
		return s.Ask(ctx, p, home, lastUse[home])
	}, p, homes)
	labels := map[string]string{}
	var probeErrs, noQuota [][2]string
	for i, home := range homes {
		if app, ok := apps[home]; ok {
			labels[home] = touchAccount(s.st, p, app.account()).Label
			continue
		}
		a := answers[i]
		// An app's per-account home with nobody logged in is an account
		// removed or not added yet, and a home with no usage is a tool
		// installed but never used, as in a bot's image, which Claude
		// Code makes its home for as soon as it is asked who is logged
		// in. Neither is a problem.
		switch {
		case errors.Is(a.err, probe.ErrNoQuota):
			noQuota = append(noQuota, [2]string{home, noQuotaText(p, home, a.err, res, s.UserHome)})
		case a.err != nil && !(errors.Is(a.err, probe.ErrNotLoggedIn) && (isManaged(p, home) || !used[home])):
			probeErrs = append(probeErrs, [2]string{home, shortErr(a.err)})
		}
		labels[home] = applyReading(s.st, p, home, a.reading, errors.Is(a.err, probe.ErrNotLoggedIn), res.Homes[home].Limits, s.now, s.prevRun)
	}
	claimUnknown(s.st, p, homes, answers, res, labels)
	var read []readSession
	for _, rs := range res.Sessions {
		label, ok := labels[rs.Home]
		if l := servedBy(s.st, p, rs, labels); l != "" {
			label, ok = l, true
		}
		if !ok || label == "" {
			// A session from a home that was not probed has no account.
			label = UnknownAccount
		}
		if app, ok := apps[rs.Home]; ok {
			rs.Project = app.project()
		}
		read = append(read, readSession{s: rs, label: label})
	}
	for _, r := range read {
		attribute(s.st, p, r.s, r.label, partial, s.now, s.growth)
	}
	// The app's homes are not asked, so they are not homes to tell apart.
	asked := len(homes) - len(apps)
	return read, homeErrors(probeErrs, asked, s.UserHome), homeNotes(noQuota, asked, s.UserHome)
}

// noQuotaText says what keeps a home of harness p from a quota reading,
// after the harness's name. When the Claude Code that reads the usage is
// older than the one the home's newest session ran, the person has two, and
// the fix is to update the one this finds or to find the other first.
func noQuotaText(p, home string, err error, res logs.Result, userHome string) string {
	msg := shortErr(err)
	var nq *probe.NoQuotaError
	if errors.As(err, &nq) && nq.Version != "" {
		if ran := sessionVersion(res, home); selfupdate.Newer("v"+ran, "v"+nq.Version) {
			kind := ""
			if nq.Kind != "" {
				kind = " (" + nq.Kind + ")"
			}
			msg = fsutil.Tilde(nq.Path, userHome) + " is Claude Code " + nq.Version + kind +
				" but your sessions run " + ran + ": update it or put the newer one first on PATH"
		}
	}
	return named(p, msg)
}

// sessionVersion is the harness version the newest session of home that
// records one ran.
func sessionVersion(res logs.Result, home string) string {
	var newest time.Time
	ver := ""
	for _, rs := range res.Sessions {
		if rs.Version == "" || rs.Home != home && !slices.Contains(rs.Homes, home) {
			continue
		}
		if ver == "" || rs.Updated.After(newest) {
			newest, ver = rs.Updated, rs.Version
		}
	}
	return ver
}

// homeNotes are the quota notes of a provider's homes, each followed by
// the home it is about when the provider has several homes to ask, unless
// every one of them has the same note.
func homeNotes(notes [][2]string, asked int, userHome string) []string {
	same := len(notes) == asked
	for _, n := range notes {
		same = same && n[1] == notes[0][1]
	}
	var out []string
	for _, n := range notes {
		if same || asked <= 1 {
			out = append(out, n[1])
			continue
		}
		out = append(out, n[1]+" ("+fsutil.Tilde(n[0], userHome)+")")
	}
	return dedupe(out)
}

// lastUses is which homes of p have usage, and when each was last used, as
// the logs show.
func lastUses(p string, homes []string, res logs.Result) (used map[string]bool, lastUse map[string]time.Time) {
	used = map[string]bool{}
	lastUse = map[string]time.Time{}
	for _, rs := range res.Sessions {
		for _, h := range append([]string{rs.Home}, rs.Homes...) {
			used[h] = true
			if rs.Updated.After(lastUse[h]) {
				lastUse[h] = rs.Updated
			}
		}
	}
	// Claude homes that share their logs go unused: the logs give their
	// sessions to one of them, and cannot say which login ran them.
	for h := range sharedClaudeLogs(p, homes) {
		delete(lastUse, h)
	}
	return used, lastUse
}

// lendAppUse gives each home logged in to the account of one of the Claude
// app's sessions, as the last run found it, the time that session was last
// used when it is later than its own. The app's sessions spend that
// account's quota, and have no login of their own to read it with.
func lendAppUse(st *state.State, p string, homes []string, apps map[string]claudeAppSession, lastUse map[string]time.Time) {
	for appHome, app := range apps {
		used, acct := lastUse[appHome], app.account()
		if used.IsZero() || acct == UnknownAccount {
			continue
		}
		for _, h := range homes {
			if _, isApp := apps[h]; !isApp && st.Current[state.Key(p, h)] == acct && used.After(lastUse[h]) {
				lastUse[h] = used
			}
		}
	}
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
