package logs

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// turn is one turn_completed update. Usage is [input, output, cached read, cache creation];
// xAI counts the cached read inside input.
func turn(prompt string, usage [4]int64) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"_x.ai/session/update","params":{"sessionId":"sid","update":{"sessionUpdate":"turn_completed","prompt_id":%q,"stopReason":"end_turn","usage":{"inputTokens":%d,"outputTokens":%d,"cachedReadTokens":%d,"cacheCreationTokens":%d,"totalTokens":%d}}}}`,
		prompt, usage[0], usage[1], usage[2], usage[3], usage[0]+usage[1])
}

func chunk() string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + secret + `"}}}}`
}

func grokSummary(id, cwd string) string {
	return fmt.Sprintf(`{"info":{"id":%q,"cwd":%q,"title":%q,"model":"grok-code-fast-1"}}`, id, cwd, secret)
}

func TestGrokCountsLastUsagePerPrompt(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "%2Fwork%2Fapp", "sid")
	mustWrite(t, filepath.Join(dir, "summary.json"), grokSummary("sid", "/work/app"))
	mustWrite(t, filepath.Join(dir, "updates.jsonl"),
		turn("p1", [4]int64{10, 1, 2, 3}),
		chunk(),
		// A retried turn reports the prompt again; the last report is the one billed.
		turn("p1", [4]int64{15, 4, 5, 6}),
		turn("p2", [4]int64{3, 1, 0, 0}),
		// An update that is not turn_completed but mentions it carries no usage.
		`{"params":{"update":{"sessionUpdate":"plan","note":"turn_completed"}}}`,
	)
	// Transcripts and prompt history are not usage and stay closed.
	mustWrite(t, filepath.Join(dir, "chat_history.jsonl"), turn("leak", [4]int64{999, 999, 0, 0}))
	mustWrite(t, filepath.Join(home, "sessions", "%2Fwork%2Fapp", "prompt_history.jsonl"), turn("leak", [4]int64{999, 999, 0, 0}))
	mustWrite(t, filepath.Join(home, "auth.json"), "{}")
	deny(t, filepath.Join(home, "auth.json"))
	mustWrite(t, filepath.Join(dir, "auth.json"), "{}")
	deny(t, filepath.Join(dir, "auth.json"))

	res := mustRead(t, "grok", home, since)
	if res.Malformed != 0 || res.Unreadable != 0 {
		t.Fatalf("malformed %d unreadable %d", res.Malformed, res.Unreadable)
	}
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %+v", res.Sessions)
	}
	got := res.Sessions[0]
	if want := (Tokens{Input: 13, Output: 5, CacheRead: 5, CacheWrite: 6}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.ID != "sid" || got.Project != "/work/app" {
		t.Fatalf("session = %+v", got)
	}
	noLeak(t, res)
}

func TestGrokSessionWithoutSummary(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "%2Fwork%2Fmy%20app", "dir-id")
	mustWrite(t, filepath.Join(dir, "updates.jsonl"),
		turn("", [4]int64{5, 1, 0, 0}),
		turn("", [4]int64{6, 1, 0, 0}),
		// More cached than input is a log error; input does not go negative.
		turn("p", [4]int64{2, 1, 9, 0}),
	)
	res := mustRead(t, "grok", home, since)
	got := byID(t, res, "dir-id")
	if got.Project != "/work/my app" {
		t.Fatalf("project = %q", got.Project)
	}
	// Turns without a prompt id cannot be matched, so each one counts.
	if want := (Tokens{Input: 11, Output: 3, CacheRead: 9}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
	}
}

func TestGrokSummaryFallbacks(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	// A summary that does not parse is counted, and the directory names the session.
	mustWrite(t, filepath.Join(sessions, "%2Fwork%2Fa", "a-dir", "summary.json"), `{"info":`)
	mustWrite(t, filepath.Join(sessions, "%2Fwork%2Fa", "a-dir", "updates.jsonl"), turn("p", [4]int64{1, 1, 0, 0}), `{"params":{"update":{"sessionUpdate":"turn_completed"`)
	// A summary without an id or cwd keeps the directory's.
	mustWrite(t, filepath.Join(sessions, "%2Fwork%2Fb", "b-dir", "summary.json"), `{"info":{"cwd":"  "}}`)
	mustWrite(t, filepath.Join(sessions, "%2Fwork%2Fb", "b-dir", "updates.jsonl"), turn("p", [4]int64{2, 1, 0, 0}))
	// The summary's id and cwd win over the directory's.
	mustWrite(t, filepath.Join(sessions, "%2Fwork%2Fc", "c-dir", "summary.json"), grokSummary("c-id", "/real/c"))
	mustWrite(t, filepath.Join(sessions, "%2Fwork%2Fc", "c-dir", "updates.jsonl"), turn("p", [4]int64{3, 1, 0, 0}))
	// A summary with no updates yet has nothing to count.
	mustWrite(t, filepath.Join(sessions, "%2Fwork%2Fd", "d-dir", "summary.json"), grokSummary("d-id", "/work/d"))

	res := mustRead(t, "grok", home, since)
	if res.Malformed != 2 {
		t.Fatalf("malformed = %d", res.Malformed)
	}
	want := map[string]string{"a-dir": "/work/a", "b-dir": "/work/b", "c-id": "/real/c"}
	if got := strings.Join(ids(res), ","); got != "a-dir,b-dir,c-id" {
		t.Fatalf("sessions = %s", got)
	}
	for id, project := range want {
		if got := byID(t, res, id); got.Project != project {
			t.Errorf("%s project = %q, want %q", id, got.Project, project)
		}
	}
}

