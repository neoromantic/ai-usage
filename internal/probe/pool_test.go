package probe

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
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

// A harness command goes without the Account Pooler's route, so it asks the
// home's own login, and keeps a gateway the person set up.
func TestHarnessEnvLeavesThePool(t *testing.T) {
	env := Env{Environ: []string{
		"ANTHROPIC_BASE_URL=" + poolClaude, "ANTHROPIC_AUTH_TOKEN=made-up-hub-token",
		"CODEX_OPENAI_BASE_URL=" + poolCodex, "CODEX_POOL_AUTH_TOKEN=made-up-hub-token",
		"ENABLE_TOOL_SEARCH=true", "HOME=/home/ann",
	}}
	if got := env.harnessEnv("DISABLE_AUTOUPDATER", "1"); !slices.Equal(got, []string{"ENABLE_TOOL_SEARCH=true", "HOME=/home/ann", "DISABLE_AUTOUPDATER=1"}) {
		t.Errorf("harnessEnv = %q", got)
	}
	own := []string{"ANTHROPIC_BASE_URL=https://gateway.acme.dev/anthropic", "ANTHROPIC_AUTH_TOKEN=made-up-gateway-token"}
	env = Env{Environ: own}
	if got := env.harnessEnv(); !slices.Equal(got, own) {
		t.Errorf("a gateway of one's own: harnessEnv = %q", got)
	}
}

func TestCodexLeavesThePool(t *testing.T) {
	env, record := fakeEnv(t, "codex-ok", "CODEX_OPENAI_BASE_URL="+poolCodex, "CODEX_POOL_AUTH_TOKEN=made-up-hub-token",
		"ANTHROPIC_BASE_URL="+poolClaude, "ANTHROPIC_AUTH_TOKEN=made-up-hub-token")
	if _, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex")); err != nil {
		t.Fatalf("error: %v", err)
	}
	rec, _ := readRecord(t, record)
	for _, k := range []string{"CODEX_OPENAI_BASE_URL", "CODEX_POOL_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"} {
		if v, ok := rec.Env[k]; ok {
			t.Errorf("%s=%q reached codex", k, v)
		}
	}
	waitNoGoroutine(t, "probe.(*codexRPC)")
}
