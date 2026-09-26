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
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/neoromantic/ai-usage/internal/collect"
	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/schedule"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/internal/team"
	"github.com/neoromantic/ai-usage/internal/tui"
	"github.com/neoromantic/ai-usage/internal/view"
	"github.com/neoromantic/ai-usage/relay"
)

// Set at release build time with -ldflags "-X main.version=v1.2.3 -X main.defaultRelay=https://…".
var (
	version      = "dev"
	defaultRelay = ""
)

// updateFloor is the least time between release checks. Scheduled runs are
// 15 minutes apart, so each of them checks, and a release reaches a device
// within about a quarter of an hour. Runs a person starts in between, such as
// the view's r, do not ask GitHub again.
const updateFloor = 10 * time.Minute

// updateTimeout bounds a release check in a run. A slow link needs minutes
// for the download. Finding the latest release has a shorter limit of its own.
const updateTimeout = 7 * time.Minute

// retryFailed is how long a release that was downloaded and did not install,
// such as one that does not start here, is not downloaded again. Runs still
// look up the latest release, so a newer one is installed at once.
const retryFailed = 6 * time.Hour

// Tests replace these so they never run a real harness, edit the crontab, or
// call GitHub.
var (
	clock        = time.Now
	probeEnv     = probe.DefaultEnv
	newScheduler = schedule.Default
	newUpdater   = func() *selfupdate.Updater { return &selfupdate.Updater{Current: version} }
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

// deviceName is what the team calls this device: AI_USAGE_NAME, else the
// name set with `ai-usage name set`, else the host name.
func deviceName(cfg state.Config) string {
	if v, ok := envName(); ok {
		return v
	}
	if cfg.Name != "" {
		return cfg.Name
	}
	return hostname()
}

// envName is AI_USAGE_NAME when it is set and a name `ai-usage name set`
// would take.
func envName() (string, bool) {
	v := strings.TrimSpace(os.Getenv("AI_USAGE_NAME"))
	return v, v != "" && validName(v) == nil
}

// maxName keeps a device name short enough for the report's columns.
const maxName = 64

// validName says what is wrong with a device name, if anything.
func validName(n string) error {
	switch {
	case n == "":
		return errors.New("a name cannot be empty")
	case len([]rune(n)) > maxName:
		return fmt.Errorf("a name is at most %d characters", maxName)
	case strings.ContainsFunc(n, unicode.IsControl):
		return errors.New("a name cannot contain control characters")
	case snapshot.Printable(n) != n:
		return errors.New("a name cannot contain invisible characters that change the text's direction")
	}
	return nil
}

// hostname and osUser name this device in reports. Tests pin them.
var hostname = func() string {
	// macOS takes its host name from the network, so one Mac can read as
	// Mac.localdomain on one network and by its own name on another. Its
	// local host name, set in Sharing settings, stays put.
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("/usr/sbin/scutil", "--get", "LocalHostName").Output(); err == nil {
			if h := strings.TrimSpace(string(out)); h != "" {
				return h
			}
		}
	}
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSuffix(h, ".local")
}

var osUser = func() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return os.Getenv("USERNAME")
}

func cmdCollect(ctx context.Context, args []string, stdout, stderr io.Writer) error {
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
	guide := false
	res, err := collection{
		d:         d,
		offline:   *offline,
		scheduled: *quiet,
		waiting: func() {
			fmt.Fprintln(stderr, "ai-usage: another run, such as the scheduled one, is collecting now; waiting for its result")
		},
		locked: func(st *state.State) {
			// The guide waits for a report a person reads, even when the
			// scheduler collected first.
			if st.GuideDue && !*quiet && !*jsonOut {
				st.GuideDue, guide = false, true
			}
		},
	}.run(ctx)
	if err != nil {
		return err
	}
	if *quiet {
		return nil
	}
	// A run that waited shows the other run's result and has no After of
	// its own, so it takes the guide here.
	if res.Waited && res.State.GuideDue && !*jsonOut {
		guide = takeGuide(d)
	}
	if disp.interactive(stdout, *jsonOut, guide) {
		return tui.Run(ctx, viewConfig(d, res, disp, *offline, stdout), os.Stdin, stdout)
	}
	return printReport(stdout, reportAt(d, res, clock().UTC()), *jsonOut, guide, disp)
}

// takeGuide clears the guide's due mark under the run lock and reports
// whether this run should print it. While another run holds the lock, the
// guide is left for a later report.
func takeGuide(d state.Dir) bool {
	unlock, err := d.Lock()
	if err != nil {
		return false
	}
	defer unlock()
	st, err := d.LoadState()
	if err != nil || !st.GuideDue {
		return false
	}
	st.GuideDue = false
	return d.SaveState(st) == nil
}

