package probe

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const (
	poolClaude = "http://127.0.0.1:4100/api/v1/plugins/account-pool/http"
	poolCodex  = "http://127.0.0.1:4100/api/v1/plugins/account-pool/http/v1"
)

func TestPoolURL(t *testing.T) {
	for u, want := range map[string]bool{
		poolClaude: true,
		poolCodex:  true,
		"https://bb.acme.dev/base/api/v1/plugins/account-pool/http": true,
		// A gateway of one's own, Anthropic's own API, or no URL at all.
		"https://gateway.acme.dev/anthropic":            false,
		"https://api.anthropic.com":                     false,
		"/api/v1/plugins/account-pool/http":             false,
		"https://gateway.acme.dev/plugins/account-pool": false,
		"": false,
	} {
		if got := PoolURL(u); got != want {
			t.Errorf("PoolURL(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestPooled(t *testing.T) {
	env := Env{Environ: []string{"ANTHROPIC_BASE_URL=" + poolClaude, "CODEX_OPENAI_BASE_URL=" + poolCodex}}
	if got := env.Pooled(); !slices.Equal(got, []string{"claude", "codex"}) {
		t.Errorf("Pooled = %q", got)
	}
	env = Env{Environ: []string{"ANTHROPIC_BASE_URL=https://gateway.acme.dev/anthropic"}}
	if got := env.Pooled(); got != nil {
		t.Errorf("a gateway of one's own: Pooled = %q", got)
	}
}

// BB's pool status says when its enabled accounts of each harness it routes
// were last used; a harness it does not route, a disabled account, and a bb
// that does not answer say nothing.
func TestPoolUse(t *testing.T) {
	env, record := fakeEnv(t, "bb-pool")
	want := map[string]time.Time{"claude": time.UnixMilli(1790154000000).UTC()}
	if got := env.PoolUse(context.Background()); !maps.Equal(got, want) {
		t.Errorf("PoolUse = %v, want %v", got, want)
	}
	rec, _ := readRecord(t, record)
	if rec.Name != "bb" || !slices.Equal(rec.Args, []string{"pool", "status", "--json"}) {
		t.Errorf("ran %s %q", rec.Name, rec.Args)
	}
	env, _ = fakeEnv(t, "bb-fail")
	if got := env.PoolUse(context.Background()); got != nil {
		t.Errorf("bb that fails: PoolUse = %v", got)
	}
	env, _ = fakeEnv(t, "bb-other")
	if got := env.PoolUse(context.Background()); got != nil {
		t.Errorf("another tool named bb: PoolUse = %v", got)
	}
	env.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	if got := env.PoolUse(context.Background()); got != nil {
		t.Errorf("no bb: PoolUse = %v", got)
	}
	// The bb BB writes to ~/.bb/bin comes before one on PATH.
	env, record = fakeEnv(t, "bb-pool")
	shim := filepath.Join(env.HomeDir, ".bb", "bin", "bb")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := env.PoolUse(context.Background()); !maps.Equal(got, want) {
		t.Errorf("shim: PoolUse = %v", got)
	}
	if rec, _ := readRecord(t, record); rec.Path != shim {
		t.Errorf("ran %s, want %s", rec.Path, shim)
	}
}
