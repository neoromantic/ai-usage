package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/neoromantic/ai-usage/internal/snapshot"
)

func TestCodexChatGPT(t *testing.T) {
	// A CODEX_HOME inherited from the caller would point the server at
	// another home, so it is removed for the default one.
	env, record := fakeEnv(t, "codex-ok", "CODEX_HOME=/stale")
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if r.Account != "dev@example.com" || r.Plan != "pro" {
		t.Errorf("reading = %+v", r)
	}
	want := &Quota{At: testNow, Source: "harness", Windows: []snapshot.Window{
		{Name: "5h", Percent: 12, Minutes: 300, ResetsAt: unix(1790003600)},
		{Name: "7d", Percent: 40, Minutes: 10080, ResetsAt: unix(1790500000)},
		{Name: "codex_other 1h", Percent: 9, Minutes: 60, ResetsAt: unix(1790000000)},
	}}
	if !reflect.DeepEqual(r.Quota, want) {
		t.Errorf("quota\n got  %v\n want %v", describe(r.Quota), describe(want))
	}

	rec, msgs := readRecord(t, record)
	if rec.Name != "codex" || strings.Join(rec.Args, " ") != "app-server" {
		t.Errorf("ran %s %q", rec.Name, rec.Args)
	}
	if v, ok := rec.Env["CODEX_HOME"]; ok {
		t.Errorf("CODEX_HOME=%q reached the default home", v)
	}
	if got := methods(msgs); !slices.Equal(got, []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}) {
		t.Errorf("conversation = %q", got)
	}
	if _, ok := msgs[1]["id"]; ok {
		t.Error("initialized must be a notification")
	}
	sameDir(t, rec.Dir, env.HomeDir)
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

// app-server gets to finish what it does after its input ends.
func TestCodexExitsByItself(t *testing.T) {
	env, record := fakeEnv(t, "codex-slow-exit")
	if _, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex")); err != nil {
		t.Fatal(err)
	}
	_, msgs := readRecord(t, record)
	if last := msgs[len(msgs)-1]; last["exit"] != "clean" {
		t.Errorf("app-server was killed before it exited; last line %v", last)
	}
}

func TestCodexKilledWhenItLingers(t *testing.T) {
	grace := codexExitGrace
	codexExitGrace = 200 * time.Millisecond
	t.Cleanup(func() { codexExitGrace = grace })
	env, record := fakeEnv(t, "codex-linger")
	start := time.Now()
	if _, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex")); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %v", took)
	}
	rec, _ := readRecord(t, record)
	if runtime.GOOS != "windows" && !processGone(rec.PID) {
		t.Errorf("app-server %d still running", rec.PID)
	}
}

func unix(sec int64) *time.Time {
	t := time.Unix(sec, 0).UTC()
	return &t
}

func TestCodexCustomHome(t *testing.T) {
	env, record := fakeEnv(t, "codex-ok")
	home := filepath.Join(env.HomeDir, "work-codex")
	if _, err := Codex(context.Background(), env, home); err != nil {
		t.Fatal(err)
	}
	rec, _ := readRecord(t, record)
	if got := rec.Env["CODEX_HOME"]; got != home {
		t.Errorf("CODEX_HOME = %q, want %q", got, home)
	}
}

func TestCodexAnswers(t *testing.T) {
	tests := []struct {
		mode      string
		account   string
		plan      string
		wantErr   string
		wantCalls []string
		loggedOut bool
	}{
		{
			mode: "codex-no-email", account: "chatgpt", plan: "plus",
			wantCalls: []string{"initialize", "initialized", "account/read", "account/rateLimits/read"},
		},
		// wantErr is part of the error, which says why.
		{
			mode: "codex-apikey", account: "api key",
			wantErr:   "rate limits need a ChatGPT login",
			wantCalls: []string{"initialize", "initialized", "account/read", "account/rateLimits/read"},
		},
		{
			mode:      "codex-logged-out",
			wantErr:   "not logged in",
			wantCalls: []string{"initialize", "initialized", "account/read"},
			loggedOut: true,
		},
		{
			mode:      "codex-account-error",
			wantErr:   "boom",
			wantCalls: []string{"initialize", "initialized", "account/read"},
		},
		{
			// TestCodexNoneServes has one that says why on its way out.
			mode:      "codex-exit",
			wantErr:   "exited without answering",
			wantCalls: []string{"initialize"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			env, record := fakeEnv(t, tc.mode)
			r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
			if (err == nil) != (tc.wantErr == "") || !strings.Contains(errText(err), tc.wantErr) {
				t.Errorf("error = %q, want one with %q", errText(err), tc.wantErr)
			}
			if got := saysLoggedOut(err); got != tc.loggedOut {
				t.Errorf("logged out = %v, want %v", got, tc.loggedOut)
			}
			if r.Account != tc.account || r.Plan != tc.plan {
				t.Errorf("reading = %+v", r)
			}
			if tc.wantErr != "" && r.Quota != nil {
				t.Errorf("quota = %v", describe(r.Quota))
			}
			_, msgs := readRecord(t, record)
			if got := methods(msgs); !slices.Equal(got, tc.wantCalls) {
				t.Errorf("conversation = %q, want %q", got, tc.wantCalls)
			}
			waitNoGoroutine(t, "probe.(*codexRPC)")
		})
	}
}

