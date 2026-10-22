package probe

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neoromantic/ai-usage/internal/fsutil"
	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/selfupdate"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Claude asks `claude auth status --json` who is logged in, then reads the
// usage Claude Code cached in its own config file. The cache is used only for
// a claude.ai login, and only when it names the same account the config file
// says is logged in. When auth status gives no answer, as when it fails, that
// cache is still read, for the account the config names.
//
// Claude Code writes that cache only when it reads the usage, as its /usage
// dialog does. So for a claude.ai subscription whose config names its
// account and whose cache is missing or older than claudeFresh, in a home
// this OS user owns and used in the last claudeInUse and since that cache,
// Claude Code is first asked to read it again. Claude Code renews an expired
// login when it reads the usage, so it is asked only for a home in use,
// whose own sessions keep that login alive anyway. An idle login is left to
// expire, and an idle home has spent nothing since. The cache is as fresh as
// Claude Code last made it. Its age is reported, and when the read leaves it
// stale, why. A subscription whose type auth status does not tell, as it does
// not for some logins, counts when its account has a usage cache.
//
// auth status runs the claude that find locates, or the newest install when
// it locates none. The usage is read by the newest install that caches it,
// which need not be the one first on PATH.
//
// remembered is the CLAUDE_CONFIG_DIR an earlier run saw for home, and
// lastUse is when home was last used, as its logs show.
func Claude(ctx context.Context, env Env, home, remembered string, lastUse time.Time) (Reading, error) {
	var r Reading
	var errs []error
	configDir := env.claudeConfigDir(home, remembered)
	file := claudeConfigFile(env.HomeDir, home, configDir)

	var installs []candidate
	bin, found := env.find("claude")
	if !found {
		installs = env.claudeCandidates(ctx)
		if c, ok := claudeNewest(installs); ok {
			bin, found = c.path, true
		}
	}
	var st claudeStatus
	var authErr error
	var printed *Quota
	if found {
		st, authErr = claudeAuthStatus(ctx, env, bin, configDir)
		r.Account, r.Plan = st.label(), st.SubscriptionType
	} else {
		errs = append(errs, errors.New("claude binary not found; account unknown"))
	}

	cfg, cfgErr := readClaudeConfig(file)
	if authErr != nil {
		// auth status failed without saying nobody is logged in. The config
		// still says who is, and a cache of that account's usage is Claude
		// Code's own reading of it.
		if email := cfg.email(); !errors.Is(authErr, ErrNotLoggedIn) && cfgErr == nil && cfg.cache() != nil && email != "" {
			r.Account, r.Quota = email, cfg.quota()
			return r, nil
		}
		errs = append(errs, authErr)
	}
	// A zero st is no subscription, so a missing binary is never asked. A
	// config that cannot be read is not refreshed either: Claude Code would
	// meet the same file, and its error is reported below.
	if st.subscription(cfg) && cfgErr == nil && cfg.hasAccount() && cfg.usedSince(lastUse, env.now()) && claudeOwned(file, home) && cfg.stale(env.now()) {
		if installs == nil {
			installs = env.claudeCandidates(ctx)
		}
		var err error
		if cfg, printed, err = claudeReadUsage(ctx, env, bin, installs, configDir, home, file, cfg); err != nil {
			errs = append(errs, err)
		}
	}
	if cfgErr != nil {
		errs = append(errs, cfgErr)
	}
	// The cache holds a claude.ai subscription's limits. An API key or a
	// cloud provider has none, even with a claude.ai account in the config.
	// The usage Claude Code printed is newer than the cache it left.
	if st.AuthMethod == "claude.ai" {
		r.Quota = cmp.Or(printed, cfg.quota())
		cfg.subscription(&r)
	}
	return r, joinErrors(errs)
}

// claudeNewest is the install of the highest version, or the first when no
// version is known.
func claudeNewest(cs []candidate) (candidate, bool) {
	if len(cs) == 0 {
		return candidate{}, false
	}
	best := cs[0]
	for _, c := range cs[1:] {
		if compareVersions(c.version, best.version) > 0 {
			best = c
		}
	}
	return best, true
}

