package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/logs"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// Codex asks `codex app-server` over stdio for account/read and
// account/rateLimits/read. Neither starts a thread or spends quota.
// account/read is sent with refreshToken false.
func Codex(ctx context.Context, env Env, home string) (Reading, error) {
	bin, ok := env.find("codex")
	if !ok {
		return Reading{}, errors.New("codex binary not found; account unknown")
	}
	ctx, cancel := context.WithTimeout(ctx, env.timeout())
	defer cancel()

	cmd := env.command(ctx, bin, "app-server")
	custom := ""
	if !isDefaultHome(env.HomeDir, home, ".codex") {
		custom = home
	}
	cmd.Env = env.harnessEnv("CODEX_HOME", custom)
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Reading{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Reading{}, err
	}
	if err := cmd.Start(); err != nil {
		return Reading{}, fmt.Errorf("codex app-server: %s", shortErr(err))
	}
	rpc := newCodexRPC(stdin, stdout)
	defer func() {
		rpc.close()
		// app-server exits by itself at the end of its input, once its own
		// work is done. Killing it at once could cut a write short.
		_ = stdin.Close()
		waitOrKill(cmd, codexExitGrace)
	}()
	return codexConversation(ctx, rpc, env.now())
}

// codexExitGrace is how long app-server has to exit after its input ends.
var codexExitGrace = 3 * time.Second

// waitOrKill waits for cmd to exit and kills it once grace has passed. Wait
// closes stdout, which ends the reader if it is still reading.
func waitOrKill(cmd *exec.Cmd, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	_ = killGroup(cmd)
	<-done
}

func codexConversation(ctx context.Context, rpc *codexRPC, now time.Time) (Reading, error) {
	var r Reading
	if _, err := rpc.call(ctx, 1, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "ai-usage", "version": "1"},
	}); err != nil {
		return r, err
	}
	if err := rpc.notify("initialized"); err != nil {
		return r, err
	}

	var errs []error
	raw, err := rpc.call(ctx, 2, "account/read", map[string]any{"refreshToken": false})
	if err != nil {
		errs = append(errs, err)
	} else {
		var acct struct {
			Account *struct {
				Type     string `json:"type"`
				Email    string `json:"email"`
				PlanType string `json:"planType"`
			} `json:"account"`
		}
		if json.Unmarshal(raw, &acct) != nil {
			errs = append(errs, errors.New("codex account/read: unexpected result"))
		} else if acct.Account == nil {
			errs = append(errs, notLoggedIn("codex"))
		} else {
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
		}
	}

	if r.Account != "" {
		raw, err = rpc.call(ctx, 3, "account/rateLimits/read", nil)
		if err != nil {
			errs = append(errs, err)
		} else if q, plan, perr := codexLimits(raw, now); perr != nil {
			errs = append(errs, perr)
		} else {
			r.Quota = q
			if r.Plan == "" {
				r.Plan = plan
			}
		}
	}
	return r, joinErrors(errs)
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
	buckets := []*codexBucket{}
	if len(res.ByLimitID) > 0 {
		ids := make([]string, 0, len(res.ByLimitID))
		for id := range res.ByLimitID {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if (ids[i] == "codex") != (ids[j] == "codex") {
				return ids[i] == "codex"
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids {
			if b := res.ByLimitID[id]; b != nil {
				if b.LimitID == "" {
					b.LimitID = id
				}
				buckets = append(buckets, b)
			}
		}
	} else if res.RateLimits != nil {
		buckets = append(buckets, res.RateLimits)
	}
	q := &Quota{At: now, Source: "harness"}
	plan := ""
	for _, b := range buckets {
		if plan == "" {
			plan = b.PlanType
		}
		for _, w := range []*codexWindow{b.Primary, b.Secondary} {
			if w == nil || len(q.Windows) >= snapshot.MaxWindows {
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
			q.Windows = append(q.Windows, win)
		}
	}
	if len(q.Windows) == 0 {
		return nil, plan, nil
	}
	return q, plan, nil
}

// codexRPC speaks newline-delimited JSON-RPC to codex app-server. One
// goroutine reads the server for the whole conversation and hands lines to
// whichever call is waiting, so a call that gave up cannot swallow a line
// meant for the next one.
type codexRPC struct {
	in    io.Writer
	lines chan []byte
	done  chan struct{} // closed once the server's output ends; err says why
	err   error
	quit  chan struct{}
	once  sync.Once
}

func newCodexRPC(in io.Writer, out io.Reader) *codexRPC {
	c := &codexRPC{
		in:    in,
		lines: make(chan []byte),
		done:  make(chan struct{}),
		quit:  make(chan struct{}),
	}
	go c.read(bufio.NewReaderSize(out, 64<<10))
	return c
}

func (c *codexRPC) read(r *bufio.Reader) {
	defer close(c.done)
	for {
		line, err := r.ReadBytes('\n')
		// The last message may end at EOF without a newline.
		if len(bytes.TrimSpace(line)) > 0 {
			select {
			case c.lines <- line:
			case <-c.quit:
				c.err = errors.New("closed")
				return
			}
		}
		if err != nil {
			c.err = err
			return
		}
	}
}

// close releases the reader if it is waiting to hand over a line. A reader
// blocked on the pipe ends when the pipe closes.
func (c *codexRPC) close() {
	c.once.Do(func() { close(c.quit) })
}

func (c *codexRPC) notify(method string) error {
	return c.send(map[string]any{"method": method})
}

func (c *codexRPC) send(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

type codexReply struct {
	ID     *int            `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// call sends a request and waits for its response. Notifications and requests
// from the server carry a method and are skipped, as are late answers to
// calls that already gave up.
func (c *codexRPC) call(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.send(msg); err != nil {
		return nil, fmt.Errorf("codex %s: %s", method, shortErr(err))
	}
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("codex %s: no answer in time", method)
		case <-c.done:
			// The context also kills the process, so its end is a timeout.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("codex %s: no answer in time", method)
			}
			if errors.Is(c.err, io.EOF) {
				return nil, fmt.Errorf("codex %s: app-server exited without answering", method)
			}
			return nil, fmt.Errorf("codex %s: %s", method, shortErr(c.err))
		case line := <-c.lines:
			var r codexReply
			if json.Unmarshal(line, &r) != nil || r.ID == nil || *r.ID != id || r.Method != "" {
				continue
			}
			if r.Error != nil {
				return nil, fmt.Errorf("codex %s: %s", method, truncate(r.Error.Message, 160))
			}
			return r.Result, nil
		}
	}
}

// truncate cuts s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
