package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/telemetry"
)

// Usage stats: how often ai-usage runs, which commands and which parts of
// the menu bar app are used, and what fails, sent with internal/telemetry
// once the notice has been seen. The README's Usage stats says what is sent
// and how to turn it off.

// statsCommands are the commands a run records, as the dispatcher names
// them; the bare ai-usage is collect.
var statsCommands = []string{"collect", "report", "status", "team", "home", "relay", "name", "alias", "cost",
	"schedule", "update", "app", "uninstall"}

// appFeatures are what the menu bar app records with `ai-usage telemetry
// record feature_used`.
var appFeatures = []string{"popover", "team window", "settings"}

// textCommands are the commands whose errors are sent with their text:
// what fails in them names no account, device, relay, or project. The others
// send the error's type alone.
var textCommands = []string{"collect", "report", "status", "schedule", "update", "app"}

// callerEnv names the front end that runs a command, `app` for the menu bar
// app and `bb` for the BB plugin. Their runs, such as the app reading the
// report every minute, record nothing; the app records its own events with
// `ai-usage telemetry record`.
const callerEnv = "AI_USAGE_CALLER"

// statsFlush is how long a run waits, before it exits, for the events it
// sent.
const statsFlush = 500 * time.Millisecond

// statsEndpoint is where events go, "" for the client's own. Tests point it
// at a local server.
var statsEndpoint = ""

// newStats is the usage stats client of state folder d.
func newStats(d state.Dir, channel, mode string, features []string) *telemetry.Client {
	v := strings.TrimPrefix(version, "v")
	if selfupdate.Dev(version) && !strings.Contains(strings.ToLower(v), "dev") {
		// A build such as v9.9.9-local is a development build, which sends
		// nothing unless AI_USAGE_TELEMETRY says to.
		v += "-dev"
	}
	return telemetry.New(telemetry.Options{
		Product:    "ai-usage",
		Version:    v,
		EnvPrefix:  "AI_USAGE",
		StateDir:   string(d),
		Channel:    channel,
		Mode:       mode,
		Features:   features,
		OffCommand: "ai-usage telemetry off",
		Endpoint:   statsEndpoint,
	})
}

// runStats records one run of a command, or is nil when the run records
// nothing.
type runStats struct {
	c *telemetry.Client
	// text says the command's errors go with their text.
	text bool
}

// stats is this run's.
var stats *runStats

// startStats shows the notice when it is due, and records that cmd started.
// help, version, telemetry, a command it does not know, the runs of a front
// end, and the binary brew installed finishing `ai-usage update` record
// nothing.
func startStats(cmd string, args []string, stderr io.Writer) *runStats {
	cmd = cmp.Or(cmd, "collect")
	if !slices.Contains(statsCommands, cmd) || os.Getenv(callerEnv) != "" || (cmd == "update" && hasFlag(args, "after-brew")) {
		return nil
	}
	d, err := state.DefaultDir()
	mode := ""
	switch {
	case cmd == "collect":
		// The scheduler names the state folder, and asks for quiet.
		if home, ok := flagValue(args, "home"); ok && home != "" {
			d, err = state.Dir(home), nil
		}
		if hasFlag(args, "quiet") {
			mode = "scheduled"
		}
	case cmd == "schedule" && slices.Equal(args, []string{"run"}):
		mode = "daemon"
	case cmd == "relay" && len(args) > 0 && args[0] == "serve":
		mode = "server"
	}
	if err != nil {
		return nil
	}
	c := newStats(d, installChannel(), mode, statsCommands)
	// status before the first run creates nothing, so an installer can ask
	// first; the notice waits for another command. A scheduled run shows
	// nobody the notice, though on Windows its hidden console is a
	// terminal.
	if _, err := os.Stat(d.StateFile()); mode != "scheduled" && (cmd != "status" || err == nil) {
		c.ShowNotice(stderr)
	}
	c.Started()
	c.Feature(cmd)
	return &runStats{c: c, text: slices.Contains(textCommands, cmd) && !(cmd == "report" && hasFlag(args, "from"))}
}

// failed records a command's error. A run stopped by a signal failed at
// nothing.
func (s *runStats) failed(ctx context.Context, err error) {
	if s == nil || err == nil || ctx.Err() != nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	var (
		ue usageError
		ae argError
		ec exitCode
		pe *panicError
	)
	switch {
	case errors.As(err, &ue):
		// Usage errors repeat what was typed.
		err = telemetry.TypeOnly("usage")
	case errors.As(err, &ae):
		err = telemetry.TypeOnly("argument")
	case errors.As(err, &ec):
		err = telemetry.TypeOnly("exit " + strconv.Itoa(int(ec)))
	case !s.text:
		err = withoutText(err)
	case errors.As(err, &pe):
		// The panic's value, without the stack.
		err = &panicError{value: pe.value}
	}
	s.c.Error(err)
}

// sources records each tool that failed in a collection by its name alone:
// its error can hold an account, a device, or a folder.
func (s *runStats) sources(st *state.State) {
	if s == nil || st == nil {
		return
	}
	for _, p := range snapshot.Providers {
		if st.Sources[p].Status == "error" {
			s.c.Error(telemetry.TypeOnly("source " + p))
		}
	}
}

// flush waits briefly for the events in flight.
func (s *runStats) flush() {
	if s != nil {
		s.c.Flush(statsFlush)
	}
}

// withoutText keeps err's type for usage stats, such as ENOENT, and drops
// its text: a wrapper with an empty message.
func withoutText(err error) error { return fmt.Errorf("%.0w", err) }