// claudeForUsage is the install to read the usage with: the newest that
// caches it, else the newest that prints it, else the first whose version is
// not known. ok is false when each is known to be too old to do either.
func claudeForUsage(cs []candidate) (c candidate, ok bool) {
	for _, from := range []string{claudeCaching, claudePrinting} {
		var best *candidate
		for i := range cs {
			if claudeAtLeast(cs[i].version, from) && (best == nil || compareVersions(cs[i].version, best.version) > 0) {
				best = &cs[i]
			}
		}
		if best != nil {
			return *best, true
		}
	}
	for _, c := range cs {
		if c.version == "" {
			return c, true
		}
	}
	return candidate{}, false
}

// claudeAtLeast reports whether version is known and not older than min.
func claudeAtLeast(version, min string) bool {
	return claudeVersionName.MatchString(version) && !selfupdate.Newer(min, version)
}

// claudeInstall is the install in cs at bin's real path, or bin as it is.
func claudeInstall(cs []candidate, bin string) candidate {
	real := fsutil.RealPath(bin)
	for _, c := range cs {
		if fsutil.RealPath(c.path) == real {
			return c
		}
	}
	return candidate{path: bin, version: claudeVersion(bin), kind: claudeKind(bin)}
}

// claudeConfigDir is the CLAUDE_CONFIG_DIR claude runs with for home, or ""
// to run it without one. Once the variable is set, even to the default
// ~/.claude, Claude Code keeps its config inside the directory and its login
// under a keychain entry named after the exact string. So a value in Environ
// that names this home is passed on as it is. When Environ does not name it,
// as under the system scheduler, the exact string remembered from a run that
// saw the home through the variable is passed the same way. Otherwise a
// custom home is passed, and the default home runs without the variable.
func (e Env) claudeConfigDir(home, remembered string) string {
	for _, v := range []string{e.getenv("CLAUDE_CONFIG_DIR"), remembered} {
		if v != "" && samePath(v, home) {
			if filepath.IsAbs(v) {
				return v
			}
			// Relative to the caller's directory, not the one claude starts in.
			return home
		}
	}
	if filepath.Clean(home) == DefaultHome(e.HomeDir, "claude") {
		return ""
	}
	return home
}

