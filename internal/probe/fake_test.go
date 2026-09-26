package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

// fakeEnv runs this test binary as the harness named by the probe, in the
// given mode. Probes replace cmd.Env, so the mode travels in Environ.
func fakeEnv(t *testing.T, mode string, environ ...string) (Env, string) {
	t.Helper()
	record := filepath.Join(t.TempDir(), "record.jsonl")
	env := Env{
		Command: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			argv := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
			return exec.CommandContext(ctx, os.Args[0], argv...)
		},
		LookPath: func(name string) (string, error) { return filepath.Join("fake", "bin", name), nil },
		Environ:  append(helperEnviron(mode, record), environ...),
		HomeDir:  t.TempDir(),
		Now:      func() time.Time { return testNow },
		Timeout:  10 * time.Second,
	}
	return env, record
}

func helperEnviron(mode, record string) []string {
	// The race runtime otherwise sleeps a second before a clean exit.
	env := []string{"PROBE_HELPER=" + mode, "PROBE_RECORD=" + record, "GORACE=atexit_sleep_ms=0"}
	for _, k := range []string{"PATH", "SYSTEMROOT", "TMPDIR", "TEMP", "TMP", "GOCOVERDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// helperRecord is the first line a fake harness writes to PROBE_RECORD; each
// line it reads from stdin follows.
type helperRecord struct {
	Name string            `json:"name"`
	Path string            `json:"path"`
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
	PID  int               `json:"pid"`
	Dir  string            `json:"dir"`
	// Stdin is what `claude -p` found on its input: "N bytes" once it
	// ended, or "open" while it did not.
	Stdin string `json:"stdin,omitempty"`
}

// recordLines is each line a fake harness wrote to the record at path.
func recordLines(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fake harness left no record: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

func readRecord(t *testing.T, path string) (helperRecord, []map[string]any) {
	t.Helper()
	lines := recordLines(t, path)
	var rec helperRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("record: %v", err)
	}
	var msgs []map[string]any
	for _, l := range lines[1:] {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("recorded message %q: %v", l, err)
		}
		msgs = append(msgs, m)
	}
	return rec, msgs
}

// readRuns is the record of each run of a fake harness, in order.
func readRuns(t *testing.T, path string) []helperRecord {
	t.Helper()
	var out []helperRecord
	for _, l := range recordLines(t, path) {
		var r helperRecord
		if json.Unmarshal([]byte(l), &r) == nil && r.Name != "" {
			out = append(out, r)
		}
	}
	return out
}

func methods(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		s, _ := m["method"].(string)
		out = append(out, s)
	}
	return out
}

// TestHelperProcess is not a test. fakeEnv runs it as a harness binary.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("PROBE_HELPER")
	if mode == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(fakeHarness(mode, args))
}

