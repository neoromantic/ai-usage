package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cl is one Claude transcript line. Usage is [input, output, cache write, cache read].
type cl struct {
	typ, uuid, id, req, session, cwd, at string
	side                                 bool
	usage                                *[4]int64
}

func (c cl) String() string {
	if c.typ == "" {
		c.typ = "assistant"
	}
	usage := ""
	if c.usage != nil {
		usage = fmt.Sprintf(`,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"service_tier":"standard"}`, c.usage[0], c.usage[1], c.usage[2], c.usage[3])
	}
	return fmt.Sprintf(`{"parentUuid":null,"isSidechain":%t,"userType":"external","cwd":%q,"sessionId":%q,"version":"2.1.3","type":%q,"message":{"id":%q,"role":"assistant","model":"claude-opus-4-5","content":[{"type":"text","text":%q}]%s},"requestId":%q,"uuid":%q,"timestamp":%q}`,
		c.side, c.cwd, c.session, c.typ, c.id, secret, usage, c.req, c.uuid, c.at)
}

func use(in, out, cacheWrite, cacheRead int64) *[4]int64 {
	return &[4]int64{in, out, cacheWrite, cacheRead}
}

// costState is the line Claude Code writes when it exits: what it tracked for
// session so far, per model. Usage is [input, output, cache write, cache read].
func costState(session string, perModel ...[4]int64) string {
	models := make([]string, 0, len(perModel))
	for i, u := range perModel {
		models = append(models, fmt.Sprintf(`"claude-model-%d[1m]":{"inputTokens":%d,"outputTokens":%d,"thinkingTokens":1,"cacheReadInputTokens":%d,"cacheCreationInputTokens":%d,"webSearchRequests":0,"costUSD":0.25}`, i, u[0], u[1], u[3], u[2]))
	}
	return fmt.Sprintf(`{"type":"cost-state","sessionId":%q,"totalCostUSD":1.5,"totalAPIDuration":900,"totalAPIDurationWithoutRetries":800,"totalToolDuration":10,"totalLinesAdded":3,"totalLinesRemoved":0,"totalDuration":5000,"startTime":1790000000000,"modelUsage":{%s},"hasUnknownModelCost":false}`,
		session, strings.Join(models, ","))
}

func TestClaudeStreamingSnapshotsCountOnce(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "projects", "-work-claude", "sess.jsonl"),
		`{"type":"user","cwd":"/work/claude","sessionId":"sess","message":{"role":"user","content":"`+secret+`"},"uuid":"u0","timestamp":"2026-09-20T10:00:00Z"}`,
		cl{uuid: "u1", id: "m1", req: "r1", session: "sess", cwd: "/work/claude", at: "2026-09-20T10:00:01Z", usage: use(10, 1, 2, 3)}.String(),
		cl{uuid: "u2", id: "m1", req: "r1", session: "sess", cwd: "/work/claude", at: "2026-09-20T10:00:02Z", usage: use(10, 9, 2, 3)}.String(),
		cl{uuid: "u3", id: "m2", req: "r2", session: "sess", cwd: "/work/claude", at: "2026-09-20T10:00:03Z", usage: use(2, 2, 0, 1)}.String(),
		// A user line never counts, whatever it carries.
		cl{typ: "user", uuid: "u4", id: "user", session: "sess", cwd: "/work/claude", usage: use(100, 100, 0, 0)}.String(),
		// No message id: the line uuid tells snapshots apart; with neither, each line counts.
		cl{uuid: "u5", session: "sess", cwd: "/work/claude", usage: use(1, 0, 0, 0)}.String(),
		cl{uuid: "u5", session: "sess", cwd: "/work/claude", usage: use(1, 1, 0, 0)}.String(),
		cl{session: "sess", cwd: "/work/claude", usage: use(1, 0, 0, 0)}.String(),
		cl{session: "sess", cwd: "/work/claude", usage: use(1, 0, 0, 0)}.String(),
		`{"type":"assistant","usage"`,
	)
	res := mustRead(t, "claude", home, since)
	if res.Malformed != 1 || res.Unreadable != 0 {
		t.Fatalf("malformed %d unreadable %d", res.Malformed, res.Unreadable)
	}
	got := byID(t, res, "sess")
	if want := (Tokens{Input: 15, Output: 12, CacheRead: 4, CacheWrite: 2}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.Project != "/work/claude" || got.ParentID != "" {
		t.Fatalf("session = %+v", got)
	}
	noLeak(t, res)
}

