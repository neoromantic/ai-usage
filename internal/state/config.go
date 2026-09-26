package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

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
	// Aliases are the short names given to accounts with `ai-usage alias`,
	// keyed by Key(provider, label). They travel in this device's snapshot
	// and name the account for the whole team. A cleared name stays, with no
	// name, so that the clearing reaches the team too.
	Aliases map[string]Alias `json:"aliases,omitempty"`
}

// Alias is a short name for an account, and when it was set or cleared.
type Alias struct {
	Name string    `json:"name,omitempty"`
	At   time.Time `json:"at"`
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