// panicError is a collection that panicked.
type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string {
	return fmt.Sprintf("collection stopped by a bug: %v\n%s", e.value, e.stack)
}

// collection is a collection and its housekeeping, as `collect` and `r` in
// the interactive view run it. scheduled says the scheduler started it, with
// collect --quiet. waiting is called when a run that is not scheduled starts
// to wait for one that holds the run lock. locked, when set, runs under the
// run lock after housekeeping.
type collection struct {
	d                  state.Dir
	offline, scheduled bool
	waiting            func()
	locked             func(*state.State)
}

func (c collection) run(ctx context.Context) (res *collect.Result, err error) {
	// A failure before housekeeping still gets its release check, so a
	// release that breaks collection can be replaced by the one that fixes it.
	// A run stopped by a signal, or by the view closing, failed at nothing.
	housekept := false
	defer func() {
		if err != nil && !housekept && ctx.Err() == nil {
			rescue(ctx, c.d, err, c.scheduled)
		}
	}()
	// A panic becomes an error before the rescue reads it, so the run can
	// still record it and look for a release that fixes it.
	defer func() {
		if v := recover(); v != nil {
			res, err = nil, &panicError{v, debug.Stack()}
		}
	}()
	cfg, err := c.d.LoadConfig()
	if err != nil {
		return nil, err
	}
	opts := collect.Options{
		Dir:      c.d,
		Version:  version,
		Probe:    probeEnv(),
		Hostname: deviceName(cfg),
		OSUser:   osUser(),
		Now:      clock,
		After: func(ctx context.Context, cfg *state.Config, st *state.State) {
			housekeeping(ctx, c.d, cfg, st, c.scheduled)
			housekept = true
			if c.locked != nil {
				c.locked(st)
			}
		},
	}
	if endpoint := relayURL(cfg); endpoint != "" && !c.offline {
		// Run signs with the key it loads under the run lock.
		opts.Relay = &relay.Client{BaseURL: endpoint}
	}
	if c.scheduled {
		opts.PullEvery = time.Hour
	} else {
		// A run the person started while another, usually the scheduled
		// one, collects waits for it and shows its result rather than
		// collect twice. The scheduler runs with --quiet and just skips.
		opts.Wait = lockWait
		opts.Waiting = c.waiting
	}
	return collect.Run(ctx, opts)
}

// rescue runs when a collection failed before its housekeeping: it keeps a
// panic as the last error and still checks for a release. A state.json that
// does not load is left as it is, and the check is then not throttled.
func rescue(ctx context.Context, d state.Dir, cause error, scheduled bool) {
	var pe *panicError
	panicked := errors.As(cause, &pe)
	if !panicked && selfupdate.Dev(version) {
		return
	}
	// A run that holds the lock does its own housekeeping.
	unlock, err := d.Lock()
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
	if updateIfDue(ctx, st, now, scheduled) {
		changed = true
	}
	if loaded && changed {
		_ = d.SaveState(st)
	}
}

// housekeeping registers with the scheduler and applies self-update, both
// inside the run lock. A development build does neither on its own. scheduled
// says the scheduler started the run, with collect --quiet.
func housekeeping(ctx context.Context, d state.Dir, cfg *state.Config, st *state.State, scheduled bool) {
	now := clock().UTC()
	// The release staged by an earlier run is this binary now, or was replaced.
	if st.Update.Installed != "" && !selfupdate.Newer(st.Update.Installed, version) {
		st.Update.Installed = ""
	}
	switch {
	case d.Foreground():
		// `ai-usage schedule run` is the scheduler, so the system's is not
		// needed; a run never registers there while it runs.
		st.Schedule = state.Schedule{Registered: true, CheckedAt: now, Foreground: true}
	case !selfupdate.Dev(version) && !cfg.ScheduleOff && os.Getenv("AI_USAGE_NO_SCHEDULE") == "":
		ensureSchedule(ctx, d, st, now)
	case cfg.ScheduleOff:
		st.Schedule.Registered, st.Schedule.Foreground = false, false
		st.Schedule.Error = "removed by `ai-usage schedule remove`"
	}
	updateIfDue(ctx, st, now, scheduled)
}