func TestCodexTimeoutKillsServer(t *testing.T) {
	env, record := fakeEnv(t, "codex-hang")
	env.Timeout = 300 * time.Millisecond
	start := time.Now()
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	// Two seconds is the WaitDelay a server that survived the kill would cost.
	if took := time.Since(start); took > 1900*time.Millisecond {
		t.Errorf("took %v", took)
	}
	if !strings.Contains(errText(err), "in time") || r.Account != "" {
		t.Errorf("reading = %+v, %v", r, err)
	}
	rec, _ := readRecord(t, record)
	if runtime.GOOS != "windows" && !processGone(rec.PID) {
		t.Errorf("app-server %d still running", rec.PID)
	}
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

func TestCodexBinaryMissing(t *testing.T) {
	env := Env{LookPath: notOnPath, HomeDir: t.TempDir(), Environ: []string{}}
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if !strings.Contains(errText(err), "not found") || r != (Reading{}) {
		t.Errorf("reading = %+v, %v", r, err)
	}
}

// withRan makes env's harness commands pass their whole path to the fake,
// and lists the paths run.
func withRan(env *Env) *[]string {
	var ran []string
	env.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		ran = append(ran, name)
		argv := append([]string{"-test.run=^TestHelperProcess$", "--", name}, args...)
		return exec.CommandContext(ctx, os.Args[0], argv...)
	}
	return &ran
}

// bundle puts an executable at home/rel, modified at mod.
func bundle(t *testing.T, home, rel string, mod time.Time) string {
	t.Helper()
	p := filepath.Join(home, rel)
	writeExe(t, p)
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	chatGPTApp = filepath.Join("Applications", "ChatGPT.app", "Contents", "Resources", exeName("codex"))
	vscodeExt  = filepath.Join(".vscode", "extensions", "openai.chatgpt-26.5.1-darwin-arm64", "bin", "macos-aarch64", exeName("codex"))
)

func TestCodexFallsBackToBundled(t *testing.T) {
	// A codex installed long ago cannot serve; the one the user's apps
	// bundle, the newest first, answers for the same home.
	env, _ := fakeEnv(t, "codex-old-on-path")
	ran := withRan(&env)
	bundle(t, env.HomeDir, chatGPTApp, testNow.Add(-48*time.Hour))
	ext := bundle(t, env.HomeDir, vscodeExt, testNow.Add(-time.Hour))
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if err != nil || r.Account != "dev@example.com" || r.Plan != "pro" || r.Quota == nil {
		t.Errorf("reading = %+v, %v", r, err)
	}
	if want := []string{filepath.Join("fake", "bin", "codex"), ext}; !slices.Equal(*ran, want) {
		t.Errorf("ran %q, want %q", *ran, want)
	}
}

func TestCodexWithoutAccountReadGivesWay(t *testing.T) {
	// A codex with app-server but older than account/read turns it down
	// as unknown; a bundled copy that has it answers.
	env, _ := fakeEnv(t, "codex-no-account-read-on-path")
	ran := withRan(&env)
	app := bundle(t, env.HomeDir, chatGPTApp, testNow)
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if err != nil || r.Account != "dev@example.com" {
		t.Errorf("reading = %+v, %v", r, err)
	}
	if want := []string{filepath.Join("fake", "bin", "codex"), app}; !slices.Equal(*ran, want) {
		t.Errorf("ran %q, want %q", *ran, want)
	}
}