// claudeConfigFile is where Claude Code keeps its global config for a home,
// given the CLAUDE_CONFIG_DIR it runs with. A legacy .config.json in the home
// comes first. Otherwise the file is inside the home when the variable is
// set, and beside the default home when it is not.
func claudeConfigFile(userHome, home, configDir string) string {
	if legacy := filepath.Join(home, ".config.json"); isFile(legacy) {
		return legacy
	}
	if configDir == "" && userHome != "" {
		return filepath.Join(userHome, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

func samePath(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	return errA == nil && errB == nil && a == b
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

type claudeStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	SubscriptionType string `json:"subscriptionType"`
}

// subscription reports whether the login is a claude.ai subscription, the
// only kind with usage limits. A Console login also signs in through
// claude.ai, and has no subscription. Some subscriptions' logins do not tell
// their type either: one whose account has a usage cache in cfg is one, as
// Claude Code caches the usage only of a subscription.
func (st claudeStatus) subscription(cfg claudeConfig) bool {
	return st.AuthMethod == "claude.ai" && (strings.TrimSpace(st.SubscriptionType) != "" || cfg.cache() != nil)
}

// label names a login by its email, or by how it signs in when it has none.
func (st claudeStatus) label() string {
	if !st.LoggedIn {
		return ""
	}
	if label := strings.TrimSpace(st.Email); label != "" {
		return label
	}
	if label := strings.TrimSpace(st.AuthMethod); label != "" {
		return label
	}
	return "logged in"
}

func claudeAuthStatus(ctx context.Context, env Env, bin, configDir string) (claudeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, env.timeout())
	defer cancel()
	cmd := env.command(ctx, bin, []string{"CLAUDE_CONFIG_DIR", configDir}, "auth", "status", "--json")
	out, runErr := cmd.Output()
	var st claudeStatus
	// auth status exits non-zero when logged out and still prints JSON.
	if jsonErr := json.Unmarshal(out, &st); jsonErr != nil {
		if ctx.Err() != nil {
			return claudeStatus{}, errors.New("claude auth status: no answer in time")
		}
		if runErr != nil {
			return claudeStatus{}, fmt.Errorf("claude auth status: %s", shortErr(runErr))
		}
		return claudeStatus{}, errors.New("claude auth status: output is not JSON")
	}
	if !st.LoggedIn {
		return claudeStatus{}, notLoggedIn("claude")
	}
	return st, nil
}

// claudeFresh is how old the usage cache may be before Claude Code is asked
// to read the usage again. A run every 15 minutes finds it older.
const claudeFresh = 10 * time.Minute

// claudeUsageTimeout bounds that read, which takes a few seconds.
var claudeUsageTimeout = time.Minute

// claudeGuardModel names no model. /usage calls none and ignores it, and no
// Claude Code since 2.1.0 sends /usage to a model: one that does not know
// /usage says so. The name stays as a safety net, so that one that sent it
// to a model as a prompt would fail before it spends a token. From about
// 2.1.251, Claude Code warns on stderr at every run that its model catalog
// does not describe the name, which is no error.
const claudeGuardModel = "ai-usage-no-model"

// claudeModelMissing reports whether a line says that the model
// claudeGuardModel names does not exist, as it would were /usage sent to it.
func claudeModelMissing(l string) bool {
	return strings.Contains(l, claudeGuardModel) &&
		(strings.Contains(l, "may not exist") || strings.Contains(l, "not found") || strings.Contains(l, "not_found"))
}

// claudeCaching is the first Claude Code that caches the usage it reads.
// An older one from claudePrinting shows the usage without caching it, and
// what it shows is the reading. One older still shows nothing to read.
const (
	claudeCaching  = "2.1.208"
	claudePrinting = "2.1.193"
)

// claudeReadUsage has Claude Code read the usage into its cache, and says
// why the cache is still stale afterwards. The read is judged by the cache
// it leaves, not by how Claude Code exits: one that could not reach the
// usage, as offline, still exits 0. And one that failed is no problem when
// the cache is fresh anyway, as when Claude Code wrote it before it failed,
// or the person's own session did meanwhile. When the cache is stale but
// Claude Code printed the usage, as one older than claudeCaching does, the
// usage it printed is the reading.
//
// Of installs, the newest that can read the usage does. bin is the one auth
// status ran, which the error names when none can.
//
// Claude Code is not run when it cannot cache the usage: when every install
// is too old to, or its settings turn off the traffic the read needs.
// Neither goes away by itself, so it is said at every run instead, as a
// NoQuotaError, as is a read that shows the usage in a form that cannot be
// read.
//
// It returns the config to take the quota from: cfg, or the file as the
// read left it once Claude Code ran, unless that went back in time. And the
// usage Claude Code printed, when that is the reading.
func claudeReadUsage(ctx context.Context, env Env, bin string, installs []candidate, configDir, home, file string, cfg claudeConfig) (claudeConfig, *Quota, error) {
	c, ok := claudeForUsage(installs)
	if !ok {
		c = claudeInstall(installs, bin)
		about := fsutil.Tilde(c.path, env.HomeDir) + " is " + c.version
		if c.kind != "" {
			about += ", " + c.kind
		}
		return cfg, nil, &NoQuotaError{
			Msg:  fmt.Sprintf("update Claude Code to %s or newer (%s)", claudeCaching, about),
			Path: c.path, Version: c.version, Kind: c.kind,
		}
	}
	noQuota := func(err error) error {
		return &NoQuotaError{Msg: err.Error(), Path: c.path, Version: c.version, Kind: c.kind}
	}
	if claudeNoTraffic(env, home) {
		return cfg, nil, noQuota(claudeUsageError("nonessential traffic is off in Claude Code's settings", c.version, cfg.cacheState(env.now()), ""))
	}
	run := claudeRefresh(ctx, env, c.path, configDir)
	now := env.now()
	after, err := readClaudeConfig(file)
	if err != nil || !after.stale(now) {
		return after, nil, err
	}
	if q := claudePrintedQuota(run.out, now); q != nil {
		return after, q, nil
	}
	// Another Claude Code that saves its config as it had it, when it
	// started or last read it, takes the cache back to its own. The reading
	// that was there stands.
	before, left := cfg.cache(), after.cache()
	if before != nil && (left == nil && after.OAuthAccount != nil && after.OAuthAccount.AccountUUID == before.AccountUUID || left != nil && left.FetchedAtMs < before.FetchedAtMs) {
		return cfg, nil, noQuota(fmt.Errorf("claude /usage: another Claude Code process rewrote %s; reading is %s old",
			fsutil.Tilde(file, env.HomeDir), claudeAge(now.Sub(time.UnixMilli(before.FetchedAtMs)))))
	}
	why, said, quotaOnly := run.why(c.version)
	err = claudeUsageError(why, c.version, after.cacheState(now), said)
	if quotaOnly {
		err = noQuota(err)
	}
	return after, nil, err
}

// claudeUsageError says why a read left no new reading, then which Claude
// Code it was when that is known and what cache there is, and last a line of
// what it printed, if any, so that an error cut short loses that first.
func claudeUsageError(why, version, cache, said string) error {
	var about []string
	if version != "" {
		about = append(about, "Claude Code "+version)
	}
	if cache != "" {
		about = append(about, cache)
	}
	msg := "claude /usage: " + why
	if len(about) > 0 {
		msg += " (" + strings.Join(about, ", ") + ")"
	}
	if said != "" {
		msg += ": " + said
	}
	return errors.New(msg)
}

// claudeRefresh has Claude Code read the account's usage and cache it, as
// its /usage dialog does. In print mode /usage is a local command: it calls
// no model, and --no-session-persistence leaves no session behind. It gets
// no input: there is no prompt to wait for. The person's hooks and the
// updater are off; hooks an organization manages still run. Nonessential
// traffic is left on, since without it Claude Code does not read the usage.
// Like any of its sessions, Claude Code renews its own login on the way when
// that has expired. Only the start of what it prints and the end of its
// errors are kept, to tell why a read failed.
func claudeRefresh(ctx context.Context, env Env, bin, configDir string) claudeRun {
	ctx, cancel := context.WithTimeout(ctx, claudeUsageTimeout)
	defer cancel()
	cmd := env.command(ctx, bin, []string{
		"CLAUDE_CONFIG_DIR", configDir,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "",
		"DISABLE_AUTOUPDATER", "1",
	}, "-p", "/usage", "--no-session-persistence",
		"--model", claudeGuardModel, "--settings", `{"disableAllHooks":true}`)
	var out firstBytes
	var errOut lastLine
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	return claudeRun{
		err:      err,
		timedOut: ctx.Err() != nil,
		out:      claudePrinted(printedLines(out.String())),
		errOut:   claudePrinted(errOut.lines()),
	}
}

// claudeRun is what one `claude -p /usage` did.
type claudeRun struct {
	err      error
	timedOut bool
	// out and errOut are the lines it printed and its errors, as
	// claudePrinted keeps them.
	out, errOut []string
}

// claudePrinted is lines without what /usage adds after the usage: the
// skills, subagents, plugins, and MCP servers that contribute to it, which
// are the person's own and never passed on. Nor is the model catalog's
// warning about claudeGuardModel, which is no error.
func claudePrinted(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, "contributing to your limits") {
			break
		}
		if !strings.Contains(l, "model catalog") {
			out = append(out, l)
		}
	}
	return out
}

