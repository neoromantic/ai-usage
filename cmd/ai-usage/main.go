// Command ai-usage collects Codex, Claude, Grok, and Hermes usage on this
// device, publishes it to the team relay, and prints the team's readings.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/state"
)

// Set at release build time with -ldflags "-X main.version=v1.2.3 -X main.defaultRelay=https://…".
var (
	version      = "dev"
	defaultRelay = ""
)

// Tests replace these so they never run a real harness, edit the crontab, or
// call GitHub.
var (
	clock        = time.Now
	probeEnv     = probe.DefaultEnv
	newScheduler = schedule.Default
	newUpdater   = func() *selfupdate.Updater { return &selfupdate.Updater{Current: version, App: menuBarApp()} }
)

func main() {
	// launchd stops a run it started with SIGTERM; the run still releases its
	// lock on the way out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 {
		switch a := args[0]; {
		case !strings.HasPrefix(a, "-"), a == "-version", a == "--version", a == "-h", a == "-help", a == "--help":
			cmd, args = a, args[1:]
		}
	}
	var err error
	switch cmd {
	case "", "collect":
		err = cmdCollect(ctx, args, stdout, stderr)
	case "report":
		err = cmdReport(ctx, args, stdout)
	case "status":
		err = cmdStatus(args, stdout)
	case "team":
		err = cmdTeam(ctx, args, stdin, stdout, stderr)
	case "home":
		err = cmdHome(ctx, args, stdout, stderr)
	case "relay":
		err = cmdRelay(ctx, args, stdout, stderr)
	case "name":
		err = cmdName(args, stdout)
	case "alias":
		err = cmdAlias(args, stdout)
	case "schedule":
		err = cmdSchedule(ctx, args, stdout, stderr)
	case "update":
		err = cmdUpdate(ctx, stdout, stderr)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, version)
	case "help", "-h", "-help", "--help":
		writeHelp(stdout)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", cmd)
		writeHelp(stderr)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		writeHelp(stdout)
		return 0
	}
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "%s\n\n", err)
			writeHelp(stderr)
			return 2
		}
		fmt.Fprintf(stderr, "ai-usage: %s\n", err)
		if errors.As(err, new(argError)) {
			return 2
		}
		return 1
	}
	return 0
}

type usageError string

func (e usageError) Error() string { return string(e) }

// argError is an argument that names nothing, or more than one thing, this
// device knows. It exits 2 like a usage error, but says what there is instead
// of printing the usage.
type argError string

func (e argError) Error() string { return string(e) }

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() > 0 {
		return usageError("unexpected arguments: " + strings.Join(fs.Args(), " "))
	}
	return nil
}

func subcommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}

func loadConfig() (state.Dir, state.Config, error) {
	d, err := state.DefaultDir()
	if err != nil {
		return "", state.Config{}, err
	}
	cfg, err := d.LoadConfig()
	return d, cfg, err
}

// writeJSON prints v as every --json does, indented by two spaces.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
