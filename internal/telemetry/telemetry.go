// managed by devkit v1 — change it in neoromantic/devkit templates/telemetry/, not here. Spec: https://github.com/neoromantic/devkit/blob/main/docs/standards/telemetry.md

// Package telemetry sends anonymous usage events (starts, features, errors) to PostHog through
// usage.sergeybaus.com. It uses only the standard library, swallows every error, prints nothing
// but the notice and debug output, and sends in the background. A disabled client writes
// nothing to disk and opens no connection.
package telemetry

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// NoticeVersion is the notice the user must have seen before anything is sent.
const NoticeVersion = 1

const projectKey = "phc_np7sxHvzFH1HDTlQdMaqRDrpOVZ0xytnSDxyCMBwNO9" // PostHog's public, write-only key. gitleaks:allow

// endpoint receives the events. A packager who wants a build without telemetry blanks it:
// go build -ldflags "-X <module>/internal/telemetry.endpoint=".
var endpoint = "https://usage.sergeybaus.com/i/v0/e/"

// What the client reads from the process, replaced in tests.
var (
	getenv               = os.Getenv
	testRun              = testing.Testing
	now                  = time.Now
	isTerminal           = terminal
	stderr     io.Writer = os.Stderr
)

// Options describe the product. Product and Version are required.
type Options struct {
	Product    string   // the slug from products.json, such as "ai-usage"
	Version    string   // empty, "0.0.0" or containing "dev" (a leading v ignored) sends nothing
	EnvPrefix  string   // reads <EnvPrefix>_TELEMETRY; default: Product upper-cased, other characters as _
	StateDir   string   // holds telemetry.json; without it nothing is sent
	Channel    string   // homebrew, npm, binary, install-script, plugin, app, web or source
	Mode       string   // scheduled, daemon, app, plugin or server; empty: agent, interactive or script
	Features   []string // feature names that may be sent; any other is sent as "other"
	OffCommand string   // named in the notice; default: "<Product> telemetry off"
	Endpoint   string   // default: the built-in endpoint; tests point it at a local server
}

// Status is what `<tool> telemetry status` prints. Reason names the rule that decided, as the
// other clients do: no-endpoint, do-not-track, env (<EnvPrefix>_TELEMETRY), ci, test,
// dev-version, state (telemetry.json) or default.
type Status struct {
	Enabled   bool   `json:"enabled"`
	Debug     bool   `json:"debug"` // events are printed to stderr, not sent
	Reason    string `json:"reason"`
	ID        string `json:"id"` // empty (null in JSON) until the notice has been seen
	StateFile string `json:"stateFile"`
	Endpoint  string `json:"endpoint"`
}

// MarshalJSON writes a missing id as null, as the other clients do.
func (s Status) MarshalJSON() ([]byte, error) {
	type plain Status
	return json.Marshal(struct {
		plain
		ID optional `json:"id"`
	}{plain(s), optional(s.ID)})
}

// optional is a string written as null when empty.
type optional string

func (s optional) MarshalJSON() ([]byte, error) {
	if s == "" {
		return []byte("null"), nil
	}
	return json.Marshal(string(s))
}

// TypeOnly is an error reported by its type alone, for code paths whose message could hold user
// content: c.Error(telemetry.TypeOnly("FloodWait")). Anywhere in a chain, it drops the message.
type TypeOnly string

func (t TypeOnly) Error() string { return string(t) }

// state is telemetry.json, one line of JSON in the same shape all three clients write. A lost
// update between two processes is acceptable.
type state struct {
	ID        optional          `json:"id"`
	Enabled   *bool             `json:"enabled"`
	Notice    int               `json:"notice"`
	Installed bool              `json:"installed"`
	Daily     map[string]string `json:"daily"` // throttled key -> last UTC day sent
}

var validID = regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Client records the events of one product. Its methods are safe for concurrent use.
type Client struct {
	mu        sync.Mutex
	o         Options
	path      string // telemetry.json; empty without a state directory
	on, debug bool
	reason    string
	shownNow  bool // this run showed the notice, so it sends nothing
}

