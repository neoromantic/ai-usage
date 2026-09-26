package probe

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// errExited is app-server ending its output before it answered, as a Codex
// too old to have it does.
var errExited = errors.New("app-server exited without answering")

// errUnsupported is app-server answering that it does not know a request, as
// a Codex older than that request does.
var errUnsupported = errors.New("request not supported")

// unsupported is an error answer that is errUnsupported.
type unsupported struct{ error }

func (unsupported) Is(target error) bool { return target == errUnsupported }

// lastLine keeps the last line written to it, such as the error a program
// printed before it exited, without its terminal colors and cut short.
type lastLine struct {
	mu   sync.Mutex
	tail []byte
}

func (l *lastLine) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tail = append(l.tail, p...)
	if len(l.tail) > 4096 {
		l.tail = append([]byte(nil), l.tail[len(l.tail)-4096:]...)
	}
	return len(p), nil
}

var terminalCodes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// String is the line of the lines kept that errorOf picks.
func (l *lastLine) String() string { return errorOf(l.lines()) }

// lines are the lines kept, without terminal colors, control or hidden
// characters, or blank lines.
func (l *lastLine) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return printedLines(string(l.tail))
}

// printedLines are the lines of what a program printed, without terminal
// colors, control or hidden characters, or blank lines.
func printedLines(s string) []string {
	var lines []string
	for line := range strings.SplitSeq(terminalCodes.ReplaceAllString(s, ""), "\n") {
		line = strings.TrimSpace(snapshot.Printable(line))
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// errorOf is the last line that says it is an error, or else the last line
// that is not a hint, a stack frame, or a runtime's version, as a command-line
// parser, Node, or a Rust panic prints after the error. A line cut at a comma
// is joined with the one after it.
func errorOf(lines []string) string {
	for i, line := range slices.Backward(lines) {
		if !errorLine.MatchString(line) {
			continue
		}
		// A panic since Rust 1.73 says where on one line and why on the next.
		if strings.Contains(line, "panicked at") && strings.HasSuffix(line, ":") && i+1 < len(lines) && !hint(lines[i+1]) {
			return truncate(lines[i+1], maxErrLine)
		}
		return truncate(line, maxErrLine)
	}
	for i, line := range slices.Backward(lines) {
		if hint(line) {
			continue
		}
		if i > 0 && strings.HasSuffix(lines[i-1], ",") {
			return truncate(lines[i-1]+" "+line, maxErrLine)
		}
		return truncate(line, maxErrLine)
	}
	return ""
}

// errorLine matches a line that says it is the error: "error: ...", Node's
// "TypeError: ...", or a Rust panic.
var errorLine = regexp.MustCompile(`^(?i:error)|^[A-Za-z]*Error\b|panicked at`)

// hint reports whether a line is one printed after an error rather than the
// error itself.
func hint(line string) bool {
	for _, p := range []string{"For more information", "Usage:", "Node.js v", "at ", "note: "} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// codexExitGrace is how long app-server has to exit after its input ends.
var codexExitGrace = 3 * time.Second

// waitOrKill waits for cmd to exit and kills it once grace has passed. Wait
// closes stdout, which ends the reader if it is still reading. How cmd exited
// is not the probe's answer, so the only error is a bug while waiting.
func waitOrKill(cmd *exec.Cmd, grace time.Duration) (err error) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer rescue(&err)
		_ = cmd.Wait()
	}()
	select {
	case <-done:
		return err
	case <-time.After(grace):
	}
	_ = killGroup(cmd)
	<-done
	return err
}

// rescue, deferred in a goroutine a probe starts, turns a panic there into
// *err, so a bug fails that probe rather than ending the process.
func rescue(err *error) {
	if v := recover(); v != nil {
		*err = fmt.Errorf("stopped by a bug: %v", v)
	}
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
	defer rescue(&c.err)
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
		Code    int    `json:"code"`
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
				return nil, fmt.Errorf("codex %s: %w", method, errExited)
			}
			return nil, fmt.Errorf("codex %s: %s", method, shortErr(c.err))
		case line := <-c.lines:
			var r codexReply
			if json.Unmarshal(line, &r) != nil || r.ID == nil || *r.ID != id || r.Method != "" {
				continue
			}
			if r.Error != nil {
				err := fmt.Errorf("codex %s: %s", method, truncate(r.Error.Message, maxErrLine))
				// Method not found, or a request type serde does not know.
				if r.Error.Code == -32601 || strings.Contains(r.Error.Message, "unknown variant") {
					return nil, unsupported{err}
				}
				return nil, err
			}
			return r.Result, nil
		}
	}
}

// maxErrLine is how many bytes of one error line from a harness the probe
// keeps. The line is cut on a rune boundary and without an ellipsis, so the
// error text the probe stores stays byte-identical.
const maxErrLine = 160

func shortErr(err error) string {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return fmt.Sprintf("exit status %d", exit.ExitCode())
	}
	return truncate(err.Error(), maxErrLine)
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
