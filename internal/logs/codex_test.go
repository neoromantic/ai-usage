package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Thread ids look like the UUIDs Codex puts at the end of rollout file names.
const (
	rootID  = "019a0000-0000-7000-8000-000000000001"
	kidA    = "019a0000-0000-7000-8000-00000000000a"
	kidB    = "019a0000-0000-7000-8000-00000000000b"
	guardID = "019a0000-0000-7000-8000-00000000000c"
	forkID  = "019a0000-0000-7000-8000-00000000000f"
	lostID  = "019a0000-0000-7000-8000-0000000000ff"
)

// u is [input, cached, output] as Codex reports them: input includes cached.
type u [3]int64

func (x u) json() string {
	return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}`, x[0], x[1], x[2], x[0]+x[2])
}

// cxMeta is a session_meta line. extra holds more payload members, each
// starting with a comma.
type cxMeta struct {
	at, id, session, cwd, source, extra string
}

func (m cxMeta) String() string {
	if m.session == "" {
		m.session = m.id
	}
	if m.source == "" {
		m.source = `"cli"`
	}
	return fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"session_id":%q,"id":%q,"timestamp":%q,"cwd":%q,"originator":"codex_cli_rs","cli_version":"0.154.0","source":%s,"model_provider":"openai","base_instructions":{"text":%q}%s}}`,
		m.at, m.session, m.id, m.at, m.cwd, m.source, secret, m.extra)
}

func spawnedBy(parent string) string {
	return fmt.Sprintf(`{"subagent":{"thread_spawn":{"parent_thread_id":%q,"depth":1}}}`, parent)
}

// cxCount is a token_count line with the cumulative total and the last request.
func cxCount(at string, total, last u) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s,"last_token_usage":%s,"model_context_window":258400},"rate_limits":null}}`,
		at, total.json(), last.json())
}

// cxTotal is an older token_count line that has no last usage.
func cxTotal(at string, total u) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s}}}`, at, total.json())
}

// cxLimits is a token_count line that carries only rate limits.
func cxLimits(at, limits string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":%s}}`, at, limits)
}

// cxChat is a transcript line that mentions the marker words the line filter
// looks for. It must be decoded, ignored, and not counted as malformed.
func cxChat(at string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"why does token_count follow session_meta? %s"}]}}`, at, secret)
}

// cxRecord is a token_usage_record line: one response's usage in thread.
// thread_token_usage is not a running total of anything the reader counts.
func cxRecord(at, thread, response string, usage u) string {
	return fmt.Sprintf(`{"timestamp":%q,"ordinal":7,"type":"token_usage_record","payload":{"thread_id":%q,"turn_id":"turn","session_id":%q,"root_turn_id":"turn","response_id":%q,"usage":%s,"turn_token_usage":%s,"thread_token_usage":%s}}`,
		at, thread, thread, response, usage.json(), usage.json(), u{999999, 0, 9999}.json())
}

// cxCompacted is the line Codex writes when it compacts a thread's history.
func cxCompacted(at string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}],"window_number":1}}`, at, secret)
}

func rollout(home, leaf, day, id string) string {
	return filepath.Join(home, leaf, "2026", "09", day, "rollout-2026-09-"+day+"T10-00-00-"+id+".jsonl")
}

// rootLines is a parent thread with two requests, E1 and E2.
var (
	e1        = [2]u{{1000, 400, 50}, {1000, 400, 50}}
	e2        = [2]u{{2500, 1900, 80}, {1500, 1500, 30}}
	rootOwn   = Tokens{Input: 600, CacheRead: 1900, Output: 80}
	rootLines = []string{
		cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
		cxChat("2026-09-20T10:00:00.500Z"),
		cxLimits("2026-09-20T10:00:01.000Z", `{"limit_id":"codex","primary":{"used_percent":12.0,"window_minutes":300,"resets_at":1790000000},"secondary":null,"plan_type":"plus"}`),
		cxCount("2026-09-20T10:00:05.000Z", e1[0], e1[1]),
		// The same info again after a rate limit update is not a new request.
		cxCount("2026-09-20T10:00:06.000Z", e1[0], e1[1]),
		cxCount("2026-09-20T10:00:30.000Z", e2[0], e2[1]),
	}
)