// New never returns nil; a disabled client does nothing.
func New(o Options) (c *Client) {
	c = &Client{}
	defer guard()
	c.o = defaults(o)
	if o.StateDir != "" {
		c.path = filepath.Join(o.StateDir, "telemetry.json")
	}
	c.refresh(c.load())
	return c
}

// NoticeText is the notice while it is due (telemetry on, notice not seen yet), otherwise "". An
// app or plugin shows it in its own UI and then calls MarkNoticeSeen.
func (c *Client) NoticeText() (text string) {
	defer guard()
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.load(); c.due(st) {
		return notice(c.o)
	}
	return ""
}

// ShowNotice prints the notice to w (normally os.Stderr) if it is due and stderr is a terminal
// or an AI agent runs the process, and reports whether it printed.
func (c *Client) ShowNotice(w io.Writer) (shown bool) {
	defer guard()
	c.mu.Lock()
	defer c.mu.Unlock()
	// The notice goes to stderr, so only a terminal there counts. It is recorded before it is
	// printed: a notice that cannot be recorded would print on every run.
	st := c.load()
	if !c.due(st) || (!isTerminal(os.Stderr) && detectAgent() == "") || !c.markSeen(st) {
		return false
	}
	if w == nil {
		w = os.Stderr
	}
	_, _ = io.WriteString(w, notice(c.o)+"\n")
	return true
}

// MarkNoticeSeen is for apps, plugins and install scripts that showed the notice themselves.
func (c *Client) MarkNoticeSeen() {
	defer guard()
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.load(); c.due(st) {
		c.markSeen(st)
	}
}

// Started records app_started (and app_installed the first time).
func (c *Client) Started() { c.track("app_started", nil, "") }

// Feature records feature_used; a name missing from Options.Features is sent as "other".
func (c *Client) Feature(name string) {
	defer guard()
	if !slices.Contains(c.o.Features, name) {
		name = "other"
	}
	c.track("feature_used", map[string]any{"feature": name}, name)
}

// Error records error_occurred with the error's type and its scrubbed message.
func (c *Client) Error(err error) {
	defer guard()
	if err == nil {
		return
	}
	typ, props := errorType(err), map[string]any{}
	var only TypeOnly
	if errors.As(err, &only) {
		typ = string(only)
	} else if msg := scrub(err.Error()); msg != "" {
		props["error_message"] = msg
	}
	typ = cut(cmp.Or(typ, "error"), 100)
	props["error_type"] = typ
	c.track("error_occurred", props, typ)
}

// Flush waits up to d for the events already in flight, as Flush does.
func (c *Client) Flush(d time.Duration) { Flush(d) }

// Status reports whether telemetry is on and which rule decided.
func (c *Client) Status() (s Status) {
	defer guard()
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.load()
	c.refresh(st)
	return Status{Enabled: c.on, Debug: c.debug, Reason: c.reason, ID: string(st.ID), StateFile: c.path, Endpoint: c.o.Endpoint}
}

// SetEnabled is `<tool> telemetry on|off`. Off forgets the id and the counters; on marks the
// notice as seen.
func (c *Client) SetEnabled(on bool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("telemetry: %v", r)
		}
	}()
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.load()
	if on { // an explicit opt-in is not a notice run: this process may send
		st.Enabled, st.Notice = &on, NoticeVersion
		if st.ID == "" {
			st.ID = optional(newID())
		}
	} else {
		st = &state{Enabled: &on, Notice: st.Notice}
	}
	if err := c.save(st); err != nil {
		return err
	}
	c.refresh(st)
	return nil
}