func fakeHarness(mode string, args []string) int {
	if mode == "sleep-until-released" {
		release := os.Getenv("PROBE_RELEASE")
		for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(release); err == nil {
				break
			}
		}
		return 0
	}
	rec, err := os.OpenFile(os.Getenv("PROBE_RECORD"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 90
	}
	defer rec.Close()
	seen := map[string]string{}
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"} {
		if v, ok := os.LookupEnv(k); ok {
			seen[k] = v
		}
	}
	dir, _ := os.Getwd()
	run := helperRecord{Name: filepath.Base(args[0]), Path: args[0], Args: args[1:], Env: seen, PID: os.Getpid(), Dir: dir}
	usage := strings.HasPrefix(mode, "claude-") && slices.Contains(args, "-p")
	if usage {
		run.Stdin = stdinState()
	}
	head, _ := json.Marshal(run)
	_, _ = rec.Write(append(head, '\n'))
	if usage {
		return fakeClaudeUsage(os.Getenv("PROBE_USAGE"), args)
	}

	if strings.HasPrefix(mode, "codex-") {
		// The codex on PATH is too old for app-server, or for account/read;
		// any other serves.
		if old, ok := map[string]string{"codex-old-on-path": "codex-exit-says", "codex-no-account-read-on-path": "codex-no-account-read"}[mode]; ok {
			mode = "codex-ok"
			if filepath.Dir(args[0]) == filepath.Join("fake", "bin") {
				mode = old
			}
		}
		return fakeCodex(mode, rec)
	}
	switch mode {
	case "claude-ok":
		fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":" dev@example.com ","orgName":"Example","subscriptionType":"max"}`)
	case "claude-console":
		// A Console login signs in through claude.ai and pays per token.
		fmt.Println(`{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"dev@example.com","orgName":"Example","subscriptionType":null}`)
	case "claude-no-email":
		fmt.Println(`{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty"}`)
	case "claude-bedrock":
		fmt.Println(`{"loggedIn":true,"authMethod":"third_party","apiProvider":"bedrock"}`)
	case "claude-logged-out":
		// The real CLI exits 1 when logged out and still prints JSON.
		fmt.Println(`{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}`)
		return 1
	case "claude-not-json":
		fmt.Println("Checking authentication status...")
	case "claude-fail":
		fmt.Fprintln(os.Stderr, "boom")
		return 2
	case "claude-orphan":
		return hangWithChild(rec)
	default:
		return 92
	}
	return 0
}

// hangWithChild starts a child that keeps stdout open after this process is
// killed, records its pid, and hangs.
func hangWithChild(rec *os.File) int {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--", "sleeper")
	cmd.Env = append(os.Environ(), "PROBE_HELPER=sleep-until-released")
	cmd.Stdout = os.Stdout
	if err := cmd.Start(); err != nil {
		return 91
	}
	_, _ = fmt.Fprintf(rec, `{"child_pid":%d}`+"\n", cmd.Process.Pid)
	time.Sleep(time.Minute)
	return 0
}

// stdinState reads stdin to its end, for up to two seconds.
func stdinState() string {
	n := make(chan int64, 1)
	go func() {
		c, _ := io.Copy(io.Discard, os.Stdin)
		n <- c
	}()
	select {
	case c := <-n:
		return fmt.Sprintf("%d bytes", c)
	case <-time.After(2 * time.Second):
		return "open"
	}
}