func TestCodexCountsEachRequestOnceAndMovesCachedOut(t *testing.T) {
	home := t.TempDir()
	path := rollout(home, "sessions", "20", rootID)
	mustWrite(t, path, rootLines...)
	ageFile(t, path, now.Add(-time.Hour))

	res := mustRead(t, "codex", home, since)
	if res.Malformed != 0 || res.Unreadable != 0 {
		t.Fatalf("malformed %d unreadable %d", res.Malformed, res.Unreadable)
	}
	got := byID(t, res, rootID)
	if got.Tokens != rootOwn {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, rootOwn)
	}
	if got.Project != "/work/app" || got.ParentID != "" || got.Account != "" {
		t.Fatalf("session = %+v", got)
	}
	if !got.Updated.Equal(now.Add(-time.Hour)) || got.Updated.Location() != time.UTC {
		t.Fatalf("updated = %v", got.Updated)
	}
	noLeak(t, res)
}

// Codex counts cached input inside input. A log that reports more cached than
// input does not make input negative.
func TestCodexUsageTokens(t *testing.T) {
	got := codexUsage{Input: 100, Cached: 150, Output: 1}.tokens()
	if got != (Tokens{Input: 0, CacheRead: 150, Output: 1}) {
		t.Errorf("tokens = %+v", got)
	}
}

// Sub-agent files name the root thread in session_id and copy the parent's
// session_meta after their own. Each is its own thread and rolls into its parent.
func TestCodexSubAgentsRollIntoRoot(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)
	mustWrite(t, rollout(home, "sessions", "20", kidA),
		cxMeta{at: "2026-09-20T10:01:00.000Z", id: kidA, session: rootID, cwd: "/work/app", source: spawnedBy(rootID), extra: `,"parent_thread_id":"` + rootID + `","thread_source":"subagent"`}.String(),
		cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
		cxCount("2026-09-20T10:01:10.000Z", u{300, 100, 20}, u{300, 100, 20}),
	)
	mustWrite(t, rollout(home, "sessions", "20", kidB),
		cxMeta{at: "2026-09-20T10:02:00.000Z", id: kidB, session: rootID, cwd: "/work/app/sub", source: spawnedBy(kidA)}.String(),
		cxCount("2026-09-20T10:02:10.000Z", u{50, 0, 5}, u{50, 0, 5}),
	)
	mustWrite(t, rollout(home, "sessions", "20", guardID),
		cxMeta{at: "2026-09-20T10:03:00.000Z", id: guardID, session: rootID, cwd: "/work/app", source: `{"subagent":{"other":"guardian"}}`, extra: `,"parent_thread_id":"` + rootID + `"`}.String(),
		cxCount("2026-09-20T10:03:10.000Z", u{70, 20, 7}, u{70, 20, 7}),
	)
	for _, id := range []string{rootID, kidA, guardID} {
		ageFile(t, rollout(home, "sessions", "20", id), now.Add(-time.Hour))
	}
	newest := now.Add(-10 * time.Minute)
	ageFile(t, rollout(home, "sessions", "20", kidB), newest)

	res := mustRead(t, "codex", home, since)
	if got := strings.Join(ids(res), ","); got != rootID {
		t.Fatalf("sessions = %s", got)
	}
	want := rootOwn.Add(Tokens{Input: 200, CacheRead: 100, Output: 20}).
		Add(Tokens{Input: 50, Output: 5}).
		Add(Tokens{Input: 50, CacheRead: 20, Output: 7})
	got := byID(t, res, rootID)
	if got.Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, want)
	}
	if got.Project != "/work/app" || !got.Updated.Equal(newest) {
		t.Fatalf("session = %+v", got)
	}
}

// A fork replays its parent's history, token_count lines included, and then
// continues the parent's cumulative total.
func TestCodexForkCountsOnlyItsOwnRequests(t *testing.T) {
	replay := func(at string) []string {
		return []string{cxCount(at, e1[0], e1[1]), cxCount(at, e2[0], e2[1])}
	}
	// A user's fork, which stays its own session, is in TestReadHomesCountsAcrossHomesOnce.
	t.Run("forked sub-agent rolls in without its replay", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)
		lines := []string{cxMeta{at: "2026-09-20T11:00:00.000Z", id: kidA, session: rootID, cwd: "/work/app", source: spawnedBy(rootID), extra: `,"forked_from_id":"` + rootID + `"`}.String()}
		lines = append(lines, replay("2026-09-20T11:00:00.000Z")...)
		lines = append(lines, cxCount("2026-09-20T11:00:09.000Z", u{3000, 2300, 95}, u{500, 400, 15}))
		mustWrite(t, rollout(home, "sessions", "20", kidA), lines...)

		res := mustRead(t, "codex", home, since)
		want := rootOwn.Add(Tokens{Input: 100, CacheRead: 400, Output: 15})
		if len(res.Sessions) != 1 || res.Sessions[0].Tokens != want {
			t.Fatalf("sessions = %+v, want one with %+v", res.Sessions, want)
		}
	})
}

