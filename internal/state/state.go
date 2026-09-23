// Package state is the collector's own directory: config, the team key, the
// ledger of last readings, the 15-minute samples, and the run lock.
//
// Nothing here is a provider secret. The only secret is the team key.
package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

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

// Config is set once and changed only by the person.
type Config struct {
	Device string `json:"device"`
	// Name is what this device is called in the team instead of its host
	// name, set with `ai-usage name set`. A container's host name is random.
	Name  string `json:"name,omitempty"`
	Relay string `json:"relay,omitempty"`
	// Homes are harness homes seen through environment variables in an
	// interactive run. The scheduler's environment does not have them.
	Homes map[string][]string `json:"homes,omitempty"`
	// HomeEnv keeps, by provider and home, the exact value of the variable
	// an interactive run saw naming that home, where the harness depends on
	// the string itself. Claude Code names its login after the exact
	// CLAUDE_CONFIG_DIR, even when it names the default ~/.claude.
	HomeEnv map[string]map[string]string `json:"home_env,omitempty"`
	// QuotaFrom names, by Hermes home and then by harness, the home of that
	// harness whose login the Hermes home bills its subscription through. It
	// is set with `ai-usage home add hermes DIR --quota-from codex:DIR`. A
	// Hermes home without an entry for a harness is taken to use the login
	// in that harness's default home. An entry covers the profiles inside
	// its home too.
	QuotaFrom map[string]map[string]string `json:"quota_from,omitempty"`
	// ScheduleOff is set by `ai-usage schedule remove` so a later run does not
	// register again.
	ScheduleOff bool `json:"schedule_off,omitempty"`
}

// LoadConfig reads config.json, creating a device id on first use.
func (d Dir) LoadConfig() (Config, error) {
	var c Config
	if err := readJSON(d.Path("config.json"), &c); err != nil && !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	if !snapshot.ValidDevice(c.Device) {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			return c, err
		}
		c.Device = "d-" + hex.EncodeToString(b)
		if err := d.SaveConfig(c); err != nil {
			return c, err
		}
	}
	return c, nil
}

func (d Dir) SaveConfig(c Config) error { return writeJSON(d.Path("config.json"), c) }

// EditConfig changes config.json under a lock of its own, held only while the
// file is read and written. A person's command never waits for a collection,
// and a run that records a newly found folder does not undo the command's
// change, or the other way round.
func (d Dir) EditConfig(edit func(*Config) error) (Config, error) {
	unlock, err := d.lockWait(context.Background(), "config.lock", 10*time.Second, nil)
	if errors.Is(err, ErrBusy) {
		// Not ErrBusy, which means a run is collecting.
		return Config{}, errors.New("another ai-usage command is changing config.json")
	}
	if err != nil {
		return Config{}, fmt.Errorf("config.json: %w", err)
	}
	defer unlock()
	c, err := d.LoadConfig()
	if err != nil {
		return c, err
	}
	if err := edit(&c); err != nil {
		return c, err
	}
	return c, d.SaveConfig(c)
}

// State is rewritten by every run.
type State struct {
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
	// the account it names then, and only then. False is a home that named
	// an account in a run that could not read all its logs; it claims at a
	// run that does.
	Answered map[string]bool     `json:"answered,omitempty"`
	Accounts map[string]*Account `json:"accounts"`
	Sessions map[string]*Session `json:"sessions"`

	Relay    Relay    `json:"relay"`
	Update   Update   `json:"update"`
	Schedule Schedule `json:"schedule"`

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
}

// Relay is the last exchange with the team store.
type Relay struct {
	LastPushAt  time.Time `json:"last_push_at"`
	LastPullAt  time.Time `json:"last_pull_at"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at"`
	// Pending means the newest snapshot has not reached the relay.
	Pending bool `json:"pending"`
}

// Update is the self-update bookkeeping.
type Update struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest,omitempty"`
	Installed string    `json:"installed,omitempty"`
	Error     string    `json:"error,omitempty"`
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

