package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/state"
	"github.com/neoromantic/ai-usage/relay"
)

func relayURL(cfg state.Config) string {
	if v := strings.TrimSpace(os.Getenv("AI_USAGE_RELAY")); v != "" {
		return v
	}
	if cfg.Relay != "" {
		return cfg.Relay
	}
	return defaultRelay
}

func cmdRelay(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	sub, args := subcommand(args)
	if sub == "serve" {
		return relayServe(ctx, args, stderr)
	}
	d, cfg, err := loadConfig()
	if err != nil {
		return err
	}
	switch sub {
	case "", "show":
		if endpoint := relayURL(cfg); endpoint == "" {
			fmt.Fprintln(stdout, "no relay configured")
		} else {
			fmt.Fprintln(stdout, endpoint)
		}
		return nil
	case "set":
		if len(args) != 1 {
			return usageError("relay set takes one URL")
		}
		u := strings.TrimRight(strings.TrimSpace(args[0]), "/")
		if p, err := url.Parse(u); err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || p.RawQuery != "" || p.Fragment != "" {
			return usageError("relay URL must look like https://host or https://host/path")
		}
		if _, err := d.EditConfig(func(c *state.Config) error { c.Relay = u; return nil }); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "relay set to %s\n", u)
		return nil
	case "clear":
		if _, err := d.EditConfig(func(c *state.Config) error { c.Relay = ""; return nil }); err != nil {
			return err
		}
		if defaultRelay != "" {
			fmt.Fprintf(stdout, "relay reset to the default %s\n", defaultRelay)
		} else {
			fmt.Fprintln(stdout, "relay cleared")
		}
		return nil
	default:
		return usageError("unknown relay command " + sub)
	}
}

func relayServe(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flags("relay serve")
	addr := fs.String("addr", ":8080", "")
	ipHeader := fs.String("client-ip-header", "", "")
	if err := parse(fs, args); err != nil {
		return err
	}
	header := strings.TrimSpace(*ipHeader)
	if strings.ContainsAny(header, " \t:") {
		return usageError("--client-ip-header takes a header name, such as X-Real-Ip")
	}
	store, kind := relay.StoreFromEnv()
	handler := relay.NewServer(store)
	// Behind a reverse proxy, the header it sets names the client.
	handler.ClientIPHeader = header
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
	// ListenAndServe returns as soon as the shutdown starts, so the command
	// waits for it to finish. A bug in it closes the server and is the
	// command's error rather than the end of the process.
	stopped := make(chan error, 1)
	go func() {
		defer close(stopped)
		defer func() {
			if v := recover(); v != nil {
				_ = srv.Close()
				stopped <- fmt.Errorf("relay shutdown stopped by a bug: %v", v)
			}
		}()
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	clients := "the connection's address"
	if header != "" {
		clients = header + " from a local proxy"
	}
	fmt.Fprintf(stderr, "relay listening on %s with %s store; clients by %s\n", *addr, kind, clients)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-stopped
}
