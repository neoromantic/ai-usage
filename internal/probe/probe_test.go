package probe

import (
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestHarnessEnv(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		value   string
		want    []string
	}{
		{"replace", []string{"CLAUDE_CONFIG_DIR=/old", "PATH=/bin"}, "/h", []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/h"}},
		{"remove for default home", []string{"CLAUDE_CONFIG_DIR=/old", "PATH=/bin", "CLAUDE_CONFIG_DIR=/older"}, "", []string{"PATH=/bin"}},
		{"similar names kept", []string{"CLAUDE_CONFIG_DIRS=a", "XCLAUDE_CONFIG_DIR=b", "CLAUDE_CONFIG_DIR"}, "", []string{"CLAUDE_CONFIG_DIRS=a", "XCLAUDE_CONFIG_DIR=b", "CLAUDE_CONFIG_DIR"}},
		{"empty environ stays empty", []string{}, "", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Env{Environ: tc.environ}.harnessEnv("CLAUDE_CONFIG_DIR", tc.value)
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHarnessEnvNilInheritsProcess(t *testing.T) {
	t.Setenv("PROBE_MARKER", "yes")
	t.Setenv("CODEX_HOME", "/stale")
	got := Env{}.harnessEnv("CODEX_HOME", "")
	if !slices.Contains(got, "PROBE_MARKER=yes") {
		t.Errorf("nil Environ should inherit the process environment, got %d entries", len(got))
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "CODEX_HOME=") {
			t.Errorf("CODEX_HOME kept: %q", kv)
		}
	}
}

func TestHarnessEnvWindowsIgnoresCase(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("variable names are case-insensitive only on Windows")
	}
	got := Env{Environ: []string{"Claude_Config_Dir=/old"}}.harnessEnv("CLAUDE_CONFIG_DIR", "")
	if len(got) != 0 {
		t.Errorf("got %q", got)
	}
}

func TestPathFor(t *testing.T) {
	sep := string(filepath.ListSeparator)
	bin := filepath.Join("npm", "bin", "codex")
	e := Env{SystemBinDirs: []string{"/opt/homebrew/bin", "/usr/bin"}}
	tests := []struct {
		name    string
		environ []string
		want    []string
	}{
		{"adds what PATH lacks", []string{"A=1", "PATH=/usr/bin" + sep + "/bin"},
			[]string{"A=1", "PATH=/usr/bin" + sep + "/bin" + sep + filepath.Join("npm", "bin") + sep + "/opt/homebrew/bin"}},
		{"keeps the order of what it has", []string{"PATH=/opt/homebrew/bin" + sep + filepath.Join("npm", "bin") + sep + "/usr/bin"},
			[]string{"PATH=/opt/homebrew/bin" + sep + filepath.Join("npm", "bin") + sep + "/usr/bin"}},
		{"sets it when missing", []string{"A=1"},
			[]string{"A=1", "PATH=" + filepath.Join("npm", "bin") + sep + "/opt/homebrew/bin" + sep + "/usr/bin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := slices.Clone(tc.environ)
			got := e.pathFor(in, bin)
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if !slices.Equal(in, tc.environ) {
				t.Errorf("changed its input: %q", in)
			}
		})
	}
}

func TestJoinedErrorsKeepLoggedOut(t *testing.T) {
	err := joinErrors([]error{errors.New("claude auth status: slow"), notLoggedIn("claude"), errors.New("claude config is not JSON")})
	for _, part := range []string{"slow", "not logged in", "not JSON"} {
		if !strings.Contains(errText(err), part) {
			t.Errorf("message %q lacks %q", errText(err), part)
		}
	}
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Error("the joined error lost the logged-out answer")
	}
	if errors.Is(joinErrors([]error{errors.New("codex initialize: no answer in time")}), ErrNotLoggedIn) {
		t.Error("a timeout reads as logged out")
	}
	if joinErrors(nil) != nil {
		t.Error("no problems should be no error")
	}
}
