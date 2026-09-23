package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

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
		{
			mode: "codex-apikey", account: "api key",
			wantErr:   "codex account/rateLimits/read: rate limits need a ChatGPT login",
			wantCalls: []string{"initialize", "initialized", "account/read", "account/rateLimits/read"},
		},
		{
			mode:      "codex-logged-out",
			wantErr:   "codex: not logged in",
			wantCalls: []string{"initialize", "initialized", "account/read"},
			loggedOut: true,
		},
		{
			mode:      "codex-account-error",
			wantErr:   "codex account/read: boom",
			wantCalls: []string{"initialize", "initialized", "account/read"},
		},
		{
			mode:      "codex-exit",
			wantErr:   "codex initialize: app-server exited without answering",
			wantCalls: []string{"initialize"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			env, record := fakeEnv(t, tc.mode)
			r, err := Codex(context.Background(), env, filepath.Join(env.HomeDir, ".codex"))
			if errText(err) != tc.wantErr {
				t.Errorf("error = %q, want %q", errText(err), tc.wantErr)
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
	if errText(err) != "codex account/read: no answer in time" || r.Account != "" {
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
	if errText(err) != "codex binary not found; account unknown" || r != (Reading{}) {
		t.Errorf("reading = %+v, %v", r, err)
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
			name: "no duration",
			raw:  `{"rateLimits":{"primary":{"usedPercent":5,"windowDurationMins":null,"resetsAt":null}}}`,
			want: []snapshot.Window{{Name: "window", Percent: 5}},
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
	if _, err := rpc.call(ctx1, 1, "first", nil); errText(err) != "codex first: no answer in time" {
		t.Fatalf("first call: %v", err)
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

func TestCodexRPCSkipsOtherTraffic(t *testing.T) {
	rpc, stop := pipeServer(t, func(id int, method string) string {
		return strings.Join([]string{
			`{"method":"warning","params":{}}`,
			`{"id":7,"method":"item/tool/call","params":{}}`,
			`{"id":"7","result":{"string id":true}}`,
			`{"id":null,"error":{"code":-32700,"message":"parse error"}}`,
			``,
			`garbage`,
			`{"id":7,"result":{"ok":true}}`,
			`{"method":"after","params":{}}`,
		}, "\n") + "\n"
	})
	defer stop()
	res, err := rpc.call(context.Background(), 7, "m", map[string]any{"x": 1})
	if err != nil || string(res) != `{"ok":true}` {
		t.Fatalf("call: %s, %v", res, err)
	}
}

func TestCodexRPCErrorAnswer(t *testing.T) {
	long := strings.Repeat("é", 100) // 200 bytes; the cut must not split one
	rpc, stop := pipeServer(t, func(id int, method string) string {
		return fmt.Sprintf(`{"id":%d,"error":{"code":-32600,"message":%q}}`+"\n", id, long)
	})
	defer stop()
	_, err := rpc.call(context.Background(), 1, "m", nil)
	want := "codex m: " + strings.Repeat("é", 80)
	if errText(err) != want {
		t.Errorf("error = %q", errText(err))
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