// why says in a few words why a read left the cache stale, from how it ended
// and the forms of what it printed that tell a cause, and gives the line of
// an error it does not know, cut short. Nothing else it printed is passed
// on: not the usage, when its windows reset, or in which time zone.
//
// quotaOnly is true when the reason is this Claude Code itself, too old to
// read the usage, or showing it in a form that cannot be read: no problem
// of the run.
func (r claudeRun) why(version string) (why, said string, quotaOnly bool) {
	all := append(slices.Clone(r.out), r.errOut...)
	switch {
	case r.timedOut:
		return "no answer in time", "", false
	case slices.ContainsFunc(all, claudeCannotRead):
		return "no new reading: this Claude Code cannot read the usage; update it", "", true
	case slices.ContainsFunc(r.out, claudeShowsUsage):
		// One older than claudeCaching shows the usage and caches none, in
		// a form claudePrintedQuota did not read.
		if version == "" {
			return "no new reading: Claude Code showed the usage but did not cache it; update it", "", true
		}
		return "no new reading: Claude Code showed the usage but did not cache it", "", true
	case slices.ContainsFunc(r.out, func(l string) bool { return strings.HasPrefix(l, "Total cost:") }):
		return "no new reading: Claude Code sees no claude.ai plan", "", false
	case slices.ContainsFunc(r.out, func(l string) bool { return strings.Contains(l, claudeHeadline) }):
		// Claude Code could not reach the usage: offline, with a login
		// without the profile scope, or with nonessential traffic off where
		// claudeNoTraffic does not look. Or, older than claudeCaching, it did
		// not try. The likely causes are left to the docs, so that two homes'
		// reasons fit in a source's error.
		return "no new reading: could not read the usage", "", false
	case r.err != nil:
		// Its errors, or what it printed that says it is an error.
		said := errorOf(r.errOut)
		if said == "" {
			said = errorOf(slices.DeleteFunc(slices.Clone(r.out), func(l string) bool { return !errorLine.MatchString(l) }))
		}
		return shortErr(r.err), truncate(said, maxSaid), false
	case len(all) == 0:
		return "no new reading: it printed nothing", "", false
	}
	return "no new reading", "", false
}

