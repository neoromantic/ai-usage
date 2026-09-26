package collect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverAndRemember(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	defCodex := mk("home", ".codex")
	envCodex := mk("codex-env")
	oldCodex := mk("codex-old")
	env := map[string]string{
		"CODEX_HOME":        envCodex + string(filepath.Separator), // cleaned
		"CLAUDE_CONFIG_DIR": filepath.Join(root, "missing"),        // not a directory: left out
	}
	remembered := map[string][]string{"codex": {oldCodex, envCodex, filepath.Join(root, "gone")}}
	got := Discover(home, func(k string) string { return env[k] }, remembered)
	want := map[string][]string{"codex": {defCodex, envCodex, oldCodex}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover = %v, want %v", got, want)
	}

	next, changed := Remember(nil, home, got)
	if !changed || !reflect.DeepEqual(next, map[string][]string{"codex": {envCodex, oldCodex}}) {
		t.Fatalf("Remember = %v, %v", next, changed)
	}
	if _, changed := Remember(next, home, got); changed {
		t.Fatal("remembering the same homes again reported a change")
	}
}

func TestDiscoverFindsHermesProfiles(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	mk := func(parts ...string) string {
		p := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	db := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "state.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	def := mk("home", ".hermes")
	work := mk("home", ".hermes", "profiles", "work")
	db(work)
	mk("home", ".hermes", "profiles", "empty") // no usage yet
	deleted := mk("home", ".hermes", "profiles", ".deleted")
	db(deleted)
	other := mk("hermes-root")
	otherProf := mk("hermes-root", "profiles", "lab")
	db(otherProf)

	env := map[string]string{"HERMES_HOME": other}
	got := Discover(home, func(k string) string { return env[k] }, nil)
	if want := []string{def, other, otherProf, work}; !reflect.DeepEqual(got["hermes"], want) {
		t.Fatalf("hermes homes = %v, want %v", got["hermes"], want)
	}
	// Profiles are found again through their root and are not remembered.
	next, _ := Remember(nil, home, got)
	if want := []string{other}; !reflect.DeepEqual(next["hermes"], want) {
		t.Fatalf("remembered = %v, want %v", next["hermes"], want)
	}
	// HERMES_HOME naming the profile itself: found, and not remembered either.
	env["HERMES_HOME"] = work
	got = Discover(home, func(k string) string { return env[k] }, nil)
	if want := []string{def, work}; !reflect.DeepEqual(got["hermes"], want) {
		t.Fatalf("hermes homes = %v, want %v", got["hermes"], want)
	}
	if next, changed := Remember(nil, home, got); changed {
		t.Fatalf("remembered %v", next)
	}
}

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
