package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRelayServeClientIPHeader(t *testing.T) {
	hermetic(t)
	// A stopped context shuts the relay down as soon as it starts.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if err := cmdRelay(ctx, []string{"serve", "--addr", "127.0.0.1:0", "--client-ip-header", " X-Real-Ip "}, &stdout, &stderr); err != nil {
		t.Fatalf("relay serve: %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "X-Real-Ip") {
		t.Fatalf("stderr = %q", got)
	}
	err := cmdRelay(ctx, []string{"serve", "--addr", "127.0.0.1:0", "--client-ip-header", "X-Real-Ip: 1.2.3.4"}, &stdout, &stderr)
	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("a header value instead of a name: %v", err)
	}
}

// buggyContext panics when asked for its end, as a bug in the goroutine that
// shuts the relay down would.
type buggyContext struct{ context.Context }

func (buggyContext) Done() <-chan struct{} { panic("shutdown bug") }

// A bug while the relay shuts down closes it and is the command's error.
func TestRelayShutdownPanicIsItsError(t *testing.T) {
	hermetic(t)
	var out bytes.Buffer
	err := cmdRelay(buggyContext{context.Background()}, []string{"serve", "--addr", "127.0.0.1:0"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "shutdown bug") {
		t.Fatalf("relay serve: %v", err)
	}
}

func TestRelayCommands(t *testing.T) {
	hermetic(t)
	d := newDevice(t)
	if out := d.ok("relay", "show"); !strings.Contains(out, "no relay") {
		t.Fatalf("show = %q", out)
	}
	d.ok("relay", "set", " https://relay.example.test/ ")
	if out := d.ok("relay"); out != "https://relay.example.test\n" {
		t.Fatalf("show = %q", out)
	}
	for _, bad := range []string{"ftp://relay.example.test", "https://", "relay.example.test", "https://relay.example.test/?team=x", "https://relay.example.test/#x"} {
		if r := d.run("", "relay", "set", bad); r.code != 2 {
			t.Fatalf("relay set %q: exit %d", bad, r.code)
		}
	}
	if d.config().Relay != "https://relay.example.test" {
		t.Fatal("a rejected URL changed the config")
	}
	t.Setenv("AI_USAGE_RELAY", "http://127.0.0.1:9")
	if out := d.ok("relay", "show"); out != "http://127.0.0.1:9\n" {
		t.Fatalf("show with AI_USAGE_RELAY = %q", out)
	}
	t.Setenv("AI_USAGE_RELAY", "")
	d.ok("relay", "clear")
	if out := d.ok("relay", "show"); !strings.Contains(out, "no relay") {
		t.Fatalf("show after clear = %q", out)
	}

	defaultRelay = "https://default.example.test"
	if out := d.ok("relay", "clear"); !strings.Contains(out, "https://default.example.test") {
		t.Fatalf("clear with a built-in relay = %q", out)
	}
	defaultRelay = ""

	r := d.run("", "team", "forget-device", "d-0123456789abcdef01234567")
	if r.code != 1 || !strings.Contains(r.stderr, "no relay") {
		t.Fatalf("forget-device without a relay: exit %d, %q", r.code, r.stderr)
	}
}
