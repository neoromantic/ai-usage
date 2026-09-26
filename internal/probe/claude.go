package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
// says is logged in.
//
// Claude Code writes that cache only when it reads the usage, as its /usage
// dialog does. So for a claude.ai subscription whose config names its
// account and whose cache is missing or older than claudeFresh, in a home
// this OS user owns and used in the last claudeInUse and since that cache,
// Claude Code is first asked to read it again. The cache is as fresh as
// Claude Code last made it. Its age is reported, and when the read leaves it
// stale, why.
func Claude(ctx context.Context, env Env, home string) (Reading, error) {
	var r Reading
	var errs []error
	configDir := env.claudeConfigDir(home)
	file := claudeConfigFile(env.HomeDir, home, configDir)

	var st claudeStatus
	if bin, ok := env.find("claude"); ok {
		var err error
		st, err = claudeAuthStatus(ctx, env, bin, configDir)
		if err != nil {
			errs = append(errs, err)
		}
		r.Account, r.Plan = st.label(), st.SubscriptionType
		if st.subscription() && claudeHasAccount(file) && claudeUsedSince(file, LastUse(ctx), env.now()) && claudeOwned(file, home) && claudeStale(file, env.now()) {
			if err := claudeReadUsage(ctx, env, bin, configDir, home, file); err != nil {
				errs = append(errs, err)
			}
		}
	} else {
		errs = append(errs, errors.New("claude binary not found; account unknown"))
	}

	quota, err := claudeCachedUsage(file)
	if err != nil {
		errs = append(errs, err)
	}
	// The cache holds a claude.ai subscription's limits. An API key or a
	// cloud provider has none, even with a claude.ai account in the config.
	if st.AuthMethod == "claude.ai" {
		r.Quota = quota
	}
	return r, joinErrors(errs)
}

func isDefaultHome(userHome, home, leaf string) bool {
	if userHome == "" {
		return false
	}
	return filepath.Clean(home) == filepath.Join(userHome, leaf)
}

