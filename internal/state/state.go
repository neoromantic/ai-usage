// Package state is the collector's own directory: config, the team key, the
// ledger of last readings, the 15-minute samples, and the run lock.
//
// Nothing here is a provider secret. The only secret is the team key.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Retention is how long samples, sessions, and idle accounts are kept.
const Retention = 90 * 24 * time.Hour

// Dir is the collector's directory.
type Dir string

// DefaultDir is AI_USAGE_HOME, or ai-usage under the OS config directory.
// On Windows that is Local AppData: a roaming profile would carry this
// device's id and ledger to every machine the person signs in to.
func DefaultDir() (Dir, error) {
	if d := os.Getenv("AI_USAGE_HOME"); d != "" {
		return Dir(d), nil
	}
	base, err := os.UserConfigDir()
	if runtime.GOOS == "windows" {
		base, err = os.UserCacheDir()
	}
	if err != nil {
		return "", err
	}
	return Dir(filepath.Join(base, "ai-usage")), nil
}

func (d Dir) Path(name string) string { return filepath.Join(string(d), name) }
func (d Dir) KeyFile() string         { return d.Path("team.key") }
func (d Dir) SamplesDir() string      { return d.Path("samples") }
func (d Dir) StateFile() string       { return d.Path("state.json") }
func (d Dir) TeamCacheFile() string   { return d.Path("team-cache.json") }

// LedgerVersion is the version of the ledger's format this build writes.
// Version 2 keeps every kind of token by hour; before it, the ledger kept
// input plus output.
const LedgerVersion = 2

// State is rewritten by every run.
type State struct {
	// Ledger is the version of the ledger's format.
	Ledger        int       `json:"ledger"`
	LastRunAt     time.Time `json:"last_run_at"`
	LastSuccessAt time.Time `json:"last_success_at"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at"`
	// LastRunInputs fingerprints what the last run collected with: its
	// version, relay, and homes. A run that waited for it reuses its result
	// only when its own inputs are the same.
	LastRunInputs string `json:"last_run_inputs,omitempty"`

	Sources map[string]Source `json:"sources"`
	// Current maps provider and home to the account logged in there at the last run.
	Current map[string]string `json:"current"`
	// Answered keys, by provider and home, the homes whose harness has
	// answered who is logged in there, or that nobody is, at some run. It is
	// never pruned: usage counted before a home first answers is given to
	// the account it names then, and only then. A false entry reads as not
	// answered.
	Answered map[string]bool `json:"answered,omitempty"`
	// Switched is, by provider and home, when a run last found another
	// account logged in there than the run before it, or nobody. A request
	// refused there before then is not the account logged in now.
	Switched map[string]time.Time `json:"switched,omitempty"`
	Accounts map[string]*Account  `json:"accounts"`
	Sessions map[string]*Session  `json:"sessions"`

	Relay    Relay    `json:"relay"`
	Update   Update   `json:"update"`
	Schedule Schedule `json:"schedule"`

	// GuideDue says the short guide a new device prints once, under the
	// first report a person sees, is still to come. LoadState sets it only
	// when state.json does not exist.
	GuideDue bool `json:"guide_due,omitempty"`

	// Damage says why LoadState started from an empty state. It is not saved.
	Damage string `json:"-"`
}

// Source is the last health of one provider on this device.
type Source struct {
	Status string   `json:"status"`
	Error  string   `json:"error,omitempty"`
	Homes  []string `json:"homes,omitempty"`
}

// Account is one login on one provider, kept after it is logged out.
type Account struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Plan     string `json:"plan,omitempty"`
	// Quota is the last good reading. It survives failed runs.
	Quota      *Quota    `json:"quota,omitempty"`
	LastSeenAt time.Time `json:"last_seen_at"`
	// Link is the account of another harness this one bills through, as
	// far as the collector can tell: Hermes on a Codex or Grok subscription
	// is taken to use the account logged in to that harness's default home,
	// or to the home Config.QuotaFrom names for the Hermes home.
	Link *Link `json:"link,omitempty"`
}

// Link names an account of another provider.
type Link struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// Quota is a reading and where it came from.
type Quota struct {
	At      time.Time         `json:"at"`
	Source  string            `json:"source"`
	Windows []snapshot.Window `json:"windows"`
}

