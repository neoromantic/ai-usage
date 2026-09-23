package collect

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Providers in display order.
var Providers = []string{"claude", "codex", "grok", "hermes"}

var homeEnv = map[string]string{
	"claude": "CLAUDE_CONFIG_DIR",
	"codex":  "CODEX_HOME",
	"grok":   "GROK_HOME",
	"hermes": "HERMES_HOME",
}

// profiles is where a harness keeps named profiles inside a home, each a home
// of its own, and the file a profile with usage has. Hermes picks its active
// profile itself (`hermes profile use`), so no exported variable names it.
var profiles = map[string]struct{ dir, marker string }{
	"hermes": {"profiles", "state.db"},
}

// Discover returns candidate homes per provider: the default home, the one an
// environment variable names, the ones remembered from earlier runs, and the
// profiles inside each of them. Only directories that exist are returned.
func Discover(userHome string, getenv func(string) string, remembered map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, p := range Providers {
		var cands []string
		if v := getenv(homeEnv[p]); v != "" {
			cands = append(cands, v)
		}
		if userHome != "" {
			cands = append(cands, filepath.Join(userHome, "."+p))
		}
		cands = append(cands, remembered[p]...)
		seen := map[string]bool{}
		add := func(c string) {
			if seen[c] {
				return
			}
			seen[c] = true
			if info, err := os.Stat(c); err == nil && info.IsDir() {
				out[p] = append(out[p], c)
			}
		}
		for _, c := range cands {
			// A relative CODEX_HOME and the like is remembered for scheduler
			// runs, which start in another directory.
			if abs, err := filepath.Abs(c); err == nil {
				c = abs
			}
			c = filepath.Clean(c)
			add(c)
			for _, prof := range profileHomes(p, c) {
				add(prof)
			}
		}
	}
	return out
}

// profileHomes lists the profiles inside home that have usage, in name order.
// A name starting with a dot is not a profile, such as Hermes' tombstone of
// deleted ones.
func profileHomes(p, home string) []string {
	pr, ok := profiles[p]
	if !ok {
		return nil
	}
	root := filepath.Join(home, pr.dir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if info, err := os.Stat(filepath.Join(dir, pr.marker)); err == nil && info.Mode().IsRegular() {
			out = append(out, dir)
		}
	}
	return out
}

// isProfile reports whether h is a profile inside one of homes, which finds
// it again on every run.
func isProfile(p, h string, homes []string) bool {
	pr, ok := profiles[p]
	if !ok {
		return false
	}
	for _, r := range homes {
		if filepath.Dir(h) == filepath.Join(r, pr.dir) {
			return true
		}
	}
	return false
}

// Remember adds env-named homes to the remembered set and reports a change.
// The system scheduler runs without the person's environment, so a home seen
// once through CODEX_HOME and the like is kept for later runs.
func Remember(remembered map[string][]string, userHome string, found map[string][]string) (map[string][]string, bool) {
	if remembered == nil {
		remembered = map[string][]string{}
	}
	changed := false
	for p, homes := range found {
		for _, h := range homes {
			if userHome != "" && h == filepath.Join(userHome, "."+p) {
				continue
			}
			if isProfile(p, h, homes) {
				continue
			}
			if contains(remembered[p], h) {
				continue
			}
			remembered[p] = append(remembered[p], h)
			sort.Strings(remembered[p])
			changed = true
		}
	}
	return remembered, changed
}

// envKeptVerbatim are the variables whose exact value a harness depends on,
// beyond the directory it names.
var envKeptVerbatim = []string{"claude"}

// RememberEnv records the exact value of each provider variable in
// envKeptVerbatim that names one of the found homes, trailing slash and all,
// and reports a change. It is kept even for the default home: Claude Code
// behaves differently once CLAUDE_CONFIG_DIR is set at all. A relative value
// is not kept, since it names another directory from where the scheduler
// starts.
func RememberEnv(remembered map[string]map[string]string, getenv func(string) string, found map[string][]string) (map[string]map[string]string, bool) {
	changed := false
	for _, p := range envKeptVerbatim {
		v := getenv(homeEnv[p])
		if v == "" || !filepath.IsAbs(v) {
			continue
		}
		home := filepath.Clean(v)
		if !contains(found[p], home) || remembered[p][home] == v {
			continue
		}
		if remembered == nil {
			remembered = map[string]map[string]string{}
		}
		if remembered[p] == nil {
			remembered[p] = map[string]string{}
		}
		remembered[p][home] = v
		changed = true
	}
	return remembered, changed
}

// samePath reports whether a and b name the same directory, relative paths
// taken from this process's directory.
func samePath(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)
	return errA == nil && errB == nil && a == b
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
