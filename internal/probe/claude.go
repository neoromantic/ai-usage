package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Claude asks `claude auth status --json` who is logged in, then reads the
// usage Claude Code cached in its own config file. The cache is used only for
// a claude.ai login, and only when it names the same account the config file
// says is logged in.
//
// The cache is as fresh as Claude Code last made it. Its age is reported.
func Claude(ctx context.Context, env Env, home string) (Reading, error) {
	var r Reading
	var errs []error
	configDir := env.claudeConfigDir(home)

	var st claudeStatus
	if bin, ok := env.find("claude"); ok {
		var err error
		st, err = claudeAuthStatus(ctx, env, bin, configDir)
		if err != nil {
			errs = append(errs, err)
		}
		r.Account, r.Plan = st.label(), st.SubscriptionType
	} else {
		errs = append(errs, errors.New("claude binary not found; account unknown"))
	}

	quota, err := claudeCachedUsage(claudeConfigFile(env.HomeDir, home, configDir))
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
	cmd.Env = env.harnessEnv("CLAUDE_CONFIG_DIR", configDir)
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

type claudeConfig struct {
	OAuthAccount *struct {
		AccountUUID string `json:"accountUuid"`
	} `json:"oauthAccount"`
	Cached *struct {
		FetchedAtMs int64                      `json:"fetchedAtMs"`
		AccountUUID string                     `json:"accountUuid"`
		Utilization map[string]json.RawMessage `json:"utilization"`
	} `json:"cachedUsageUtilization"`
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
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claude config: %s", shortErr(err))
	}
	var cfg claudeConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, errors.New("claude config is not JSON")
	}
	c := cfg.Cached
	if c == nil || c.FetchedAtMs <= 0 {
		return nil, nil
	}
	if cfg.OAuthAccount == nil || c.AccountUUID == "" || c.AccountUUID != cfg.OAuthAccount.AccountUUID {
		// The cache belongs to an account that is no longer logged in.
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
		for _, k := range []struct {
			key, name string
			minutes   int
		}{
			{"five_hour", "5h", 300},
			{"seven_day", "7d", 10080},
			{"seven_day_opus", "7d Opus", 10080},
			{"seven_day_sonnet", "7d Sonnet", 10080},
		} {
			raw, ok := c.Utilization[k.key]
			if !ok {
				continue
			}
			var w claudeWindow
			if json.Unmarshal(raw, &w) != nil || w.Utilization == nil {
				continue
			}
			q.Windows = append(q.Windows, snapshot.Window{Name: k.name, Percent: *w.Utilization, Minutes: k.minutes, ResetsAt: parseTime(w.ResetsAt)})
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