// claudeHeadline ends the line /usage starts with for a claude.ai
// subscription, on its own limits or on extra usage, whether or not it then
// reads the usage.
const claudeHeadline = "to power your Claude Code usage"

// claudeCannotRead reports whether a line says that this Claude Code has no
// /usage to run: it does not know the command or an option, it has no /usage
// in print mode, or it sent /usage to the model, which does not exist.
func claudeCannotRead(l string) bool {
	for _, p := range []string{"Unknown skill", "Unknown slash command", "Unknown command"} {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return strings.Contains(l, "isn't available in this environment") || strings.Contains(l, "unknown option") || claudeModelMissing(l)
}

// claudeShowsUsage reports whether a line is one of the usage /usage shows.
func claudeShowsUsage(l string) bool {
	return strings.HasPrefix(l, "Current session") || strings.HasPrefix(l, "Current week")
}

// claudeUsageLine matches a line of the usage /usage prints: which window,
// how full, and when it resets, in which time zone, as
// "Current week (all models): 40% used · resets Oct 1, 9am (Europe/Berlin)".
var claudeUsageLine = regexp.MustCompile(`^Current (session|week \(([^()]+)\)):\s*(\d+(?:\.\d+)?)% used(?:\s*·\s*resets\s+(.+?)(?:\s+\(([^()]+)\))?)?$`)

// claudePrintedQuota is the usage Claude Code printed in lines, as of now,
// or nil when it printed none it can be read from. A window whose reset
// cannot be read has none. Nothing else of what it printed is kept.
func claudePrintedQuota(lines []string, now time.Time) *Quota {
	var ws []snapshot.Window
	for _, l := range lines {
		m := claudeUsageLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		pct, err := strconv.ParseFloat(m[3], 64)
		if err != nil {
			continue
		}
		w := snapshot.Window{Name: "5h", Percent: pct, Minutes: 300}
		if m[1] != "session" {
			w.Name, w.Minutes = "7d", 10080
			if model := strings.TrimSpace(m[2]); model != "all models" {
				w.Name += " " + model
			}
		}
		w.ResetsAt = claudeResetAt(m[4], m[5], now, w.Minutes)
		ws = append(ws, w)
	}
	if len(ws) == 0 {
		return nil
	}
	return &Quota{At: now, Source: "harness", Windows: ws}
}

// claudeResetLayouts are the forms /usage tells a reset in, once "at" and
// commas are dropped: a time today or a day and a time, with or without
// minutes.
var claudeResetLayouts = []string{"Jan 2 3:04pm", "Jan 2 3pm", "3:04pm", "3pm"}

// claudeResetAt is when a window of minutes resets, as /usage tells it in
// zone, or nil when it cannot be read or is not within the window from now.
// A time without a day is the next one to come, and a day without a year
// is in the year that puts it nearest.
func claudeResetAt(when, zone string, now time.Time, minutes int) *time.Time {
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "" {
		return nil
	}
	when = strings.Join(strings.Fields(strings.NewReplacer(" at ", " ", ",", " ").Replace(strings.ToLower(when))), " ")
	local := now.In(loc)
	for _, layout := range claudeResetLayouts {
		t, err := time.ParseInLocation(layout, when, loc)
		if err != nil {
			continue
		}
		if strings.HasPrefix(layout, "Jan") {
			t = time.Date(local.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(now.Add(-24 * time.Hour)) {
				t = t.AddDate(1, 0, 0)
			}
		} else {
			t = time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, loc)
			if t.Before(now) {
				t = t.AddDate(0, 0, 1)
			}
		}
		if t.Before(now.Add(-time.Hour)) || t.After(now.Add(time.Duration(minutes)*time.Minute+time.Hour)) {
			return nil
		}
		t = t.UTC()
		return &t
	}
	return nil
}

// firstBytes keeps the first claudeOutMax bytes written to it, where /usage
// says what it found.
type firstBytes struct {
	mu   sync.Mutex
	head []byte
}

const claudeOutMax = 16 << 10

func (f *firstBytes) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if room := claudeOutMax - len(f.head); room > 0 {
		f.head = append(f.head, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (f *firstBytes) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.head)
}

// cacheState says in a few words what usage cache the config holds for the
// account logged in: none, another account's, or one of what age.
func (cfg claudeConfig) cacheState(now time.Time) string {
	c := cfg.Cached
	switch {
	case c == nil || c.FetchedAtMs <= 0:
		return "no cache"
	case cfg.cache() == nil:
		return "cache of another account"
	}
	age := now.Sub(time.UnixMilli(c.FetchedAtMs))
	if age < 0 {
		return "cache from the future"
	}
	return "cache " + claudeAge(age) + " old"
}

// claudeAge is an age in whole minutes, hours, or days, as "40m".
func claudeAge(age time.Duration) string {
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%dm", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age.Hours()))
	}
	return fmt.Sprintf("%dd", int(age.Hours()/24))
}