func TestGrokWindowUsesNewestFile(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions", "%2Fwork")
	write := func(id string, summaryAt, updatesAt time.Time) {
		dir := filepath.Join(sessions, id)
		mustWrite(t, filepath.Join(dir, "summary.json"), grokSummary(id, "/work"))
		mustWrite(t, filepath.Join(dir, "updates.jsonl"), turn("p", [4]int64{1, 1, 0, 0}))
		mustWrite(t, filepath.Join(dir, "chat_history.jsonl"), chunk())
		ageFile(t, filepath.Join(dir, "summary.json"), summaryAt)
		ageFile(t, filepath.Join(dir, "updates.jsonl"), updatesAt)
		// Transcript writes are not usage and do not keep a session in the window.
		ageFile(t, filepath.Join(dir, "chat_history.jsonl"), now)
	}
	old := since.Add(-time.Hour)
	write("old", old, old)
	write("new-updates", old, now.Add(-2*time.Hour))
	write("new-summary", now.Add(-time.Hour), old)

	res := mustRead(t, "grok", home, since)
	if got := strings.Join(ids(res), ","); got != "new-summary,new-updates" {
		t.Fatalf("sessions = %s", got)
	}
	if got := byID(t, res, "new-updates").Updated; !got.Equal(now.Add(-2 * time.Hour)) {
		t.Fatalf("updated = %v", got)
	}
	if got := byID(t, res, "new-summary").Updated; !got.Equal(now.Add(-time.Hour)) {
		t.Fatalf("updated = %v", got)
	}
}

func TestGrokIgnoresFilesAtOtherDepths(t *testing.T) {
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	mustWrite(t, filepath.Join(sessions, "updates.jsonl"), turn("p", [4]int64{100, 0, 0, 0}))
	mustWrite(t, filepath.Join(sessions, "%2Fwork", "updates.jsonl"), turn("p", [4]int64{100, 0, 0, 0}))
	mustWrite(t, filepath.Join(sessions, "%2Fwork", "sid", "nested", "updates.jsonl"), turn("p", [4]int64{100, 0, 0, 0}))
	mustWrite(t, filepath.Join(sessions, "%2Fwork", "sid", "updates.jsonl"), turn("p", [4]int64{1, 0, 0, 0}))

	res := mustRead(t, "grok", home, since)
	if got := total(res); got != (Tokens{Input: 1}) {
		t.Fatalf("total = %+v", got)
	}
}

func TestGrokUnreadableFiles(t *testing.T) {
	needDeny(t)
	home := t.TempDir()
	sessions := filepath.Join(home, "sessions")
	mustWrite(t, filepath.Join(sessions, "%2Fa", "ok", "updates.jsonl"), turn("p", [4]int64{2, 1, 0, 0}))
	mustWrite(t, filepath.Join(sessions, "%2Fa", "locked", "updates.jsonl"), turn("p", [4]int64{9, 9, 0, 0}))
	mustWrite(t, filepath.Join(sessions, "%2Fa", "hidden-summary", "summary.json"), grokSummary("x", "/x"))
	mustWrite(t, filepath.Join(sessions, "%2Fa", "hidden-summary", "updates.jsonl"), turn("p", [4]int64{3, 1, 0, 0}))
	mustWrite(t, filepath.Join(sessions, "%2Fb", "gone", "updates.jsonl"), turn("p", [4]int64{9, 9, 0, 0}))
	deny(t, filepath.Join(sessions, "%2Fa", "locked", "updates.jsonl"))
	deny(t, filepath.Join(sessions, "%2Fa", "hidden-summary", "summary.json"))
	deny(t, filepath.Join(sessions, "%2Fb"))

	res := mustRead(t, "grok", home, since)
	if res.Unreadable != 3 {
		t.Fatalf("unreadable = %d", res.Unreadable)
	}
	if got := strings.Join(ids(res), ","); got != "hidden-summary,ok" {
		t.Fatalf("sessions = %s", got)
	}
}

func TestGrokMissingSessions(t *testing.T) {
	res := mustRead(t, "grok", t.TempDir(), since)
	if len(res.Sessions) != 0 || res.Unreadable != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestDecodeGrokPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"%2Fwork%2Fapp", "/work/app"},
		{"%2FUsers%2Fme%2Fmy%20app", "/Users/me/my app"},
		{"C%3A%5Cwork", `C:\work`},
		{"plain", "plain"},
		{"bad%zz", "bad%zz"},
		{"%20", "%20"},
		{"", ""},
	} {
		if got := decodeGrokPath(tc.in); got != tc.want {
			t.Errorf("decodeGrokPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