// LoadState reads state.json. A missing file is an empty state. So is one
// that does not parse, such as a file cut short by a crash: otherwise every
// later run, and the self-update that could fix it, would stop on it. Its
// bytes are kept in state.json.bad, and Damage and LastError say so.
func (d Dir) LoadState() (*State, error) {
	path := d.Path("state.json")
	s := &State{}
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if jerr := json.Unmarshal(b, s); jerr != nil {
			s = &State{Damage: "state.json did not parse (" + jerr.Error() + "); started again from an empty state"}
			if werr := WriteFile(path+".bad", b); werr == nil {
				s.Damage += ", the damaged file is state.json.bad"
			}
			s.LastError, s.LastErrorAt = s.Damage, time.Now().UTC()
		}
	}
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
	return s, nil
}

func (d Dir) SaveState(s *State) error { return writeJSON(d.Path("state.json"), s) }

// ErrBusy is Lock's error while another run holds the lock.
var ErrBusy = errors.New("another ai-usage run is in progress")

// Lock takes the run lock. A collection holds it from start to end, since it
// reads and rewrites the state, samples, team cache, and binary. It is the
// system's lock on run.lock, which ends with the process that holds it
// however that process ends, so a run that was killed never leaves it behind.
func (d Dir) Lock() (func(), error) { return d.lock("run.lock") }

// ScheduleLock is held by `ai-usage schedule run` for as long as it runs, so
// other runs can tell that it schedules this folder. A check holds a shared
// lock on the same file for a moment, so a second is allowed for that to end.
func (d Dir) ScheduleLock() (func(), error) {
	return d.lockWait(context.Background(), "schedule.lock", checkWait, nil)
}

// Foreground reports whether `ai-usage schedule run` runs for this folder now.
// It takes a shared lock, which only the exclusive one `schedule run` holds
// keeps out, so it answers at once, and checks never wait for each other.
func (d Dir) Foreground() bool {
	f, err := os.OpenFile(d.Path("schedule.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	return errors.Is(sharedLockFile(f), ErrBusy)
}

// checkWait is how long ScheduleLock waits for a check's shared lock to end.
var checkWait = time.Second

func (d Dir) lock(name string) (func(), error) {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return nil, err
	}
	// The file stays in place: removing it would let a later run lock a new
	// file while an earlier one still holds the old.
	f, err := os.OpenFile(d.Path(name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	if heldBefore(f) {
		f.Close()
		return nil, ErrBusy
	}
	// The holder's process id, for a person who wonders which run it is.
	if f.Truncate(0) == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		_ = f.Truncate(0)
		_ = f.Close()
	}, nil
}

// heldBefore reports whether a release from before the system's lock holds
// the lock file. Those create it and write "pid nonce" into it, and remove it
// when done, so one that is still running while a newer release is installed
// holds the lock by the file alone. It is held while its process runs, up to
// the 10 minutes those releases allowed a run.
func heldBefore(f *os.File) bool {
	b := make([]byte, 64)
	n, _ := f.ReadAt(b, 0)
	pid, nonce, ok := strings.Cut(string(b[:n]), " ")
	info, err := f.Stat()
	if !ok || len(nonce) != 16 || err != nil {
		return false
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return false
	}
	id, err := strconv.Atoi(pid)
	if err != nil || id <= 0 || id == os.Getpid() {
		return false
	}
	age := time.Since(info.ModTime())
	return age > -10*time.Minute && age < 10*time.Minute && processAlive(id)
}

// lockPoll is how often LockWait tries the lock again.
var lockPoll = 250 * time.Millisecond

// LockWait takes the run lock like Lock, waiting up to wait for the run that
// holds it, such as a scheduled one, to finish. waiting is called once, when
// the wait starts.
func (d Dir) LockWait(ctx context.Context, wait time.Duration, waiting func()) (func(), error) {
	return d.lockWait(ctx, "run.lock", wait, waiting)
}

func (d Dir) lockWait(ctx context.Context, name string, wait time.Duration, waiting func()) (func(), error) {
	deadline := time.Now().Add(wait)
	for first := true; ; first = false {
		unlock, err := d.lock(name)
		if !errors.Is(err, ErrBusy) || !time.Now().Before(deadline) {
			return unlock, err
		}
		if first && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return nil, ErrBusy
		case <-time.After(min(lockPoll, time.Until(deadline))):
		}
	}
}

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
	return WriteFile(path, append(b, '\n'))
}

// WriteFile writes through a temporary file and a rename.
func WriteFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if werr == nil {
		// Without this a crash can leave the renamed file empty.
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