// claudeVersionName matches a version as Claude Code's installs name their
// folders and files.
var claudeVersionName = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// claudeVersion is the version of the Claude Code at bin, as its install
// tells without running it, or "" when it does not. With links resolved,
// that is the nearest of the file and the few folders above it that is named
// as a version where Claude Code's own installs keep their versions, as a
// native install's claude/versions/X.Y.Z and Homebrew's
// Caskroom/claude-code/X.Y.Z/claude are, or that holds the package.json of
// an npm install. On Windows, npm puts that package beside its claude.cmd.
// Another tool's folder named as a version tells nothing, as a version
// manager's shim resolves to the manager's own binary in its own version's
// folder.
func claudeVersion(bin string) string {
	path := fsutil.RealPath(bin)
	for range 4 {
		if name := filepath.Base(path); claudeVersionName.MatchString(name) && claudeVersionsFolder(filepath.Dir(path)) {
			return name
		}
		if v := claudePackageVersion(filepath.Join(path, "package.json")); v != "" {
			return v
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return claudePackageVersion(filepath.Join(filepath.Dir(bin), "node_modules", "@anthropic-ai", "claude-code", "package.json"))
}

// claudeVersionsFolder reports whether dir is where an install of Claude
// Code keeps its versions: a native install's claude/versions, or
// Homebrew's claude-code or claude-code@channel.
func claudeVersionsFolder(dir string) bool {
	name := filepath.Base(dir)
	if name == "versions" {
		return filepath.Base(filepath.Dir(dir)) == "claude"
	}
	return name == "claude-code" || strings.HasPrefix(name, "claude-code@")
}

// claudePackageVersion is the version in the package.json at path when that
// is Claude Code's, or "".
func claudePackageVersion(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pkg struct{ Name, Version string }
	if json.Unmarshal(body, &pkg) != nil || pkg.Name != "@anthropic-ai/claude-code" || !claudeVersionName.MatchString(pkg.Version) {
		return ""
	}
	return pkg.Version
}

// claudeNoTraffic reports whether Claude Code's settings turn off its
// nonessential traffic, without which it does not read the usage, in the env
// they give its sessions. Removing the variable from its own environment
// does not undo that. The settings are an organization's, the files in the
// managed-settings.d beside its file over that file, then those of the
// project it starts in, which is the user's home, then the user settings in
// home, and the first to set the variable decides. Nothing else is taken
// from them.
func claudeNoTraffic(env Env, home string) bool {
	var files []string
	for _, managed := range env.ClaudeManaged {
		files = append(files, claudeDropIns(filepath.Join(filepath.Dir(managed), "managed-settings.d"))...)
		files = append(files, managed)
	}
	if env.HomeDir != "" {
		project := filepath.Join(env.HomeDir, ".claude")
		files = append(files, filepath.Join(project, "settings.local.json"), filepath.Join(project, "settings.json"))
	}
	files = append(files, filepath.Join(home, "settings.json"))
	for _, f := range files {
		if v, ok := claudeSettingsEnv(f, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"); ok {
			return v != `""` && v != "false"
		}
	}
	return false
}

// claudeDropIns are the settings files in dir, those named .json and not
// hidden, the last by name first: Claude Code applies them in name order,
// each over the ones before.
func claudeDropIns(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range slices.Backward(entries) {
		name := e.Name()
		if strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".") && (e.Type().IsRegular() || e.Type()&os.ModeSymlink != 0) {
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files
}

// claudeSettingsEnv is the value, as JSON, that the settings file at path
// gives the variable name in its "env", and whether it gives one. Keys match
// as Claude Code reads them: "env" exactly, and the variable as the OS names
// variables. Of several that match, the last is set last. A null sets nothing.
func claudeSettingsEnv(path, name string) (string, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(body, &settings) != nil {
		return "", false
	}
	dec := json.NewDecoder(bytes.NewReader(settings["env"]))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return "", false
	}
	var value json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return "", false
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return "", false
		}
		if key, _ := t.(string); sameEnvName(key, name) {
			value = v
		}
	}
	v := strings.TrimSpace(string(value))
	return v, v != "" && v != "null"
}

