// Command ai-usage collects Codex, Claude, Grok, and Hermes usage on this
// device, publishes it to the team relay, and prints the team's readings.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/view"
	"github.com/neoromantic/ai-usage/relay"
)

// Set at release build time with -ldflags "-X main.version=v1.2.3 -X main.defaultRelay=https://…".
var (
	version      = "dev"
	defaultRelay = ""
)

// updateEvery throttles release checks.
const updateEvery = 6 * time.Hour

// updateTimeout bounds a release check in a run. A slow link needs minutes
// for the download; the run lock is taken over after 10.
const updateTimeout = 7 * time.Minute

// Tests replace these so they never run a real harness, edit the crontab, or
// call GitHub.
var (
	clock        = time.Now
	probeEnv     = probe.DefaultEnv
	newScheduler = schedule.Default
	newUpdater   = func() *selfupdate.Updater { return &selfupdate.Updater{Current: version} }
)

const usage = `ai-usage collects AI harness usage on this device and shares it with a team.

Usage:
  ai-usage [--json] [--offline] [VIEW] [DISPLAY]
                                         collect now and print the report
  ai-usage collect [--quiet] [--json] [--offline] [--home DIR] [VIEW] [DISPLAY]
                                         what the system scheduler runs; --home overrides AI_USAGE_HOME
  ai-usage report [--json] [VIEW] [DISPLAY]
                                         print the last collected report, no collection
  ai-usage status [--json] [DISPLAY]     collector health: version, last success, last error
  ai-usage team                          show the team fingerprint and devices
  ai-usage team key                      print the team private key (the only secret)
  ai-usage team join [KEY]               join a team; the key is read from stdin when omitted
  ai-usage team forget-device ID         remove a device's snapshot from the relay
  ai-usage home                          list the harness homes this device reads
  ai-usage home add PROVIDER DIR... [--quota-from PROVIDER:DIR]
                                         read more homes; --quota-from names the Codex or
                                         Grok home whose login these Hermes homes bill through
  ai-usage home remove PROVIDER DIR...   stop reading homes added before
  ai-usage relay show|set URL|clear      choose the relay this device publishes to
  ai-usage relay serve [--addr :8080] [--client-ip-header NAME]
                                         run a relay (Vercel KV from env, else memory)
  ai-usage schedule install|remove|status
  ai-usage update                        check for a release now
  ai-usage version

Views, one at a time (the default is accounts, devices, and this device):
  --projects             every project on this device
  --tokens               every team account's tokens, device by device
  --devices              every team device, none folded away

Display:
  --color=auto|always|never
                         auto colors a terminal, unless NO_COLOR is set or TERM=dumb
  --ascii                ASCII glyphs; the default without a UTF-8 locale
  --width N              columns, 80 to 160; default: the terminal's, else COLUMNS, else 80

Environment:
  AI_USAGE_HOME          collector directory (default: OS config dir/ai-usage)
  AI_USAGE_RELAY         relay URL, overrides the configured one
  AI_USAGE_NO_SCHEDULE   set to skip scheduler registration on this run
  CLAUDE_CONFIG_DIR, CODEX_HOME, GROK_HOME, HERMES_HOME   extra harness homes
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
		err = cmdCollect(ctx, args, stdout)
	case "report":
		err = cmdReport(args, stdout)
	case "status":
		err = cmdStatus(args, stdout)
	case "team":
		err = cmdTeam(ctx, args, stdin, stdout)
	case "home":
		err = cmdHome(args, stdout)
	case "relay":
		err = cmdRelay(ctx, args, stdout, stderr)
	case "schedule":
		err = cmdSchedule(ctx, args, stdout)
	case "update":
		err = cmdUpdate(ctx, stdout)
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, version)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "%s\n\n%s", err, usage)
			return 2
		}
		fmt.Fprintf(stderr, "ai-usage: %s\n", err)
		return 1
	}
	return 0
}

type usageError string

func (e usageError) Error() string { return string(e) }

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

func dir() (state.Dir, error) { return state.DefaultDir() }

func relayURL(cfg state.Config) string {
	if v := strings.TrimSpace(os.Getenv("AI_USAGE_RELAY")); v != "" {
		return v
	}
	if cfg.Relay != "" {
		return cfg.Relay
	}
	return defaultRelay
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSuffix(h, ".local")
}

func osUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return os.Getenv("USERNAME")
}

func cmdCollect(ctx context.Context, args []string, stdout io.Writer) (err error) {
	fs := flags("collect")
	jsonOut := fs.Bool("json", false, "")
	quiet := fs.Bool("quiet", false, "")
	offline := fs.Bool("offline", false, "")
	home := fs.String("home", "", "")
	disp := displayFlags(fs, true)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := disp.check(); err != nil {
		return err
	}
	d, err := dir()
	if *home != "" {
		// The scheduler names the folder, since it does not see AI_USAGE_HOME.
		d, err = state.Dir(*home), nil
	}
	if err != nil {
		return err
	}
	// A failure before housekeeping still gets its release check, so a
	// release that breaks collection can be replaced by the one that fixes it.
	housekept := false
	defer func() {
		if err != nil && !housekept {
			rescue(ctx, d, err)
		}
	}()
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	endpoint := relayURL(cfg)
	opts := collect.Options{
		Dir:      d,
		Version:  version,
		Probe:    probeEnv(),
		Hostname: hostname(),
		OSUser:   osUser(),
		Now:      clock,
		After: func(ctx context.Context, cfg *state.Config, st *state.State) {
			housekeeping(ctx, d, cfg, st)
			housekept = true
		},
	}
	if endpoint != "" && !*offline {
		// Run signs with the key it loads under the run lock.
		opts.Relay = &relay.Client{BaseURL: endpoint}
	}
	res, err := runCollect(ctx, opts)
	if err != nil {
		return err
	}
	if *quiet {
		return nil
	}
	return printReport(stdout, d, res, endpoint, *jsonOut, disp)
}

// panicError is a collection that panicked.
type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string {
	return fmt.Sprintf("collection stopped by a bug: %v\n%s", e.value, e.stack)
}

// runCollect turns a panic in a collection into an error, so the run can still
// record it and look for a release that fixes it.
func runCollect(ctx context.Context, o collect.Options) (res *collect.Result, err error) {
	defer func() {
		if v := recover(); v != nil {
			res, err = nil, &panicError{v, debug.Stack()}
		}
	}()
	return collect.Run(ctx, o)
}

// rescue runs when a collection failed before its housekeeping: it keeps a
// panic as the last error and still checks for a release. A state.json that
// does not load is left as it is, and the check is then not throttled.
func rescue(ctx context.Context, d state.Dir, cause error) {
	var pe *panicError
	panicked := errors.As(cause, &pe)
	if !panicked && selfupdate.Dev(version) {
		return
	}
	// A run that holds the lock does its own housekeeping.
	unlock, err := d.Lock(10 * time.Minute)
	if err != nil {
		return
	}
	defer unlock()
	now := clock().UTC()
	st, err := d.LoadState()
	loaded := err == nil
	if !loaded {
		st = &state.State{}
	}
	changed := false
	if panicked {
		msg, _, _ := strings.Cut(pe.Error(), "\n")
		if len(msg) > 600 {
			msg = strings.ToValidUTF8(msg[:600], "")
		}
		st.LastError, st.LastErrorAt = msg, now
		changed = true
	}
	if updateIfDue(ctx, st, now) {
		changed = true
	}
	if loaded && changed {
		_ = d.SaveState(st)
	}
}

// housekeeping registers with the scheduler and applies self-update, both
// inside the run lock. A development build does neither on its own.
func housekeeping(ctx context.Context, d state.Dir, cfg *state.Config, st *state.State) {
	now := clock().UTC()
	// The release staged by an earlier run is this binary now, or was replaced.
	if st.Update.Installed != "" && !selfupdate.Newer(st.Update.Installed, version) {
		st.Update.Installed = ""
	}
	if !selfupdate.Dev(version) && !cfg.ScheduleOff && os.Getenv("AI_USAGE_NO_SCHEDULE") == "" {
		ensureSchedule(ctx, d, st, now)
	} else if cfg.ScheduleOff {
		st.Schedule.Registered = false
		st.Schedule.Error = "removed by `ai-usage schedule remove`"
	}
	updateIfDue(ctx, st, now)
}

// updateIfDue checks for a release on a release build at most every
// updateEvery, and reports whether it did. A check time in the future, left
// by a clock that once ran ahead, counts as due: otherwise updates would stop
// until the clock caught up with it.
func updateIfDue(ctx context.Context, st *state.State, now time.Time) bool {
	if selfupdate.Dev(version) {
		return false
	}
	if since := now.Sub(st.Update.CheckedAt); since >= 0 && since < updateEvery {
		return false
	}
	uctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	res, err := newUpdater().Check(uctx)
	noteUpdate(st, now, res, err)
	return true
}

func noteUpdate(st *state.State, now time.Time, res selfupdate.Result, err error) {
	st.Update.CheckedAt = now
	if res.Latest != "" {
		// A failed check keeps the last release it did see.
		st.Update.Latest = res.Latest
	}
	st.Update.Error = ""
	if err != nil {
		st.Update.Error = err.Error()
	}
	if res.Installed {
		st.Update.Installed = res.Latest
	}
}

// disabledByHand is the schedule error for an entry the person paused.
const disabledByHand = "disabled by hand in the system scheduler; `ai-usage schedule install` turns it back on"

// ensureSchedule registers this binary and state folder unless the scheduler
// already runs them. An entry the person commented out or disabled stays so.
func ensureSchedule(ctx context.Context, d state.Dir, st *state.State, now time.Time) {
	st.Schedule.CheckedAt = now
	exe, home, err := job(d)
	if err != nil {
		st.Schedule.Registered, st.Schedule.Error = false, err.Error()
		return
	}
	s := newScheduler()
	got, err := s.Lookup(ctx, exe, home)
	if err == nil && got == schedule.Disabled {
		st.Schedule.Registered, st.Schedule.Error = false, disabledByHand
		return
	}
	if err == nil && got != schedule.Active {
		err = s.Install(ctx, exe, home, os.Getenv("PATH"))
	}
	st.Schedule.Registered = err == nil
	st.Schedule.Error = ""
	if err != nil {
		st.Schedule.Error = err.Error()
	}
}

// job is what the scheduler should run: this binary, collecting into the
// absolute state folder, since cron starts in the home directory.
func job(d state.Dir) (exe, home string, err error) {
	if exe, err = executable(); err != nil {
		return "", "", err
	}
	if home, err = filepath.Abs(string(d)); err != nil {
		return "", "", err
	}
	return exe, home, nil
}

func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}

func printReport(stdout io.Writer, d state.Dir, res *collect.Result, endpoint string, jsonOut bool, disp *display) error {
	now := clock().UTC()
	samples, _ := d.LoadSamples(now.Add(-7 * 24 * time.Hour))
	r := view.Build(view.Input{
		Version:  version,
		RelayURL: endpoint,
		Config:   res.Config,
		State:    res.State,
		Key:      res.Key,
		Doc:      res.Doc,
		Team:     res.Team,
		Samples:  samples,
		Hostname: hostname(),
		OSUser:   osUser(),
		Now:      now,
	})
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	_, err := io.WriteString(stdout, view.Text(r, disp.options(stdout)))
	return err
}

// loadResult rebuilds a run result from disk without collecting.
func loadResult(d state.Dir) (*collect.Result, error) {
	cfg, err := d.LoadConfig()
	if err != nil {
		return nil, err
	}
	key, _, err := collect.LoadKey(d)
	if err != nil {
		return nil, err
	}
	st, err := d.LoadState()
	if err != nil {
		return nil, err
	}
	cache, _ := collect.LoadTeamCache(d)
	doc := collect.BuildDoc(st, key, cfg.Device, hostname(), osUser(), version, st.LastRunAt)
	return &collect.Result{Config: cfg, State: st, Key: key, Doc: doc, Team: cache}, nil
}

func cmdReport(args []string, stdout io.Writer) error {
	fs := flags("report")
	jsonOut := fs.Bool("json", false, "")
	disp := displayFlags(fs, true)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := disp.check(); err != nil {
		return err
	}
	d, err := dir()
	if err != nil {
		return err
	}
	// Checked before loadResult, which would create a team key and a device id.
	if st, err := d.LoadState(); err != nil {
		return err
	} else if st.LastRunAt.IsZero() {
		return errors.New("nothing collected yet; run `ai-usage` first")
	}
	res, err := loadResult(d)
	if err != nil {
		return err
	}
	return printReport(stdout, d, res, relayURL(res.Config), *jsonOut, disp)
}

func cmdStatus(args []string, stdout io.Writer) error {
	fs := flags("status")
	jsonOut := fs.Bool("json", false, "")
	disp := displayFlags(fs, false)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := disp.check(); err != nil {
		return err
	}
	d, err := dir()
	if err != nil {
		return err
	}
	res, err := loadResult(d)
	if err != nil {
		return err
	}
	r := view.Build(view.Input{
		Version: version, RelayURL: relayURL(res.Config), Config: res.Config, State: res.State,
		Key: res.Key, Doc: res.Doc, Team: res.Team, Hostname: hostname(), OSUser: osUser(), Now: clock().UTC(),
	})
	type source struct {
		Provider string   `json:"provider"`
		Status   string   `json:"status"`
		Error    *string  `json:"error"`
		Homes    []string `json:"homes"`
	}
	out := struct {
		SchemaVersion int            `json:"schema_version"`
		Collector     view.Collector `json:"collector"`
		Sources       []source       `json:"sources"`
	}{SchemaVersion: view.SchemaVersion, Collector: r.Collector}
	for _, p := range r.Providers {
		out.Sources = append(out.Sources, source{p.Provider, p.Status, p.Error, p.Homes})
	}
	if *jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	_, err = io.WriteString(stdout, view.StatusText(r, string(d), disp.options(stdout)))
	return err
}

func cmdTeam(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	d, err := dir()
	if err != nil {
		return err
	}
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "":
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
			fmt.Fprintf(stdout, "  %s  %s (%s)  collected %s  %s\n", doc.Device, label, who, doc.CollectedAt.Local().Format("2006-01-02 15:04"), doc.CollectorVersion)
		}
		return nil
	case "key":
		if len(args) > 0 {
			return usageError("team key takes no arguments")
		}
		key, _, err := collect.LoadKey(d)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, key.Export())
		return nil
	case "join":
		var line string
		switch len(args) {
		case 0:
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
		return joinTeam(ctx, d, line, stdout)
	case "forget-device":
		if len(args) != 1 {
			return usageError("team forget-device takes one device id")
		}
		if !snapshot.ValidDevice(args[0]) {
			return usageError("not a device id: " + args[0])
		}
		cfg, err := d.LoadConfig()
		if err != nil {
			return err
		}
		endpoint := relayURL(cfg)
		if endpoint == "" {
			return errors.New("no relay configured; set one with `ai-usage relay set URL`")
		}
		key, _, err := collect.LoadKey(d)
		if err != nil {
			return err
		}
		c := &relay.Client{BaseURL: endpoint, Key: key}
		if err := c.Remove(ctx, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s from team %s\n", args[0], key.Fingerprint())
		// The cached team read no longer lists it either.
		unlock, err := d.Lock(10 * time.Minute)
		if err != nil {
			return err
		}
		defer unlock()
		return collect.ForgetCachedDevice(d, args[0])
	default:
		return usageError("unknown team command " + sub)
	}
}

func joinTeam(ctx context.Context, d state.Dir, line string, stdout io.Writer) error {
	next, err := team.Import(line)
	if err != nil {
		return err
	}
	unlock, err := d.Lock(10 * time.Minute)
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
		if err := prev.Save(backup); err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
		backup = ""
	default:
		if err := os.Rename(d.KeyFile(), backup); err != nil {
			return err
		}
	}
	if err := next.Save(d.KeyFile()); err != nil {
		return err
	}
	_ = os.Remove(d.Path("team-cache.json"))
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

func cmdRelay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if sub == "serve" {
		fs := flags("relay serve")
		addr := fs.String("addr", ":8080", "")
		ipHeader := fs.String("client-ip-header", "", "")
		if err := parse(fs, args); err != nil {
			return err
		}
		header := strings.TrimSpace(*ipHeader)
		if strings.ContainsAny(header, " \t:") {
			return usageError("--client-ip-header takes a header name, such as X-Real-Ip")
		}
		store, kind := relay.StoreFromEnv()
		handler := relay.NewServer(store, relay.Limits{})
		// Behind a reverse proxy, the header it sets names the client.
		handler.ClientIPHeader = header
		srv := &http.Server{
			Addr:              *addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    16 << 10,
		}
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		clients := "the connection's address"
		if header != "" {
			clients = header + " from a local proxy"
		}
		fmt.Fprintf(stderr, "relay listening on %s with %s store; clients by %s\n", *addr, kind, clients)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	d, err := dir()
	if err != nil {
		return err
	}
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	switch sub {
	case "", "show":
		if endpoint := relayURL(cfg); endpoint == "" {
			fmt.Fprintln(stdout, "no relay configured")
		} else {
			fmt.Fprintln(stdout, endpoint)
		}
		return nil
	case "set":
		if len(args) != 1 {
			return usageError("relay set takes one URL")
		}
		u := strings.TrimRight(strings.TrimSpace(args[0]), "/")
		if p, err := url.Parse(u); err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || p.RawQuery != "" || p.Fragment != "" {
			return usageError("relay URL must look like https://host or https://host/path")
		}
		cfg.Relay = u
		if err := d.SaveConfig(cfg); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "relay set to %s\n", u)
		return nil
	case "clear":
		cfg.Relay = ""
		if err := d.SaveConfig(cfg); err != nil {
			return err
		}
		if defaultRelay != "" {
			fmt.Fprintf(stdout, "relay reset to the default %s\n", defaultRelay)
		} else {
			fmt.Fprintln(stdout, "relay cleared")
		}
		return nil
	default:
		return usageError("unknown relay command " + sub)
	}
}

func cmdSchedule(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return usageError("schedule takes install, remove, or status")
	}
	d, err := dir()
	if err != nil {
		return err
	}
	cfg, err := d.LoadConfig()
	if err != nil {
		return err
	}
	s := newScheduler()
	exe, home, err := job(d)
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		if err := s.Install(ctx, exe, home, os.Getenv("PATH")); err != nil {
			return err
		}
		cfg.ScheduleOff = false
		if err := d.SaveConfig(cfg); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "registered %s to run every %s with state folder %s\n", exe, schedule.Interval, home)
	case "remove":
		if err := s.Remove(ctx); err != nil {
			return err
		}
		cfg.ScheduleOff = true
		if err := d.SaveConfig(cfg); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "removed from the system scheduler; later runs will not register again until `ai-usage schedule install`")
	case "status":
		got, err := s.Lookup(ctx, exe, home)
		if err != nil {
			return err
		}
		switch got {
		case schedule.Active:
			fmt.Fprintf(stdout, "registered: %s runs every %s with state folder %s\n", exe, schedule.Interval, home)
		case schedule.Other:
			fmt.Fprintln(stdout, "registered, but for a different binary path or state folder; `ai-usage schedule install` registers this one")
		case schedule.Disabled:
			fmt.Fprintln(stdout, disabledByHand)
		default:
			fmt.Fprintln(stdout, "not registered")
		}
	default:
		return usageError("schedule takes install, remove, or status")
	}
	return nil
}

func cmdUpdate(ctx context.Context, stdout io.Writer) error {
	if selfupdate.Dev(version) {
		return errors.New("this is a development build (" + version + "); install a release to self-update")
	}
	d, err := dir()
	if err != nil {
		return err
	}
	// The lock keeps a scheduled run from replacing the binary at the same time.
	unlock, err := d.Lock(10 * time.Minute)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := d.LoadState()
	if err != nil {
		return err
	}
	res, err := newUpdater().Check(ctx)
	noteUpdate(st, clock().UTC(), res, err)
	if serr := d.SaveState(st); serr != nil && err == nil {
		err = serr
	}
	if err != nil {
		return err
	}
	if res.Installed {
		fmt.Fprintf(stdout, "installed %s; the next run uses it\n", res.Latest)
	} else {
		fmt.Fprintf(stdout, "up to date (%s; latest %s)\n", version, res.Latest)
	}
	return nil
}