// fakeClaudeUsage is `claude -p /usage`, in the way PROBE_USAGE names. By
// default it caches a fresh reading of 7% in the config file PROBE_CACHE
// names, if any, for the account logged in there, as Claude Code does. Like
// Claude Code from 2.1.251, it first warns on stderr that its model catalog
// does not describe the model it was given.
func fakeClaudeUsage(mode string, args []string) int {
	model := args[slices.Index(args, "--model")+1]
	catalog := fmt.Sprintf("%q isn't described by this version's model catalog; update Claude Code, or map it with behavesAs in settings.", model)
	fmt.Fprintln(os.Stderr, catalog)
	switch mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "Error: usage is unavailable right now")
		return 1
	case "old":
		// Sent to the model as a prompt, which the API does not know.
		fmt.Printf("There's an issue with the selected model (%s). It may not exist or you may not have access to it.\n", model)
		return 1
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "unknown-skill":
		// 2.1.40 to 2.1.110.
		fmt.Println("Unknown skill: usage")
		return 0
	case "unknown-command":
		// 2.1.0.
		fmt.Println("Unknown slash command: usage")
		return 0
	case "unavailable":
		// 2.1.111 to 2.1.117.
		fmt.Println("/usage isn't available in this environment.")
		return 0
	case "no-option":
		// 2.0.0.
		fmt.Fprintln(os.Stderr, "error: unknown option '--no-session-persistence'")
		return 1
	case "offline":
		// From 2.1.208 when the usage cannot be fetched, and 2.1.118 to
		// 2.1.191 always.
		fmt.Print(claudeHeadlineOut + claudeContributing)
		return 0
	case "overage-offline":
		// The same, on extra usage.
		fmt.Print("You are currently using your overages to power your Claude Code usage. We will automatically switch you back to your subscription rate limits when they reset\n\n" + claudeContributing)
		return 0
	case "shows":
		// 2.1.193 to 2.1.207: the usage, and no cache.
		fmt.Print(claudeHeadlineOut + claudeUsageOut + claudeContributing)
		return 0
	case "cost":
		fmt.Print("Total cost:            $0.0000\nTotal duration (API):  0s\nUsage:                 0 input, 0 output\n" + claudeContributing)
		return 0
	case "silent":
		return 0
	case "odd-fail":
		fmt.Print("Something unexpected happened\n\n" + claudeContributing)
		return 1
	case "catalog-fail":
		// The model catalog's warning is the last line.
		fmt.Fprintln(os.Stderr, catalog)
		return 1
	}
	if path := os.Getenv("PROBE_CACHE"); path != "" {
		body, err := os.ReadFile(path)
		var cfg map[string]any
		if err != nil || json.Unmarshal(body, &cfg) != nil {
			return 94
		}
		acct, _ := cfg["oauthAccount"].(map[string]any)
		cfg["cachedUsageUtilization"] = map[string]any{
			"fetchedAtMs": time.Now().UnixMilli(),
			"accountUuid": acct["accountUuid"],
			"utilization": map[string]any{"limits": []any{map[string]any{"kind": "session", "percent": 7}}},
		}
		body, _ = json.Marshal(cfg)
		if os.WriteFile(path, body, 0o600) != nil {
			return 95
		}
	}
	if mode == "write-then-fail" {
		return 1
	}
	fmt.Print(claudeHeadlineOut + claudeUsageOut + claudeContributing)
	return 0
}

// What /usage prints: its headline, the usage, and what contributes to it,
// with names only a person's own setup has.
const (
	claudeHeadlineOut  = "You are currently using your subscription to power your Claude Code usage\n\n"
	claudeUsageOut     = "Current session: 7% used · resets 3:40pm (Europe/Berlin)\nCurrent week (all models): 40% used · resets Oct 1, 9am (Europe/Berlin)\n\n"
	claudeContributing = "What's contributing to your limits usage?\n  Skills       zebra-quill-skill   12%\n  Subagents    heron-drafts        4%\n  MCP servers  otter-lantern-mcp   3%\n  error-walrus-plugin              1%\n"
)

