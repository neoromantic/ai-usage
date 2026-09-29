package logs

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/neoromantic/ai-usage/internal/meter"
	"github.com/neoromantic/ai-usage/internal/snapshot"
)

// cl is one Claude transcript line. Usage is [input, output, cache write,
// cache read], and ttl splits the cache writes into [kept 5m, kept 1h]. Stop
// is the stop reason as JSON, left out when empty, and so is the version.
type cl struct {
	typ, uuid, id, req, session, cwd, at string
	side                                 bool
	usage                                *[4]int64
	ttl                                  *[2]int64
	stop, version                        string
}

func (c cl) String() string {
	if c.typ == "" {
		c.typ = "assistant"
	}
	usage, stop, version := "", "", ""
	if c.usage != nil {
		ttl := ""
		if c.ttl != nil {
			ttl = fmt.Sprintf(`,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d}`, c.ttl[0], c.ttl[1])
		}
		usage = fmt.Sprintf(`,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d%s,"service_tier":"standard"}`, c.usage[0], c.usage[1], c.usage[2], c.usage[3], ttl)
	}
	if c.stop != "" {
		stop = `,"stop_reason":` + c.stop + `,"stop_sequence":null`
	}
	if c.version != "" {
		version = fmt.Sprintf(`"version":%q,`, c.version)
	}
	return fmt.Sprintf(`{"parentUuid":null,"isSidechain":%t,"userType":"external","cwd":%q,"sessionId":%q,%s"type":%q,"message":{"id":%q,"role":"assistant","model":"claude-opus-4-5","content":[{"type":"text","text":%q}]%s%s},"requestId":%q,"uuid":%q,"timestamp":%q}`,
		c.side, c.cwd, c.session, version, c.typ, c.id, secret, stop, usage, c.req, c.uuid, c.at)
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

// costStateAt is a cost-state line that records when it was written, which
// the lines Claude Code writes today do not.
func costStateAt(session, at string, perModel ...[4]int64) string {
	return strings.Replace(costState(session, perModel...), `"sessionId"`, fmt.Sprintf(`"timestamp":%q,"sessionId"`, at), 1)
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
	// The session was last active at its newest message, a sub-agent's,
	// whenever the files were written; the journal records no usage.
	if got.Project != "/work/claude" || !got.Updated.Equal(mustTime(t, "2026-09-20T10:02:00Z")) {
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
	// The messages with a time spent 33 and 44. The 12 without one, the
	// anonymous lines and the sub-agent, go along in proportion, with what
	// rounding leaves to the largest hour.
	checkHours(t, got, map[string]int64{"2026-09-20T10:00:00Z": 38, "2026-09-21T10:00:00Z": 51})
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
		if _, err := readOne("claude", home, since); err == nil {
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

// rejected is a request Claude refused with a rate limit, in the shape Claude
// Code writes it.
func rejected(id, session, at, status, limit string, resets time.Time) string {
	return fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":"/work/claude","sessionId":%q,"type":"assistant","message":{"id":%q,"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":%q}],"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}},"requestId":"req-%s","uuid":"u-%s","timestamp":%q,"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429,"quotaLimits":{"status":%q,"resetsAt":%d,"rateLimitType":%q,"overageStatus":"rejected","isUsingOverage":false}}`,
		session, id, secret, id, id, at, status, resets.Unix(), limit)
}

func TestClaudeRejectedRequests(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-work-claude")
	hour := time.Date(2026, 9, 20, 15, 0, 0, 0, time.UTC)
	week := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	mustWrite(t, filepath.Join(dir, "sess.jsonl"),
		cl{id: "m1", req: "r1", session: "sess", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(10, 1, 0, 0)}.String(),
		// A newer refusal of another window does not hide this one, which
		// holds after the 5h window resets.
		rejected("x0", "sess", "2026-09-20T10:02:00Z", "rejected", "seven_day_opus", week),
		rejected("x1", "sess", "2026-09-20T10:04:00Z", "rejected", "five_hour", hour),
		rejected("x2", "sess", "2026-09-20T10:05:00Z", "rejected", "five_hour", hour),
		// Only a refusal counts, and only for a window Claude's usage cache names.
		rejected("x3", "sess", "2026-09-20T10:20:00Z", "rejected", "fortnight", week),
		rejected("x4", "sess", "2026-09-20T10:25:00Z", "allowed_warning", "five_hour", hour),
	)
	// A refusal in a sub-agent is its session's.
	mustWrite(t, filepath.Join(dir, "other.jsonl"),
		cl{id: "m2", req: "r2", session: "other", cwd: "/work/claude", at: "2026-09-20T10:00:00Z", usage: use(5, 1, 0, 0)}.String())
	mustWrite(t, filepath.Join(dir, "other", "subagents", "agent-1.jsonl"),
		rejected("x5", "other", "2026-09-20T10:10:00Z", "rejected", "seven_day", week))

	res := mustRead(t, "claude", home, since)
	if res.Malformed != 0 || byID(t, res, "sess").Tokens != (Tokens{Input: 10, Output: 1}) {
		t.Fatalf("malformed %d, sess %+v", res.Malformed, byID(t, res, "sess"))
	}
	for id, want := range map[string][]*Limits{
		"sess": {
			{ObservedAt: time.Date(2026, 9, 20, 10, 2, 0, 0, time.UTC), Windows: []snapshot.Window{{Name: "7d Opus", Percent: 100, ResetsAt: &week, Minutes: 10080}}},
			{ObservedAt: time.Date(2026, 9, 20, 10, 5, 0, 0, time.UTC), Windows: []snapshot.Window{{Name: "5h", Percent: 100, ResetsAt: &hour, Minutes: 300}}},
		},
		"other": {{ObservedAt: time.Date(2026, 9, 20, 10, 10, 0, 0, time.UTC), Windows: []snapshot.Window{{Name: "7d", Percent: 100, ResetsAt: &week, Minutes: 10080}}}},
	} {
		if got := byID(t, res, id).Rejected; !reflect.DeepEqual(got, want) {
			t.Errorf("%s rejected = %+v, want %+v", id, got, want)
		}
	}
	noLeak(t, res)
}

// Each message's input and output go to the hour of its last snapshot, in the
// session that counts it, and the tracker's side calls to the hour of its
// line. A line without a time is spread over those hours in proportion.
func TestClaudeHours(t *testing.T) {
	t.Run("messages, sub-agents, copies, and the tracker", func(t *testing.T) {
		home := t.TempDir()
		dir := filepath.Join(home, "projects", "-work-claude")
		orig := filepath.Join(dir, "orig.jsonl")
		m2 := cl{id: "m2", req: "r2", session: "orig", cwd: "/work/claude", at: "2026-09-20T22:10:00Z", usage: use(20, 2, 0, 0)}.String()
		mustWrite(t, orig,
			m2,
			// Snapshots of one message on both sides of midnight: the last is the one counted.
			cl{uuid: "u1", id: "m1", req: "r1", session: "orig", cwd: "/work/claude", at: "2026-09-20T23:59:59Z", usage: use(10, 1, 5, 7)}.String(),
			cl{uuid: "u2", id: "m1", req: "r1", session: "orig", cwd: "/work/claude", at: "2026-09-21T00:00:01Z", usage: use(10, 9, 5, 7)}.String(),
			cl{id: "m3", req: "r3", session: "orig", cwd: "/work/claude", usage: use(3, 0, 0, 0)}.String(),
			costState("orig", [4]int64{50, 20, 5, 7}),
		)
		sub := filepath.Join(dir, "orig", "subagents", "agent-1.jsonl")
		mustWrite(t, sub, cl{id: "s1", req: "rs1", session: "orig", side: true, at: "2026-09-21T05:30:00Z", usage: use(4, 4, 0, 0)}.String())
		// A resumed copy repeats m2 and adds a message of its own.
		mustWrite(t, filepath.Join(dir, "copy.jsonl"),
			`{"type":"user","cwd":"/work/claude","sessionId":"copy","message":{"role":"user","content":"`+secret+`"},"uuid":"c0","timestamp":"2026-09-21T08:59:00Z"}`,
			m2,
			cl{id: "m4", req: "r4", session: "copy", cwd: "/work/claude", at: "2026-09-21T09:00:00Z", usage: use(40, 4, 0, 0)}.String(),
		)
		ageFile(t, orig, now.Add(-time.Hour))
		ageFile(t, sub, now.Add(-2*time.Hour))

		res := mustRead(t, "claude", home, since)
		got := byID(t, res, "orig")
		if want := (Tokens{Input: 50, Output: 20, CacheWrite: 5, CacheRead: 7}); got.Tokens != want {
			t.Fatalf("orig tokens = %+v, want %+v", got.Tokens, want)
		}
		// The messages with a time spent 22, 19, and 8. What the tracker
		// adds to the messages, 18, was spent by the newest message before
		// its line. The 3 of m3, which has no time, goes along in
		// proportion, with what rounding leaves to the largest hour.
		checkHours(t, got, map[string]int64{
			"2026-09-20T22:10:00Z": 23,
			"2026-09-21T00:00:01Z": 39,
			"2026-09-21T05:30:00Z": 8,
		})
		checkHours(t, byID(t, res, "copy"), map[string]int64{"2026-09-21T09:00:00Z": 44})
	})
}

// Claude splits each message's cache writes by how long they are kept. The
// hour keeps the split; the session's counts do not carry it.
func TestClaudeCacheWriteTTL(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "projects", "-Users-ann-src-app", "a4c2e917.jsonl"),
		cl{id: "m1", req: "r1", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T10:05:00Z", usage: use(10, 20, 4000, 900), ttl: &[2]int64{1000, 3000}}.String(),
		// No split: none of it is kept an hour.
		cl{id: "m2", req: "r2", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T11:05:00Z", usage: use(10, 20, 500, 900)}.String(),
		// More kept an hour than written: no more than was written.
		cl{id: "m3", req: "r3", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T12:05:00Z", usage: use(10, 20, 100, 900), ttl: &[2]int64{0, 700}}.String(),
	)
	got := byID(t, mustRead(t, "claude", home, since), "a4c2e917")
	if want := (Tokens{Input: 30, Output: 60, CacheRead: 2700, CacheWrite: 4600}); got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
	}
	for at, want := range map[string]meter.Use{
		"2026-09-20T10:05:00Z": {Input: 10, Output: 20, CacheRead: 900, CacheWrite: 4000, CacheWrite1h: 3000},
		"2026-09-20T11:05:00Z": {Input: 10, Output: 20, CacheRead: 900, CacheWrite: 500},
		"2026-09-20T12:05:00Z": {Input: 10, Output: 20, CacheRead: 900, CacheWrite: 100, CacheWrite1h: 100},
	} {
		if h := got.Hours[HourOf(mustTime(t, at))]; h != want {
			t.Errorf("hour of %s = %+v, want %+v", at, h, want)
		}
	}
	if len(got.Hours) != 3 {
		t.Fatalf("hours = %+v", got.Hours)
	}
}

// What Claude Code tracked beyond the transcript, for the session and its
// sub-agents, was spent when it wrote its line. It lands in that hour whole,
// and the hours the messages place keep what they had.
func TestClaudeTrackedLandsInItsHour(t *testing.T) {
	msgs := []string{
		cl{id: "m1", req: "r1", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T10:15:00Z", usage: use(100, 1000, 50, 400)}.String(),
		cl{id: "m2", req: "r2", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T11:30:00Z", usage: use(50, 2000, 0, 800)}.String(),
	}
	sub := cl{id: "s1", req: "rs1", session: "a4c2e917", side: true, at: "2026-09-20T11:45:00Z", usage: use(20, 500, 0, 100)}.String()
	// The tracker has 5000 more output than the transcripts, and nothing
	// more of the rest.
	tracked := [4]int64{170, 8500, 50, 1300}
	hours := map[string]meter.Use{
		"2026-09-20T10:15:00Z": {Input: 100, Output: 1000, CacheRead: 400, CacheWrite: 50},
		"2026-09-20T11:30:00Z": {Input: 70, Output: 2500, CacheRead: 900},
	}
	for _, tc := range []struct {
		name, at string
		lines    []string
		more     map[string]meter.Use
	}{
		{name: "a line with its time", at: "2026-09-20T12:40:00Z",
			lines: append(slices.Clone(msgs), costStateAt("a4c2e917", "2026-09-20T12:40:00Z", tracked))},
		// Claude Code writes no time on the line. The newest message before
		// it was the last the tracker saw; one after it is a resumed run's,
		// which the tracker has not seen yet.
		{name: "a line without one", at: "2026-09-20T11:30:00Z",
			lines: append(slices.Clone(msgs), costState("a4c2e917", tracked),
				cl{id: "m3", req: "r3", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T14:20:00Z", usage: use(30, 0, 0, 0)}.String()),
			more: map[string]meter.Use{"2026-09-20T14:20:00Z": {Input: 30}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, "projects", "-Users-ann-src-app")
			mustWrite(t, filepath.Join(dir, "a4c2e917.jsonl"), tc.lines...)
			mustWrite(t, filepath.Join(dir, "a4c2e917", "subagents", "agent-1.jsonl"), sub)

			// Before the hours are fitted to the tokens they already add up.
			files, _ := claudeFiles(home, since, nil)
			for _, s := range rollup(countClaude(files)) {
				if s.Hours.Sum().Tokens() != s.Tokens {
					t.Fatalf("%s hours add up to %+v, tokens %+v", s.ID, s.Hours.Sum(), s.Tokens)
				}
			}
			got := byID(t, mustRead(t, "claude", home, since), "a4c2e917")
			if got.Side != (Tokens{Output: 5000}) {
				t.Fatalf("side = %+v", got.Side)
			}
			want := maps.Clone(hours)
			maps.Copy(want, tc.more)
			want[tc.at] = want[tc.at].Add(meter.Use{Output: 5000})
			if len(got.Hours) != len(want) {
				t.Fatalf("hours = %+v, want %+v", got.Hours, want)
			}
			for at, w := range want {
				if h := got.Hours[HourOf(mustTime(t, at))]; h != w {
					t.Errorf("hour of %s = %+v, want %+v", at, h, w)
				}
			}
		})
	}
}

// A reply whose stream ended before its final count keeps a null stop reason
// and next to no output. The session counts those it counts the messages of,
// its sub-agents' among them.
func TestClaudeNullReplies(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-ann-src-app")
	null := func(id string, out int64) string {
		return cl{id: id, req: "r" + id, session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T10:00:00Z", usage: use(1, out, 0, 0), stop: "null"}.String()
	}
	mustWrite(t, filepath.Join(dir, "a4c2e917.jsonl"),
		null("m1", 5),
		// Enough output is a reply, however it ended.
		null("m2", 20),
		cl{id: "m3", req: "rm3", session: "a4c2e917", at: "2026-09-20T10:01:00Z", usage: use(1, 3, 0, 0), stop: `"tool_use"`}.String(),
		// No stop reason at all is not a null one.
		cl{id: "m4", req: "rm4", session: "a4c2e917", at: "2026-09-20T10:02:00Z", usage: use(1, 0, 0, 0)}.String(),
		// The last snapshot of a message decides.
		null("m5", 1),
		cl{id: "m5", req: "rm5", session: "a4c2e917", at: "2026-09-20T10:03:00Z", usage: use(1, 300, 0, 0), stop: `"end_turn"`}.String(),
		null("m6", 1),
		null("m6", 2),
	)
	mustWrite(t, filepath.Join(dir, "a4c2e917", "subagents", "agent-1.jsonl"),
		cl{id: "s1", req: "rs1", session: "a4c2e917", side: true, at: "2026-09-20T10:04:00Z", usage: use(1, 0, 0, 0), stop: "null"}.String())
	// A resumed copy repeats m1, which counts in the session that has it first.
	mustWrite(t, filepath.Join(dir, "b7d1e3a0.jsonl"),
		null("m1", 5),
		cl{id: "c1", req: "rc1", session: "b7d1e3a0", cwd: "/Users/ann/src/app", at: "2026-09-21T09:00:00Z", usage: use(1, 40, 0, 0), stop: `"end_turn"`}.String())

	res := mustRead(t, "claude", home, since)
	if got := byID(t, res, "a4c2e917").Nulls; got != 3 {
		t.Errorf("session nulls = %d, want 3", got)
	}
	if got := byID(t, res, "b7d1e3a0").Nulls; got != 0 {
		t.Errorf("copy nulls = %d, want 0", got)
	}
}

// A session records the Claude Code version of its newest message, whatever
// order the lines are in, and a session whose messages record none has none.
func TestClaudeVersion(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-ann-src-app")
	msg := func(id, at, version string) string {
		return cl{id: id, req: "r" + id, session: "a4c2e917", cwd: "/Users/ann/src/app", at: at, usage: use(1, 1, 0, 0), version: version}.String()
	}
	mustWrite(t, filepath.Join(dir, "a4c2e917.jsonl"),
		msg("m1", "2026-09-20T10:00:00Z", "2.1.200"),
		msg("m2", "2026-09-20T12:00:00Z", "2.1.210"),
		msg("m3", "2026-09-20T11:00:00Z", "2.1.200"),
	)
	mustWrite(t, filepath.Join(dir, "a4c2e917", "subagents", "agent-1.jsonl"),
		cl{id: "s1", req: "rs1", session: "a4c2e917", side: true, at: "2026-09-20T11:30:00Z", usage: use(1, 1, 0, 0), version: "2.1.200"}.String())
	mustWrite(t, filepath.Join(dir, "c9e2f4b1.jsonl"),
		cl{id: "x1", req: "rx1", session: "c9e2f4b1", cwd: "/Users/ann/src/app", at: "2026-09-20T13:00:00Z", usage: use(1, 1, 0, 0)}.String())

	res := mustRead(t, "claude", home, since)
	if got := byID(t, res, "a4c2e917").Version; got != "2.1.210" {
		t.Errorf("version = %q, want 2.1.210", got)
	}
	if got := byID(t, res, "c9e2f4b1").Version; got != "" {
		t.Errorf("version = %q, want none", got)
	}
}

// A session was last active at its newest message, not when its file was
// last written: Claude Code rewrites a file it resumes or compacts.
func TestClaudeUpdatedIsLastMessage(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "projects", "-Users-ann-src-app", "a4c2e917.jsonl")
	mustWrite(t, path,
		cl{id: "m1", req: "r1", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T10:00:00Z", usage: use(1, 1, 0, 0)}.String(),
		cl{id: "m2", req: "r2", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T11:20:00Z", usage: use(1, 1, 0, 0)}.String(),
	)
	ageFile(t, path, now.Add(-time.Minute))
	if got := byID(t, mustRead(t, "claude", home, since), "a4c2e917").Updated; !got.Equal(mustTime(t, "2026-09-20T11:20:00Z")) {
		t.Fatalf("updated = %v", got)
	}
}

// claudeCorpus is a home whose logs hold every kind of line the parse keeps
// something of, and the lines it skips.
type claudeCorpus struct {
	home, main, sub, copy string
}

// Line makers of the corpus: a message of session a4c2e917 with Claude Code's
// version, a user line, and a line that only names a working directory.
func corpusMsg(id, at string, usage *[4]int64, stop string) string {
	return cl{id: id, req: "r" + id, session: "a4c2e917", cwd: "/Users/ann/src/app", at: at, usage: usage, stop: stop, version: "2.1.210"}.String()
}

func corpusUser(uuid, at, extra string) string {
	return fmt.Sprintf(`{"type":"user","cwd":"/Users/ann/src/app","sessionId":"a4c2e917","version":"2.1.210","message":{"role":"user","content":[{"type":"tool_result","content":"usage: app [-h] and \"usage\" in quotes, %s"}]},"uuid":%q,"timestamp":%q%s}`, secret, uuid, at, extra)
}

func corpusCwd(cwd, at string) string {
	return fmt.Sprintf(`{"type":"system","subtype":"local_command","cwd":%q,"sessionId":"a4c2e917","content":%q,"timestamp":%q}`, cwd, secret, at)
}

func newClaudeCorpus(t *testing.T) claudeCorpus {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "projects", "-Users-ann-src-app")
	c := claudeCorpus{
		home: home,
		main: filepath.Join(dir, "a4c2e917.jsonl"),
		sub:  filepath.Join(dir, "a4c2e917", "subagents", "agent-1.jsonl"),
		copy: filepath.Join(dir, "b7d1e3a0.jsonl"),
	}
	week := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	mustWrite(t, c.main,
		`{"type":"file-history-snapshot","messageId":"u0","snapshot":{"trackedFileBackups":{}}}`,
		corpusUser("u0", "2026-09-20T10:00:00Z", ""),
		cl{uuid: "u1", id: "m1", req: "r1", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T10:00:05Z", usage: use(10, 2, 4000, 900), ttl: &[2]int64{1000, 3000}, version: "2.1.200"}.String(),
		cl{uuid: "u2", id: "m1", req: "r1", session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T10:00:09Z", usage: use(10, 90, 4000, 900), ttl: &[2]int64{1000, 3000}, stop: `"tool_use"`, version: "2.1.200"}.String(),
		// A tool result that names usage, and one that carries a usage
		// object of its own: neither is a message.
		corpusUser("u3", "2026-09-20T10:01:00Z", ""),
		corpusUser("u4", "2026-09-20T10:01:30Z", `,"toolUseResult":{"usage":{"input_tokens":999,"output_tokens":999}}`),
		corpusCwd("/Users/ann/src/app/web", "2026-09-20T10:02:00Z"),
		corpusMsg("m2", "2026-09-20T11:05:00Z", use(3, 4, 0, 50), "null"),
		rejected("x1", "a4c2e917", "2026-09-20T11:10:00Z", "rejected", "seven_day", week),
		cl{session: "a4c2e917", cwd: "/Users/ann/src/app", at: "2026-09-20T11:20:00Z", usage: use(1, 1, 0, 0), version: "2.1.210"}.String(),
		`{"type":"assistant","usage"`,
		costStateAt("a4c2e917", "2026-09-20T11:30:00Z", [4]int64{60, 9000, 4000, 1900}),
	)
	mustWrite(t, c.sub,
		cl{id: "s1", req: "rs1", session: "a4c2e917", side: true, cwd: "/Users/ann/src/app", at: "2026-09-20T10:30:00Z", usage: use(5, 50, 0, 100), version: "2.1.200"}.String())
	// A resumed copy repeats m1 and adds a message of its own.
	mustWrite(t, c.copy,
		cl{uuid: "c0", id: "m1", req: "r1", session: "b7d1e3a0", cwd: "/Users/ann/src/app", at: "2026-09-20T10:00:09Z", usage: use(10, 90, 4000, 900), stop: `"tool_use"`}.String(),
		cl{id: "c1", req: "rc1", session: "b7d1e3a0", cwd: "/Users/ann/src/app", at: "2026-09-21T09:00:00Z", usage: use(7, 70, 0, 10), stop: `"end_turn"`}.String(),
	)
	ageFile(t, c.main, now.Add(-2*time.Hour))
	ageFile(t, c.sub, now.Add(-3*time.Hour))
	ageFile(t, c.copy, now.Add(-time.Hour))
	return c
}

// appendLines adds lines to the end of path, and returns how many bytes.
func appendLines(t *testing.T, path string, lines ...string) int64 {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return int64(len(body))
}

// Past the header, the parse decodes only the lines that can hold usage or a
// cost-state, and keeps what decoding every line with a working directory
// kept.
func TestClaudeLineFilter(t *testing.T) {
	c := newClaudeCorpus(t)
	journal := filepath.Join(c.home, "projects", "-Users-ann-src-app", "a4c2e917", "subagents", "journal.jsonl")
	mustWrite(t, journal, `{"type":"workflow","cwd":"/Users/ann/src/app"}`)
	for _, tc := range []struct{ path, id, parent string }{
		{c.main, "a4c2e917", ""},
		{c.sub, "a4c2e917/agent-1.jsonl", "a4c2e917"},
		{c.copy, "b7d1e3a0", ""},
		{journal, "a4c2e917/journal.jsonl", "a4c2e917"},
	} {
		parse := func(visit func(*claudeParse, []byte)) *claudeFile {
			f := &claudeFile{path: tc.path, sess: Session{ID: tc.id, ParentID: tc.parent}}
			p := newClaudeParse(f)
			if _, err := ForEachLine(tc.path, maxLineBytes, func(line []byte) { visit(p, line) }); err != nil {
				t.Fatal(err)
			}
			return f.done()
		}
		got := parse((*claudeParse).line)
		want := parse(func(p *claudeParse, line []byte) {
			for _, key := range []string{`"usage"`, `"cwd"`, `"cost-state"`} {
				if bytes.Contains(line, []byte(key)) {
					p.decode(line)
					return
				}
			}
		})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: filtered parse =\n%+v\nwant\n%+v", filepath.Base(tc.path), got, want)
		}
	}

	// Once the header is known, a line that only names a working directory
	// is not decoded at all.
	appendLines(t, c.main, `{"type":"system","cwd":"/Users/ann/src/app"`)
	if res := mustRead(t, "claude", c.home, since); res.Malformed != 1 {
		t.Fatalf("malformed = %d, want the corpus's 1", res.Malformed)
	}
}