type claudeConfig struct {
	OAuthAccount *struct {
		AccountUUID  string `json:"accountUuid"`
		EmailAddress string `json:"emailAddress"`
		// The plan's tier, who bills it, and when it began, as Claude
		// Code's profile of the account last said.
		OrgTier      string `json:"organizationRateLimitTier"`
		UserTier     string `json:"userRateLimitTier"`
		BillingType  string `json:"billingType"`
		SubscribedAt string `json:"subscriptionCreatedAt"`
	} `json:"oauthAccount"`
	Cached *claudeUsageCache `json:"cachedUsageUtilization"`
}

// subscription adds to r what the config says of the subscription of r's
// account: the tier of a Max plan, as "max 20x" or "max 5x", who bills it,
// and when it began. A config of another account says nothing of it.
func (cfg claudeConfig) subscription(r *Reading) {
	a := cfg.OAuthAccount
	if a == nil || !strings.EqualFold(strings.TrimSpace(a.EmailAddress), strings.TrimSpace(r.Account)) {
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.Plan), "max") {
		switch cmp.Or(a.OrgTier, a.UserTier) {
		case "default_claude_max_20x":
			r.Plan = "max 20x"
		case "default_claude_max_5x":
			r.Plan = "max 5x"
		}
	}
	r.Billing = snapshot.PlainLabel(a.BillingType)
	if t, err := time.Parse(time.RFC3339Nano, a.SubscribedAt); err == nil {
		r.SubscribedAt = t.UTC()
	}
}

// email is the email of the account the config says is logged in, or "".
func (cfg claudeConfig) email() string {
	if cfg.OAuthAccount == nil {
		return ""
	}
	return strings.TrimSpace(cfg.OAuthAccount.EmailAddress)
}

type claudeUsageCache struct {
	FetchedAtMs int64                      `json:"fetchedAtMs"`
	AccountUUID string                     `json:"accountUuid"`
	Utilization map[string]json.RawMessage `json:"utilization"`
}

// readClaudeConfig reads Claude Code's config file. A missing file is an
// empty config, not an error.
func readClaudeConfig(path string) (claudeConfig, error) {
	var cfg claudeConfig
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("claude config: %s", shortErr(err))
	}
	if json.Unmarshal(body, &cfg) != nil {
		return claudeConfig{}, errors.New("claude config is not JSON")
	}
	return cfg, nil
}

// cache is the usage cached for the account logged in, or nil.
func (cfg claudeConfig) cache() *claudeUsageCache {
	c := cfg.Cached
	if c == nil || c.FetchedAtMs <= 0 {
		return nil
	}
	if cfg.OAuthAccount == nil || c.AccountUUID == "" || c.AccountUUID != cfg.OAuthAccount.AccountUUID {
		// The cache belongs to an account that is no longer logged in.
		return nil
	}
	return c
}

// stale reports whether Claude Code should read the usage again: the cache
// is missing, another account's, or older than claudeFresh. One from the
// future is stale too, as it is to Claude Code.
func (cfg claudeConfig) stale(now time.Time) bool {
	c := cfg.cache()
	if c == nil {
		return true
	}
	age := now.Sub(time.UnixMilli(c.FetchedAtMs))
	return age < 0 || age >= claudeFresh
}