func TestClaudeSubAgentsRollIntoSession(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-claude")
	parent := filepath.Join(dir, "sess.jsonl")
	direct := filepath.Join(dir, "sess", "subagents", "agent-1.jsonl")
	nested := filepath.Join(dir, "sess", "subagents", "workflows", "wf_1", "agent-2.jsonl")
	mustWrite(t, parent, cl{id: "m1", req: "r1", session: "sess", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String())
	mustWrite(t, direct, cl{id: "s1", req: "rs1", session: "sess", cwd: "/work/other", side: true, at: "2026-09-20T10:01:00Z", usage: use(3, 4, 1, 0)}.String())
	mustWrite(t, nested, cl{id: "s2", req: "rs2", session: "sess", cwd: "/work/other", side: true, at: "2026-09-20T10:02:00Z", usage: use(5, 5, 0, 2)}.String())
	// A journal beside the workflow agents has no usage.
	journal := filepath.Join(dir, "sess", "subagents", "workflows", "wf_1", "journal.jsonl")
	mustWrite(t, journal, `{"type":"workflow","cwd":"/work/other"}`)
	ageFile(t, journal, now.Add(-2*time.Hour))
	ageFile(t, parent, now.Add(-2*time.Hour))
	ageFile(t, direct, now.Add(-3*time.Hour))
	ageFile(t, nested, now.Add(-time.Hour))

	res := mustRead(t, "claude", home, since)
	if got := strings.Join(ids(res), ","); got != "sess" {
		t.Fatalf("sessions = %s", got)
	}
	got := res.Sessions[0]
	if want := (Tokens{Input: 18, Output: 10, CacheWrite: 1, CacheRead: 2}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.Project != "/work/claude" || !got.Updated.Equal(now.Add(-time.Hour)) {
		t.Fatalf("session = %+v", got)
	}
}

// Older Claude Code wrote sub-agent transcripts beside the session file.
func TestClaudeTopLevelSidechainRollsIntoSession(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-claude")
	mustWrite(t, filepath.Join(dir, "sess.jsonl"), cl{id: "m1", req: "r1", session: "sess", cwd: "/work/claude", usage: use(10, 1, 0, 0)}.String())
	mustWrite(t, filepath.Join(dir, "agent-abc.jsonl"), cl{id: "a1", req: "ra1", session: "sess", cwd: "/work/claude", side: true, usage: use(4, 4, 0, 0)}.String())
	mustWrite(t, filepath.Join(dir, "solo.jsonl"), cl{id: "x1", req: "rx1", session: "solo", cwd: "/work/solo", side: true, usage: use(1, 1, 0, 0)}.String())

	res := mustRead(t, "claude", home, since)
	if got := strings.Join(ids(res), ","); got != "sess,solo" {
		t.Fatalf("sessions = %s", got)
	}
	if got := byID(t, res, "sess").Tokens; got != (Tokens{Input: 14, Output: 5}) {
		t.Fatalf("sess = %+v", got)
	}
}

// A resumed or forked session copies earlier messages into its own file.
func TestClaudeCopiedMessagesCountInFirstSession(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-claude")
	original := []string{
		cl{id: "m1", req: "r1", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String(),
		cl{id: "m2", req: "r2", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:01:00Z", usage: use(20, 2, 0, 0)}.String(),
		// Without a request id a message stays scoped to its file.
		cl{id: "m9", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:02:00Z", usage: use(1, 0, 0, 0)}.String(),
	}
	// The copy sorts first by name but started later.
	mustWrite(t, filepath.Join(dir, "ffff-orig.jsonl"), original...)
	copied := append(append([]string{}, original...),
		cl{id: "m3", req: "r3", session: "copy", cwd: "/work/claude", at: "2026-09-21T09:00:00Z", usage: use(40, 4, 0, 0)}.String())
	copied[0] = strings.Replace(copied[0], `"timestamp":"2026-09-20T10:00:00Z"`, `"timestamp":"2026-09-21T08:59:00Z"`, 1)
	mustWrite(t, filepath.Join(dir, "0000-copy.jsonl"), copied...)

	res := mustRead(t, "claude", home, since)
	if got := byID(t, res, "ffff-orig").Tokens; got != (Tokens{Input: 31, Output: 3}) {
		t.Fatalf("original = %+v", got)
	}
	// The copy keeps its own request and the message that had no request id.
	if got := byID(t, res, "0000-copy").Tokens; got != (Tokens{Input: 41, Output: 4}) {
		t.Fatalf("copy = %+v", got)
	}
}

// A copy that kept the original lines verbatim starts at the same moment. The
// file those lines name keeps them, whatever the file names, so the owner does
// not change between runs as copies come and go.
func TestClaudeVerbatimCopyLeavesMessagesWithOriginal(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-claude")
	original := []string{
		cl{id: "m1", req: "r1", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String(),
		cl{id: "m2", req: "r2", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:01:00Z", usage: use(20, 2, 0, 0)}.String(),
	}
	mustWrite(t, filepath.Join(dir, "orig.jsonl"), original...)
	mustWrite(t, filepath.Join(dir, "a-copy.jsonl"), append(append([]string{}, original...),
		cl{id: "m3", req: "r3", session: "a-copy", cwd: "/work/claude", at: "2026-09-21T09:00:00Z", usage: use(40, 4, 0, 0)}.String())...)

	res := mustRead(t, "claude", home, since)
	if got := byID(t, res, "orig").Tokens; got != (Tokens{Input: 30, Output: 3}) {
		t.Fatalf("original = %+v", got)
	}
	if got := byID(t, res, "a-copy").Tokens; got != (Tokens{Input: 40, Output: 4}) {
		t.Fatalf("copy = %+v", got)
	}
}

// The same session file can sit under two project directories, one a longer
// copy of the other. The session counts once, with every message once.
func TestClaudeOneSessionInTwoProjects(t *testing.T) {
	home := t.TempDir()
	shared := []string{
		cl{id: "m1", req: "r1", session: "dup", cwd: "/work/a", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String(),
		cl{id: "m2", session: "dup", cwd: "/work/a", at: "2026-09-20T10:01:00Z", usage: use(20, 2, 0, 0)}.String(),
		cl{session: "dup", cwd: "/work/a", usage: use(1, 0, 0, 0)}.String(),
	}
	a := filepath.Join(home, "projects", "-work-a", "dup.jsonl")
	b := filepath.Join(home, "projects", "-work-b", "dup.jsonl")
	mustWrite(t, a, shared...)
	mustWrite(t, b, append(append([]string{}, shared...),
		cl{id: "m3", req: "r3", session: "dup", cwd: "/work/a", at: "2026-09-21T10:00:00Z", usage: use(40, 4, 0, 0)}.String())...)
	mustWrite(t, filepath.Join(home, "projects", "-work-a", "dup", "subagents", "agent-1.jsonl"),
		cl{id: "s1", req: "rs1", session: "dup", side: true, usage: use(5, 5, 0, 0)}.String())
	mustWrite(t, filepath.Join(home, "projects", "-work-b", "dup", "subagents", "agent-1.jsonl"),
		cl{id: "s1", req: "rs1", session: "dup", side: true, usage: use(5, 5, 0, 0)}.String())
	ageFile(t, a, now.Add(-2*time.Hour))
	ageFile(t, b, now.Add(-time.Hour))

	res := mustRead(t, "claude", home, since)
	if len(res.Sessions) != 1 {
		t.Fatalf("sessions = %+v", res.Sessions)
	}
	got := res.Sessions[0]
	// m1, m2, m3, and the sub-agent once each. Only the anonymous line, which
	// nothing identifies, counts in both files.
	if want := (Tokens{Input: 77, Output: 12}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.ID != "dup" || got.Project != "/work/a" {
		t.Fatalf("session = %+v", got)
	}
}

func TestClaudeWindowAndCredentialFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-claude")
	mustWrite(t, filepath.Join(dir, "sess.jsonl"), cl{id: "m1", req: "r1", session: "sess", cwd: "/work/claude", usage: use(4, 1, 0, 0)}.String())
	mustWrite(t, filepath.Join(dir, "credentials.jsonl"), cl{id: "nope", req: "rn", session: "credentials", cwd: "/work/secret", usage: use(999, 999, 0, 0)}.String())
	mustWrite(t, filepath.Join(dir, ".credentials.json"), "{")
	deny(t, filepath.Join(dir, ".credentials.json"))
	mustWrite(t, filepath.Join(home, ".credentials.json"), "{")
	deny(t, filepath.Join(home, ".credentials.json"))
	old := filepath.Join(dir, "old.jsonl")
	mustWrite(t, old, cl{id: "o1", req: "ro", session: "old", cwd: "/work/old", usage: use(500, 500, 0, 0)}.String())
	mustWrite(t, filepath.Join(dir, "old", "subagents", "agent-1.jsonl"), cl{id: "o2", req: "ro2", session: "old", cwd: "/work/old", usage: use(500, 500, 0, 0)}.String())
	ageFile(t, old, since.Add(-time.Second))
	edge := filepath.Join(dir, "edge.jsonl")
	mustWrite(t, edge, cl{id: "e1", req: "re", session: "edge", cwd: "/work/edge", usage: use(1, 0, 0, 0)}.String())
	ageFile(t, edge, since)
	// Not a transcript.
	mustWrite(t, filepath.Join(dir, "notes.txt"), cl{id: "t1", usage: use(7, 7, 0, 0)}.String())

	res := mustRead(t, "claude", home, since)
	if res.Unreadable != 0 || res.Malformed != 0 {
		t.Fatalf("unreadable %d malformed %d", res.Unreadable, res.Malformed)
	}
	if got := strings.Join(ids(res), ","); got != "edge,sess" {
		t.Fatalf("sessions = %s", got)
	}
	if got := byID(t, res, "edge"); !got.Updated.Equal(since) {
		t.Fatalf("edge updated = %v", got.Updated)
	}
}

func TestClaudeMissingOrBrokenHome(t *testing.T) {
	t.Run("no projects", func(t *testing.T) {
		res := mustRead(t, "claude", t.TempDir(), since)
		if len(res.Sessions) != 0 {
			t.Fatalf("sessions = %+v", res.Sessions)
		}
	})
	t.Run("projects is a file", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, filepath.Join(home, "projects"), "x")
		res := mustRead(t, "claude", home, since)
		if len(res.Sessions) != 0 {
			t.Fatalf("sessions = %+v", res.Sessions)
		}
	})
	t.Run("one unreadable project leaves the others", func(t *testing.T) {
		needDeny(t)
		home := t.TempDir()
		mustWrite(t, filepath.Join(home, "projects", "a", "sess.jsonl"), cl{id: "m1", req: "r1", session: "sess", cwd: "/work/a", usage: use(4, 1, 0, 0)}.String())
		mustWrite(t, filepath.Join(home, "projects", "b", "hidden.jsonl"), cl{id: "m2", req: "r2", session: "hidden", cwd: "/work/b", usage: use(4, 1, 0, 0)}.String())
		mustWrite(t, filepath.Join(home, "projects", "a", "sess", "subagents", "agent-1.jsonl"), cl{id: "m3", req: "r3", session: "sess", usage: use(1, 1, 0, 0)}.String())
		deny(t, filepath.Join(home, "projects", "b"))
		deny(t, filepath.Join(home, "projects", "a", "sess", "subagents", "agent-1.jsonl"))
		res := mustRead(t, "claude", home, since)
		if res.Unreadable != 2 {
			t.Fatalf("unreadable = %d", res.Unreadable)
		}
		if got := byID(t, res, "sess").Tokens; got != (Tokens{Input: 4, Output: 1}) {
			t.Fatalf("sess = %+v", got)
		}
	})
	t.Run("unreadable projects is an error", func(t *testing.T) {
		needDeny(t)
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "projects"), 0o755); err != nil {
			t.Fatal(err)
		}
		deny(t, filepath.Join(home, "projects"))
		if _, err := Read("claude", home, since); err == nil {
			t.Fatal("no error")
		}
	})
}

// Claude Code tracks calls that never become transcript messages, such as
// compaction and titles, and writes the session's totals, sub-agents included,
// when it exits. Each field is the larger of that and the messages.
func TestClaudeTrackedUsageRaisesSession(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-claude")
	mustWrite(t, filepath.Join(dir, "sess.jsonl"),
		cl{id: "m1", req: "r1", session: "sess", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 5, 100, 1000)}.String(),
		costState("sess", [4]int64{1, 1, 1, 1}),
		// The last line is the newest. Messages from a resume still running
		// can be ahead of it.
		costState("sess", [4]int64{50, 4, 150, 900}, [4]int64{7, 1, 0, 0}),
		cl{id: "m2", req: "r2", session: "sess", cwd: "/work/claude", at: "2026-09-20T11:00:00Z", usage: use(0, 3, 0, 0)}.String(),
	)
	// Sub-agent lines name their parent session. Its tracker is not theirs.
	mustWrite(t, filepath.Join(dir, "sess", "subagents", "agent-1.jsonl"),
		cl{id: "s1", req: "rs1", session: "sess", side: true, usage: use(2, 2, 20, 200)}.String(),
		costState("sess", [4]int64{9999, 9999, 9999, 9999}))
	mustWrite(t, filepath.Join(dir, "agent-old.jsonl"),
		cl{id: "a1", req: "ra1", session: "sess", side: true, usage: use(1, 0, 0, 0)}.String(),
		costState("sess", [4]int64{9999, 9999, 9999, 9999}))
	// Not yet exited: the messages are all there is.
	mustWrite(t, filepath.Join(dir, "live.jsonl"),
		cl{id: "l1", req: "rl1", session: "live", cwd: "/work/live", usage: use(3, 3, 0, 0)}.String())

	res := mustRead(t, "claude", home, since)
	if res.Malformed != 0 || res.Unreadable != 0 {
		t.Fatalf("malformed %d unreadable %d", res.Malformed, res.Unreadable)
	}
	if got := strings.Join(ids(res), ","); got != "live,sess" {
		t.Fatalf("sessions = %s", got)
	}
	if got, want := byID(t, res, "sess").Tokens, (Tokens{Input: 57, Output: 10, CacheWrite: 150, CacheRead: 1200}); got != want {
		t.Fatalf("sess = %+v, want %+v", got, want)
	}
	if got := byID(t, res, "live").Tokens; got != (Tokens{Input: 3, Output: 3}) {
		t.Fatalf("live = %+v", got)
	}
}

// A fork starts from its parent's tracker, so its tracked usage repeats the
// parent's. While the parent is read, the fork keeps its own messages only.
func TestClaudeForkDoesNotRepeatParentTracker(t *testing.T) {
	parent := []string{
		cl{id: "m1", req: "r1", session: "orig", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String(),
		costState("orig", [4]int64{30, 1, 0, 0}),
	}
	fork := []string{
		cl{id: "m1", req: "r1", session: "fork", cwd: "/work/claude", at: "2026-09-21T09:00:00Z", usage: use(10, 1, 0, 0)}.String(),
		cl{id: "m2", req: "r2", session: "fork", cwd: "/work/claude", at: "2026-09-21T09:01:00Z", usage: use(5, 2, 0, 0)}.String(),
		costState("fork", [4]int64{45, 3, 0, 0}),
	}
	t.Run("parent read", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, "projects", "-work-claude")
		mustWrite(t, filepath.Join(dir, "orig.jsonl"), parent...)
		mustWrite(t, filepath.Join(dir, "fork.jsonl"), fork...)
		res := mustRead(t, "claude", home, since)
		if got := byID(t, res, "orig").Tokens; got != (Tokens{Input: 30, Output: 1}) {
			t.Fatalf("parent = %+v", got)
		}
		if got := byID(t, res, "fork").Tokens; got != (Tokens{Input: 5, Output: 2}) {
			t.Fatalf("fork = %+v", got)
		}
	})
	t.Run("parent gone", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, filepath.Join(home, "projects", "-work-claude", "fork.jsonl"), fork...)
		res := mustRead(t, "claude", home, since)
		if got := byID(t, res, "fork").Tokens; got != (Tokens{Input: 45, Output: 3}) {
			t.Fatalf("fork = %+v", got)
		}
	})
}
