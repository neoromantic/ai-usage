package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/relay"
)

func cmdTeam(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	sub, args := subcommand(args)
	switch sub {
	case "":
		return showTeam(d, stdout)
	case "key":
		if len(args) > 0 {
			return usageError("team key takes no arguments")
		}
		key, err := collect.LoadKey(d)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, key.Export())
		return nil
	case "join":
		return joinTeam(ctx, d, args, stdin, stdout, stderr)
	case "forget-device":
		if len(args) != 1 {
			return usageError("team forget-device takes one device id")
		}
		id, err := cachedDevice(d, args[0])
		if err != nil {
			return err
		}
		if !snapshot.ValidDevice(id) {
			return usageError("not a device id: " + id)
		}
		return forgetDevice(ctx, d, id, stdout, stderr)
	default:
		return usageError("unknown team command " + sub)
	}
}

// minIDStart is the shortest start of a device id that names it: what
// ATTENTION prints. A shorter one, typed by mistake, could remove a
// teammate.
const minIDStart = 8

// cachedDevice is the device id arg names. ATTENTION names a device by the
// start of its id after "d-", as 3f9a1c2e: such a start of exactly one id the
// cached team read holds is that id, when it is at least minIDStart long.
// Any other arg is an id itself.
func cachedDevice(d state.Dir, arg string) (string, error) {
	c, err := collect.LoadTeamCache(d)
	if err != nil {
		return "", err
	}
	ids := map[string]bool{}
	for _, doc := range c.Docs {
		ids[doc.Device] = true
	}
	for id := range c.Known {
		ids[id] = true
	}
	if ids[arg] {
		return arg, nil
	}
	start := strings.TrimPrefix(arg, "d-")
	var match []string
	for id := range ids {
		if strings.HasPrefix(strings.TrimPrefix(id, "d-"), start) {
			match = append(match, id)
		}
	}
	if len(match) > 0 && len(start) < minIDStart {
		return "", usageError(strconv.Quote(arg) + " is too short to name a device: give its id, or the " + strconv.Itoa(minIDStart) + " characters ATTENTION prints")
	}
	switch len(match) {
	case 0:
		return arg, nil
	case 1:
		return match[0], nil
	}
	sort.Strings(match)
	return "", usageError(arg + " starts the ids of " + strconv.Itoa(len(match)) + " devices: " + strings.Join(match, ", "))
}

func showTeam(d state.Dir, stdout io.Writer) error {
	res, err := loadResult(d)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "team %s\n", res.Key.Fingerprint())
	fmt.Fprintf(stdout, "this device %s\n", res.Config.Device)
	if res.Team.Team != res.Key.Fingerprint() || res.Team.PulledAt.IsZero() {
		fmt.Fprintln(stdout, "the team has not been read from the relay yet")
		return nil
	}
	fmt.Fprintf(stdout, "read from the relay %s\n", res.Team.PulledAt.Local().Format("2006-01-02 15:04"))
	for _, doc := range res.Team.Docs {
		label, _ := res.Key.Open(doc.DeviceLabel)
		who, _ := res.Key.Open(doc.OSUser)
		fmt.Fprintf(stdout, "  %s  %s (%s)  collected %s  %s\n", doc.Device, snapshot.Printable(label), snapshot.Printable(who), doc.CollectedAt.Local().Format("2006-01-02 15:04"), doc.CollectorVersion)
	}
	return nil
}

func forgetDevice(ctx context.Context, d state.Dir, id string, stdout, stderr io.Writer) error {
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	endpoint := relayURL(cfg)
	if endpoint == "" {
		return errors.New("no relay configured; set one with `ai-usage relay set URL`")
	}
	key, err := collect.LoadKey(d)
	if err != nil {
		return err
	}
	c := &relay.Client{BaseURL: endpoint, Key: key}
	if err := c.Remove(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s from team %s\n", id, key.Fingerprint())
	// The cached team read no longer lists it either, nor remembers the
	// release it ran.
	unlock, err := waitLock(ctx, d, stderr)
	if err != nil {
		return err
	}
	defer unlock()
	return collect.ForgetCachedDevice(d, id)
}

func joinTeam(ctx context.Context, d state.Dir, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	var line string
	switch len(args) {
	case 0:
		var err error
		// One line, so a key pasted at a terminal needs no end-of-input.
		line, err = bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
	case 1:
		line = args[0]
	default:
		return usageError("team join takes one key")
	}
	next, err := team.Import(line)
	if err != nil {
		return err
	}
	unlock, err := waitLock(ctx, d, stderr)
	if err != nil {
		return err
	}
	defer unlock()
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	// Joining is how a new device starts, so there may be no key yet. A key
	// that does not load is kept byte for byte, and joining replaces it.
	backup := d.KeyFile() + ".previous"
	prev, err := team.Load(d.KeyFile())
	switch {
	case err == nil:
		if prev.Fingerprint() == next.Fingerprint() {
			fmt.Fprintf(stdout, "already in team %s\n", next.Fingerprint())
			return nil
		}
		if err := fsutil.WriteFile(backup, []byte(prev.Export()+"\n"), 0o600); err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
		backup = ""
	default:
		if err := os.Rename(d.KeyFile(), backup); err != nil {
			return err
		}
	}
	if err := fsutil.WriteFile(d.KeyFile(), []byte(next.Export()+"\n"), 0o600); err != nil {
		return err
	}
	_ = os.Remove(d.TeamCacheFile())
	// Best effort: take this device out of the old team.
	if endpoint := relayURL(cfg); endpoint != "" && prev != nil {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_ = (&relay.Client{BaseURL: endpoint, Key: prev}).Remove(rctx, cfg.Device)
		cancel()
	}
	fmt.Fprintf(stdout, "joined team %s\n", next.Fingerprint())
	if backup != "" {
		fmt.Fprintf(stdout, "previous key saved to %s\n", backup)
	}
	return nil
}