// Session is the last token counts seen for one root session, and how its
// growth was split between accounts.
type Session struct {
	Provider string                     `json:"p"`
	Project  string                     `json:"proj"`
	Seen     snapshot.Tokens            `json:"seen"`
	By       map[string]snapshot.Tokens `json:"by"`
	Updated  time.Time                  `json:"at"`
	// Last is when each account's share last grew. A session continued
	// after a switch is newer than the earlier account's use of it.
	Last map[string]time.Time `json:"last,omitempty"`
	// Parts is the last count seen per account, for a session whose log
	// splits it by account (Hermes). Seen is their sum.
	Parts map[string]snapshot.Tokens `json:"parts,omitempty"`
	// Via is the growth spent through each linked account of another
	// provider, keyed by Key(provider, label). It is also in By.
	Via map[string]snapshot.Tokens `json:"via,omitempty"`
	// U is the session's tokens, of every kind, by the UTC hour they were
	// spent in, keyed by Unix time / 3600: the hours its log showed when first
	// read, then each run's growth, where the log grew or, for a log that
	// records no times, at the hour of the session's last activity. It
	// outlives the log, which a harness may delete before the ledger forgets
	// the session, and it holds only the hours the ledger keeps.
	U meter.Hours `json:"u,omitempty"`
	// UB splits U by account once a second account spends in the session, so
	// each keeps the hours it spent in. Until then, U is the one account's.
	UB map[string]meter.Hours `json:"ub,omitempty"`
	// Side is what Claude Code's own count of the session has beyond its
	// transcript, as the last full read found it.
	Side snapshot.Tokens `json:"side,omitzero"`
	// Nulls is how many replies of the session end without a final token
	// count, as the last full read found it.
	Nulls int `json:"nulls,omitempty"`
	// Rederive says U came from a ledger that kept only input plus output by
	// hour: the next read whose log records times places U anew.
	Rederive bool `json:"rd,omitempty"`

	// Hours and ByHours are what ledgers before version 2 kept in place of U
	// and UB: input plus output by hour. They are read, migrated, and never
	// written again.
	Hours   map[int64]int64            `json:"h,omitempty"`
	ByHours map[string]map[int64]int64 `json:"bh,omitempty"`
}

// Relay is the last exchange with the team store.
type Relay struct {
	LastPushAt  time.Time `json:"last_push_at"`
	LastPullAt  time.Time `json:"last_pull_at"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at"`
	// Pending means the newest snapshot has not reached the relay.
	Pending bool `json:"pending"`
	// NoExtUntil is when the relay last refused a snapshot with fields added
	// after v1 began, plus a day: until then only v1's first fields are sent.
	NoExtUntil time.Time `json:"no_ext_until,omitzero"`
}

// Update is the self-update bookkeeping.
type Update struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
	Installed string    `json:"installed,omitempty"`
	Error     string    `json:"error,omitempty"`
	// Failed is the last release that was downloaded and did not install,
	// which runs do not download again for a while.
	Failed *FailedRelease `json:"failed,omitempty"`
	// AppFailed is the same for the macOS menu bar app of a release that the
	// binary did install.
	AppFailed *FailedRelease `json:"app_failed,omitempty"`
}

// FailedRelease is a release that was downloaded and did not install: when,
// and why.
type FailedRelease struct {
	Tag   string    `json:"tag"`
	At    time.Time `json:"at"`
	Error string    `json:"error"`
}

// Schedule is whether the system scheduler runs the collector.
type Schedule struct {
	Registered bool      `json:"registered"`
	CheckedAt  time.Time `json:"checked_at"`
	Error      string    `json:"error,omitempty"`
	// Foreground says `ai-usage schedule run` started the last scheduled
	// run, in place of the system scheduler.
	Foreground bool `json:"foreground,omitempty"`
}

// Key joins parts of a map key.
func Key(parts ...string) string { return strings.Join(parts, "\x00") }

// SplitKey undoes Key.
func SplitKey(k string) []string { return strings.Split(k, "\x00") }

// NewState is an empty state, ready for a run to add to.
func NewState() *State {
	s := &State{Ledger: LedgerVersion}
	s.fill()
	return s
}

