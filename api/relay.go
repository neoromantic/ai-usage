// Package handler runs the relay as a Vercel Go function.
//
// vercel.json rewrites /v1/<rest> to /api/relay?path=<rest>. The function sees
// the rewritten path, so the handler puts /v1/<rest> back before the relay's
// routes match it.
//
// Vercel moves this file into a package of its own at build time, so the
// function must stay in this one file.
package handler

import (
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/neoromantic/ai-usage/relay"
)

var (
	once   sync.Once
	served http.Handler
)

// Handler serves the relay API.
func Handler(w http.ResponseWriter, r *http.Request) {
	h := instance()
	restorePath(r)
	h.ServeHTTP(w, r)
}

// instance builds the relay once per warm function instance, so later
// requests reuse its store client.
func instance() http.Handler {
	once.Do(func() { served = build() })
	return served
}

// build picks the store from the environment. The memory store forgets every
// snapshot when Vercel recycles the instance, so a deployment without KV
// refuses requests rather than pretend to store them. `vercel dev` is one
// long-lived process and may use memory.
func build() http.Handler {
	store, kind := relay.StoreFromEnv()
	if kind == "memory" && os.Getenv("VERCEL") != "" && os.Getenv("VERCEL_ENV") != "development" {
		return http.HandlerFunc(unconfigured)
	}
	srv := relay.NewServer(store)
	// Vercel's edge sets X-Real-Ip to the client's address, replacing any the
	// client sent. The Go bridge copies it into RemoteAddr too; naming it also
	// covers a runtime that leaves RemoteAddr empty or connects over loopback.
	srv.ClientIPHeader = "X-Real-Ip"
	return srv
}

func unconfigured(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"relay store not configured"}` + "\n"))
}

// restorePath undoes the rewrite: /api/relay?path=teams/x becomes /v1/teams/x.
// A request that still carries its /v1 path is left alone.
func restorePath(r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		return
	}
	q := r.URL.Query()
	p, ok := q["path"]
	if !ok || len(p) == 0 {
		return
	}
	q.Del("path")
	r.URL.Path = "/v1/" + strings.TrimLeft(p[0], "/")
	r.URL.RawPath = ""
	r.URL.RawQuery = q.Encode()
	r.RequestURI = r.URL.RequestURI()
}