func TestCodexKeepsTheAccountWhenItExitsAfter(t *testing.T) {
	// A codex that named the account and then died has served: the account
	// stands, with the reason its quota is missing.
	for _, bundled := range []bool{false, true} {
		env, _ := fakeEnv(t, "codex-exit-after-account")
		ran := withRan(&env)
		if bundled {
			bundle(t, env.HomeDir, chatGPTApp, testNow)
		}
		r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
		if r.Account != "dev@example.com" || r.Plan != "pro" || r.Quota != nil {
			t.Errorf("bundled %v: reading = %+v", bundled, r)
		}
		if !errors.Is(err, errExited) || !strings.Contains(errText(err), "no backend") {
			t.Errorf("bundled %v: error %q", bundled, errText(err))
		}
		if len(*ran) != 1 {
			t.Errorf("bundled %v: ran %q", bundled, *ran)
		}
	}
}

func TestCodexNoneServes(t *testing.T) {
	// When none can, the one on PATH says why.
	env, _ := fakeEnv(t, "codex-exit-says")
	ran := withRan(&env)
	app := bundle(t, env.HomeDir, chatGPTApp, testNow)
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if !errors.Is(err, errExited) || !strings.Contains(errText(err), "unrecognized subcommand 'app-server'") || r != (Reading{}) {
		t.Errorf("reading = %+v, %v", r, err)
	}
	if want := []string{filepath.Join("fake", "bin", "codex"), app}; !slices.Equal(*ran, want) {
		t.Errorf("ran %q, want %q", *ran, want)
	}
}

func TestCodexAnswerEndsTheSearch(t *testing.T) {
	// A codex that answered, even that nobody is logged in, is the answer:
	// another binary reads the same home.
	env, _ := fakeEnv(t, "codex-logged-out")
	ran := withRan(&env)
	bundle(t, env.HomeDir, chatGPTApp, testNow)
	_, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if !saysLoggedOut(err) || len(*ran) != 1 {
		t.Errorf("ran %q, error %v", *ran, err)
	}
}

func TestCodexOnlyBundled(t *testing.T) {
	// Someone who uses only the app has no codex on PATH.
	env, _ := fakeEnv(t, "codex-ok")
	env.LookPath = notOnPath
	ran := withRan(&env)
	apps := t.TempDir()
	env.AppDirs = []string{filepath.Join(apps, "missing"), apps}
	app := bundle(t, apps, filepath.Join("ChatGPT.app", "Contents", "Resources", exeName("codex")), testNow)
	if !env.Find("codex") || env.Find("claude") {
		t.Error("Find disagrees with the bundled codex")
	}
	r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
	if err != nil || r.Account != "dev@example.com" || !slices.Equal(*ran, []string{app}) {
		t.Errorf("ran %q: %+v, %v", *ran, r, err)
	}
}