// track sends event, after app_installed the first time, once the notice has been seen. In the
// scheduled, daemon and plugin modes each event and key go out at most once per UTC day.
func (c *Client) track(event string, props map[string]any, key string) {
	defer guard()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shownNow {
		return
	}
	st := c.load() // fresh: another process may have run `telemetry off|on` since this one started
	if c.refresh(st); !c.on || st.Notice < NoticeVersion {
		return
	}
	var send []string
	dirty := !st.Installed || st.ID == ""
	if st.ID == "" { // deleted by `telemetry off`, then turned on by the environment
		st.ID = optional(newID())
	}
	if !st.Installed {
		st.Installed = true
		send = append(send, "app_installed")
	}
	if !slices.Contains([]string{"scheduled", "daemon", "plugin"}, c.o.Mode) {
		send = append(send, event)
	} else {
		day, k := now().UTC().Format(time.DateOnly), event
		if key != "" {
			k += ":" + key
		}
		if st.Daily[k] != day {
			maps.DeleteFunc(st.Daily, func(_, d string) bool { return d != day }) // older days are spent
			if st.Daily == nil {
				st.Daily = map[string]string{}
			}
			st.Daily[k], dirty = day, true
			send = append(send, event)
		}
	}
	if dirty && c.save(st) != nil {
		return // a state directory that cannot be written means nothing is sent
	}
	for _, e := range send {
		if e == event {
			Capture(e, props, string(st.ID), c.o)
		} else {
			Capture(e, nil, string(st.ID), c.o)
		}
	}
}

// due refreshes the decision from st and reports whether the notice is due.
func (c *Client) due(st *state) bool {
	c.refresh(st)
	return c.on && st.Notice < NoticeVersion
}

// markSeen records that the notice was shown and creates the id; this run then sends nothing. It
// reports false when the state cannot be written.
func (c *Client) markSeen(st *state) bool {
	if st.Notice = NoticeVersion; st.ID == "" {
		st.ID = optional(newID())
	}
	if c.save(st) != nil {
		return false
	}
	c.shownNow = true
	return true
}

func (c *Client) refresh(st *state) { c.on, c.debug, c.reason = decide(c.o, st.Enabled) }

func (c *Client) load() *state {
	st := &state{}
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, st)
	}
	if !validID.MatchString(string(st.ID)) {
		st.ID = ""
	}
	return st
}

// save writes a temp file and renames it into place, so readers never see half a file.
func (c *Client) save(st *state) error {
	if c.path == "" {
		return errors.New("telemetry: no state directory")
	}
	if st.Daily == nil {
		st.Daily = map[string]string{}
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".telemetry-*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err = errors.Join(err, f.Close()); err == nil {
		err = os.Rename(f.Name(), c.path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// decide applies the rules in order, first match wins; enabled comes from telemetry.json.
func decide(o Options, enabled *bool) (on, debug bool, reason string) {
	if o.Endpoint == "" {
		return false, false, "no-endpoint"
	}
	if v := setting("DO_NOT_TRACK"); v != "" && v != "0" && v != "false" {
		return false, false, "do-not-track"
	}
	switch setting(o.EnvPrefix + "_TELEMETRY") {
	case "0", "off", "false":
		return false, false, "env"
	case "debug":
		return true, true, "env"
	case "1", "on", "true":
		return true, false, "env"
	}
	// dev, devel, (devel), 1.2.0-dev.1, 0.0.0, v0.0.0
	v := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(o.Version)), "v")
	switch {
	case inCI():
		return false, false, "ci"
	case testRun():
		return false, false, "test"
	case v == "" || v == "0.0.0" || strings.Contains(v, "dev"):
		return false, false, "dev-version"
	case enabled != nil:
		return *enabled, false, "state"
	}
	return true, false, "default"
}

// setting reads an environment variable trimmed and lower-cased; empty counts as unset.
func setting(k string) string { return strings.ToLower(strings.TrimSpace(getenv(k))) }

func inCI() bool {
	if v := setting("CI"); v != "" && v != "false" {
		return true
	}
	return slices.ContainsFunc([]string{"GITHUB_ACTIONS", "GITLAB_CI", "BUILDKITE", "CIRCLECI", "TF_BUILD",
		"JENKINS_URL", "TEAMCITY_VERSION", "CODEBUILD_BUILD_ID"}, func(k string) bool { return setting(k) != "" })
}

// detectAgent names the AI agent running the process, first match wins, or returns "".
func detectAgent() string {
	if v, _, _ := strings.Cut(setting("AI_AGENT"), "_"); v != "" {
		return cut(v, 40)
	}
	switch {
	case setting("CLAUDECODE") != "":
		return "claude-code"
	case setting("CODEX_SANDBOX") != "" || setting("CODEX_THREAD_ID") != "":
		return "codex"
	case setting("CURSOR_AGENT") != "":
		return "cursor"
	case setting("GEMINI_CLI") != "":
		return "gemini"
	}
	return ""
}

func defaults(o Options) Options {
	o.EnvPrefix = cmp.Or(o.EnvPrefix, strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' {
			return r
		}
		return '_'
	}, strings.ToUpper(o.Product)))
	o.OffCommand = cmp.Or(o.OffCommand, o.Product+" telemetry off")
	o.Endpoint = cmp.Or(o.Endpoint, endpoint)
	switch {
	case o.Mode != "":
	case detectAgent() != "":
		o.Mode = "agent"
	case isTerminal(os.Stdin) || isTerminal(os.Stderr):
		o.Mode = "interactive"
	default:
		o.Mode = "script"
	}
	return o
}