// claudeConfigDir is the CLAUDE_CONFIG_DIR claude runs with for home, or ""
// to run it without one. Once the variable is set, even to the default
// ~/.claude, Claude Code keeps its config inside the directory and its login
// under a keychain entry named after the exact string. So a value in Environ
// that names this home is passed on as it is. Otherwise a custom home is
// passed, and the default home runs without the variable.
func (e Env) claudeConfigDir(home string) string {
	if v := e.getenv("CLAUDE_CONFIG_DIR"); v != "" && samePath(v, home) {
		if filepath.IsAbs(v) {
			return v
		}
		// Relative to the caller's directory, not the one claude starts in.
		return home
	}
	if isDefaultHome(e.HomeDir, home, ".claude") {
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
// claude.ai, and has no subscription.
func (st claudeStatus) subscription() bool {
	return st.AuthMethod == "claude.ai" && strings.TrimSpace(st.SubscriptionType) != ""
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
	cmd := env.command(ctx, bin, "auth", "status", "--json")
	cmd.Env = env.pathFor(env.harnessEnv("CLAUDE_CONFIG_DIR", configDir), bin)
	cmd.Stdin = nil
	// A child the CLI leaves behind can hold stdout open after the kill.
	cmd.WaitDelay = time.Second
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
// An older one shows the usage at most, so asking it leaves no reading.
const claudeCaching = "2.1.208"

// claudeReadUsage has Claude Code read the usage into its cache, and says
// why the cache is still stale afterwards. The read is judged by the cache
// it leaves, not by how Claude Code exits: one that could not reach the
// usage, as offline, still exits 0. And one that failed is no problem when
// the cache is fresh anyway, as when Claude Code wrote it before it failed,
// or the person's own session did meanwhile.
//
// Claude Code is not run when it cannot cache the usage: when it is too old
// to, or its settings turn off the traffic the read needs. Neither goes away
// by itself, so it is said at every run instead.
func claudeReadUsage(ctx context.Context, env Env, bin, configDir, home, file string) error {
	version := claudeVersion(bin)
	if version != "" && selfupdate.Newer(claudeCaching, version) {
		why := fmt.Sprintf("Claude Code %s does not cache the usage; update it to %s or later", version, claudeCaching)
		return claudeUsageError(why, "", file, env.now(), "")
	}
	if claudeNoTraffic(env, home) {
		return claudeUsageError("nonessential traffic is off in Claude Code's settings", version, file, env.now(), "")
	}
	run := claudeRefresh(ctx, env, bin, configDir)
	if !claudeStale(file, env.now()) {
		return nil
	}
	why, said := run.why(version)
	return claudeUsageError(why, version, file, env.now(), said)
}

// claudeUsageError says why a read left no new reading, then which Claude
// Code it was when that is known and what cache there is, and last a line of
// what it printed, if any, so that an error cut short loses that first.
func claudeUsageError(why, version, file string, now time.Time, said string) error {
	var about []string
	if version != "" {
		about = append(about, "Claude Code "+version)
	}
	if c := claudeCacheState(file, now); c != "" {
		about = append(about, c)
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
// no model, and --no-session-persistence leaves no session behind. The
// person's hooks and the updater are off; hooks an organization manages
// still run. Nonessential traffic is left on, since without it Claude Code
// does not read the usage. Like any of its sessions, Claude Code renews its
// own login on the way when that has expired. Only the start of what it
// prints and the end of its errors are kept, to tell why a read failed.
func claudeRefresh(ctx context.Context, env Env, bin, configDir string) claudeRun {
	ctx, cancel := context.WithTimeout(ctx, claudeUsageTimeout)
	defer cancel()
	cmd := env.command(ctx, bin, "-p", "/usage", "--no-session-persistence",
		"--model", claudeGuardModel, "--settings", `{"disableAllHooks":true}`)
	child := env.WithEnv("CLAUDE_CONFIG_DIR", configDir).WithEnv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "")
	cmd.Env = env.pathFor(child.harnessEnv("DISABLE_AUTOUPDATER", "1"), bin)
	// The null device: there is no prompt to wait for.
	cmd.Stdin = nil
	var out firstBytes
	var errOut lastLine
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = time.Second
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
func (r claudeRun) why(version string) (why, said string) {
	all := append(slices.Clone(r.out), r.errOut...)
	switch {
	case r.timedOut:
		return "no answer in time", ""
	case slices.ContainsFunc(all, claudeCannotRead):
		return "no new reading: this Claude Code cannot read the usage; update it", ""
	case slices.ContainsFunc(r.out, claudeShowsUsage):
		// One older than claudeCaching shows the usage and caches none.
		if version == "" {
			return "no new reading: Claude Code showed the usage but did not cache it; update it", ""
		}
		return "no new reading: Claude Code showed the usage but did not cache it", ""
	case slices.ContainsFunc(r.out, func(l string) bool { return strings.HasPrefix(l, "Total cost:") }):
		return "no new reading: Claude Code sees no claude.ai plan", ""
	case slices.ContainsFunc(r.out, func(l string) bool { return strings.Contains(l, claudeHeadline) }):
		// Claude Code could not reach the usage: offline, with a login
		// without the profile scope, or with nonessential traffic off where
		// claudeNoTraffic does not look. Or, older than claudeCaching, it did
		// not try. The likely causes are left to the docs, so that two homes'
		// reasons fit in a source's error.
		return "no new reading: could not read the usage", ""
	case r.err != nil:
		// Its errors, or what it printed that says it is an error.
		said := errorOf(r.errOut)
		if said == "" {
			said = errorOf(slices.DeleteFunc(slices.Clone(r.out), func(l string) bool { return !errorLine.MatchString(l) }))
		}
		return shortErr(r.err), truncate(said, 100)
	case len(all) == 0:
		return "no new reading: it printed nothing", ""
	}
	return "no new reading", ""
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

// claudeCacheState says in a few words what usage cache the config file at
// path holds for the account logged in: none, another account's, or one of
// what age. It says nothing of a config that cannot be read, whose error is
// reported when the cache is read.
func claudeCacheState(path string, now time.Time) string {
	cfg, err := readClaudeConfig(path)
	if err != nil {
		return ""
	}
	c := cfg.Cached
	switch {
	case c == nil || c.FetchedAtMs <= 0:
		return "no cache"
	case cfg.cache() == nil:
		return "cache of another account"
	}
	age := now.Sub(time.UnixMilli(c.FetchedAtMs))
	switch {
	case age < 0:
		return "cache from the future"
	case age < time.Hour:
		return fmt.Sprintf("cache %dm old", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("cache %dh old", int(age.Hours()))
	}
	return fmt.Sprintf("cache %dd old", int(age.Hours()/24))
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
		AccountUUID string `json:"accountUuid"`
	} `json:"oauthAccount"`
	Cached *claudeUsageCache `json:"cachedUsageUtilization"`
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

// claudeStale reports whether Claude Code should read the usage again: the
// cache in the config file at path is missing, another account's, or older
// than claudeFresh. One from the future is stale too, as it is to Claude
// Code. A config that cannot be read is not: Claude Code would meet the
// same file, and reading the cache reports it.
func claudeStale(path string, now time.Time) bool {
	cfg, err := readClaudeConfig(path)
	if err != nil {
		return false
	}
	c := cfg.cache()
	if c == nil {
		return true
	}
	age := now.Sub(time.UnixMilli(c.FetchedAtMs))
	return age < 0 || age >= claudeFresh
}

type lastUseKey struct{}

// WithLastUse tells a probe when the home it asks about was last used, as
// its logs show.
func WithLastUse(ctx context.Context, t time.Time) context.Context {
	return context.WithValue(ctx, lastUseKey{}, t)
}

// LastUse is the time WithLastUse gave, or zero.
func LastUse(ctx context.Context) time.Time {
	t, _ := ctx.Value(lastUseKey{}).(time.Time)
	return t
}

// claudeInUse is how recently a home must have been used for Claude Code to
// be asked to read its usage. Runs every 15 minutes ask within it, and a
// read that keeps failing stops being made soon after the home goes idle.
const claudeInUse = time.Hour

// claudeUsedSince reports whether the home was used in the last claudeInUse,
// and after the cache in the config file at path was fetched when there is
// one. A cache fetched after now counts as none, as it does to Claude Code.
// Claude Code renews an expired login when it reads the usage, so it is
// asked only for a home in use, whose own sessions keep that login alive
// anyway. An idle login is left to expire, and an idle home has spent
// nothing since.
func claudeUsedSince(path string, used, now time.Time) bool {
	if used.IsZero() || now.Sub(used) > claudeInUse {
		return false
	}
	cfg, err := readClaudeConfig(path)
	if err != nil {
		return false
	}
	c := cfg.cache()
	if c == nil {
		return true
	}
	fetched := time.UnixMilli(c.FetchedAtMs)
	return fetched.After(now) || used.After(fetched)
}

// claudeHasAccount reports whether the config file at path names the account
// logged in. Claude Code tags the usage it caches with that account, and a
// cache without one is not read. So for a config that names none, as one
// reset while the login stays, a read would leave no reading, every run.
func claudeHasAccount(path string) bool {
	cfg, err := readClaudeConfig(path)
	return err == nil && cfg.OAuthAccount != nil && cfg.OAuthAccount.AccountUUID != ""
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

// claudeCachedUsage reads cachedUsageUtilization. A missing file or cache is
// no reading, not an error.
func claudeCachedUsage(path string) (*Quota, error) {
	cfg, err := readClaudeConfig(path)
	if err != nil {
		return nil, err
	}
	c := cfg.cache()
	if c == nil {
		return nil, nil
	}
	q := &Quota{At: time.UnixMilli(c.FetchedAtMs).UTC(), Source: "cache"}
	if raw, ok := c.Utilization["limits"]; ok {
		var limits []claudeLimit
		if json.Unmarshal(raw, &limits) == nil {
			for _, l := range limits {
				name, minutes := claudeLimitName(l)
				// A limit without a percent is no reading, not 0%.
				if name == "" || l.Percent == nil {
					continue
				}
				q.Windows = append(q.Windows, snapshot.Window{Name: name, Percent: *l.Percent, Minutes: minutes, ResetsAt: parseTime(l.ResetsAt)})
			}
		}
	}
	if len(q.Windows) == 0 {
		for _, k := range logs.ClaudeWindows {
			raw, ok := c.Utilization[k.Key]
			if !ok {
				continue
			}
			var w claudeWindow
			if json.Unmarshal(raw, &w) != nil || w.Utilization == nil {
				continue
			}
			q.Windows = append(q.Windows, snapshot.Window{Name: k.Name, Percent: *w.Utilization, Minutes: k.Minutes, ResetsAt: parseTime(w.ResetsAt)})
		}
	}
	if len(q.Windows) == 0 {
		return nil, nil
	}
	return q, nil
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
		return snapshot.PlainLabel("7d " + model), 10080
	case "":
		return "", 0
	default:
		name := l.Kind
		if model != "" {
			name += " " + model
		}
		return snapshot.PlainLabel(name), 0
	}
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