// claudeInUse is how recently a home must have been used for Claude Code to
// be asked to read its usage. Runs every 15 minutes ask within it, and a
// read that keeps failing stops being made soon after the home goes idle.
const claudeInUse = time.Hour

// usedSince reports whether the home was used in the last claudeInUse, and
// after the cache was fetched when there is one. A cache fetched after now
// counts as none, as it does to Claude Code.
func (cfg claudeConfig) usedSince(used, now time.Time) bool {
	if used.IsZero() || now.Sub(used) > claudeInUse {
		return false
	}
	c := cfg.cache()
	if c == nil {
		return true
	}
	fetched := time.UnixMilli(c.FetchedAtMs)
	return fetched.After(now) || used.After(fetched)
}

// hasAccount reports whether the config names the account logged in. Claude
// Code tags the usage it caches with that account, and a cache without one
// is not read. So for a config that names none, as one reset while the login
// stays, a read would leave no reading, every run.
func (cfg claudeConfig) hasAccount() bool {
	return cfg.OAuthAccount != nil && cfg.OAuthAccount.AccountUUID != ""
}

// claudeOwned reports whether this OS user owns the home and the config
// file, or for one that does not exist yet, the folder it would be made in.
// Claude Code saves a file as the user that runs it, so a read run for a
// home another user owns, as a root collector's bot folders, would leave
// that user's config, and a login renewed on the way, to this one, and lock
// that user's own Claude Code out of them. Such a home's cache is only read.
var claudeOwned = func(file, home string) bool {
	return ownedByUs(existing(file)) && ownedByUs(existing(home))
}

// existing is path, or the nearest folder above it that exists.
func existing(path string) string {
	for {
		_, err := os.Stat(path)
		parent := filepath.Dir(path)
		if !errors.Is(err, os.ErrNotExist) || parent == path {
			return path
		}
		path = parent
	}
}

type claudeWindow struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// quota reads cachedUsageUtilization. A missing cache is no reading.
func (cfg claudeConfig) quota() *Quota {
	c := cfg.cache()
	if c == nil {
		return nil
	}
	ws := claudeLimitWindows(c.Utilization["limits"])
	if len(ws) == 0 {
		ws = claudeKeyWindows(c.Utilization)
	}
	if len(ws) == 0 {
		return nil
	}
	return &Quota{At: time.UnixMilli(c.FetchedAtMs).UTC(), Source: "cache", Windows: ws}
}

func claudeLimitWindows(raw json.RawMessage) []snapshot.Window {
	var limits []claudeLimit
	if json.Unmarshal(raw, &limits) != nil {
		return nil
	}
	var ws []snapshot.Window
	for _, l := range limits {
		name, minutes := claudeLimitName(l)
		// A limit without a percent is no reading, not 0%.
		if name == "" || l.Percent == nil {
			continue
		}
		ws = append(ws, snapshot.Window{Name: name, Percent: *l.Percent, Minutes: minutes, ResetsAt: parseTime(l.ResetsAt)})
	}
	return ws
}

func claudeKeyWindows(u map[string]json.RawMessage) []snapshot.Window {
	var ws []snapshot.Window
	for _, k := range logs.ClaudeWindows {
		raw, ok := u[k.Key]
		if !ok {
			continue
		}
		var w claudeWindow
		if json.Unmarshal(raw, &w) != nil || w.Utilization == nil {
			continue
		}
		ws = append(ws, snapshot.Window{Name: k.Name, Percent: *w.Utilization, Minutes: k.Minutes, ResetsAt: parseTime(w.ResetsAt)})
	}
	return ws
}

func claudeLimitName(l claudeLimit) (string, int) {
	model := ""
	if l.Scope != nil && l.Scope.Model != nil {
		model = strings.TrimSpace(l.Scope.Model.DisplayName)
	}
	switch l.Kind {
	case "session":
		return "5h", 300
	case "weekly_all":
		return "7d", 10080
	case "weekly_scoped":
		if model == "" {
			return "7d scoped", 10080
		}
		return "7d " + model, 10080
	case "":
		return "", 0
	default:
		name := l.Kind
		if model != "" {
			name += " " + model
		}
		return name, 0
	}
}
