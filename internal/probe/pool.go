package probe

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// BB's Account Pooler routes the Claude Code and Codex sessions it starts
// through a hub on the BB server, which picks one of several accounts for
// each request. It points a session at the hub with two variables: a base
// URL on the hub, and the hub's token. A harness command a probe runs from
// such a session, as when ai-usage runs inside a BB thread, would inherit
// them and ask the hub, so the reading would be of whichever account the
// hub picked, not of the login of the home asked.

// poolPath is in the path of every URL on the Account Pooler's hub.
const poolPath = "/api/v1/plugins/account-pool/"

// poolRoutes are the variables the Account Pooler routes each harness with:
// the base URL, and the token that goes with it. Codex does not read its
// pair itself, BB hands it to the app-server it starts, but a probe drops
// it all the same.
var poolRoutes = []struct{ provider, url, token string }{
	{"claude", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"},
	{"codex", "CODEX_OPENAI_BASE_URL", "CODEX_POOL_AUTH_TOKEN"},
}

// PoolURL reports whether u is a URL on BB's Account Pooler hub. A base URL
// of any other gateway is not.
func PoolURL(u string) bool {
	p, err := url.Parse(strings.TrimSpace(u))
	return err == nil && p.Host != "" && strings.Contains(p.Path, poolPath)
}

// Pooled is the harnesses whose sessions started from this environment go
// through BB's Account Pooler, in the order of snapshot.Providers.
func (e Env) Pooled() []string {
	var out []string
	for _, r := range poolRoutes {
		if PoolURL(e.getenv(r.url)) {
			out = append(out, r.provider)
		}
	}
	return out
}

// poolVars are the variables of the Account Pooler's routes in this
// environment, which harness commands go without: a route whose base URL
// is not on the hub, such as a gateway the person runs, is kept.
func (e Env) poolVars() []string {
	var out []string
	for _, r := range poolRoutes {
		if PoolURL(e.getenv(r.url)) {
			out = append(out, r.url, r.token)
		}
	}
	return out
}

// PoolUse asks BB, with `bb pool status --json`, when its Account Pooler
// last served each harness it routes: the newest last use of the pool's
// enabled accounts of that harness, by harness. It is nil when no bb is
// found or it does not answer. BB's command line is on PATH in BB's own
// sessions, and in the folder BB writes it to, ~/.bb/bin, for the system
// scheduler. The pool serves every machine of that BB, and its status does
// not say which one a use was on, so a use may be another machine's. Only
// the routes and the times are read: not the accounts' emails or quota.
func (e Env) PoolUse(ctx context.Context) map[string]time.Time {
	bin, ok := e.find("bb")
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	out, err := e.command(ctx, bin, nil, "pool", "status", "--json").Output()
	if err != nil {
		return nil
	}
	var st struct {
		Routing  map[string]bool `json:"routing"`
		Accounts []struct {
			Provider   string `json:"provider"`
			Enabled    bool   `json:"enabled"`
			LastUsedAt int64  `json:"lastUsedAt"`
		} `json:"accounts"`
	}
	if json.Unmarshal(out, &st) != nil {
		return nil
	}
	use := map[string]time.Time{}
	for _, a := range st.Accounts {
		if at := time.UnixMilli(a.LastUsedAt).UTC(); st.Routing[a.Provider] && a.Enabled && a.LastUsedAt > 0 && at.After(use[a.Provider]) {
			use[a.Provider] = at
		}
	}
	return use
}
