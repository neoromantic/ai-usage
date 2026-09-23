package collect

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/probe"
)

func TestRememberEnvKeepsClaudeConfigDirVerbatim(t *testing.T) {
	root := t.TempDir()
	def := filepath.Join(root, ".claude")
	work := filepath.Join(root, "claude-work")
	found := map[string][]string{"claude": {def, work}}
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	// The default home named explicitly is kept, trailing slash and all.
	env["CLAUDE_CONFIG_DIR"] = def + string(filepath.Separator)
	got, changed := RememberEnv(nil, getenv, found)
	if !changed || got["claude"][def] != def+string(filepath.Separator) {
		t.Fatalf("remembered = %v, %v", got, changed)
	}
	if _, changed := RememberEnv(got, getenv, found); changed {
		t.Fatal("the same value changed the config again")
	}

	// A second home is added beside it; a new spelling replaces the old.
	env["CLAUDE_CONFIG_DIR"] = work
	got, _ = RememberEnv(got, getenv, found)
	env["CLAUDE_CONFIG_DIR"] = def
	got, changed = RememberEnv(got, getenv, found)
	want := map[string]map[string]string{"claude": {def: def, work: work}}
	if !changed || !reflect.DeepEqual(got, want) {
		t.Fatalf("remembered = %v, want %v", got, want)
	}

	// A relative value, or one naming no home found, is not kept.
	for _, v := range []string{"rel-claude", filepath.Join(root, "missing")} {
		env["CLAUDE_CONFIG_DIR"] = v
		if _, changed := RememberEnv(got, getenv, found); changed {
			t.Fatalf("%q was remembered", v)
		}
	}
}

// The scheduler run and a variable naming this home are covered end to end
// by TestSchedulerRunProbesClaudeWithRememberedConfigDir.
func TestClaudeEnvUsesRememberedValueWhenUnset(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".claude")
	remembered := home + string(filepath.Separator)
	other := "CLAUDE_CONFIG_DIR=" + filepath.Join(filepath.Dir(home), "other")

	for _, tc := range []struct {
		name, remembered string
		environ          []string
		want             string
	}{
		{"variable names another home", remembered, []string{"PATH=/usr/bin", other}, remembered},
		{"nothing remembered", "", []string{"PATH=/usr/bin"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := claudeEnv(probe.Env{Environ: tc.environ}, home, tc.remembered)
			if got := env.Getenv("CLAUDE_CONFIG_DIR"); got != tc.want {
				t.Fatalf("CLAUDE_CONFIG_DIR = %q, want %q", got, tc.want)
			}
			if !slices.Contains(env.Environ, "PATH=/usr/bin") {
				t.Fatalf("environ lost PATH: %v", env.Environ)
			}
		})
	}
}

// A scheduler run, which has no CLAUDE_CONFIG_DIR, asks claude with the exact
// value an interactive run saw, even for the default home.
func TestSchedulerRunProbesClaudeWithRememberedConfigDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	w, o := newWorld(t)
	def := w.home(t, "claude")
	bin := t.TempDir()
	record := filepath.Join(bin, "record")
	script := "#!/bin/sh\nprintf '%s|' \"${CLAUDE_CONFIG_DIR-unset}\" >> '" + record + "'\n" +
		"echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"email\":\"ann\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	o.Ask = nil
	o.Probe.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return filepath.Join(bin, "claude"), nil
		}
		return "", os.ErrNotExist
	}
	o.Probe.Timeout = 10 * time.Second
	exported := def + "/"
	w.env["CLAUDE_CONFIG_DIR"] = exported
	o.Probe.Environ = []string{"PATH=/usr/bin:/bin", "CLAUDE_CONFIG_DIR=" + exported}
	res := run(t, o)
	if !IsCurrent(res.State, "claude", "ann") {
		t.Fatalf("current = %v", res.State.Current)
	}

	w.env = map[string]string{}
	o.Probe.Environ = []string{"PATH=/usr/bin:/bin"}
	w.now = t0.Add(15 * time.Minute)
	run(t, o)

	body, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSuffix(string(body), "|"), exported+"|"+exported; got != want {
		t.Fatalf("claude saw CLAUDE_CONFIG_DIR %q, want %q", got, want)
	}
	cfg, _ := o.Dir.LoadConfig()
	if cfg.HomeEnv["claude"][def] != exported || len(cfg.Homes["claude"]) != 0 {
		t.Fatalf("config = %+v", cfg)
	}
}