// fakeCodex speaks the app-server's newline JSON-RPC on stdio.
func fakeCodex(mode string, rec *os.File) int {
	in := bufio.NewReader(os.Stdin)
	write := func(lines ...string) {
		for _, l := range lines {
			_, _ = os.Stdout.WriteString(l + "\n")
		}
	}
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			switch mode {
			case "codex-hang", "codex-linger":
				time.Sleep(time.Minute)
			case "codex-slow-exit":
				// Work the real server finishes after its input ends.
				time.Sleep(300 * time.Millisecond)
				_, _ = rec.WriteString(`{"exit":"clean"}` + "\n")
			}
			return 0
		}
		_, _ = rec.Write(line)
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line, &m) != nil {
			return 93
		}
		reply := func(result string) { write(fmt.Sprintf(`{"id":%d,"result":%s}`, *m.ID, result)) }
		fail := func(msg string) { write(fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":%q}}`, *m.ID, msg)) }
		switch m.Method {
		case "initialize":
			if mode == "codex-exit" {
				return 3
			}
			if mode == "codex-exit-says" {
				// As clap prints it: the error, then usage and a hint.
				fmt.Fprint(os.Stderr, "starting\n\x1b[31merror:\x1b[0m unrecognized subcommand 'app-server'\n\n"+
					"Usage: codex [OPTIONS] [PROMPT]\n\nFor more information, try '--help'.\n")
				return 2
			}
			var p struct {
				ClientInfo *struct{ Name, Version string } `json:"clientInfo"`
			}
			if json.Unmarshal(m.Params, &p) != nil || p.ClientInfo == nil || p.ClientInfo.Name == "" {
				fail("clientInfo is required")
				continue
			}
			write(`{"method":"configWarning","params":{"summary":"noise before the answer"}}`)
			reply(`{"userAgent":"fake/1","codexHome":"/nowhere"}`)
		case "initialized":
		case "account/read":
			var p struct {
				RefreshToken *bool `json:"refreshToken"`
			}
			if json.Unmarshal(m.Params, &p) != nil || p.RefreshToken == nil || *p.RefreshToken {
				fail("refreshToken must be sent as false")
				continue
			}
			// Server-initiated traffic may reuse the id; it carries a method.
			write(
				fmt.Sprintf(`{"id":%d,"method":"item/tool/requestUserInput","params":{}}`, *m.ID),
				`{"id":99,"result":{"late":true}}`,
				`{"method":"account/rateLimits/updated","params":{"rateLimits":{}}}`,
				`not json at all`,
			)
			switch mode {
			case "codex-hang":
				continue
			case "codex-account-error":
				fail("boom")
			case "codex-no-account-read":
				write(fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":"Invalid request: unknown variant `+"`account/read`"+`, expected one of `+"`initialize`"+`"}}`, *m.ID))
			case "codex-logged-out":
				reply(`{"account":null,"requiresOpenaiAuth":true}`)
			case "codex-apikey":
				reply(`{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}`)
			case "codex-no-email":
				reply(`{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}`)
			default:
				// Split mid-line to show the reader waits for the newline.
				msg := fmt.Sprintf(`{"id":%d,"result":{"account":{"type":"chatgpt","email":"dev@example.com","planType":"pro"},"requiresOpenaiAuth":true}}`, *m.ID)
				_, _ = os.Stdout.WriteString(msg[:20])
				time.Sleep(20 * time.Millisecond)
				_, _ = os.Stdout.WriteString(msg[20:] + "\n" + `{"method":"account/updated","params":{}}` + "\n")
			}
		case "account/rateLimits/read":
			if mode == "codex-exit-after-account" {
				fmt.Fprint(os.Stderr, "thread 'tokio-runtime-worker' panicked at src/rate_limits.rs:9:5:\nno backend\nnote: run with `RUST_BACKTRACE=1` environment variable to display a backtrace\n")
				return 101
			}
			if mode == "codex-apikey" {
				fail("rate limits need a ChatGPT login")
				continue
			}
			reply(`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":1,"windowDurationMins":300,"resetsAt":null},"planType":"pro"},` +
				`"rateLimitsByLimitId":{` +
				`"codex_other":{"limitId":"codex_other","primary":{"usedPercent":9,"windowDurationMins":60,"resetsAt":1790000000},"secondary":null,"planType":"team"},` +
				`"codex":{"limitId":"codex","primary":{"usedPercent":12,"windowDurationMins":300,"resetsAt":1790003600},"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":1790500000},"planType":"pro"}}}`)
		default:
			fail("unexpected method " + m.Method)
		}
	}
}

// sameDir fails unless the fake harness ran in want.
func sameDir(t *testing.T, got, want string) {
	t.Helper()
	a, errA := os.Stat(got)
	b, errB := os.Stat(want)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Errorf("harness ran in %q, want %q", got, want)
	}
}

// waitGone reports whether pid is gone within a few seconds. An orphan is
// reaped by init only after it dies, so this polls.
func waitGone(pid int) bool {
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if processGone(pid) {
			return true
		}
	}
	return false
}

// processGone reports whether pid has exited and been reaped.
func processGone(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	return p.Signal(syscall.Signal(0)) != nil
}

// waitNoGoroutine fails if a goroutine running fn is still alive after a grace period.
func waitNoGoroutine(t *testing.T, fn string) {
	t.Helper()
	buf := make([]byte, 1<<20)
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), fn) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine still running %s:\n%s", fn, buf[:n])
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