// A fork whose parent file is gone starts from a seeded total with no last
// usage of its own. The seed is the parent's usage, not the fork's.
func TestCodexSeededForkWithoutParent(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, rollout(home, "sessions", "21", forkID),
		cxMeta{at: "2026-09-21T09:00:00.000Z", id: forkID, session: lostID, cwd: "/work/app", source: spawnedBy(lostID), extra: `,"forked_from_id":"` + lostID + `"`}.String(),
		cxCount("2026-09-21T09:00:00.100Z", u{9000, 8000, 300}, u{0, 0, 0}),
		cxCount("2026-09-21T09:00:12.000Z", u{9500, 8400, 320}, u{500, 400, 20}),
	)
	res := mustRead(t, "codex", home, since)
	got := byID(t, res, forkID)
	if got.Tokens != (Tokens{Input: 100, CacheRead: 400, Output: 20}) || got.ParentID != "" {
		t.Fatalf("fork = %+v", got)
	}
}

// Two forks of a parent that is no longer on disk replay the same history.
// It counts once, with the fork that started first.
func TestCodexSiblingForksShareReplayOnce(t *testing.T) {
	home := t.TempDir()
	sibling := func(id, at string, own [2]u) []string {
		return []string{
			cxMeta{at: at, id: id, session: lostID, cwd: "/work/app", source: spawnedBy(lostID), extra: `,"forked_from_id":"` + lostID + `"`}.String(),
			cxCount(at, u{1000, 0, 10}, u{1000, 0, 10}),
			cxCount(at, u{1500, 0, 15}, u{500, 0, 5}),
			cxCount(at, own[0], own[1]),
		}
	}
	// B sorts first on disk but started later.
	mustWrite(t, rollout(home, "sessions", "20", kidB), sibling(kidB, "2026-09-20T11:00:00.000Z", [2]u{{1700, 0, 20}, {200, 0, 5}})...)
	mustWrite(t, rollout(home, "sessions", "21", kidA), sibling(kidA, "2026-09-20T10:00:00.000Z", [2]u{{1600, 0, 17}, {100, 0, 2}})...)

	res := mustRead(t, "codex", home, since)
	if got := byID(t, res, kidA).Tokens; got != (Tokens{Input: 1600, Output: 17}) {
		t.Fatalf("first fork = %+v", got)
	}
	if got := byID(t, res, kidB).Tokens; got != (Tokens{Input: 200, Output: 5}) {
		t.Fatalf("second fork = %+v", got)
	}
}

// A parent older than the window is read only to recognise what a fresh fork
// replays. It reports no session and no quota.
func TestCodexStaleParentOnlyMatchesReplay(t *testing.T) {
	home := t.TempDir()
	parent := rollout(home, "archived_sessions", "01", rootID)
	mustWrite(t, parent, append(append([]string{}, rootLines...),
		cxLimits("2026-09-20T10:00:40.000Z", `{"limit_id":"codex","primary":{"used_percent":99.0,"window_minutes":300,"resets_at":1790000000},"plan_type":"pro"}`))...)
	ageFile(t, parent, since.Add(-time.Hour))
	unrelated := rollout(home, "sessions", "02", kidB)
	mustWrite(t, unrelated, cxMeta{at: "2026-06-01T10:00:00.000Z", id: kidB, cwd: "/work/old"}.String(), cxCount("2026-06-01T10:00:05.000Z", u{5, 0, 1}, u{5, 0, 1}))
	ageFile(t, unrelated, since.Add(-time.Hour))

	lines := []string{cxMeta{at: "2026-09-21T09:00:00.000Z", id: forkID, cwd: "/work/app", source: `"vscode"`, extra: `,"forked_from_id":"` + rootID + `"`}.String()}
	lines = append(lines, cxCount("2026-09-21T09:00:00.000Z", e1[0], e1[1]), cxCount("2026-09-21T09:00:00.000Z", e2[0], e2[1]))
	lines = append(lines, cxCount("2026-09-21T09:00:20.000Z", u{2600, 1900, 90}, u{100, 0, 10}))
	mustWrite(t, rollout(home, "sessions", "21", forkID), lines...)

	res := mustRead(t, "codex", home, since)
	if got := strings.Join(ids(res), ","); got != forkID {
		t.Fatalf("sessions = %s", got)
	}
	if got := byID(t, res, forkID).Tokens; got != (Tokens{Input: 100, Output: 10}) {
		t.Fatalf("fork = %+v", got)
	}
	if res.Limits != nil {
		t.Fatalf("stale file gave limits %+v", res.Limits)
	}
}