// updateIfDue checks for a release on a release build, unless the last check
// was less than updateFloor ago, and reports whether it did. A scheduled run
// tries a failed check again, so a failure that passes shows for one run at
// most. A run a person starts, such as the view's r, does not, so it never
// asks GitHub twice within updateFloor. A check time in the future, left by a
// clock that once ran ahead, counts as due: otherwise updates would stop
// until the clock caught up with it.
func updateIfDue(ctx context.Context, st *state.State, now time.Time, scheduled bool) bool {
	if selfupdate.Dev(version) {
		return false
	}
	if since := now.Sub(st.Update.CheckedAt); since >= 0 && since < updateFloor && (st.Update.Error == "" || !scheduled) {
		return false
	}
	u := newUpdater()
	// A release that was downloaded and did not install would most likely
	// fail the same way at every run.
	if f := st.Update.Failed; f != nil {
		if since := now.Sub(f.At); since >= 0 && since < retryFailed {
			u.Skip = f.Tag
		}
	}
	uctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	res, err := u.Check(uctx)
	if err != nil && ctx.Err() != nil {
		// A check the run's stop cut short is left for the next run.
		return false
	}
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
	switch {
	case errors.Is(err, selfupdate.ErrSkipped) && st.Update.Failed != nil:
		// The latest release is still the one that failed, for the same
		// reason as far as anyone knows.
		st.Update.Error = st.Update.Failed.Error
	case err != nil:
		st.Update.Error = err.Error()
		if res.Downloaded {
			st.Update.Failed = &state.FailedRelease{Tag: res.Latest, At: now, Error: err.Error()}
		}
	default:
		st.Update.Failed = nil
	}
	if res.Installed {
		st.Update.Installed = res.Latest
	}
}

// lockWait is how long a command the person started waits for another run,
// such as a scheduled one, to release the run lock. A scheduled run on a Mac
// takes up to about a minute at background priority.
const lockWait = 3 * time.Minute

// waitLock takes the run lock for a command the person started that changes
// what a collection also writes or acts on: the team key and cache, the
// binary, or the scheduler entry.
func waitLock(ctx context.Context, d state.Dir, stderr io.Writer) (func(), error) {
	return d.LockWait(ctx, lockWait, func() {
		fmt.Fprintln(stderr, "ai-usage: waiting for another run, such as the scheduled one, to finish")
	})
}

// disabledByHand is the schedule error for an entry the person paused.
const disabledByHand = "disabled by hand in the system scheduler; `ai-usage schedule install` turns it back on"

// ensureSchedule registers this binary and state folder unless the scheduler
// already runs them. An entry the person commented out or disabled stays so.
func ensureSchedule(ctx context.Context, d state.Dir, st *state.State, now time.Time) {
	last := st.Schedule
	st.Schedule.CheckedAt = now
	st.Schedule.Foreground = false
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
	if err != nil && ctx.Err() != nil {
		// A lookup or install the run's stop cut short leaves the schedule
		// as the last run found it.
		st.Schedule = last
		return
	}
	st.Schedule.Registered = err == nil
	st.Schedule.Error = ""
	switch {
	case errors.Is(err, exec.ErrNotFound):
		// Containers and minimal systems often have no crontab.
		st.Schedule.Error = err.Error() + "; keep `ai-usage schedule run` running instead, such as beside a container's other services"
	case err != nil:
		st.Schedule.Error = err.Error()
	}
}

// job is what the scheduler should run: this binary, collecting into the
// absolute state folder, since the scheduler starts it in another directory.
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
	return fsutil.RealPath(exe), nil
}

func printReport(stdout io.Writer, r view.Report, jsonOut, guide bool, disp *display) error {
	if jsonOut {
		return writeJSON(stdout, r)
	}
	o := disp.options(stdout, true)
	text := view.Text(r, o)
	if guide {
		text += "\n" + view.Guide(r, newScheduler().Name(), o)
	}
	_, err := io.WriteString(disp.writer(stdout), text)
	return err
}

// writeJSON prints v as every --json does, indented by two spaces.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// loadResult rebuilds a run result from disk without collecting.
func loadResult(d state.Dir) (*collect.Result, error) {
	cfg, err := d.LoadConfig()
	if err != nil {
		return nil, err
	}
	key, err := collect.LoadKey(d)
	if err != nil {
		return nil, err
	}
	st, err := d.LoadState()
	if err != nil {
		return nil, err
	}
	cache, _ := collect.LoadTeamCache(d)
	doc := collect.BuildDoc(st, key, cfg, deviceName(cfg), osUser(), version, st.LastRunAt)
	return &collect.Result{Config: cfg, State: st, Key: key, Doc: doc, Team: cache}, nil
}

