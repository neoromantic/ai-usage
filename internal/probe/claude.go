package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
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
// Claude Code last made it. Its age is reported.
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
			err := claudeRefresh(ctx, env, bin, configDir)
			// The read is judged by the cache it leaves, not by how Claude
			// Code exits: one that could not reach the usage, as offline,
			// still exits 0. And one that failed is no problem when the
			// cache is fresh anyway, as when Claude Code wrote it before it
			// failed, or the person's own session did meanwhile.
			if claudeStale(file, env.now()) {
				if err == nil {
					err = errors.New("claude /usage: no new reading")
				}
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

// claudeRefresh has Claude Code read the account's usage and cache it, as
// its /usage dialog does. In print mode /usage is a local command: it calls
// no model, and --no-session-persistence leaves no session behind. The
// person's hooks and the updater are off; hooks an organization manages
// still run. Nonessential traffic is left on, since without it Claude Code
// does not read the usage. Like any of its sessions, Claude Code renews its
// own login on the way when that has expired. Only the last line of what it
// prints is kept, for the error of a read that failed.
func claudeRefresh(ctx context.Context, env Env, bin, configDir string) error {
	ctx, cancel := context.WithTimeout(ctx, claudeUsageTimeout)
	defer cancel()
	cmd := env.command(ctx, bin, "-p", "/usage", "--no-session-persistence",
		"--model", claudeGuardModel, "--settings", `{"disableAllHooks":true}`)
	child := env.WithEnv("CLAUDE_CONFIG_DIR", configDir).WithEnv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "")
	cmd.Env = env.pathFor(child.harnessEnv("DISABLE_AUTOUPDATER", "1"), bin)
	// The null device: there is no prompt to wait for.
	cmd.Stdin = nil
	var said lastLine
	cmd.Stdout, cmd.Stderr = &said, &said
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	// The model catalog's warning is no error, even as the last line.
	lines := slices.DeleteFunc(said.lines(), func(l string) bool { return strings.Contains(l, "model catalog") })
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return errors.New("claude /usage: no answer in time")
	case slices.ContainsFunc(lines, claudeModelMissing):
		return errors.New("claude /usage: not supported by this Claude Code; update it")
	}
	msg := "claude /usage: " + shortErr(err)
	if l := errorOf(lines); l != "" {
		msg += ": " + l
	}
	return errors.New(msg)
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

func shortErr(err error) string {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit status %d", exit.ExitCode())
	}
	return truncate(err.Error(), 160)
}
