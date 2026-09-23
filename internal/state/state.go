// Package state is the collector's own directory: config, the team key, the
// ledger of last readings, the 15-minute samples, and the run lock.
//
// Nothing here is a provider secret. The only secret is the team key.
package state

import (
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
	Relay  string `json:"relay,omitempty"`
	// Homes are harness homes seen through environment variables in an
	// interactive run. The scheduler's environment does not have them.
	Homes map[string][]string `json:"homes,omitempty"`
	// HomeEnv keeps, by provider and home, the exact value of the variable
	// an interactive run saw naming that home, where the harness depends on
	// the string itself. Claude Code names its login after the exact
	// CLAUDE_CONFIG_DIR, even when it names the default ~/.claude.
	HomeEnv map[string]map[string]string `json:"home_env,omitempty"`
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

// State is rewritten by every run.
type State struct {
	LastRunAt     time.Time `json:"last_run_at"`
	LastSuccessAt time.Time `json:"last_success_at"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at"`

	Sources map[string]Source `json:"sources"`
	// Current maps provider and home to the account logged in there at the last run.
	Current  map[string]string   `json:"current"`
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

// Lock takes the run lock. A lock older than stale is taken over.
func (d Dir) Lock(stale time.Duration) (func(), error) {
	if err := os.MkdirAll(string(d), 0o700); err != nil {
		return nil, err
	}
	path := d.Path("run.lock")
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	owner := strconv.Itoa(os.Getpid()) + " " + hex.EncodeToString(nonce)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(owner)
			cerr := f.Close()
			if werr != nil || cerr != nil {
				_ = os.Remove(path)
				return nil, errors.Join(werr, cerr)
			}
			// A run that outlived stale may have lost the lock to a later
			// run; releasing must not remove that run's lock.
			return func() {
				if b, err := os.ReadFile(path); err == nil && string(b) == owner {
					_ = os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil || lockAge(info.ModTime()) < stale {
			return nil, errors.New("another ai-usage run is in progress")
		}
		_ = os.Remove(path)
	}
	return nil, errors.New("could not take the run lock")
}

// lockAge is how long ago a lock was taken. A lock stamped in the future was
// left by a crashed run before the clock went back; it counts from then too,
// or it would hold off every run until the clock caught up.
func lockAge(taken time.Time) time.Duration {
	age := time.Since(taken)
	if age < 0 {
		return -age
	}
	return age
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