func TestBins(t *testing.T) {
	home := t.TempDir()
	onPath := filepath.Join(home, ".local", "bin", exeName("codex"))
	writeExe(t, onPath)
	env := Env{HomeDir: home, LookPath: notOnPath}
	old := bundle(t, home, filepath.Join(".cursor", "extensions", "openai.chatgpt-0.4.1", "bin", "linux-x86_64", exeName("codex")), testNow.Add(-72*time.Hour))
	newer := bundle(t, home, filepath.Join(".cursor", "extensions", "openai.chatgpt-0.5.0", "bin", "linux-x86_64", exeName("codex")), testNow)
	app := bundle(t, home, chatGPTApp, testNow.Add(-24*time.Hour))
	want := []string{onPath, newer, app, old}
	if runtime.GOOS != "windows" {
		// A link to a binary already listed, and a file that cannot run,
		// are left out.
		link := filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(onPath, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(bundle(t, home, vscodeExt, testNow), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := env.bins("codex"); !slices.Equal(got, want) {
		t.Errorf("bins = %q, want %q", got, want)
	}
	writeExe(t, filepath.Join(home, ".vscode", "extensions", "openai.chatgpt-26.5.1", "bin", "x", exeName("claude")))
	if got := env.bins("claude"); got != nil {
		t.Errorf("claude bins = %q", got)
	}
}

func TestCodexLimits(t *testing.T) {
	window := func(pct float64, mins int, reset int64) string {
		return fmt.Sprintf(`{"usedPercent":%g,"windowDurationMins":%d,"resetsAt":%d}`, pct, mins, reset)
	}
	tests := []struct {
		name    string
		raw     string
		want    []snapshot.Window
		plan    string
		wantErr bool
	}{
		{
			name: "single bucket",
			raw:  `{"rateLimits":{"limitId":null,"planType":"plus","primary":` + window(12, 300, 1790003600) + `,"secondary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":null}}}`,
			want: []snapshot.Window{
				{Name: "5h", Percent: 12, Minutes: 300, ResetsAt: unix(1790003600)},
				{Name: "7d", Percent: 40, Minutes: 10080},
			},
			plan: "plus",
		},
		{
			name: "buckets put codex first, then by id",
			raw: `{"rateLimits":{"primary":` + window(99, 300, 0) + `},"rateLimitsByLimitId":{` +
				`"zeta":{"primary":` + window(3, 60, 0) + `,"planType":"team"},` +
				`"codex":{"limitId":"codex","primary":` + window(1, 300, 0) + `,"secondary":null,"planType":"pro"},` +
				`"alpha":{"limitId":"alpha","secondary":` + window(2, 1440, 0) + `},` +
				`"gone":null}}`,
			want: []snapshot.Window{
				{Name: "5h", Percent: 1, Minutes: 300},
				{Name: "alpha 1d", Percent: 2, Minutes: 1440},
				{Name: "zeta 1h", Percent: 3, Minutes: 60},
			},
			plan: "pro",
		},
		{
			name: "no windows",
			raw:  `{"rateLimits":{"primary":null,"secondary":null,"planType":"free"},"rateLimitsByLimitId":null}`,
			plan: "free",
		},
		{
			name:    "not an object",
			raw:     `[1,2]`,
			wantErr: true,
		},
	}
	now := testNow
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, plan, err := codexLimits(json.RawMessage(tc.raw), now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v", err)
			}
			if plan != tc.plan {
				t.Errorf("plan = %q, want %q", plan, tc.plan)
			}
			var want *Quota
			if tc.want != nil {
				want = &Quota{At: now, Source: "harness", Windows: tc.want}
			}
			if !reflect.DeepEqual(q, want) {
				t.Errorf("got  %v\nwant %v", describe(q), describe(want))
			}
		})
	}
}

func TestCodexLimitsCapsWindows(t *testing.T) {
	var parts []string
	for i := range 6 {
		parts = append(parts, fmt.Sprintf(`"b%d":{"primary":{"usedPercent":1,"windowDurationMins":300},"secondary":{"usedPercent":2,"windowDurationMins":10080}}`, i))
	}
	q, _, err := codexLimits(json.RawMessage(`{"rateLimitsByLimitId":{`+strings.Join(parts, ",")+`}}`), testNow)
	if err != nil || q == nil || len(q.Windows) != snapshot.MaxWindows {
		t.Fatalf("got %v, %v", describe(q), err)
	}
	if q.Windows[0].Name != "b0 5h" || q.Windows[7].Name != "b3 7d" {
		t.Errorf("windows = %v", describe(q))
	}
}

// pipeServer is an in-process app-server: it answers what answer returns
// for each request, and says nothing for an empty answer.
func pipeServer(t *testing.T, answer func(id int, method string) string) (*codexRPC, func()) {
	t.Helper()
	cr, cw := io.Pipe()
	sr, sw := io.Pipe()
	go func() {
		br := bufio.NewReader(cr)
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				return
			}
			var m struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(line, &m)
			if out := answer(m.ID, m.Method); out != "" {
				if _, err := io.WriteString(sw, out); err != nil {
					return
				}
			}
		}
	}()
	rpc := newCodexRPC(cw, sr)
	return rpc, func() {
		rpc.close()
		_ = cw.Close()
		_ = sw.Close()
		_ = cr.Close()
	}
}

