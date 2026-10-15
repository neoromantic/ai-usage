package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHomebrew(t *testing.T) {
	for _, tc := range []struct {
		path, prefix string
		ok           bool
	}{
		{"/opt/homebrew/Cellar/ai-usage/0.8.0/bin/ai-usage", "/opt/homebrew", true},
		{"/home/linuxbrew/.linuxbrew/Cellar/ai-usage/0.8.0_1/bin/ai-usage", "/home/linuxbrew/.linuxbrew", true},
		{"/opt/homebrew/Cellar/other/0.8.0/bin/ai-usage", "", false},
		{"/opt/homebrew/bin/ai-usage", "", false},
		{"/Users/ann/.local/bin/ai-usage", "", false},
		{"/opt/homebrew/Cellar/ai-usage/0.8.0/bin/ai-usage-old", "", false},
	} {
		prefix, ok := Homebrew(filepath.FromSlash(tc.path))
		if ok != tc.ok || prefix != filepath.FromSlash(tc.prefix) {
			t.Errorf("Homebrew(%q) = %q, %v; want %q, %v", tc.path, prefix, ok, tc.prefix, tc.ok)
		}
	}
}

func TestHomebrewStable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Homebrew does not run on Windows")
	}
	prefix := t.TempDir()
	real := filepath.Join(prefix, "Cellar", "ai-usage", "0.8.0", "bin", "ai-usage")
	// Without the opt link, the keg's path is all there is.
	if got := HomebrewStable(real); got != real {
		t.Fatalf("without opt: %q, want %q", got, real)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "opt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/ai-usage/0.8.0", filepath.Join(prefix, "opt", "ai-usage")); err != nil {
		t.Fatal(err)
	}
	// The keg itself need not exist: brew may have removed it already.
	want := filepath.Join(prefix, "opt", "ai-usage", "bin", "ai-usage")
	if got := HomebrewStable(real); got != want {
		t.Fatalf("with opt: %q, want %q", got, want)
	}
	if got := HomebrewStable("/usr/local/bin/ai-usage"); got != "/usr/local/bin/ai-usage" {
		t.Fatalf("outside a keg: %q", got)
	}
}
