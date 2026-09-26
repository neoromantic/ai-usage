package collect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
)

// world stands in for the harness homes and the harnesses themselves.
type world struct {
	mu       sync.Mutex
	now      time.Time
	userHome string
	env      map[string]string
	logs     map[string]homeLogs // provider+home
	readErr  map[string]error
	readings map[string]probe.Reading // provider+home
	askErr   map[string]error
	asked    []string
	// lastUse is when each asked home was last used, as the probe was told.
	lastUse map[string]time.Time
}

// homeLogs is what a read of one home's logs finds.
type homeLogs struct {
	Sessions              []logs.Session
	Limits                *logs.Limits
	Malformed, Unreadable int
}

func newWorld(t *testing.T) (*world, Options) {
	t.Helper()
	root := t.TempDir()
	w := &world{
		now:      t0,
		userHome: filepath.Join(root, "home"),
		env:      map[string]string{},
		logs:     map[string]homeLogs{},
		readErr:  map[string]error{},
		readings: map[string]probe.Reading{},
		askErr:   map[string]error{},
		lastUse:  map[string]time.Time{},
	}
	mkdirs(t, w.userHome)
	o := Options{
		Dir:      state.Dir(filepath.Join(root, "ai-usage")),
		Version:  "v1.2.3",
		Getenv:   func(k string) string { return w.env[k] },
		UserHome: w.userHome,
		Hostname: "workbox",
		OSUser:   "sam",
		Probe: probe.Env{
			LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
			HomeDir:  w.userHome,
		},
		Now:      func() time.Time { return w.now },
		ReadLogs: w.read,
		Ask:      w.ask,
	}
	return w, o
}

// home makes a provider's default home and returns its path.
func (w *world) home(t *testing.T, p string) string {
	t.Helper()
	h := filepath.Join(w.userHome, "."+p)
	mkdirs(t, h)
	return h
}

// extraHome makes a non-default home named through the provider's variable.
func (w *world) extraHome(t *testing.T, p, name string) string {
	t.Helper()
	h := filepath.Join(filepath.Dir(w.userHome), name)
	mkdirs(t, h)
	w.env[homeEnv[p]] = h
	return h
}

// mkdirs makes each folder, with the folders above it.
func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

// hermesProfile gives dir the empty state.db a Hermes profile with usage has.
func hermesProfile(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "state.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// editConfig changes config.json as a person's command would.
func editConfig(t *testing.T, d state.Dir, edit func(*state.Config)) {
	t.Helper()
	cfg, err := d.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	edit(&cfg)
	if err := d.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

// read stands in for logs.ReadHomes: each home's logs, tagged with the home,
// and a session kept in two homes once, from the larger copy.
func (w *world) read(p string, homes []string, since time.Time) logs.Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := logs.Result{Homes: map[string]logs.HomeRead{}}
	at := map[string]int{}
	for _, home := range homes {
		k := state.Key(p, home)
		r := w.logs[k]
		out.Homes[home] = logs.HomeRead{Err: w.readErr[k], Malformed: r.Malformed, Unreadable: r.Unreadable, Limits: r.Limits}
		for _, s := range r.Sessions {
			s.Home = home
			if i, ok := at[s.ID]; ok {
				if s.Tokens.Total() > out.Sessions[i].Tokens.Total() {
					out.Sessions[i] = s
				}
				continue
			}
			at[s.ID] = len(out.Sessions)
			out.Sessions = append(out.Sessions, s)
		}
	}
	return out
}

func (w *world) ask(ctx context.Context, p, home string, lastUse time.Time) (probe.Reading, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	k := state.Key(p, home)
	w.asked = append(w.asked, k)
	w.lastUse[k] = lastUse
	return w.readings[k], w.askErr[k]
}

func (w *world) sessions(p, home string, ss ...logs.Session) {
	r := w.logs[state.Key(p, home)]
	r.Sessions = ss
	w.logs[state.Key(p, home)] = r
}

func (w *world) login(p, home, account string, q *probe.Quota) {
	w.readings[state.Key(p, home)] = probe.Reading{Account: account, Quota: q}
}

// logout makes the harness in home answer that nobody is logged in.
func (w *world) logout(p, home string) {
	k := state.Key(p, home)
	w.readings[k] = probe.Reading{}
	w.askErr[k] = notLoggedIn(p)
}

func sess(id, project string, input int64, updated time.Time) logs.Session {
	return logs.Session{ID: id, Project: project, Tokens: snapshot.Tokens{Input: input, Output: input / 10}, Updated: updated}
}

func quota(at time.Time, pct ...float64) *probe.Quota {
	q := &probe.Quota{At: at, Source: "harness"}
	names := []string{"5h", "7d"}
	for i, p := range pct {
		reset := at.Add(time.Duration(i+1) * 5 * time.Hour)
		q.Windows = append(q.Windows, snapshot.Window{Name: names[i], Percent: p, ResetsAt: &reset})
	}
	return q
}

func run(t *testing.T, o Options) *Result {
	t.Helper()
	res, err := Run(context.Background(), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func findTotals(st *state.State, provider, label string) (AccountTotals, bool) {
	for _, a := range Totals(st) {
		if a.Provider == provider && a.Label == label {
			return a, true
		}
	}
	return AccountTotals{}, false
}

func totalsFor(t *testing.T, st *state.State, provider, label string) AccountTotals {
	t.Helper()
	a, ok := findTotals(st, provider, label)
	if !ok {
		t.Fatalf("no totals for %s %s in %+v", provider, label, Totals(st))
	}
	return a
}

func hasTotals(st *state.State, provider, label string) bool {
	_, ok := findTotals(st, provider, label)
	return ok
}

func lastSample(t *testing.T, o Options) state.Sample {
	t.Helper()
	ss, err := o.Dir.LoadSamples(time.Time{})
	if err != nil || len(ss) == 0 {
		t.Fatalf("samples: %v, %v", ss, err)
	}
	return ss[len(ss)-1]
}

func growthOf(s state.Sample, provider, label string) snapshot.Tokens {
	for _, a := range s.Accounts {
		if a.Provider == provider && a.Label == label {
			return a.Growth
		}
	}
	return snapshot.Tokens{}
}

func tok(in int64) snapshot.Tokens { return snapshot.Tokens{Input: in, Output: in / 10} }