// A call that gave up must not leave a reader behind that takes the next
// call's answer. The old reader-per-call design raced here and lost it.
func TestCodexRPCAbandonedCall(t *testing.T) {
	rpc, stop := pipeServer(t, func(id int, method string) string {
		if id == 2 {
			return `{"id":1,"result":{"late":true}}` + "\n" + `{"id":2,"result":{"ok":true}}` + "\n"
		}
		return ""
	})
	defer stop()
	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()
	if _, err := rpc.call(ctx1, 1, "first", nil); err == nil {
		t.Fatal("first call was answered")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	res, err := rpc.call(ctx2, 2, "second", nil)
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("second call: %s, %v", res, err)
	}
	stop()
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

// A long error answer is cut short, and not inside a character.
func TestCodexRPCErrorAnswer(t *testing.T) {
	long := "x" + strings.Repeat("é", 100) // two-byte characters from an odd offset
	rpc, stop := pipeServer(t, func(id int, method string) string {
		return fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":%q}}`+"\n", id, long)
	})
	defer stop()
	_, err := rpc.call(context.Background(), 1, "m", nil)
	if msg := errText(err); !strings.Contains(msg, "éééé") || strings.Contains(msg, long) || !utf8.ValidString(msg) {
		t.Errorf("error = %q", msg)
	}
}

func TestCodexRPCLastLineWithoutNewline(t *testing.T) {
	cr, cw := io.Pipe()
	sr, sw := io.Pipe()
	go func() {
		_, _ = bufio.NewReader(cr).ReadBytes('\n')
		_, _ = io.WriteString(sw, `{"id":1,"result":{"ok":true}}`)
		_ = sw.Close()
		_ = cr.Close()
	}()
	rpc := newCodexRPC(cw, sr)
	defer rpc.close()
	res, err := rpc.call(context.Background(), 1, "m", nil)
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("call: %s, %v", res, err)
	}
	if _, err := rpc.call(context.Background(), 2, "next", nil); err == nil {
		t.Error("call after the server went away succeeded")
	}
	waitNoGoroutine(t, "probe.(*codexRPC)")
}

// close releases a reader that holds a line nobody asked for.
func TestCodexRPCCloseReleasesReader(t *testing.T) {
	sr, sw := io.Pipe()
	rpc := newCodexRPC(io.Discard, sr)
	go func() { _, _ = io.WriteString(sw, `{"method":"unasked"}`+"\n") }()
	time.Sleep(20 * time.Millisecond)
	rpc.close()
	rpc.close()
	waitNoGoroutine(t, "probe.(*codexRPC).read")
	_ = sw.Close()
}

// buggyReader panics as a bug in the reading goroutine would.
type buggyReader struct{}

func (buggyReader) Read([]byte) (int, error) { panic("reader bug") }

// A bug in the goroutine that reads app-server fails the call, in one line,
// rather than ending the process.
func TestCodexRPCReaderPanicIsTheCallError(t *testing.T) {
	rpc := newCodexRPC(io.Discard, buggyReader{})
	defer rpc.close()
	_, err := rpc.call(context.Background(), 1, "initialize", nil)
	if msg := errText(err); !strings.Contains(msg, "reader bug") || strings.Contains(msg, "\n") {
		t.Errorf("error = %q", msg)
	}
	waitNoGoroutine(t, "probe.(*codexRPC).read")
}

// A bug in the goroutine that waits for app-server is the probe's error. A
// nil command panics there as such a bug would.
func TestWaitOrKillPanicIsItsError(t *testing.T) {
	if msg := errText(waitOrKill(nil, time.Minute)); msg == "" || strings.Contains(msg, "\n") {
		t.Errorf("error = %q", msg)
	}
}

func TestLastLineSaysTheError(t *testing.T) {
	for in, want := range map[string]string{
		"node:internal/modules/cjs/loader:1228\n  throw err;\n  ^\n\nError: Cannot find module '/x'\n    at Module._resolveFilename (node:internal)\n\nNode.js v22.1.0\n": "Error: Cannot find module '/x'",
		"it broke\n\nFor more information, try '--help'.\n": "it broke",
		"\x1b[2mlast words\x1b[0m\r\n\n":                    "last words",
		"":                                                  "",
		"thread 'main' panicked at src/main.rs:5:5:\nfailed to load config\nnote: run with `RUST_BACKTRACE=1` environment variable to display a backtrace\n": "failed to load config",
		"thread 'main' panicked at 'old style', src/main.rs:5:5\nnote: run with `RUST_BACKTRACE=1` environment variable to display a backtrace\n":            "thread 'main' panicked at 'old style', src/main.rs:5:5",
		"TypeError: foo is not a function\n    at ModuleJob.run (node:internal/modules/esm/module_job:195:25)\n\nNode.js v20.11.0\n":                         "TypeError: foo is not a function",
		"'node' is not recognized as an internal or external command,\r\noperable program or batch file.\r\n":                                                "'node' is not recognized as an internal or external command, operable program or batch file.",
	} {
		var l lastLine
		_, _ = l.Write([]byte(in))
		if got := l.String(); got != want {
			t.Errorf("lastLine(%q) = %q, want %q", in, got, want)
		}
	}
}