func cmdReport(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flags("report")
	jsonOut := fs.Bool("json", false, "")
	from := fs.String("from", "", "")
	disp := displayFlags(fs, true)
	if err := parse(fs, args); err != nil {
		return err
	}
	if err := disp.check(); err != nil {
		return err
	}
	if *from != "" {
		return reportFrom(ctx, *from, *jsonOut, disp, stdout)
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
	if disp.interactive(stdout, *jsonOut, false) {
		return tui.Run(ctx, viewConfig(d, res, disp, false, stdout), os.Stdin, stdout)
	}
	return printReport(stdout, reportAt(d, res, clock().UTC()), *jsonOut, false, disp)
}

// reportFrom shows a report saved with --json, such as a teammate's or a
// demo's, rather than this device's. It reads no state and collects
// nothing, so the interactive view has no `r`.
func reportFrom(ctx context.Context, path string, jsonOut bool, disp *display, stdout io.Writer) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var r view.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if r.SchemaVersion != view.SchemaVersion {
		return fmt.Errorf("%s: schema_version %d, this release reads %d", path, r.SchemaVersion, view.SchemaVersion)
	}
	if disp.interactive(stdout, jsonOut, false) {
		return tui.Run(ctx, disp.tuiConfig(stdout, r), os.Stdin, stdout)
	}
	return printReport(stdout, r, jsonOut, false, disp)
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
	r := reportAt(d, res, clock().UTC())
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
		return writeJSON(stdout, out)
	}
	_, err = io.WriteString(disp.writer(stdout), view.StatusText(r, string(d), disp.options(stdout, true)))
	return err
}

func cmdTeam(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
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
			fmt.Fprintf(stdout, "  %s  %s (%s)  collected %s  %s\n", doc.Device, snapshot.Printable(label), snapshot.Printable(who), doc.CollectedAt.Local().Format("2006-01-02 15:04"), doc.CollectorVersion)
		}
		return nil
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
		return joinTeam(ctx, d, line, stdout, stderr)
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
		key, err := collect.LoadKey(d)
		if err != nil {
			return err
		}
		c := &relay.Client{BaseURL: endpoint, Key: key}
		if err := c.Remove(ctx, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed %s from team %s\n", args[0], key.Fingerprint())
		// The cached team read no longer lists it either.
		unlock, err := waitLock(ctx, d, stderr)
		if err != nil {
			return err
		}
		defer unlock()
		return collect.ForgetCachedDevice(d, args[0])
	default:
		return usageError("unknown team command " + sub)
	}
}

func joinTeam(ctx context.Context, d state.Dir, line string, stdout, stderr io.Writer) error {
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
		handler := relay.NewServer(store)
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
		// ListenAndServe returns as soon as the shutdown starts, so the command
		// waits for it to finish. A bug in it closes the server and is the
		// command's error rather than the end of the process.
		stopped := make(chan error, 1)
		go func() {
			defer close(stopped)
			defer func() {
				if v := recover(); v != nil {
					_ = srv.Close()
					stopped <- fmt.Errorf("relay shutdown stopped by a bug: %v", v)
				}
			}()
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
		return <-stopped
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
		if _, err := d.EditConfig(func(c *state.Config) error { c.Relay = u; return nil }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "relay set to %s\n", u)
		return nil
	case "clear":
		if _, err := d.EditConfig(func(c *state.Config) error { c.Relay = ""; return nil }); err != nil {
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

func cmdSchedule(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return usageError("schedule takes install, remove, status, or run")
	}
	d, err := dir()
	if err != nil {
		return err
	}
	if _, err := d.LoadConfig(); err != nil {
		return err
	}
	s := newScheduler()
	exe, home, err := job(d)
	if err != nil {
		return err
	}
	if args[0] == "install" || args[0] == "remove" {
		// A run registers itself again unless the config says otherwise, so
		// the entry and the config change between runs, never under one.
		unlock, err := waitLock(ctx, d, stderr)
		if err != nil {
			return err
		}
		defer unlock()
	}
	switch args[0] {
	case "run":
		return scheduleRun(ctx, d, exe, home, stderr)
	case "install":
		if err := s.Install(ctx, exe, home, os.Getenv("PATH")); err != nil {
			return err
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.ScheduleOff = false; return nil }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "registered %s to run every %s with state folder %s\n", exe, schedule.Interval, home)
	case "remove":
		if err := s.Remove(ctx); err != nil {
			return err
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.ScheduleOff = true; return nil }); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "removed from the system scheduler; later runs will not register again until `ai-usage schedule install`")
	case "status":
		if d.Foreground() {
			fmt.Fprintf(stdout, "`ai-usage schedule run` collects every %s with state folder %s\n", schedule.Interval, home)
			return nil
		}
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
		return usageError("schedule takes install, remove, status, or run")
	}
	return nil
}