// Capture sends one event with no state, notice or throttling, for server code with its own
// ids (random or keyed, never an email or an account id; random when empty). The environment
// rules still apply. It returns at once; Flush waits for the request.
func Capture(event string, props map[string]any, distinctID string, o Options) {
	defer guard()
	o = defaults(o)
	on, debug, _ := decide(o, nil)
	if !on || event == "" {
		return
	}
	if distinctID == "" {
		distinctID = newID()
	}
	osName := map[string]string{"darwin": "macOS", "windows": "Windows", "linux": "Linux"}[runtime.GOOS]
	p := map[string]any{
		"$process_person_profile": false, // anonymous events, no person profiles
		"$lib":                    "devkit-telemetry",
		"$lib_version":            "1",
		"product":                 o.Product,
		"$app_version":            o.Version,
		"$os":                     cmp.Or(osName, runtime.GOOS),
		"arch":                    cmp.Or(map[string]string{"amd64": "x64"}[runtime.GOARCH], runtime.GOARCH),
		"runtime":                 "go " + strings.TrimPrefix(runtime.Version(), "go"),
		"mode":                    o.Mode,
	}
	for k, v := range map[string]string{"$os_version": osVersion(), "channel": o.Channel, "agent": detectAgent()} {
		if v != "" {
			p[k] = v
		}
	}
	maps.Copy(p, props)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep <user> readable in debug output
	if enc.Encode(struct {
		APIKey     string         `json:"api_key"`
		Event      string         `json:"event"`
		DistinctID string         `json:"distinct_id"`
		Timestamp  string         `json:"timestamp"`
		Properties map[string]any `json:"properties"`
	}{projectKey, event, distinctID, now().UTC().Format("2006-01-02T15:04:05.000Z"), p}) != nil {
		return
	}
	body := bytes.TrimSpace(buf.Bytes())
	if debug {
		_, _ = fmt.Fprintf(stderr, "[telemetry] %s\n", body)
		return
	}
	ua := "devkit-telemetry/1 (" + o.Product + "/" + o.Version + ")"
	inflight.Go(func() { post(o.Endpoint, ua, body) })
}

var (
	inflight sync.WaitGroup
	// A clone of the default transport keeps HTTPS_PROXY and friends working.
	httpClient = sync.OnceValue(func() *http.Client {
		if t, ok := http.DefaultTransport.(*http.Transport); ok {
			return &http.Client{Transport: t.Clone()}
		}
		return &http.Client{}
	})
)

// Flush waits up to d for the events already in flight; it starts nothing new.
func Flush(d time.Duration) {
	done := make(chan struct{})
	go func() { inflight.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// post makes one attempt with a 3 second limit for the whole request; the response is ignored.
func post(url, ua string, body []byte) {
	defer guard()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ua)
	if resp, err := httpClient().Do(req); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}
}

