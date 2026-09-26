package collect

import (
	"cmp"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/neoromantic/ai-usage/internal/probe"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

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
// environment variable names, the ones remembered from earlier runs, the ones
// an app keeps per account or per session, and the profiles inside each of
// them. Only directories that exist are returned. The default home comes
// first and the rest follow in path order, so a run with the variables and
// one without them, like the scheduler's, read the same homes in the same
// order: where two homes hold the same log, the first one holds the session.
func Discover(userHome string, getenv func(string) string, remembered map[string][]string) map[string][]string {
	out := map[string][]string{}
	for _, p := range snapshot.Providers {
		def := probe.DefaultHome(userHome, p)
		var cands []string
		if def != "" {
			cands = append(cands, def)
		}
		if v := getenv(homeEnv[p]); v != "" {
			cands = append(cands, v)
		}
		cands = append(cands, remembered[p]...)
		cands = append(cands, managedHomes(p, userHome, getenv)...)
		cands = append(cands, claudeAppSessionHomes(p, userHome, getenv)...)
		found := map[string]bool{}
		for _, c := range cands {
			// A relative CODEX_HOME and the like is remembered for scheduler
			// runs, which start in another directory.
			if abs, err := filepath.Abs(c); err == nil {
				c = abs
			}
			c = filepath.Clean(c)
			for _, h := range append([]string{c}, profileHomes(p, c)...) {
				if info, err := os.Stat(h); err == nil && info.IsDir() {
					found[h] = true
				}
			}
		}
		for h := range found {
			out[p] = append(out[p], h)
		}
		slices.SortFunc(out[p], func(a, b string) int {
			if (a == def) != (b == def) {
				if a == def {
					return -1
				}
				return 1
			}
			return cmp.Compare(a, b)
		})
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

// managed is where an app that runs a harness once per account keeps each
// account's home: <app data>/<dir>/<account id>/home, marked by a file the
// app writes there. Orca points CODEX_HOME at one while a pane runs, so the
// scheduler, which never sees that variable, finds them by path.
var managed = map[string]struct{ app, dir, marker string }{
	"codex": {"orca", "codex-accounts", ".orca-managed-home"},
}

// managedHomes lists the per-account homes of p in the app's data
// directories, each in path order.
func managedHomes(p, userHome string, getenv func(string) string) []string {
	m, ok := managed[p]
	if !ok {
		return nil
	}
	var out []string
	for _, data := range appDataDirs(m.app, userHome, getenv) {
		root := filepath.Join(data, m.dir)
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if h := filepath.Join(root, e.Name(), "home"); isManaged(p, h) {
				out = append(out, h)
			}
		}
	}
	return out
}

// isManaged reports whether h is a per-account home an app keeps for p.
func isManaged(p, h string) bool {
	m, ok := managed[p]
	if !ok || filepath.Base(h) != "home" || filepath.Base(filepath.Dir(filepath.Dir(h))) != m.dir {
		return false
	}
	info, err := os.Stat(filepath.Join(h, m.marker))
	return err == nil && info.Mode().IsRegular()
}

// managedByDefault reports whether h is a per-account home in the app's
// default data directory, which every run finds again.
func managedByDefault(p, h, userHome string) bool {
	m, ok := managed[p]
	if !ok || userHome == "" || !isManaged(p, h) {
		return false
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(h))) == defaultAppData(m.app, userHome)
}

// appDataDirs are where an Electron app keeps its data: the default under
// userHome, and the one the environment moves it to, if it does.
func appDataDirs(app, userHome string, getenv func(string) string) []string {
	var out []string
	if userHome != "" {
		out = append(out, defaultAppData(app, userHome))
	}
	if v := appDataEnv[runtime.GOOS]; v != "" && getenv(v) != "" {
		out = append(out, filepath.Join(getenv(v), app))
	}
	return out
}

// appDataEnv is the variable that moves Electron's app data directory, by
// OS. On macOS nothing does.
var appDataEnv = map[string]string{
	"windows": "APPDATA",
	"linux":   "XDG_CONFIG_HOME",
	"freebsd": "XDG_CONFIG_HOME",
}

// defaultAppData is an Electron app's data directory when no variable
// moves it.
func defaultAppData(app, userHome string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(userHome, "Library", "Application Support", app)
	case "windows":
		return filepath.Join(userHome, "AppData", "Roaming", app)
	default:
		return filepath.Join(userHome, ".config", app)
	}
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
			if h == probe.DefaultHome(userHome, p) {
				continue
			}
			if isProfile(p, h, homes) || managedByDefault(p, h, userHome) || claudeAppRecord(p, h) != "" {
				continue
			}
			if slices.Contains(remembered[p], h) {
				continue
			}
			remembered[p] = append(remembered[p], h)
			sort.Strings(remembered[p])
			changed = true
		}
	}
	return remembered, changed
}

// unremembered is the found homes Remember would add to remembered. Found
// homes that are remembered already are left out, so recording the rest never
// brings back one a person removed meanwhile.
func unremembered(found map[string][]string, userHome string, remembered map[string][]string) map[string][]string {
	all, _ := Remember(nil, userHome, found)
	out := map[string][]string{}
	for p, homes := range all {
		for _, h := range homes {
			if !slices.Contains(remembered[p], h) {
				out[p] = append(out[p], h)
			}
		}
	}
	return out
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
		if !slices.Contains(found[p], home) || remembered[p][home] == v {
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