// fill makes the maps a run adds to, where they are nil.
func (s *State) fill() {
	if s.Sources == nil {
		s.Sources = map[string]Source{}
	}
	if s.Current == nil {
		s.Current = map[string]string{}
	}
	if s.Accounts == nil {
		s.Accounts = map[string]*Account{}
	}
	if s.Sessions == nil {
		s.Sessions = map[string]*Session{}
	}
}

// LoadState reads state.json. A missing file is an empty state. So is one
// that does not parse, such as a file cut short by a crash: otherwise every
// later run, and the self-update that could fix it, would stop on it. Its
// bytes are kept in state.json.bad, and Damage and LastError say so.
//
// A ledger older than LedgerVersion is migrated in memory, whatever the
// command, and its file is kept once as state.json.pre-v0.4, for going back
// to an older ai-usage.
func (d Dir) LoadState() (*State, error) {
	path := d.StateFile()
	s := &State{}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// No state yet is a new device.
	s.GuideDue = err != nil
	if err != nil {
		s.Ledger = LedgerVersion
	} else if jerr := json.Unmarshal(b, s); jerr != nil {
		s = &State{Ledger: LedgerVersion, Damage: "state.json did not parse (" + jerr.Error() + "); started again from an empty state"}
		if werr := fsutil.WriteFile(path+".bad", b, 0o600); werr == nil {
			s.Damage += ", the damaged file is state.json.bad"
		}
		s.LastError, s.LastErrorAt = s.Damage, time.Now().UTC()
	} else if s.Ledger < LedgerVersion {
		_ = writeOnce(path+".pre-v0.4", b)
		for _, sess := range s.Sessions {
			if sess != nil {
				migrateSession(sess)
			}
		}
		s.Ledger = LedgerVersion
	}
	s.fill()
	return s, nil
}

// migrateSession moves a session from a ledger before version 2, which kept
// input plus output by hour, to U and UB. Its tokens of every kind follow
// the hours it kept, each kind in its proportion, and only as much of them
// as those hours held: the rest was spent in hours the ledger no longer
// keeps. A session without hours has them all at its last activity. The
// next read that has the log's times places them anew.
func migrateSession(s *Session) {
	var all meter.Use
	for _, t := range s.By {
		all = all.Add(meter.FromTokens(t))
	}
	switch {
	case len(s.Hours) > 0 && all.InOut() > 0:
		s.U = meter.Spread(kept(all, s.Hours), s.Hours)
		for l, hours := range s.ByHours {
			if own := meter.FromTokens(s.By[l]); own.InOut() > 0 && len(hours) > 0 {
				if s.UB == nil {
					s.UB = map[string]meter.Hours{}
				}
				s.UB[l] = meter.Spread(kept(own, hours), hours)
			}
		}
	case !all.Zero():
		s.U = meter.Hours{hourOf(s.Updated): all}
	}
	s.Hours, s.ByHours = nil, nil
	s.Rederive = true
}

// kept is the part of u that hours, input plus output by hour, still hold:
// each kind scaled by their sum over u's input plus output, at most all of u.
func kept(u meter.Use, hours map[int64]int64) meter.Use {
	var held int64
	for _, n := range hours {
		held += max(n, 0)
	}
	io := u.InOut()
	if held >= io {
		return u
	}
	// Of held to what was not, the first part.
	return meter.Spread(u, map[int64]int64{0: held, 1: io - held})[0]
}

// hourOf is the key of the UTC hour t falls in.
func hourOf(t time.Time) int64 { return t.Unix() / 3600 }

// writeOnce writes b to path unless path exists, in one step: a reader never
// sees half of it, and of two runs that write it at once, one wins whole.
// Errors are the caller's to ignore.
func writeOnce(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	// A link fails when path exists, so it never replaces a kept copy.
	lerr := os.Link(tmp, path)
	if lerr == nil || errors.Is(lerr, os.ErrExist) {
		return lerr
	}
	// Where links are not supported, rename, which replaces: only when
	// nothing is there.
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return os.ErrExist
	}
	return os.Rename(tmp, path)
}

func (d Dir) SaveState(s *State) error { return writeJSON(d.StateFile(), s) }

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

// writeJSON replaces a file atomically.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, append(b, '\n'), 0o600)
}