// Some rollouts interleave two cumulative counters. Counting by last usage
// keeps each request once; treating every drop as a reset counted both
// counters again and again.
func TestCodexInterleavedCountersCountByLastUsage(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, rollout(home, "sessions", "20", rootID),
		cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
		cxCount("2026-09-20T10:00:01.000Z", u{100, 0, 10}, u{100, 0, 10}),
		cxCount("2026-09-20T10:00:02.000Z", u{50, 0, 5}, u{50, 0, 5}),
		cxCount("2026-09-20T10:00:03.000Z", u{220, 0, 22}, u{120, 0, 12}),
		cxCount("2026-09-20T10:00:04.000Z", u{90, 0, 9}, u{40, 0, 4}),
		// A repeat of an earlier line, not next to it, is still the same request.
		cxCount("2026-09-20T10:00:05.000Z", u{220, 0, 22}, u{120, 0, 12}),
	)
	res := mustRead(t, "codex", home, since)
	if got := byID(t, res, rootID).Tokens; got != (Tokens{Input: 310, Output: 31}) {
		t.Fatalf("tokens = %+v", got)
	}
}

// Older Codex wrote only the cumulative total. A lower input total starts a
// new epoch so a counter reset keeps the earlier usage.
func TestCodexLegacyTotalsWithoutLastUsage(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "sessions", "2025", "01", "01", "rollout-parent.jsonl"),
		`{"type":"session_meta","payload":{"cwd":"/work/app","session_id":"parent","id":"parent","source":"cli"}}`,
		`{"type":"session_meta"`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":1,"cache_write_input_tokens":0,"output_tokens":2}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":40,"cached_input_tokens":4,"cache_write_input_tokens":5,"output_tokens":6}}}}`,
	)
	mustWrite(t, filepath.Join(home, "sessions", "2025", "01", "01", "rollout-reset.jsonl"),
		`{"type":"session_meta","payload":{"cwd":"/work/reset","id":"reset","source":"cli"}}`,
		cxTotal("2025-01-01T00:00:01Z", u{40, 0, 1}),
		cxTotal("2025-01-01T00:00:02Z", u{5, 0, 2}),
	)
	// No session_meta at all: the file name is the id.
	mustWrite(t, filepath.Join(home, "sessions", "2025", "01", "01", "rollout-bare.jsonl"),
		cxTotal("2025-01-01T00:00:01Z", u{3, 0, 1}),
	)
	res := mustRead(t, "codex", home, time.Time{})
	if res.Malformed != 1 {
		t.Fatalf("malformed = %d", res.Malformed)
	}
	if got := byID(t, res, "parent").Tokens; got != (Tokens{Input: 36, CacheRead: 4, Output: 6, CacheWrite: 5}) {
		t.Fatalf("parent = %+v", got)
	}
	if got := byID(t, res, "reset").Tokens; got != (Tokens{Input: 45, Output: 3}) {
		t.Fatalf("reset = %+v", got)
	}
	if got := byID(t, res, "rollout-bare"); got.Tokens != (Tokens{Input: 3, Output: 1}) || got.Project != UnknownProject {
		t.Fatalf("bare = %+v", got)
	}
}

func TestCodexOneThreadInTwoFiles(t *testing.T) {
	t.Run("archived copy counts once", func(t *testing.T) {
		home := t.TempDir()
		live := rollout(home, "sessions", "20", rootID)
		archived := rollout(home, "archived_sessions", "20", rootID)
		mustWrite(t, live, rootLines...)
		mustWrite(t, archived, rootLines...)
		ageFile(t, live, now.Add(-2*time.Hour))
		ageFile(t, archived, now.Add(-time.Hour))
		res := mustRead(t, "codex", home, since)
		got := byID(t, res, rootID)
		if len(res.Sessions) != 1 || got.Tokens != rootOwn || !got.Updated.Equal(now.Add(-time.Hour)) {
			t.Fatalf("sessions = %+v", res.Sessions)
		}
	})
	t.Run("pages add up", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)
		// A later page continues the same thread's cumulative total.
		mustWrite(t, filepath.Join(home, "sessions", "2026", "09", "21", "rollout-2026-09-21T10-00-00-"+rootID+".jsonl"),
			cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app", extra: `,"history_base":{"thread_id":"` + rootID + `","end_ordinal_exclusive":6,"end_byte_offset":4096}`}.String(),
			cxCount("2026-09-21T10:00:05.000Z", u{3000, 2300, 90}, u{500, 400, 10}),
		)
		res := mustRead(t, "codex", home, since)
		want := rootOwn.Add(Tokens{Input: 100, CacheRead: 400, Output: 10})
		if len(res.Sessions) != 1 || byID(t, res, rootID).Tokens != want {
			t.Fatalf("sessions = %+v, want %+v", res.Sessions, want)
		}
	})
}

func TestCodexMtimeWindow(t *testing.T) {
	home := t.TempDir()
	edge := rollout(home, "sessions", "20", kidA)
	old := rollout(home, "sessions", "19", kidB)
	mustWrite(t, edge, cxMeta{at: "2026-06-24T12:00:00.000Z", id: kidA, cwd: "/work/edge"}.String(), cxCount("2026-06-24T12:00:01.000Z", u{1, 0, 0}, u{1, 0, 0}))
	mustWrite(t, old, cxMeta{at: "2026-06-24T11:00:00.000Z", id: kidB, cwd: "/work/old"}.String(), cxCount("2026-06-24T11:00:01.000Z", u{100, 0, 100}, u{100, 0, 100}))
	ageFile(t, edge, since)
	ageFile(t, old, since.Add(-time.Second))

	res := mustRead(t, "codex", home, since)
	if got := strings.Join(ids(res), ","); got != kidA {
		t.Fatalf("sessions = %s", got)
	}
	if got := byID(t, res, kidA); !got.Updated.Equal(since) {
		t.Fatalf("updated = %v", got.Updated)
	}
}

func TestCodexSkipsCredentialFiles(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "auth.json"), "SECRET-NOT-JSON")
	deny(t, filepath.Join(home, "auth.json"))
	mustWrite(t, filepath.Join(home, "sessions", "auth.json"), "{")
	deny(t, filepath.Join(home, "sessions", "auth.json"))
	// Named like a credential: never opened, whatever it holds.
	mustWrite(t, filepath.Join(home, "sessions", "credentials.jsonl"),
		cxMeta{at: "2026-09-20T10:00:00.000Z", id: forkID, cwd: "/work/secret"}.String(),
		cxCount("2026-09-20T10:00:01.000Z", u{999, 0, 999}, u{999, 0, 999}))
	mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)

	res := mustRead(t, "codex", home, since)
	if res.Unreadable != 0 || res.Malformed != 0 {
		t.Fatalf("unreadable %d malformed %d", res.Unreadable, res.Malformed)
	}
	if got := strings.Join(ids(res), ","); got != rootID {
		t.Fatalf("sessions = %s", got)
	}
}

// Older session_meta lines leave out the thread id, the payload time, or the
// spawn source. The file still names its thread and parent.
func TestCodexMetaFallbacks(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)
	// No payload id: session_id is the thread. No payload time: the line's.
	mustWrite(t, rollout(home, "sessions", "20", lostID),
		`{"timestamp":"2026-09-20T12:00:00Z","type":"session_meta","payload":{"session_id":"`+lostID+`","cwd":"/work/old"}}`,
		cxCount("2026-09-20T12:00:05Z", u{9, 0, 1}, u{9, 0, 1}))
	// No spawn source or parent_thread_id: a session_id that differs is the parent.
	mustWrite(t, rollout(home, "sessions", "20", kidB),
		`{"timestamp":"2026-09-20T10:05:00Z","type":"session_meta","payload":{"id":"`+kidB+`","session_id":"`+rootID+`","cwd":"/work/app","source":"exec"}}`,
		cxCount("2026-09-20T10:05:05Z", u{5, 0, 1}, u{5, 0, 1}))

	res := mustRead(t, "codex", home, since)
	if got := strings.Join(ids(res), ","); got != rootID+","+lostID {
		t.Fatalf("sessions = %s", got)
	}
	if got := byID(t, res, rootID).Tokens; got != rootOwn.Add(Tokens{Input: 5, Output: 1}) {
		t.Fatalf("root = %+v", got)
	}
	if got := byID(t, res, lostID); got.Tokens != (Tokens{Input: 9, Output: 1}) || got.Project != "/work/old" {
		t.Fatalf("lost = %+v", got)
	}
}

// Each response has a usage record, and the token_count after it repeats it.
// A compaction's does not: token_count then has no last usage and the same
// total. Records that no token_count repeats count once per response.
func TestCodexCountsResponsesTokenCountLeavesOut(t *testing.T) {
	thread := []string{
		cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
		cxRecord("2026-09-20T10:00:04.000Z", rootID, "resp-1", e1[1]),
		cxChat("2026-09-20T10:00:04.500Z"),
		cxCount("2026-09-20T10:00:05.000Z", e1[0], e1[1]),
		cxRecord("2026-09-20T10:00:29.000Z", rootID, "resp-2", e2[1]),
		cxLimits("2026-09-20T10:00:29.500Z", `{"limit_id":"codex","primary":{"used_percent":12.0,"window_minutes":300,"resets_at":1790000000}}`),
		cxCount("2026-09-20T10:00:30.000Z", e2[0], e2[1]),
		cxRecord("2026-09-20T10:01:00.000Z", rootID, "resp-compact", u{2000, 1800, 40}),
		cxCompacted("2026-09-20T10:01:01.000Z"),
		cxCount("2026-09-20T10:01:02.000Z", e2[0], u{0, 0, 0}),
		// A response whose token_count never came, then one whose did.
		cxRecord("2026-09-20T10:02:00.000Z", rootID, "resp-3", u{300, 0, 3}),
		cxRecord("2026-09-20T10:03:00.000Z", rootID, "resp-4", u{400, 100, 4}),
		cxCount("2026-09-20T10:03:01.000Z", u{2900, 2000, 84}, u{400, 100, 4}),
		// Another thread's record, replayed here, is that thread's.
		cxRecord("2026-09-20T10:04:00.000Z", lostID, "resp-other", u{7000, 0, 70}),
		// The newest response's token_count is not written yet.
		cxRecord("2026-09-20T10:05:00.000Z", rootID, "resp-5", u{50, 0, 5}),
	}
	want := rootOwn.Add(Tokens{Input: 200, CacheRead: 1800, Output: 40}).
		Add(Tokens{Input: 300, Output: 3}).
		Add(Tokens{Input: 300, CacheRead: 100, Output: 4}).
		Add(Tokens{Input: 50, Output: 5})

	t.Run("one file", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID), thread...)
		res := mustRead(t, "codex", home, since)
		if res.Malformed != 0 || res.Unreadable != 0 {
			t.Fatalf("malformed %d unreadable %d", res.Malformed, res.Unreadable)
		}
		if got := byID(t, res, rootID).Tokens; got != want {
			t.Fatalf("tokens = %+v, want %+v", got, want)
		}
		noLeak(t, res)
	})
	t.Run("archived copy counts once", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID), thread...)
		mustWrite(t, rollout(home, "archived_sessions", "20", rootID), thread...)
		res := mustRead(t, "codex", home, since)
		if len(res.Sessions) != 1 || byID(t, res, rootID).Tokens != want {
			t.Fatalf("sessions = %+v, want %+v", res.Sessions, want)
		}
	})
	t.Run("a total without last usage already holds the record", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID),
			cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
			cxRecord("2026-09-20T10:00:04.000Z", rootID, "resp-1", u{100, 40, 10}),
			cxTotal("2026-09-20T10:00:05.000Z", u{100, 40, 10}),
		)
		res := mustRead(t, "codex", home, since)
		if got := byID(t, res, rootID).Tokens; got != (Tokens{Input: 60, CacheRead: 40, Output: 10}) {
			t.Fatalf("tokens = %+v", got)
		}
	})
	t.Run("fork replay counts with the parent", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID), thread...)
		lines := []string{
			cxMeta{at: "2026-09-21T09:00:00.000Z", id: forkID, cwd: "/work/app", source: `"vscode"`, extra: `,"forked_from_id":"` + rootID + `"`}.String(),
			cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
		}
		lines = append(lines, thread[1:]...)
		lines = append(lines,
			cxRecord("2026-09-21T09:00:10.000Z", forkID, "resp-fork", u{3000, 2500, 60}),
			cxCompacted("2026-09-21T09:00:11.000Z"),
			cxCount("2026-09-21T09:00:12.000Z", u{2900, 2000, 84}, u{0, 0, 0}),
		)
		mustWrite(t, rollout(home, "sessions", "21", forkID), lines...)
		res := mustRead(t, "codex", home, since)
		if got := byID(t, res, rootID).Tokens; got != want {
			t.Fatalf("parent = %+v, want %+v", got, want)
		}
		if got := byID(t, res, forkID).Tokens; got != (Tokens{Input: 500, CacheRead: 2500, Output: 60}) {
			t.Fatalf("fork = %+v", got)
		}
	})
}

// Orca links each rollout into every account's home, and ~/.codex. A linked
// file counts once, from the first home, and names every home it is in.
// Its rate limits are the session's, not any home's fallback reading.
func TestCodexHardLinkedHomesReadOnce(t *testing.T) {
	root := t.TempDir()
	def, acctA, acctB := filepath.Join(root, ".codex"), filepath.Join(root, "orca-a"), filepath.Join(root, "orca-b")
	weekly := `{"limit_id":"codex","primary":{"used_percent":40.0,"window_minutes":10080,"resets_at":1790500000},"secondary":null,"plan_type":"pro"}`
	shared := rollout(def, "sessions", "20", rootID)
	mustWrite(t, shared,
		cxMeta{at: "2026-09-20T10:00:00Z", id: rootID, cwd: "/work/app"}.String(),
		cxCount("2026-09-20T10:00:01Z", e1[0], e1[1]),
		cxLimits("2026-09-20T10:00:02Z", weekly),
	)
	for _, h := range []string{acctA, acctB} {
		link := rollout(h, "sessions", "20", rootID)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(shared, link); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
	}
	// B also ran a session of its own.
	mustWrite(t, rollout(acctB, "sessions", "21", kidA),
		cxMeta{at: "2026-09-21T10:00:00Z", id: kidA, cwd: "/work/b"}.String(),
		cxCount("2026-09-21T10:00:01Z", u{10, 0, 1}, u{10, 0, 1}),
		cxLimits("2026-09-21T10:00:02Z", `{"limit_id":"codex","primary":{"used_percent":5.0,"window_minutes":10080,"resets_at":1790600000},"secondary":null,"plan_type":"plus"}`),
	)

	res := ReadHomes("codex", []string{def, acctA, acctB}, since)
	if got := strings.Join(ids(res), ","); got != rootID+","+kidA {
		t.Fatalf("sessions = %s", got)
	}
	s := byID(t, res, rootID)
	if s.Tokens != (Tokens{Input: 600, CacheRead: 400, Output: 50}) {
		t.Fatalf("linked session tokens = %+v", s.Tokens)
	}
	if s.Home != def || !reflect.DeepEqual(s.Homes, []string{def, acctA, acctB}) {
		t.Fatalf("linked session homes = %s %v", s.Home, s.Homes)
	}
	if s.Limits == nil || len(s.Limits.Windows) != 1 || s.Limits.Windows[0].ResetsAt.Unix() != 1790500000 {
		t.Fatalf("linked session limits = %+v", s.Limits)
	}
	if own := byID(t, res, kidA); own.Home != acctB || own.Homes != nil {
		t.Fatalf("own session = %+v", own)
	}
	if l := res.Homes[def].Limits; l != nil {
		t.Fatalf("default home fell back to a linked file's limits: %+v", l)
	}
	if l := res.Homes[acctB].Limits; l == nil || l.Plan != "plus" {
		t.Fatalf("B's own limits = %+v", l)
	}
	// Read alone, the default home is as before.
	if alone := mustRead(t, "codex", def, since); alone.Limits == nil || byID(t, alone, rootID).Homes != nil {
		t.Fatalf("default home alone = %+v", alone)
	}
}

// A request's input and output go to the hour of the line that counts it, in
// the thread that counts it: a repeat, a replay, or a second copy of the file
// adds no hours.
func TestCodexHours(t *testing.T) {
	t.Run("requests, repeats, and records", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID),
			cxMeta{at: "2026-09-20T23:50:00.000Z", id: rootID, cwd: "/work/app"}.String(),
			cxRecord("2026-09-20T23:59:58.000Z", rootID, "resp-1", e1[1]),
			cxCount("2026-09-20T23:59:59.000Z", e1[0], e1[1]),
			cxCount("2026-09-21T00:00:01.000Z", e1[0], e1[1]),
			cxCount("2026-09-21T01:30:00.000Z", e2[0], e2[1]),
			cxCount("2026-09-21T02:00:00.000Z", e1[0], e1[1]),
			// A compaction's record, which the token_count after it leaves out.
			cxRecord("2026-09-21T03:10:00.000Z", rootID, "resp-compact", u{2000, 1800, 40}),
			cxCount("2026-09-21T03:10:02.000Z", e2[0], u{0, 0, 0}),
		)
		// Older Codex wrote only the cumulative total.
		mustWrite(t, rollout(home, "sessions", "20", kidB),
			cxMeta{at: "2026-09-20T09:00:00.000Z", id: kidB, cwd: "/work/old"}.String(),
			cxTotal("2026-09-20T10:00:00.000Z", u{100, 0, 10}),
			cxTotal("2026-09-21T11:00:00.000Z", u{250, 50, 30}),
		)
		res := mustRead(t, "codex", home, since)
		checkHours(t, byID(t, res, rootID), map[string]int64{
			"2026-09-20T23:00:00Z": 650,
			"2026-09-21T01:00:00Z": 30,
			"2026-09-21T03:00:00Z": 240,
		})
		checkHours(t, byID(t, res, kidB), map[string]int64{"2026-09-20T10:00:00Z": 110, "2026-09-21T11:00:00Z": 120})
	})
	t.Run("a fork's replay stays in its parent's hours", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)
		mustWrite(t, rollout(home, "sessions", "21", forkID),
			cxMeta{at: "2026-09-21T09:00:00.000Z", id: forkID, cwd: "/work/app", source: `"vscode"`, extra: `,"forked_from_id":"` + rootID + `"`}.String(),
			cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app"}.String(),
			cxCount("2026-09-21T09:00:00.000Z", e1[0], e1[1]),
			cxCount("2026-09-21T09:00:00.000Z", e2[0], e2[1]),
			cxCount("2026-09-21T10:00:20.000Z", u{4000, 3300, 100}, u{1500, 1400, 20}),
		)
		// A sub-agent's hours are its root's.
		mustWrite(t, rollout(home, "sessions", "21", kidA),
			cxMeta{at: "2026-09-21T12:00:00.000Z", id: kidA, session: rootID, cwd: "/work/app", source: spawnedBy(rootID)}.String(),
			cxCount("2026-09-21T12:00:10.000Z", u{300, 100, 20}, u{300, 100, 20}),
		)
		res := mustRead(t, "codex", home, since)
		checkHours(t, byID(t, res, rootID), map[string]int64{"2026-09-20T10:00:00Z": 680, "2026-09-21T12:00:00Z": 220})
		checkHours(t, byID(t, res, forkID), map[string]int64{"2026-09-21T10:00:00Z": 120})
	})
	t.Run("a thread in two files", func(t *testing.T) {
		home := t.TempDir()
		mustWrite(t, rollout(home, "sessions", "20", rootID), rootLines...)
		mustWrite(t, rollout(home, "archived_sessions", "20", rootID), rootLines...)
		// A later page continues the thread.
		mustWrite(t, filepath.Join(home, "sessions", "2026", "09", "21", "rollout-2026-09-21T10-00-00-"+rootID+".jsonl"),
			cxMeta{at: "2026-09-20T10:00:00.000Z", id: rootID, cwd: "/work/app", extra: `,"history_base":{"thread_id":"` + rootID + `","end_ordinal_exclusive":6,"end_byte_offset":4096}`}.String(),
			cxCount("2026-09-21T10:00:05.000Z", u{3000, 2300, 90}, u{500, 400, 10}),
		)
		res := mustRead(t, "codex", home, since)
		checkHours(t, byID(t, res, rootID), map[string]int64{"2026-09-20T10:00:00Z": 680, "2026-09-21T10:00:00Z": 110})
	})
}