// osVersion reads a file or two, never a subprocess. Windows reports none.
var osVersion = sync.OnceValue(func() string {
	switch runtime.GOOS {
	case "darwin":
		b, _ := os.ReadFile("/System/Library/CoreServices/SystemVersion.plist")
		if _, rest, ok := strings.Cut(string(b), "<key>ProductVersion</key>"); ok {
			if _, rest, ok = strings.Cut(rest, "<string>"); ok {
				v, _, _ := strings.Cut(rest, "</string>")
				return strings.TrimSpace(v)
			}
		}
	case "linux":
		b, _ := os.ReadFile("/etc/os-release")
		for line := range strings.Lines(string(b)) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "VERSION_ID="); ok {
				return strings.Trim(v, `"'`)
			}
		}
		b, _ = os.ReadFile("/proc/sys/kernel/osrelease")
		return strings.TrimSpace(string(b))
	}
	return ""
})

// errorType names err: an OS file error as ENOENT, EACCES or EEXIST, a timeout as ETIMEDOUT,
// otherwise the Go type of the outermost error that is more than a message or a wrapper.
func errorType(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, fs.ErrPermission):
		return "EACCES"
	case errors.Is(err, fs.ErrExist):
		return "EEXIST"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "ETIMEDOUT"
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		switch t := strings.TrimPrefix(fmt.Sprintf("%T", e), "*"); t {
		case "errors.errorString", "fmt.wrapError", "fmt.wrapErrors", "errors.joinError":
		default:
			return t
		}
	}
	return "error"
}

// scrub replaces, in one pass and ignoring case, the home directory with ~ and the user name with
// <user> wherever they are not part of a longer word, then trims the text and cuts it to 300
// characters.
func scrub(s string) string {
	home := strings.TrimRight(cmp.Or(getenv("HOME"), getenv("USERPROFILE")), `/\`)
	swaps := map[string]string{}
	if home != "" {
		swaps[strings.ToLower(home)], swaps[strings.ToLower(strings.ReplaceAll(home, `\`, "/"))] = "~", "~"
	}
	for _, name := range []string{getenv("USER"), getenv("LOGNAME"), getenv("USERNAME"), filepath.Base(home)} {
		name = strings.ToLower(strings.TrimSpace(name))
		if _, ok := swaps[name]; !ok && len(name) > 1 { // a one-letter name would mangle every word
			swaps[name] = "<user>"
		}
	}
	if len(swaps) > 0 {
		keys := slices.SortedFunc(maps.Keys(swaps), func(a, b string) int { return len(b) - len(a) })
		for i, k := range keys {
			keys[i] = regexp.QuoteMeta(k)
		}
		var b strings.Builder
		last := 0
		for _, m := range regexp.MustCompile(`(?i)`+strings.Join(keys, "|")).FindAllStringIndex(s, -1) {
			if wordByte(s, m[0]-1) || wordByte(s, m[1]) {
				continue
			}
			b.WriteString(s[last:m[0]])
			b.WriteString(cmp.Or(swaps[strings.ToLower(s[m[0]:m[1]])], "<user>"))
			last = m[1]
		}
		s = b.String() + s[last:]
	}
	return cut(strings.TrimSpace(s), 300)
}

// wordByte reports whether s[i] is an ASCII letter or digit.
func wordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i] | 0x20 // lower-cases letters
	return 'a' <= c && c <= 'z' || '0' <= s[i] && s[i] <= '9'
}

// cut keeps the first n characters of s.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// notice is the spec's text on one line, without the line break.
func notice(o Options) string {
	return o.Product + " shares usage stats with its author: how often it runs, which features are used, " +
		"errors, version and OS, tied to a random install ID. It never sends your files, arguments or " +
		"account names. Nothing has been sent yet. Turn it off with `" + o.OffCommand + "` or " +
		"`DO_NOT_TRACK=1`. Details: https://usage.sergeybaus.com"
}

// terminal is a character device other than the null device, which cron and launchd hand out.
func terminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

// newID is a random UUID v4; crypto/rand never fails.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// guard swallows a panic: telemetry must never fail the product.
func guard() { _ = recover() }
