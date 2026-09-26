package probe

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Codex asks `codex app-server` over stdio for account/read and
// account/rateLimits/read. Neither starts a thread or spends quota.
// account/read is sent with refreshToken false.
//
// A codex that cannot serve, because it will not start, or is too old to have
// app-server or its account requests, gives way to the next one found, such
// as the copy the ChatGPT app bundles. Its error is the one reported when none
// can. One that named the account has served, whatever failed after.
func Codex(ctx context.Context, env Env, home string) (Reading, error) {
	bins := env.bins("codex")
	if len(bins) == 0 {
		return Reading{}, errors.New("codex binary not found; account unknown")
	}
	var first error
	for _, bin := range bins {
		r, served, err := codexAt(ctx, env, bin, home)
		if served || ctx.Err() != nil {
			return r, err
		}
		if first == nil {
			first = err
		}
	}
	return Reading{}, first
}

// codexAt asks the codex at bin. served is false when it could not start
// app-server, or ended or turned a request down as unknown before it named
// the account.
func codexAt(ctx context.Context, env Env, bin, home string) (r Reading, served bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, env.timeout())
	defer cancel()

	custom := ""
	if filepath.Clean(home) != DefaultHome(env.HomeDir, "codex") {
		custom = home
	}
	cmd := env.command(ctx, bin, []string{"CODEX_HOME", custom}, "app-server")
	cmd.WaitDelay = 2 * time.Second
	var said lastLine
	cmd.Stderr = &said
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Reading{}, true, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Reading{}, true, err
	}
	if err := cmd.Start(); err != nil {
		return Reading{}, false, fmt.Errorf("codex app-server: %s", shortErr(err))
	}
	rpc := newCodexRPC(stdin, stdout)
	defer func() {
		rpc.close()
		// app-server exits by itself at the end of its input, once its own
		// work is done. Killing it at once could cut a write short.
		_ = stdin.Close()
		if werr := waitOrKill(cmd, codexExitGrace); werr != nil && err == nil {
			err = fmt.Errorf("codex app-server: %w", werr)
		}
		// Once it has exited, all it wrote to stderr is in.
		if errors.Is(err, errExited) {
			if l := said.String(); l != "" {
				err = fmt.Errorf("%w: %s", err, l)
			}
		}
	}()
	r, err = codexConversation(ctx, rpc, env.now())
	gaveWay := errors.Is(err, errExited) || errors.Is(err, errUnsupported)
	return r, r.Account != "" || !gaveWay, err
}

// codexExitGrace is how long app-server has to exit after its input ends.
var codexExitGrace = 3 * time.Second

func codexConversation(ctx context.Context, rpc *codexRPC, now time.Time) (Reading, error) {
	if _, err := rpc.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "ai-usage", "version": "1"},
	}); err != nil {
		return Reading{}, err
	}
	if err := rpc.notify("initialized"); err != nil {
		return Reading{}, err
	}
	raw, err := rpc.call(ctx, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		return Reading{}, err
	}
	r, err := codexAccount(raw)
	if err != nil || r.Account == "" {
		return r, err
	}
	raw, err = rpc.call(ctx, "account/rateLimits/read", nil)
	if err != nil {
		return r, err
	}
	q, plan, err := codexLimits(raw, now)
	if err != nil {
		return r, err
	}
	r.Quota, r.Plan = q, cmp.Or(r.Plan, plan)
	return r, nil
}

// codexAccount is the account an account/read answer names, with its plan.
func codexAccount(raw json.RawMessage) (Reading, error) {
	var acct struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if json.Unmarshal(raw, &acct) != nil {
		return Reading{}, errors.New("codex account/read: unexpected result")
	}
	if acct.Account == nil {
		return Reading{}, notLoggedIn("codex")
	}
	var r Reading
	switch acct.Account.Type {
	case "chatgpt":
		// email is nullable in the protocol; the type is still a label.
		r.Account = strings.TrimSpace(acct.Account.Email)
		if r.Account == "" {
			r.Account = "chatgpt"
		}
		r.Plan = acct.Account.PlanType
	case "apiKey":
		r.Account = "api key"
	default:
		r.Account = snapshot.PlainLabel(acct.Account.Type)
	}
	return r, nil
}

type codexBucket struct {
	LimitID   string       `json:"limitId"`
	PlanType  string       `json:"planType"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

func codexLimits(raw json.RawMessage, now time.Time) (*Quota, string, error) {
	var res struct {
		RateLimits *codexBucket            `json:"rateLimits"`
		ByLimitID  map[string]*codexBucket `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, "", errors.New("codex rateLimits: unexpected result")
	}
	var plan string
	var ws []snapshot.Window
	for _, b := range codexBuckets(res.RateLimits, res.ByLimitID) {
		plan = cmp.Or(plan, b.PlanType)
		ws = append(ws, b.windows()...)
	}
	if len(ws) == 0 {
		return nil, plan, nil
	}
	return &Quota{At: now, Source: "harness", Windows: ws}, plan, nil
}

// codexBuckets are the limits of a rateLimits answer: those it lists by limit
// id, the main limit first and the rest by id, or the single one when it lists
// none by id. A bucket without its own id takes the one it is listed under.
func codexBuckets(single *codexBucket, byID map[string]*codexBucket) []*codexBucket {
	if len(byID) == 0 {
		if single == nil {
			return nil
		}
		return []*codexBucket{single}
	}
	var buckets []*codexBucket
	for _, id := range slices.Sorted(maps.Keys(byID)) {
		b := byID[id]
		if b == nil {
			continue
		}
		b.LimitID = cmp.Or(b.LimitID, id)
		if id == logs.CodexMainLimit {
			buckets = slices.Insert(buckets, 0, b)
		} else {
			buckets = append(buckets, b)
		}
	}
	return buckets
}

// windows are the bucket's primary and secondary windows, named as the Codex
// logs name them.
func (b *codexBucket) windows() []snapshot.Window {
	var ws []snapshot.Window
	for _, w := range []*codexWindow{b.Primary, b.Secondary} {
		if w == nil {
			continue
		}
		win := snapshot.Window{
			Name:    logs.CodexWindowName(b.LimitID, w.WindowDurationMins),
			Percent: w.UsedPercent,
			Minutes: w.WindowDurationMins,
		}
		if w.ResetsAt > 0 {
			t := time.Unix(w.ResetsAt, 0).UTC()
			win.ResetsAt = &t
		}
		ws = append(ws, win)
	}
	return ws
}