// installChannel says how this binary was installed: by Homebrew; by
// install.sh or install.ps1, which put it in ~/.local/bin or ~/bin, or in
// %LOCALAPPDATA%\Programs\ai-usage; else by hand.
func installChannel() string {
	if brewPrefix() != "" {
		return "homebrew"
	}
	exe, err := selfExe()
	if err != nil {
		return "binary"
	}
	exe = fsutil.RealPath(exe)
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".local", "bin", "ai-usage"), filepath.Join(home, "bin", "ai-usage"))
	}
	if runtime.GOOS == "windows" && os.Getenv("LOCALAPPDATA") != "" {
		paths = []string{filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "ai-usage", "ai-usage.exe")}
	}
	for _, p := range paths {
		if strings.EqualFold(fsutil.RealPath(p), exe) {
			return "install-script"
		}
	}
	return "binary"
}

// hasFlag says args set the boolean flag name, as -name, --name, or
// --name=true.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		k, v, valued := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		if strings.HasPrefix(a, "-") && k == name {
			on, err := strconv.ParseBool(v)
			return !valued || (err == nil && on)
		}
	}
	return false
}

// flagValue is the value args give flag name, as -name V, --name V, or
// --name=V.
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		k, v, valued := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
		switch {
		case !strings.HasPrefix(a, "-") || k != name:
		case valued:
			return v, true
		case i+1 < len(args):
			return args[i+1], true
		}
	}
	return "", false
}

// cmdTelemetry is `ai-usage telemetry`: whether usage stats are sent, and
// the switch. Two forms are for the menu bar app and not in the help:
// `notice --seen`, after it showed the notice itself, and `record EVENT
// [FEATURE]`, its own events.
func cmdTelemetry(args []string, stdout io.Writer) error {
	sub, args := subcommand(args)
	d, err := state.DefaultDir()
	if err != nil {
		return err
	}
	if sub == "record" {
		return recordApp(d, args)
	}
	c := newStats(d, installChannel(), "", statsCommands)
	switch sub {
	case "", "status":
		fs := flags("telemetry status")
		jsonOut := fs.Bool("json", false, "")
		if err := parse(fs, args); err != nil {
			return err
		}
		if *jsonOut {
			return writeStatsJSON(stdout, c)
		}
	case "on", "off":
		if len(args) > 0 {
			return usageError("telemetry " + sub + " takes no arguments")
		}
		if err := c.SetEnabled(sub == "on"); err != nil {
			return err
		}
	case "notice":
		fs := flags("telemetry notice")
		seen := fs.Bool("seen", false, "")
		if err := parse(fs, args); err != nil {
			return err
		}
		if *seen {
			c.MarkNoticeSeen()
		} else {
			// Printed, and marked seen, only on a terminal or for an AI
			// agent: an installer run in the background, as BB's, shows
			// nobody the notice.
			c.ShowNotice(stdout)
		}
		return nil
	default:
		return usageError("unknown telemetry command " + sub)
	}
	_, err = io.WriteString(stdout, statsText(c.Status(), c.NoticeText() != ""))
	return err
}

// recordApp is `ai-usage telemetry record app_started|feature_used FEATURE`,
// an event of the menu bar app's.
func recordApp(d state.Dir, args []string) error {
	c := newStats(d, "app", "app", appFeatures)
	switch {
	case slices.Equal(args, []string{"app_started"}):
		c.Started()
	case len(args) == 2 && args[0] == "feature_used":
		c.Feature(args[1])
	default:
		return usageError("telemetry record takes app_started, or feature_used and a feature")
	}
	c.Flush(statsFlush)
	return nil
}

// writeStatsJSON prints the client's status, and the notice while it is
// due, else null, for the menu bar app to show.
func writeStatsJSON(w io.Writer, c *telemetry.Client) error {
	b, err := json.Marshal(c.Status())
	if err != nil {
		return err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	out["notice"] = nil
	if n := c.NoticeText(); n != "" {
		out["notice"] = n
	}
	return writeJSON(w, out)
}

// statsReasons say why usage stats are on or off, by the client's reason.
var statsReasons = map[string]string{
	"no-endpoint":  "this build sends nothing",
	"do-not-track": "DO_NOT_TRACK is set",
	"env":          "AI_USAGE_TELEMETRY is set",
	"ci":           "in CI",
	"test":         "in tests",
	"dev-version":  "a development build",
	"default":      "the default",
}

// statsText is what `ai-usage telemetry status` prints.
func statsText(s telemetry.Status, due bool) string {
	var b strings.Builder
	on := "off"
	if s.Enabled {
		on = "on"
	}
	why := statsReasons[s.Reason]
	switch {
	case s.Reason == "state":
		why = "set with `ai-usage telemetry " + on + "`"
	case s.Debug:
		why = "AI_USAGE_TELEMETRY=debug prints each event to stderr and sends none"
	}
	fmt.Fprintf(&b, "usage stats  %s: %s\n", on, cmp.Or(why, s.Reason))
	if due {
		fmt.Fprintf(&b, "notice       not seen yet, so nothing is sent; `ai-usage telemetry notice` prints it\n")
	}
	fmt.Fprintf(&b, "install id   %s\n", cmp.Or(s.ID, "none yet"))
	fmt.Fprintf(&b, "state file   %s\n", s.StateFile)
	fmt.Fprintf(&b, "details      https://usage.sergeybaus.com\n")
	return b.String()
}