// scheduleRun collects now and then at every quarter hour, as the system
// schedulers do, until it is stopped. Each collection is a new process of the
// binary on disk, so a release one collection installs runs from the next.
// It holds the schedule lock throughout, which tells runs it is there.
func scheduleRun(ctx context.Context, d state.Dir, exe, home string, stderr io.Writer) error {
	unlock, err := d.ScheduleLock()
	if errors.Is(err, state.ErrBusy) {
		return errors.New("another `ai-usage schedule run` already collects into " + home)
	}
	if err != nil {
		return err
	}
	defer unlock()
	fmt.Fprintf(stderr, "ai-usage: collecting every %s with state folder %s\n", schedule.Interval, home)
	for {
		if err := collectOnce(ctx, exe, home, stderr); err != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "ai-usage: %s\n", err)
		}
		now := clock()
		if !sleepCtx(ctx, now.Truncate(schedule.Interval).Add(schedule.Interval).Sub(now)) {
			return nil
		}
	}
}

// liveSchedule shows a schedule that `ai-usage schedule run` kept as gone
// once it no longer runs.
func liveSchedule(d state.Dir, st *state.State) *state.State {
	if st == nil || !st.Schedule.Foreground || d.Foreground() {
		return st
	}
	c := *st
	c.Schedule = state.Schedule{CheckedAt: st.Schedule.CheckedAt, Error: "`ai-usage schedule run` has stopped; start it again, or use `ai-usage schedule install` where there is a system scheduler"}
	return &c
}

// collectOnce and sleepCtx are replaced in tests.
var (
	collectOnce = func(ctx context.Context, exe, home string, stderr io.Writer) error {
		cmd := exec.CommandContext(ctx, exe, "collect", "--quiet", "--home", home)
		cmd.Stdout, cmd.Stderr = stderr, stderr
		// A stopped schedule lets the collection release its lock, and save
		// none of what the stop made fail; Windows cannot send the signal,
		// so it waits and then kills.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 30 * time.Second
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.Exited() {
			// The collection printed its own error.
			return nil
		}
		if exit != nil {
			// Killed, as by a container's memory limit: it said nothing.
			return fmt.Errorf("the collection was stopped: %s", exit)
		}
		return err
	}
	sleepCtx = func(ctx context.Context, d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
)

func cmdName(args []string, stdout io.Writer) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
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
		if len(args) != 0 {
			return usageError("name show takes no arguments")
		}
		v, ok := envName()
		if v != "" && !ok {
			fmt.Fprintf(stdout, "AI_USAGE_NAME is ignored: %s\n", validName(v))
		}
		switch {
		case ok:
			saved := cfg.Name
			if saved == "" {
				saved = hostname()
			}
			if saved == v {
				fmt.Fprintf(stdout, "%s (from AI_USAGE_NAME)\n", v)
				break
			}
			// launchd and cron start runs without the shell's variables.
			fmt.Fprintf(stdout, "%s (from AI_USAGE_NAME, in runs that see it; runs without it, such as the system scheduler's, use %s)\n", v, saved)
		case cfg.Name != "":
			fmt.Fprintln(stdout, cfg.Name)
		default:
			fmt.Fprintf(stdout, "%s (the host name; `ai-usage name set NAME` names this device)\n", hostname())
		}
		return nil
	case "set":
		if len(args) != 1 {
			return usageError("name set takes one name; quote a name with spaces")
		}
		n := strings.TrimSpace(args[0])
		if err := validName(n); err != nil {
			return usageError(err.Error())
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.Name = n; return nil }); err != nil {
			return err
		}
		if v, ok := envName(); ok {
			fmt.Fprintf(stdout, "saved %s; while AI_USAGE_NAME is set, this device is %s\n", n, v)
			return nil
		}
		fmt.Fprintf(stdout, "this device is now %s; the team sees the name after the next run\n", n)
		return nil
	case "clear":
		if len(args) != 0 {
			return usageError("name clear takes no arguments")
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.Name = ""; return nil }); err != nil {
			return err
		}
		if v, ok := envName(); ok {
			fmt.Fprintf(stdout, "cleared; while AI_USAGE_NAME is set, this device is %s\n", v)
			return nil
		}
		fmt.Fprintf(stdout, "this device goes by its host name, %s, again\n", hostname())
		return nil
	default:
		return usageError("unknown name command " + sub)
	}
}

func cmdUpdate(ctx context.Context, stdout, stderr io.Writer) error {
	if selfupdate.Dev(version) {
		return errors.New("this is a development build (" + version + "); install a release to self-update")
	}
	d, err := dir()
	if err != nil {
		return err
	}
	// The lock keeps a scheduled run from replacing the binary at the same time.
	unlock, err := waitLock(ctx, d, stderr)
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
