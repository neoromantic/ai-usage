# Refactoring plan

Date: 2026-09-26. Tree: `568e40e` on `main`. Latest release: v0.2.5, which is `568e40e`.

## 1. Purpose

This plan lists every change of the refactoring review in ai-report.md's backlog: what changes, in which order, and how each change is checked. It comes from an audit of the whole tree. Auditors raised 224 findings, and each finding was checked by two verifiers, one for safety and one for value. A completeness review then rebased the plan from `d460afa` onto `568e40e` and added 6 findings (critic-01 to critic-06), each verified the same way and kept in its corrected form. 223 findings are carried out by 139 steps, except for the few that wait for the owner (section 7) or lost to another finding's proposal (section 8). 7 are not done, with reasons, in section 8: the 6 the verifiers rejected, and probe-16, whose premise the newer code removed. Nothing in this plan is built yet. Implementation is doing the steps in order and passing each step's check.

## 2. Goals and non-goals

Goals:

- One copy of each rule. Repeated logic, helpers and test setup merge into one (DRY).
- No code for releases before v0.2.3. Every branch, field, test and doc sentence that exists only for older collectors, state files, schedules or relay records goes. Every device runs v0.2.3 or later before the refactor ships (Precondition 1), and the product has not launched.
- Short functions. Functions that gocyclo lists over 15 or gocognit over 20 split into named steps.
- Files of one concern. The largest files split along their seams, as pure moves.
- No dead code, and no layers, options or branches that nothing needs.
- Comments that say why. Comments that restate a name or tell history go.
- Lean tests. Tests that repeat others merge or go. No behavior loses its test.
- Lint gates in CI, so the result does not drift back.

Non-goals:

- No behavior change for the current release. The report, the JSON, the wire format, the scheduler entries and self-update behave as in v0.2.5. The few exceptions are named in each step's **Behavior** line, and each one either touches only data no release writes or waits for an owner decision.
- No new features, no test speedups, no style-only rewrites.
- No change to code that serves the harnesses' own formats (Claude Code, Codex, Hermes, Grok) or to platform code (Windows, launchd, Task Scheduler, containers). Neither is legacy of this project.

## 3. Compatibility policy

### What goes

Code, tests and docs that exist only for:

- collectors before v0.2.0, whose snapshots carry no day buckets (B1);
- ledgers from v0.1.0 and v0.1.1, written before the `answered` record (B2);
- Hermes sessions without Parts, which no release wrote: every release since v0.1.0 sets them (B3);
- the pid-nonce `run.lock` of v0.1.0 and v0.1.1, before the flock lock of v0.1.2 (B4);
- the macOS crontab line of v0.1.0, before the launch agent of v0.1.1 (B5);
- team caches that hold one device twice, which no release wrote (B6);
- JSON changelogs older than "Changes from version 3" (B7), the guide case for devices from before v0.1.4 (B8), rows for a removed flag (B9), and the relay's history in comments and docs (B10).

The floor is v0.2.3. v0.2.4 and v0.2.5 changed no snapshot type, no relay code and no state.json field. v0.2.5 added two things a v0.2.3 device lacks, and both read as empty: the `behind` member of team-cache.json (`collect.TeamCache.Behind`), and an update line inside the sealed `last_error` (`snapshot.UpdateLine` and `SplitLastError`). So v0.2.3 and later send the same wire format, and the view reads a v0.2.3 snapshot as one with no update error.

### Must keep

Deployed releases update through these, run them from their scheduler entries, or send them to the relay:

- the release asset names, `checksums.txt` with its `*` binary-mode prefix, the `releases/download/<tag>/` path, and the `releases/latest` redirect that v0.2.3 and later follow, including the repository-move following;
- the GitHub API's `releases/latest` JSON that v0.2.0's updater reads: it fetches `<api>/repos/<repo>/releases/latest`, picks the asset whose `name` equals `AssetName(goos, goarch)` and `checksums.txt` by their `browser_download_url`, and runs `<new binary> version`. One device runs v0.2.0 until Precondition 1 holds, and gate 10 checks this path;
- `ai-usage version` printing the bare tag, which v0.2.x's updater checks before it swaps a binary;
- `collect --quiet --home DIR`, the command every scheduler entry runs;
- the crontab line, the launch agent's ProgramArguments and the task XML's job token, byte for byte, so every registration still looks up as Active;
- `snapshot.Doc`'s field order, JSON tags, `omitempty` and `omitzero`, because the relay checks canonical bytes; `snapshot.Version` 1;
- the HKDF label `ai-usage seal v1`, the key export prefix `aiu-team-1:`, the signed-message prefixes and the fingerprint encoding;
- the relay Record JSON, `Record.Since` with `omitzero`, and the KV keys `aiu:t:{team}:d:{device}`, `aiu:t:{team}:devices` and `aiu:rl:*`;
- state.json's and config.json's JSON tags, including the short session tags (`p`, `proj`, `h`, `bh`), and a false `answered` entry, which reads as not answered;
- the Windows `.old` swap and its cleanup in self-update;
- the JSON report at `schema_version` 4 (Owner decision 1);
- the hours fallbacks for untimed sessions: the 90-day ledger holds sessions last written by v0.1.x until about 2026-12-22.

### Preconditions

1. Every device runs v0.2.3 or later. The status view's DEVICES table shows each device's release. One device runs v0.2.0 today. The check runs before B1 lands and again right before the first release built from this tree is tagged (gate 9).
2. A1 and A2 land before any step that touches the snapshot types, and TestWireFormat's golden never changes afterwards.
3. The relay deploys on every push to `main`, so a relay change is live before any release built from the same tree.
4. These checks run on every device, against its state folder (`ai-usage status` shows where it is). Each must print the value shown. A device that fails is fixed by hand before the step lands (Owner decision 3).
   - Before B2 (the Answered check), this prints `[]`:

     ```sh
     jq -r '([.answered // {} | keys[] | split("\u0000")[0]] | unique) as $a
       | [.accounts[] | select(.label != "unknown" and .last_seen_at != "0001-01-01T00:00:00Z") | .provider]
       | unique - $a' state.json
     ```

   - Before B3 (the Hermes check), this prints `0`:

     ```sh
     jq '[.sessions[] | select(.p == "hermes" and .parts == null)] | length' state.json
     ```

   - Before B5 (the crontab check), on each Mac, this prints `0`:

     ```sh
     crontab -l 2>/dev/null | /usr/bin/grep -c ai-usage
     ```

## 4. Baseline

Measured at `568e40e`:

- 116 Go files: 20,857 lines of code and 22,893 lines of tests. 23 golden files, 4 of them `team-older-*`. 2,663 lines of Markdown docs.
- `go build ./...` and `go vet ./...` pass for linux, darwin and windows. `go test ./...` passes; the slowest packages are cmd/ai-usage (7.2 s), internal/view (6.8 s) and internal/collect (4.6 s).
- The untracked `launch/` and `press.md` are not part of this plan.

Every Go file over 400 lines, and the step that splits it. A file marked "kept" stays one file; section 8 says why.

| File | Lines | Plan |
|---|---|---|
| cmd/ai-usage/main_test.go | 2005 | F22 |
| internal/collect/collect_test.go | 1731 | F26 |
| internal/collect/collect.go | 1540 | E2 |
| internal/view/view_test.go | 1492 | F21 |
| relay/server_test.go | 1469 | F23 |
| cmd/ai-usage/main.go | 1200 | E1 |
| internal/tui/tui_test.go | 1143 | F24 |
| internal/probe/claude_test.go | 962 | F4 shortens it, F28 splits it |
| internal/logs/hermes_test.go | 832 | F27 |
| internal/view/team.go | 828 | E3 |
| internal/probe/claude.go | 793 | E14 |
| internal/logs/codex_test.go | 769 | F3 shortens it, F27 splits it |
| internal/selfupdate/selfupdate_test.go | 747 | kept (Owner decision 17) |
| internal/probe/probe_test.go | 688 | F28 |
| internal/logs/codex.go | 683 | E6 |
| internal/schedule/schedule_test.go | 626 | F31 |
| internal/tui/tui.go | 624 | kept (Owner decision 22) |
| relay/store_test.go | 616 | F7 shortens it, F30 splits it |
| internal/collect/doc_test.go | 584 | F26 |
| internal/probe/codex_test.go | 580 | F4 shortens it, F28 splits it |
| relay/server.go | 566 | E10 |
| internal/selfupdate/selfupdate.go | 552 | kept; D24 extracts install (Owner decision 17) |
| internal/schedule/launchd_test.go | 533 | kept; B5 and F9 shorten it |
| internal/state/state_test.go | 532 | F31 |
| internal/collect/doc.go | 531 | E2 |
| internal/snapshot/snapshot.go | 526 | E9 |
| internal/view/devicestatus_test.go | 509 | kept; B1 shortens it |
| internal/snapshot/snapshot_test.go | 500 | F29 |
| internal/logs/logs_test.go | 495 | kept |
| internal/probe/codex.go | 489 | E8 |
| internal/state/state.go | 483 | E13 |
| internal/logs/hermes.go | 472 | E7 |
| internal/view/devices.go | 468 | E5 |
| internal/collect/team_test.go | 467 | kept; B6 shortens it |
| internal/schedule/schedule.go | 463 | E12 |
| internal/logs/claude_test.go | 455 | kept; F3 shortens it |
| internal/view/subscriptions.go | 454 | E5 |
| internal/view/report.go | 449 | kept |
| internal/view/render.go | 438 | E5 |
| internal/view/header.go | 418 | E4 |
| internal/view/devicestatus.go | 417 | kept; C16 and D25 shorten it |
| internal/logs/claude.go | 411 | kept |

Coverage (`go test -cover`):

| Package | Coverage |
|---|---|
| api | 100.0% |
| cmd/ai-usage | 88.0% |
| internal/collect | 90.0% |
| internal/logs | 94.0% |
| internal/probe | 94.7%, but the package fails under `-cover`: the helper processes get no GOCOVERDIR, so TestClaudeUsageRefreshSaysWhy reads the coverage warning as the harness's output (F1) |
| internal/schedule | 90.9% |
| internal/selfupdate | 88.0% |
| internal/snapshot | 80.8% |
| internal/state | 80.8% |
| internal/team | 85.5% |
| internal/tui | 97.1% |
| internal/view | 92.4% |
| relay | 96.4% |

Lint:

- staticcheck `-checks all`: 13 issues. Six unused declarations in internal/view (A3); two nil-map writes and one raw format character in tests, one deprecated call in a pty test, and three unused fields in scripts/demo (A7).
- deadcode: 20 unreachable functions without `-test`. api/relay.go (the Vercel entry point) and the typeahead helpers (used by background_windows.go) are false positives; logs.Read (A4), Key.SealedLen (A8) and Dir.LoadSamples (kept, H9) are not. With `-test`, only the five v0.1 console leftovers in internal/view remain, on every OS (A3).
- gocyclo: 109 functions over 15, 50 of them outside tests. gocognit: 97 over 20, 52 outside tests. Phase D takes the non-test ones worth splitting, and section 8 names the ones kept whole.
- dupl `-t 60`: one pair, internal/view/team.go:803-812 against 818-827 (C15).
- modernize: hits in schedule, collect, logs, probe, snapshot, view, cmd/ai-usage and 13 test files. A6 takes them, except three that go with C6, one with B3, two in the view code C13 redraws, and older_test.go, which B1 deletes.
- unparam: two hits. logs/hermes.go:135, readHermesTx's `fallback` always gets "NULL" (C27's columnOr removes it), and tui/tui.go:572, clamp's `lo` always gets 0 (A5).

The tools, run from the repository root:

```sh
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...
go run golang.org/x/tools/cmd/deadcode@v0.50.0 -test ./...
go run github.com/fzipp/gocyclo/cmd/gocyclo@latest -over 15 .
go run github.com/uudashr/gocognit/cmd/gocognit@latest -over 20 .
go run github.com/mibk/dupl@latest -t 60 .
go run golang.org/x/tools/go/analysis/passes/modernize/cmd/modernize@latest -test ./...
go run mvdan.cc/unparam@latest ./...
```

Differences from the brief:

- The brief names v0.2.3 as the latest release. v0.2.4 (`d155357`) and v0.2.5 (`568e40e`) followed on 2026-09-25. They changed the report, the share mode, the Claude usage read's error text, the update error line and the team cache's `behind` member, and no wire type (section 3), so the compatibility floor stays v0.2.3. The fleet counts in the brief (13 devices on v0.2.3, one on v0.2.0) stand until Precondition 1 is checked.
- The share-mode rework and `report --from` are committed (`d155357`, `ca98fc5`), and scripts/demo is tracked. The baseline is the clean tree at `568e40e`.
- The plan was first written at `d460afa` and rebased on `568e40e`, where every line number was checked again. They drift as earlier steps land, so each step also names the declaration or the text it changes.

## 5. Steps

The steps run in phase order, A to I. Within a phase, P1 and P2 steps come first, then P3. Each step is one commit and passes its own check and the gates in section 6 before the next one starts. Two steps land early although they are listed later: F1, before phase A, and H3, with the first commit. Line numbers are at `568e40e`.

Each step gives:

- **Findings:** the audit findings it carries out. When two proposals differ, the step says which one it takes and why.
- **Files**, **Change** and **Tests:** what to edit.
- **Behavior:** what users or deployed releases could notice. "None" means byte-identical output.
- **Risk**, **Net** (estimated lines, negative is removed), and **Depends on:** the steps that must land first.
- **Check:** the commands that must pass.

Priorities: P1 steps guard later work or remove the most. P2 steps are clear gains. P3 steps are small gains, best done while the area is open.

### Phase A. Safety nets, dead code and lint

Phase A changes no output. A1 and A2 land first and against unchanged code. They pin what the relay accepts from v0.2.3 before any later step edits `internal/snapshot`.

#### A1. Pin the snapshot wire format with a golden file · P1

- **Findings:** relay-03.
- **Files:** `internal/snapshot/snapshot_test.go`; new `internal/snapshot/testdata/v1.json`.
- **Change:** Delete TestDecodeRoundTrip (snapshot_test.go:56-65) and TestQuotaFromIsOptional (95-118). Add `testdata/v1.json`: one compact snapshot, exactly as `json.Marshal` writes the current Doc, that uses every member at least once:
  - `last_error`;
  - a codex account with plan, `quota_at`, a window with `resets_at` and `minutes`, `last_active_at`, projects, `linked`, `days` with an inner 0, and `recent`;
  - a hermes account with `quota_from` "codex", no plan, a window with neither `resets_at` nor `minutes`, and `projects: []`;
  - sources, one of them with an error;
  - aliases, one with a name and one cleared.

  Use sealed-charset strings, and write the file by hand from the old test's string. Add TestWireFormat: read and trim the golden, `Decode` must accept it, and `json.Marshal` of the result must equal it byte for byte.
- **Tests:** the new test replaces two tests. No golden may be regenerated later: every later snapshot step must pass TestWireFormat with this file unchanged.
- **Behavior:** none. **Risk:** low. **Net:** +15. **Depends on:** none. Raised from P2 to P1: it guards every later edit to the wire types.
- **Check:** `go test ./internal/snapshot/`.

#### A2. Add the missing validation rows for aliases, days and recent · P1

- **Findings:** relay-04.
- **Files:** `internal/snapshot/snapshot_test.go`.
- **Change:** Add rows to the TestValidate table. Each row also goes through `Decode` in the existing loop. Let `al := Alias{Provider: "claude", Label: sealed(40), Name: sealed(12), At: t0}` and `rc := Recent{Window: "5h", Start: t0.Add(-time.Hour), Tokens: 900}`.
  - Accepted: `aliases` (`[]Alias{al}`), `alias cleared` (Name ""), `most aliases` (`repeat(al, MaxAliases)`), `most days` (`repeat(int64(1), MaxDays)`), `day max` (`[]int64{MaxTokenCount, 0}`), `recent` (`[]Recent{rc}`).
  - Rejected: `alias unknown provider` ("gemini"), `alias plain label` ("ann@acme.dev"), `alias plain name` ("Ann B"; a name of only letters, digits, `-` and `_` passes the sealed charset, so it needs a space or an `@`), `alias at missing`, `too many aliases`, `too many days`, `day negative`, `day over` (`MaxTokenCount+1`), `recent window missing`, `recent start missing`, `recent negative tokens`, `too many recent` (`repeat(rc, MaxWindows+1)`).
- **Tests:** rows only.
- **Behavior:** none. **Risk:** low. **Net:** +20. **Depends on:** none. Raised to P1 for the same reason as A1.
- **Check:** `go test ./internal/snapshot/`. Every new row passes against unchanged code.

#### A3. Delete the leftovers of the v0.1 console renderer in `internal/view` · P1

- **Findings:** view-render-02, dup-16, compat-13, tests-02 (one step; all four list the same code, and view-render-02 is the superset).
- **Files:** `internal/view/ui.go`, `format.go`, `theme.go`, `status.go`, `guide.go`, `format_test.go`.
- **Change:**
  - ui.go: delete `wideFrom`, the fields `page`, `wide`, `multi`, `allDevices` and `legend`, and `flowEven`, `titleWrap`, the `health` struct, `header`, `healthItems`, `scheduleHealth` and `provider`. Use `u.w` in place of `u.page` in `spread` and `flow`, and delete spread's one-device comment.
  - format.go: delete `redBold`, `magenta`, `line.text`, `padLeft`, `truncLabel`, `human`, `pctText` and `shortFP`, each with its doc, and the `math` import. Trim `glyphs` and both literals to `ell, sep, ok, partial, fail, skip, warn, staged` with today's values (`… · ✓ ◐ ✕ · ! ↑` and `... - + / x . ! ^`). This also drops `ge`. Drop nameList's unused `short` parameter, its branch and its doc sentence. Keep `uuidRe`.
  - theme.go: delete `Theme.Text`, which nothing reads.
  - status.go: add `func problems(c Collector) []string`. It returns "never collected" or "last run failed", "relay failing" or "relay pending", "not scheduled", and "update check failed" (not a dev build, nothing staged, an error), and the status card loops over it.
  - guide.go: add `func scheduleFix(c Collector, sep string) string` with the three detail strings. The guide calls `u.para(line{{g.fail+" ", red}}, scheduleFix(c, g.sep), red)`. Its comment says it uses the same words as status.
- **Tests:** delete TestNumbers and TestTitleWrap, the truncLabel half of TestTruncation (rename the rest TestTruncPath), and the two host/user nameList cases.
- **Behavior:** none; every golden is byte-identical. **Risk:** low. **Net:** −245. **Depends on:** none.
- **Check:** `go build ./... && GOOS=windows go build ./... && go vet ./internal/view && go test ./internal/view ./cmd/ai-usage`. `staticcheck -checks U1000 ./internal/view/` and `deadcode -test ./...` list nothing in `internal/view`.

#### A4. Delete `logs.Read` and the Result fields only it fills · P2

- **Findings:** logs-01, tests-12 (logs half).
- **Files:** `internal/logs/logs.go`, `grok.go`, `hermes.go`, `helpers_test.go`, `logs_test.go`, `claude_test.go`, `hermes_test.go`; `internal/collect/collect_test.go`.
- **Change:** Delete `Read` (logs.go:166-174), the fields `Result.Malformed`, `Unreadable` and `Limits`, and the sum loop in ReadHomes. Result becomes `{Sessions []Session; Homes map[string]HomeRead}`. Inside the package Result stops being a carrier:
  - `readGrok` and `readHermes` return `([]Session, HomeRead)`, with the error in `HomeRead.Err`;
  - `readHermesTx` returns `(sessions, malformed, err)` and `readHermesDB` returns `(sessions, malformed, live, err)`;
  - the grok/hermes case in ReadHomes sets `Home` on each session, appends them, and stores the HomeRead.
- **Tests:**
  - helpers_test.go gets `type homeResult struct{ Result; HomeRead }` and `readOne(provider, home, since) (homeResult, error)`. `mustRead` returns a homeResult, so every `res.Malformed`, `res.Unreadable` and `res.Limits` in the tests compiles unchanged.
  - A small `sessionSet` interface lets `byID`, `ids`, `total` and `noLeak` take either type.
  - The 7 direct `Read` calls become `readOne`: claude_test.go:275, helpers_test.go:72, hermes_test.go:328 and 342, and logs_test.go:84, 92 and 122.
  - collect_test.go gets `type homeLogs struct { Sessions []logs.Session; Limits *logs.Limits; Malformed, Unreadable int }`, `world.logs` becomes `map[string]homeLogs`, and the 7 `logs.Result{` literals become `homeLogs{`.

  logs-01's readOne is chosen over tests-12's readHome, because readOne keeps the Result fields out of production code.
- **Behavior:** none. **Risk:** low. **Net:** −5. **Depends on:** none.
- **Check:** `go build ./... && go vet ./internal/logs/ ./internal/collect/ && go test -count=1 ./internal/logs/ ./internal/collect/`. deadcode lists nothing in `internal/logs`.

#### A5. Remove dead state in the interactive view · P2

- **Findings:** tui-08, tui-09.
- **Files:** `internal/tui/tui.go`, `keybar.go`, `tui_test.go`.
- **Change:**
  - Delete the zero-report load at Init (tui.go:168-170). The `Config.Report` doc becomes "Report is the report shown first." Inline `loadCmd` into `load()` and delete it.
  - Delete the two redundant scroll assignments at 157 and 159. Drop the `reloadEvery` field and use the const. Delete 447-451 and move its sentence onto the loop's comment.
  - Put `m.help ||` first in scrollMatrix's guard, and drop the two `if !m.help` wrappers in `wheel`.
  - Add `maxHelpTop()` beside `maxTop` and use it at tui.go:313 and 464 and keybar.go:115.
  - `clamp(v, lo, hi int)` (tui.go:572) becomes `clamp(v, hi int) int { return max(0, min(v, hi)) }`. All four callers (313, 316, 463, 464) pass 0 for lo today, as unparam reports.
  - Run gofmt.
- **Tests:** delete tui_test.go:1047-1059, which covers only the removed load.
- **Behavior:** none. **Risk:** low. **Net:** −29. **Depends on:** none.
- **Check:** `go test ./internal/tui`, and `go run mvdan.cc/unparam@latest ./internal/tui/` prints nothing.

#### A6. Use the standard library for hand-written helpers · P2

- **Findings:** dup-07, platform-06, logs-19, collect-13 (stdlib half), cmd-07 (`contains` half), tests-07 (modernize half).
- **Files:** `internal/schedule/schedule.go`, `launchd.go`, `schedule_test.go`; `cmd/ai-usage/home.go`, `help.go`; `internal/collect/discover.go`, `collect.go`; `internal/logs/rollup.go`, `claude.go`, `codex.go`, `grok.go`, `hermes.go`; `internal/probe/codex.go`; `internal/view/paint.go`; test files named by `modernize -test`.
- **Change:**
  - Delete the four `contains`/`containsString` copies (schedule.go:449, home.go:368, discover.go:288, rollup.go:110). Every caller uses `slices.Contains`.
  - `schedule.firstLine` becomes `line, _, _ := strings.Cut(msg, "\n")` in execRunner.
  - `orUnknownName`, `orUnknown` and `orDefault` become `cmp.Or`. So do the string fallbacks in logs: `root()`, addTracked's root, `own`, `msgID`, codex `f.id`, `f.parent` and `bucket`, grok's id and project, hermes' provider, and fillFrom's fields. Never use `cmp.Or` on `time.Time`.
  - The two clamp-to-zero blocks (logs/codex.go:522, grok.go:182) and codexUsage.since's `pos` closure become `max(x, 0)`.
  - Use `slices.Sorted(maps.Keys(...))` at logs/codex.go:302, grok.go:87 and collect's quotaLinks.
  - In askAll (collect.go:565), use `wg.Go(func() { defer func() { out[i].panicked = recover() }(); ... })`, keeping the recover inside the func.
  - Delete `l := l` at collect.go:1361.
  - Range over the `strings.SplitSeq` or `FieldsSeq` iterator in place of the slice at probe/codex.go:145 (printedLines), schedule.go:173, launchd.go:250 and cmd/ai-usage/help.go:276.
  - In errorOf (probe/codex.go:164 and 174), loop with `for i, line := range slices.Backward(lines)`. The second loop keeps its `lines[i-1]` look-back.
  - schedule.go:136 uses `slices.Clip(x)` in place of `x[:len(x):len(x)]`, and view/paint.go:83 ranges over an int.
  - Apply the test-file modernize hits in 12 files: range over int, `slices.Sort`, `strings.SplitSeq`/`FieldsSeq`, and `WaitGroup.Go` in state_test.go:155. Skip older_test.go, which B1 deletes.
  - Leave hermes.go:328-330 to C27, which makes `last` a time.Time, and the countClaude and countCodex sorts to C15.
- **Tests:** no test changes beyond the modernize edits.
- **Behavior:** none. **Risk:** low. **Net:** −132. **Depends on:** none.
- **Check:** `go build ./... && go vet ./... && GOOS=windows go vet ./...`. Then `go test ./internal/schedule/ ./internal/collect/ ./internal/logs/ ./internal/probe/ ./cmd/ai-usage/`. `modernize -test ./...` reports only these, which later steps remove: the knownProvider loops at snapshot.go:461 and collect/doc.go:467 and cmd `known` at home.go:160 (C6), the maps.Copy loop at collect.go:920 (B3), and ui.go:66 and status.go:248 in view (C13 deletes ui.go and redraws status.go). `/usr/bin/grep -rnE 'func (contains|containsString|firstLine)\(' internal cmd` finds only `tui.firstLine(err error)`.

#### A7. Fix the staticcheck findings in tests and the demo · P3

- **Findings:** dup-19, relay-23, tests-07 (staticcheck half).
- **Files:** `internal/collect/collect_test.go`, `orca_test.go`; `internal/snapshot/snapshot_test.go`; `cmd/ai-usage/pty_darwin_test.go`; `scripts/demo/main.go`.
- **Change:**
  - In collect_test.go:989-990 and orca_test.go:268-269, replace `var m map[string]int; m["boom"]++` with `panic("boom")`. The recover yields "stopped by a bug: boom", which both tests match. Keep the comments that say it stands for a parser bug.
  - In snapshot_test.go:462 (TestPrintable), write the key with escapes, `"a\u200bb\u202ec"`, so the source holds no raw format characters.
  - Put `//lint:ignore SA1019 no libSystem wrapper for TIOCPTYGNAME` above pty_darwin_test.go:28.
  - Delete the unused `key`, `sessions` and `projects` fields of the demo's device struct.
- **Tests:** only these edits.
- **Behavior:** none. **Risk:** low. **Net:** −4. **Depends on:** A3, for a clean run.
- **Check:** `staticcheck -checks all ./...` and `GOOS=linux staticcheck ./...` report nothing. `go test ./internal/collect/ ./internal/snapshot/`.

#### A8. Delete `team.Key.SealedLen` · P3

- **Findings:** relay-19, tests-12 (team half).
- **Files:** `internal/team/team.go`, `team_test.go`.
- **Change:** Delete `SealedLen` (team.go:186-189) and the length check in TestSealOpen (team_test.go:236-237). Rewrite TestSealedLenFitsSnapshot as TestSealedFitsSnapshot: `len(k.Seal(strings.Repeat("x", 300)))` must not exceed `snapshot.MaxSealed`, with the comment "The collector clips sealed text to maxSealedPlain (300) bytes." This keeps tests-12's check over relay-19's plain deletion, because it is the only test that the clipped label fits the wire limit.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −8. **Depends on:** none.
- **Check:** `go test ./internal/team/`. deadcode lists nothing in `internal/team`.

#### A9. Remove the unused KV client fallback and the `ErrNoRelay` alias · P3

- **Findings:** relay-18.
- **Files:** `relay/store.go`, `relay/client.go`, `relay/server.go`, `relay/server_test.go`.
- **Change:** `pipeline` calls `kv.HTTP.Do` directly, and the KV doc says HTTP is required. Move `var errNoRelay = errors.New("relay is not configured")` from server.go:566 to client.go, and delete the exported alias `ErrNoRelay` (client.go:33-34), whose only use is client.go:53. No package outside relay names it. TestClientErrors (server_test.go:1351) uses `errNoRelay` at 1356, 1359 and 1362.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −6. **Depends on:** none.
- **Check:** `go build ./... && go test ./relay/`.

#### A10. Drop LoadKey's unused bool result · P3

- **Findings:** collect-14 (the LoadKey half; the unexport half is under "Considered and not done").
- **Files:** `internal/collect/collect.go`, `team_test.go`; `cmd/ai-usage/main.go`.
- **Change:** LoadKey (collect.go:138-139) becomes `LoadKey(dir state.Dir) (*team.Key, error)`. Update its callers: main.go:591, 744 and 780, collect.go:181, and team_test.go:78, 125, 313, 349 and 381.
- **Tests:** caller edits only.
- **Behavior:** none. **Risk:** low. **Net:** −6. **Depends on:** none.
- **Check:** `go build ./... && go test ./internal/collect/ ./cmd/ai-usage/`.

#### A11. Drop unreachable branches in the log readers · P3

- **Findings:** logs-08, logs-09.
- **Files:** `internal/logs/logs.go`, `codex.go`.
- **Change:**
  - fitHours becomes `if s.Hours != nil { ScaleHours(s.Hours, InOut(s.Tokens)) }`. Its doc: "fitHours makes a session's hours add up to its input plus output. What no time was recorded for, such as Claude's side calls, is spread over the timed hours in their proportion: side calls go along with the work that makes them, and a resumed session does not move them all to its newest day."
  - In codexLimits, delete the `!f.fresh` skip (293-295) and write the two loops with `later()`.
  - In codexFile.count, keep the map init, and replace 431-433 with `f.limits[bucket] = later(l, f.limits[bucket])` and the comment "later keeps its first argument on a tie, so a newer line read at the same time wins".
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −27. **Depends on:** none.
- **Check:** `go test -count=1 ./internal/logs/ -run 'Hours|CodexLimits|CodexStaleParent|CodexHardLinked|ReadHomesCountsAcrossHomesOnce'`.

#### A12. Drop the unreachable column fallback in USAGE · P3

- **Findings:** view-team-14 (its fallback half; the move of usage() to its own file is E5's, after B1 and C16 edit usage()).
- **Files:** `internal/view/devices.go`.
- **Change:** In usage(), replace devices.go:375-387 with `cols := p.r.Team.Matrix.Columns; if len(cols) == 0 { return nil }`. Use `Periods` in place of the local list. Add `groupName(grp string) string` ("NO QUOTA" for "") and use it in the grid headings and in usage.
- **Tests:** none change.
- **Behavior:** none; single-80 and single-120 are unchanged. **Risk:** low. **Net:** −15. **Depends on:** none.
- **Check:** `go test ./internal/view`, goldens unchanged without `-update`.

#### A13. Always pass the state folder to the scheduler · P3

- **Findings:** platform-04.
- **Files:** `internal/schedule/schedule.go`, `launchd.go`, `schedule_test.go`.
- **Change:** `args` becomes `return "collect --quiet --home " + quote(home)` and keeps its comment. programArguments becomes `argv := []string{exe, "collect", "--quiet", "--home", home}` with no `if`. Leave Windows `Remove` and schedule_test.go:238 unchanged: its `Lookup(ctx, "", "")` uses only jobToken.
- **Tests:** delete the "entry from before the state folder was named" block (schedule_test.go:298-303). The "that binary, other state folder" case still covers the Other branch.
- **Behavior:** none. For a non-empty home the cron line, plist and task XML are byte-identical, so every registration still looks up as Active. **Risk:** low. **Net:** −12. **Depends on:** none.
- **Check:** `go test ./internal/schedule/ ./cmd/ai-usage/ -run 'Schedule|Line|Lookup|Agent|Windows'` and `GOOS=windows go vet ./internal/schedule/`. TestLineFitsCronLimit still passes.

### Phase B. Remove compatibility with releases before v0.2.0

Each step removes code, its tests and its docs in one commit, so the docs never describe behavior that is gone. B1 waits for Precondition 1, and B2, B3 and B5 wait for the device checks under "Preconditions" (Owner decision 3).

#### B1. Remove the model and rendering for collectors older than v0.2.0 · P1

- **Findings:** view-team-01, view-render-01, compat-01, compat-02, dup-01, tests-01, tui-01; docs view-team-02, compat-03, docs-02. One commit: the package does not compile halfway.
- **Files:** `internal/view/report.go`, `build.go`, `team.go`, `render.go`, `paint.go`, `devices.go`, `devicestatus.go`, `projects.go`; `internal/tui/help.go`; tests in both packages; `internal/view/testdata/team-older-*.golden`; `docs/demo/team-help.png`; `README.md`, `design.md`, `docs/json-schema.md`, `ai-report.md`.
- **Change (model):**
  - Delete fromOlderCollector (team.go:301-307), olderUsage (build.go:193-213), `Usage.Unknown` and its term in `Usage.add`, `Column.WindowUnknown` and `Cell.WindowUnknown`.
  - Delete `devAccount.older` and `devAccount.inOut` with their comment. Keep `devAccount.last`, which the Hermes billing loop reads.
  - buildTeam: `usage := usageOf(a.Days, dv.shift)`, and call `da.split(through[...], dv.shift)`. `split` loses the inOut shares, the older branch and its `now` parameter.
  - `sinceStart(window, start, collectedAt) int64` loses the older branch and the bool.
  - `users` drops `known` and `only`: skip a device when n <= 0, count it, and set Busiest when n > best.
  - `matrix` adds `a.sinceStart(...)` to `c.WindowTokens` and drops the two WindowUnknown lines.
  - `shareOf` is nil when `p.Of(whole) == 0`, else `float64(p.Of(part)) / float64(total) * 100`.
- **Change (render):**
  - render.go: delete `Period.days`, `Period.Known`, `Period.bit`, and `PeriodSet` with MarshalJSON/UnmarshalJSON and the `encoding/json` import. Delete `page.tokens`; its callers call `p.millions(per.Of(u))`: in devices.go, the grid's value closure (125, 141) and TOTAL row (273) and usage() (416, 437), and in devicestatus.go, deviceStatus (118).
  - paint.go: remove `marks.atLeast` from the type and both tables.
  - devices.go:
    - sortRows loses the Known tie-break and `gridCol.known` goes;
    - heat bolds when `v == c.top` and loses its atLeast branch;
    - `part(s)` becomes `p.percent(per.share(s))`;
    - totalOf gives millions when not sharing, "100" when `per.Of(u) > 0`, else none;
    - cell() keeps only the none case and the `unknown` mark leftCell sets.
  - projects.go: drop `add(s["atLeast"], ...)`.
  - tui/help.go: delete the `≥` entry. The `?` entry reads "not known: how full a window is".
  - Remove the older-collector clauses from the comments on sinceStart, split, users, sortRows, gridCol, grid, cell and heat (among them render.go:373-377 and devices.go:78-80), from the legend's 133-column clause (projects.go:136), and from report.go's docs at 248-251, 347-351 and 419-421 (Share, Users and Busiest).
- **Tests and goldens:**
  - Move `near` and `column` from older_test.go to helpers_test.go, then delete older_test.go. Delete olderDoc, testKey and olderTeam from helpers_test.go.
  - Delete `testdata/team-older-{7d,90d,share,devices}.golden`, the `older` cases in golden_test.go (TestGolden, TestWidths, TestPageFits), page_test.go:247, and devicestatus_test.go (the map entries at 68 and 181, the by-90d block at 82-86, and the olderTeam row at 100).
  - In projects_test.go TestLegend, drop the atLeast field, its three cases and the comment sentence.
  - In TestJSONFieldNamesAreStable, drop the older device, its comment (view_test.go:1349-1356), and the 8 paths that end in `.unknown` or `window_unknown`. Delete golden_test.go's comment at 96 with the older cases.
  - In tui_test.go, delete olderReport and TestOlderDevice, and rewrite TestDeviceViewsDrawn on `teamReport(t)`. It expects "DEVICES  13 · 1 error · 2 old · by 7d" and "v1.4.0 ↓"; take the exact strings from one run. Drop "≥" from TestHelp. Rewriting it on the existing fixture wins over tui-01's deletion, over the rename to oldReleaseReport, and over a new literal, because it keeps the only draw test of the status view and adds no fixture.
  - Regenerate `docs/demo/team-help.png`, whose MARKS list shows `≥`, with `scripts/demo/shots.ts` (bun), entry team-help.
- **Docs, same commit:**
  - README.md:80: delete the sentence "A machine on a release older than v0.2.0 sends only its tokens over 90 days: … or `?` when that is under a million."
  - design.md: delete the paragraph at 166, the bullets at 177-179, the `≥ at least` sentence at 236, the last sentence of 282, and the `≥` row at 295. Drop the clause at 217 ", with `≥` and `?` for a device on a collector older than v0.2.0" and the one at 189 ", as the DEVICES table before v0.2.0 had it". Line 220 becomes "The bottom row is TOTAL for each period. With no note on any device, there is no NOTE column." Line 303 becomes "Tokens: whole millions, `<1`, or `·`."
  - docs/json-schema.md: delete bullet 15, the paragraph at 218, the last sentence of 234 ("A device on a collector older than v0.2.0 …"), and the `window_unknown` rows at 261 and 279. The busiest row at 235 becomes "the name of the one of those devices with the most tokens; `null` when there is none". At 280, cut ", and when it is not known: the period is in the column's `usage.unknown`, and the device spent some in it or its own tokens then are not known", so the sentence ends "…spent nothing on the column in it." Add no changelog line: no v0.2.x report ever carried these fields for a current device.
  - ai-report.md:213: delete the older-collector bullet.
- **Behavior:** drops support for pre-v0.2.0 snapshots. A pre-v0.2.0 record still on the relay would show 0 tokens, not `?` or `≥`. `schema_version` stays 4, and `unknown` and `window_unknown` leave the JSON (Owner decisions 1 and 2). Every other golden is byte-identical. **Risk:** medium. **Net:** −780 (code about −170, tests and goldens about −590, docs about −20). **Depends on:** A3, and Precondition 1 (DEVICES shows no device below v0.2.3).
- **Check:**
  - `go build ./... && go vet ./internal/view ./internal/tui && go test ./internal/view ./internal/tui ./cmd/ai-usage`.
  - `go test ./internal/view -update`, then `/usr/bin/git status --short internal/view/testdata` lists only the four deletions.
  - `/usr/bin/grep -rnE 'olderUsage|fromOlder|PeriodSet|atLeast|WindowUnknown|older(Doc|Team|Report)' --include='*.go' .` prints nothing.
  - `/usr/bin/grep -nE 'older than v0\.2\.0|window_unknown|≥|133 columns' README.md design.md docs/*.md ai-report.md` prints only lines that B7 and B10 remove.
  - TestReportFrom passes.

#### B2. Remove `legacyNamed` and the `Answered=false` marker · P1

- **Findings:** collect-01, compat-04, tests-19, docs-05.
- **Files:** `internal/collect/collect.go`, `collect_test.go`; `internal/state/state.go`; `ai-report.md`.
- **Change:**
  - Delete legacyNamed (collect.go:785-801), the `legacy` variable (454), claimUnknown's `legacy bool` parameter and its argument (488), and the doc sentence "legacy is legacyNamed…".
  - In claimUnknown the condition becomes `if label != "" && label != UnknownAccount {`. The partial-read block becomes `continue // It claims at a run that reads them all.` and writes no `st.Answered[k] = false`. `st.Answered[k] = true` stays.
  - state.go:151-153: replace the "False is a home that …" sentence with "A false entry reads as not answered." v0.2.3 files hold such entries, and the type stays `map[string]bool`.
  - ai-report.md:190: delete "A ledger from before that record claims nothing once any account of the harness is named."
- **Tests:** delete TestLegacyLedgerWithANamedAccountDoesNotClaim (collect_test.go:587-616), and drop the trailing `false` at collect_test.go:401. The claim tests (TestFirstNamedAccountClaimsUnknownHistory, TestEachHomeClaimsItsOwnUnknownHistory, TestClaimOnceAfterTheAccountAgesOut, TestLoggedOutHomeDoesNotClaimLater, TestClaimTakesTheUnknownHours) pass unchanged.
- **Behavior:** drops ledgers from v0.1.0 and v0.1.1. **Risk:** low. **Net:** −55. **Depends on:** the Answered check under Preconditions.
- **Check:** `go vet ./internal/collect/ ./internal/state/ && go test ./internal/collect/`. `/usr/bin/grep -rn 'legacyNamed\|legacy bool' internal` prints nothing. On a copy of a v0.2.3 state folder, `ai-usage report --json` is the same before and after one run.

#### B3. Remove the Hermes Parts migration · P1

- **Findings:** collect-02, compat-05, tests-03.
- **Files:** `internal/collect/collect.go`, `hermes_test.go`, `doc_test.go`.
- **Change:**
  - In attribute (collect.go:912-930), replace the `migrating` comment, block and fitGrowth call with `if e.Parts == nil { e.Parts = map[string]snapshot.Tokens{} }`, followed by the per-part growth loop that is already there.
  - Delete fitGrowth and tokenFields (1074-1116) and the `math/bits` import. `sort` stays.
  - In markHermesCurrent, drop `r.s.Parts != nil &&`. hermesParts' nil-Parts branch goes in C26.
  - Keep the fallback for a part that names no account (904-909): it handles rows in Hermes' own database.
- **Tests:** delete TestHermesPartsContinueOldLedger and TestHermesPartsDoNotRecountOldLedger. In TestUntimedHistoryFromBeforeHoursStays, give the s2 entry `Parts: {"openrouter": {Input: 500}}` and the s3 entry `Parts: {"openrouter": {Input: 500}, "nous": {Input: 200}}`, as a real Hermes ledger has. The expected days stay `[10 0 500]` and `[0 200]`.
- **Behavior:** none for any ledger a release wrote: every release since v0.1.0 sets Parts on Hermes sessions. **Risk:** low. **Net:** −110. **Depends on:** the Hermes check under Preconditions.
- **Check:** `go vet ./internal/collect/ && GOOS=windows go vet ./internal/collect && go test ./internal/collect/`. `/usr/bin/grep -n 'fitGrowth\|tokenFields\|math/bits\|migrating' internal/collect/*.go` prints nothing.

#### B4. Stop honoring the pid-nonce `run.lock` of v0.1.0 and v0.1.1 · P1

- **Findings:** platform-02, compat-06, tests-18, docs-04.
- **Files:** `internal/state/state.go`, `lock_unix.go`, `lock_windows.go`, `state_test.go`; `ai-report.md`.
- **Change:** Delete heldBefore and its call in `lock()`. Delete processAlive from both lock files, with the `syscall.Kill` use and the Windows comment. Keep writing the pid (it helps a person), and keep the comment on why the file is never removed. In ai-report.md:159, delete the last two sentences ("A release from before the file lock held run.lock by creating it. A lock in that format is honored …").
- **Tests:** delete TestLockHonorsEarlierRelease.
- **Behavior:** drops v0.1.0 and v0.1.1 lock files. **Risk:** low. **Net:** −86. **Depends on:** none.
- **Check:** `go test -race ./internal/state ./internal/collect ./cmd/ai-usage`, `GOOS=windows go vet ./internal/state/`, `GOOS=linux go build ./...`. `/usr/bin/grep -rn 'heldBefore\|processAlive' internal` prints nothing.

#### B5. Drop the migration off the macOS crontab line of v0.1.0 · P1

- **Findings:** platform-01, compat-07, tests-17, cmd-01, docs-06.
- **Files:** `internal/schedule/launchd.go`, `schedule.go`, `launchd_test.go`; `cmd/ai-usage/main.go`; `README.md`, `ai-report.md`.
- **Change:**
  - launchd.go: delete cronLeft and dropCronLine.
    - lookupAgent returns Absent when the plist is missing and Active after the loaded check.
    - installAgent's InAgent block keeps its first comment sentences and `return nil`, and the function ends `_, err = s.launchctl(ctx, "bootstrap", s.domain(), file); return err`.
    - removeAgent drops its dropCronLine call, and its comment becomes "boots the agent out and removes the plist".
    - Drop the second sentence of installAgent's comment (102-103).
  - schedule.go: delete `Duplicate` and its comment. State values are never saved.
  - main.go: delete `case schedule.Duplicate` (1010-1011).
  - Docs: delete README.md:174's last sentence ("A crontab line an earlier version wrote on macOS … says the line is still there."), keeping the sentence about no one logged in at the screen. Delete ai-report.md:157's sentence "A crontab line an earlier version wrote is removed … never both run the collector unnoticed."
- **Tests:**
  - Delete TestAgentReplacesCronLine and TestAgentLeftoverCronLine.
  - Delete fakeLaunchd's `cron` field and crontab dispatch. The fake then fails any crontab call, which guards that macOS never triggers the admin prompt.
  - Replace the check at launchd_test.go:248-252 with one that no crontab call was recorded.
- **Behavior:** drops the v0.1.0 crontab line. A Mac that still had one would collect twice, which is why the check under Preconditions comes first. **Risk:** medium. **Net:** −175. **Depends on:** the crontab check under Preconditions.
- **Check:** `GOOS=darwin go vet ./internal/schedule/ ./cmd/...`, `go test ./internal/schedule/ ./cmd/ai-usage/`, and `GOOS=windows go build ./...`. `/usr/bin/grep -rnE 'cronLeft|dropCronLine|Duplicate|older version' internal cmd` prints nothing. On one Mac, `ai-usage schedule status` prints "registered:".

#### B6. Drop the team cache's duplicate-device merge · P2

- **Findings:** collect-03, tests-04, relay-22.
- **Files:** `internal/collect/team.go`, `team_test.go`.
- **Change:** In LoadTeamCache, replace the merge loop (team.go:62-76) with `for _, body := range c.Bodies { if doc, err := snapshot.Decode(body); err == nil { c.Docs = append(c.Docs, doc) } }`, and drop the last sentence of its doc (46-49). The `behind` member stays as it is. `relay.Client.Pull` keeps its own dedupe, since the relay is untrusted.
- **Tests:** delete the "duplicate device" block in TestLoadTeamCache (team_test.go:448-459). TestPullKeepsOneDocumentPerDevice stays.
- **Behavior:** none; no release wrote a cache with duplicates, and every pull replaces the cache. **Risk:** low. **Net:** −26. **Depends on:** none.
- **Check:** `go test ./internal/collect/ ./relay/`.

#### B7. Cut the JSON schema changelogs of older versions · P2

- **Findings:** docs-08, compat-09, view-team-02 (changelog half).
- **Files:** `docs/json-schema.md`, `README.md`.
- **Change:** In json-schema.md, delete the sections "## Added within version 3" and "## Changes from version 2" (292-315, found by heading), up to "## Example". Keep "## Changes from version 3" (282) and "## Added within version 4" (288-291), which lists `update_error` and `behind_since` (Owner decision 12). Line 5 becomes: "A field changes meaning only with a new `schema_version`. New fields can appear within a version, so ignore the ones you do not know. [Changes from version 3](#changes-from-version-3) lists what version 4 changed, and [Added within version 4](#added-within-version-4) what it added since." Delete README.md:359, the version history paragraph ("Version 4 made … Version 3 replaced …"). README.md:361 already links json-schema.md.
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −25. **Depends on:** B1.
- **Check:** `/usr/bin/grep -nE 'version 2|within version 3|headline_percent' docs/json-schema.md README.md` prints nothing. `/usr/bin/grep -c 'added-within-version-4\|Added within version 4' docs/json-schema.md` prints 2. Every `](#` anchor in json-schema.md resolves.

#### B8. Drop the "collected before the guide existed" case · P3

- **Findings:** platform-03, compat-11, cmd-02, tests-11 (guide half), docs-03.
- **Files:** `internal/state/state.go`; `cmd/ai-usage/main_test.go`; `README.md`, `ai-report.md`.
- **Change:** The GuideDue comment (state.go:166-170) becomes "GuideDue says the short guide a new device prints once, under the first report a person sees, is still to come. LoadState sets it only when state.json does not exist." Delete the last sentence of README.md:166 ("A machine that collected with a version before the guide never prints it.") and of ai-report.md:231 ("A device that collected before the guide existed never prints it."). The code does not change.
- **Tests:** in TestGuideAfterInstall, delete the `old` device block (main_test.go:1639-1646). The second-run check covers a state without `guide_due`.
- **Behavior:** none. **Risk:** low. **Net:** −14. **Depends on:** none.
- **Check:** `go test -count=1 -run TestGuideAfterInstall ./cmd/ai-usage/`. `/usr/bin/grep -rn 'before the guide' README.md ai-report.md internal` prints nothing.

#### B9. Drop the `--tokens` rows of a removed flag · P3

- **Findings:** tui-02, tests-11 (flag half).
- **Files:** `cmd/ai-usage/term_test.go`.
- **Change:** In TestDisplayFlagErrors, `{"report", "--projects", "--tokens"}` becomes `{"report", "--projects", "--bogus"}`, and `{"--offline", "--tokens"}` becomes `{"--offline", "--color=sometimes"}`. The second row still checks that a bare run with a bad display flag exits 2, prints the help, and writes nothing. The comment at 141-142 becomes "Bad flags are refused before anything is collected."
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −2. **Depends on:** none.
- **Check:** `go test -run 'TestDisplayFlagErrors|TestVersionHelpAndUsageErrors' ./cmd/ai-usage`.

#### B10. Remove the relay's history from comments and docs · P3

- **Findings:** docs-07, compat-08, relay-01, relay-02 (comment half).
- **Files:** `relay/store.go`, `relay/server.go`; `docs/relay-protocol.md`, `docs/relay.md`; `ai-report.md`.
- **Change:**
  - store.go:19-20: Record's doc ends at "… which sets how long the record is kept." server.go:386-388: drop "A record stored before the relay kept Since has a zero one and keeps the full lifetime." Record.Since keeps `omitzero`, and the zero-since assertion at server_test.go:971 stays, because zero-since records in KV cannot be ruled out. That is why relay-02's tag change is not taken.
  - relay-protocol.md: delete 522-532 (the table of added members and the 32768/100 paragraph) and the first sentence of 480. Keep 511-520, the general rule.
  - relay.md: delete the five paragraphs at 190-198 (quota_from, linked, days and recent, aliases, older relay). At 144, "as described under [API](#api)" becomes "as [relay-protocol.md](relay-protocol.md#11-compatibility) §11 describes". This wins over compat-08's rewrite of 198, because §11 already states the rule.
  - ai-report.md: delete the last sentence of 217 ("A collector older than this format drops a snapshot …") and the whole "Deploy order for `quota_from` and `linked`" bullet at 246.
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −30. **Depends on:** none.
- **Check:** `go vet ./relay/ && go test ./relay/`. `/usr/bin/grep -nE 'valid as before|relay older than|kept \*since\*|Deploy order|kept Since|32768' docs/*.md ai-report.md relay/*.go` prints nothing.
### Phase C. One copy of each rule, and fewer layers

Phase C changes no output unless a step says so. Where two findings proposed different shapes for the same code, the step names the one chosen and why.

#### C1. One mergeByID for the log readers · P1

- **Findings:** logs-02, dup-09 (merge half).
- **Files:** `internal/logs/rollup.go`, `claude.go`, `codex.go`.
- **Change:** In rollup.go add `mergeByID(in []Session, merge func(have *Session, s Session)) []Session`. Rows keep first-seen order, and rows with ID "" never merge.
  - dedupeSessions becomes mergeByID with a merge that keeps the larger total and then calls fillFrom.
  - Add `addCopy(have *Session, s Session)`, with the doc "adds a copy that counts only its own part (a page, or the same session in another project directory or home); the session grows in the copy written last". It adds the tokens and hours, takes Home when `s.Updated` is later, and calls fillFrom.
  - countClaude builds rows in file order and calls `addTracked(mergeByID(rows, addCopy), files, copied)`. countCodex returns `mergeByID(rows, addCopy)`.
  - Delete the two restating comments.

  logs-02's mergeByID is chosen over dup-09's `absorb`, because one helper then serves all three merges.
- **Tests:** none change. TestDedupeSessions, TestClaudeOneSessionInTwoProjects, TestCodexOneThreadInTwoFiles, TestReadHomesCountsAcrossHomesOnce and the hours subtests pass unchanged.
- **Behavior:** none. **Risk:** low. **Net:** −25. **Depends on:** A4.
- **Check:** `go test -count=1 ./internal/logs/`.

#### C2. One walkLogs for the Claude, Codex and Grok file walks · P1

- **Findings:** logs-03, dup-09 (file-filter half).
- **Files:** `internal/logs/scan.go`, `logs.go`, `claude.go`, `codex.go`, `grok.go`.
- **Change:**
  - scan.go gets `walkLogs(root string, unreadable *int, keep func(rel string) bool, visit func(file, rel string, info fs.FileInfo))`, with the doc "walkLogs visits each file under root that keep accepts, by its slash-separated path relative to root. Credential files are never visited. A missing root holds nothing; any other error counts as unreadable." It also gets `isJSONL(rel string) bool`.
  - logs.go gets `(r *HomeRead) add(malformed int, err error)`. Use it at claude.go:42-45, codex.go:123-126 and grok.go:104-107 and 118-121.
  - Claude: replace the subagents walk (74-99) with one walkLogs call. The top-level filter at claude.go:60 stays inline, with no `logName` helper.
  - Codex: walkCodex calls `walkLogs(root, &out.Unreadable, isJSONL, visit)`.
  - Grok: keep accepts `summary.json` and `updates.jsonl` at depth 2. Delete the deniedFile check at 50 and the dead `if err != nil` at 83-85.

  logs-03's walkLogs is chosen over dup-09's logFile, because it also removes the three copies of the walk itself.
- **Tests:** none change. TestClaudeMissingOrBrokenHome (Unreadable 2), TestGrokUnreadableFiles (3), TestGrokIgnoresFilesAtOtherDepths, TestProviderFailureIsolation, TestCodexSkipsCredentialFiles and TestClaudeWindowAndCredentialFiles pass unchanged.
- **Behavior:** none, except in one race: a sub-agent file that vanishes between readdir and stat now counts as unreadable and adds no empty row. **Risk:** low. **Net:** −30. **Depends on:** A4.
- **Check:** `go test -count=1 ./internal/logs/`.

#### C3. Read Claude Code's config once per probe · P1

- **Findings:** probe-03, critic-02 (its value version, folded in here: one config read before the gate and one after the refresh).
- **Files:** `internal/probe/claude.go`, `claude_test.go`.
- **Change:** Today readClaudeConfig runs up to six times per probe: in claudeHasAccount, claudeUsedSince and claudeStale for the gate, in claudeStale again after the refresh, in claudeCacheState for each claudeUsageError, and in claudeCachedUsage.
  - Claude (claude.go:34-67): `bin, found := env.find("claude")`. When found, it runs claudeAuthStatus and sets Account and Plan; otherwise it appends "claude binary not found; account unknown". Then, once, found or not: `cfg, cfgErr := readClaudeConfig(file)`.
  - The gate (today claude.go:48): `if st.subscription() && cfgErr == nil && cfg.hasAccount() && cfg.usedSince(lastUse, env.now()) && claudeOwned(file, home) && cfg.stale(env.now())`. A zero `st` is not a subscription, so a missing binary never refreshes. Inside: `var err error; if cfg, err = claudeReadUsage(ctx, env, bin, configDir, home, file, cfg); err != nil { errs = append(errs, err) }`.
  - After the gate: append cfgErr when it is not nil, and `if st.AuthMethod == "claude.ai" { r.Quota = cfg.quota() }`, keeping the comment on API keys and cloud providers.
  - claudeReadUsage (211-226) becomes `func claudeReadUsage(ctx context.Context, env Env, bin, configDir, home, file string, cfg claudeConfig) (claudeConfig, error)`, which returns the config to take the quota from.
    - Too old (213-216) and no traffic (217-219): `return cfg, claudeUsageError(why, v, cfg.cacheState(env.now()), "")`, with v "" and version as today.
    - After claudeRefresh: `after, err := readClaudeConfig(file); if err != nil || !after.stale(env.now()) { return after, err }`. Then `why, said := run.why(version); return after, claudeUsageError(why, version, after.cacheState(env.now()), said)`.
    - A config the refresh leaves unreadable gives its read error, with the same text and in the same place in the joined error, and no quota, as claudeStale's false and claudeCachedUsage's error do today.
  - claudeCacheState(path, now) (390-416) becomes `func (cfg claudeConfig) cacheState(now time.Time) string`: its body from `c := cfg.Cached` on, with no file read. Its doc loses the sentence about a config that cannot be read.
  - claudeUsageError(why, version, file, now, said) (231-247) becomes `claudeUsageError(why, version, cache, said string) error`, which adds cache to the parenthesis when it is not empty.
  - Add the claudeConfig methods `hasAccount`, `usedSince(used, now)`, `stale(now)` and `quota()`, with the bodies of claudeHasAccount (662-665), claudeUsedSince (642-656), claudeStale (603-614) and claudeCachedUsage (707-747) after their reads. Delete those four functions.
  - Add no age helper: the one `now.Sub(time.UnixMilli(c.FetchedAtMs))` in stale and in cacheState stays inline.
  - Keep the idle-login reasoning once, in Claude's doc, and cut each method's comment to its rule.
- **Tests:**
  - TestClaudeCachedUsage calls readClaudeConfig and then `cfg.quota()` over the unchanged table.
  - TestClaudeCachedUsageErrors (141-157) becomes TestReadClaudeConfigErrors: a missing file gives an empty config and nil; bad JSON or a directory gives an error.
  - TestClaudeCacheState (666-682) decodes each row's config into a claudeConfig and calls cfg.cacheState(testNow). Delete its `{` row: no path meets an unreadable config there now, and TestClaudeSkipsUsageRefresh "config not JSON" covers the gate.
  - TestClaudeUsageErrorFitsTwice (840-880) runs `cfg, _ := readClaudeConfig(file)` once, then `claudeUsageError(why, version, cfg.cacheState(testNow), "")` and `_, err := claudeReadUsage(ctx, env, old, "", home, file, cfg)`. Its rows and assertions do not change.
- **Behavior:** none; every error string stays byte-identical, in the same order. **Risk:** low. **Net:** −32. **Depends on:** none. Until C7 lands, lastUse comes from `LastUse(ctx)`.
- **Check:** `go test ./internal/probe/ -run Claude`. It includes, unchanged: TestClaudeUsageRefreshSaysWhy's expected strings, TestClaudeOldVersionIsNotAsked, TestClaudeBehindAShimIsAsked, TestClaudeNoTrafficIsNotAsked, TestClaudeSkipsUsageRefresh "config not JSON", TestClaudeUsageRefreshFails "write-then-fail" (still 7%), TestClaudeBadCacheKeepsAccount and TestClaudeBinaryMissing, with TestClaudeCacheState, TestClaudeUsageErrorFitsTwice and TestReadClaudeConfigErrors as changed above. `/usr/bin/grep -c 'readClaudeConfig(' internal/probe/claude.go` prints 3: the definition, Claude, and claudeReadUsage.

#### C4. One atomic file writer in a new leaf package `internal/fsutil` · P1

- **Findings:** relay-20, dup-02, platform-05. dup-02's `internal/fsutil` is chosen over platform-05's `internal/atomicfile`, because C19 adds RealPath and Tilde to the same leaf (Owner decision 4).
- **Files:** new `internal/fsutil/fsutil.go`, `fsutil_test.go`; `internal/state/state.go`, `state_test.go`; `internal/team/team.go`, `team_test.go`; `internal/schedule/launchd.go`; `internal/collect/team.go`, `collect.go`, `team_test.go`; `cmd/ai-usage/main.go`.
- **Change:**
  - fsutil.go: `WriteFile(path string, b []byte, perm os.FileMode) error`. It does MkdirAll(dir, 0o700), CreateTemp(dir, "."+base+".*"), Write, Sync, Close, Chmod(perm) and Rename. It removes the temp file on every failure, a failed rename included. Keep state.go's one-line reason for Sync and team.go's reason for CreateTemp.
  - Delete state.WriteFile. writeJSON, LoadState's `.bad` copy and collect's saveTeamCache call `fsutil.WriteFile(..., 0o600)`.
  - Delete `team.Key.Save` and team.go's `path/filepath` import. Its three callers (main.go:825 backup, main.go:835, collect.go:151) call `fsutil.WriteFile(path, []byte(k.Export()+"\n"), 0o600)`.
  - Delete writePlist. installAgent keeps `os.MkdirAll(s.AgentDir, 0o755)` first, so a new LaunchAgents folder is not created 0700, and then calls `fsutil.WriteFile(file, []byte(AgentPlist(exe, home, path)), 0o644)`.
- **Tests:**
  - Move TestWriteFileReplacesAtomicallyAndPrivately to fsutil_test.go. It writes twice, checks the content, checks 0o600 and 0o644 (skipped on Windows), checks that no temp file is left, and checks that a new nested folder has no group or other bits.
  - state_test.go:266 and 287 and collect/team_test.go:461 call fsutil.WriteFile.
  - Delete TestSaveFileMode. TestSaveLoad becomes TestLoad: `Load(missing)` wraps `os.ErrNotExist`, and a file holding "garbage\n" is a parse error. TestImportIgnoresByteOrderMark still loads a good file.
- **Behavior:** none visible. The plist gains an fsync and a temp name made of a dot, the plist's file name and a random suffix, and the key file gets an explicit chmod 0600. **Risk:** low. **Net:** −110. **Depends on:** none.
- **Check:** `go build ./... && GOOS=windows go vet ./... && GOOS=linux go vet ./...`. Then `go test ./internal/... ./cmd/ai-usage/`. TestAgentInstall still sees 0644, and checkPrivate sees 0600. `/usr/bin/grep -rn 'CreateTemp' internal` shows only fsutil, selfupdate (stage, writable) and the schedule task XML.

#### C5. Grok's line scanner uses the logs reader · P1

- **Findings:** dup-11, probe-06. dup-11 is chosen over probe-06, because it keeps Grok's 1 MiB line cap and its two tests. probe-06 would have moved Grok to the logs cap.
- **Files:** `internal/logs/scan.go`, `claude.go`, `codex.go`, `grok.go`, `logs_test.go`; `internal/probe/grok.go`, `codex.go`.
- **Change:**
  - Export `ForEachLine(path string, max int, visit func([]byte)) (long int, err error)`, and give forEachReader a `max` parameter. The logs callers and logs_test.go:465 and 491 pass `maxLineBytes`.
  - `probe grokScan.scan` becomes `_, err := logs.ForEachLine(path, maxGrokLine, s.add); return err`. Drop the bufio, io and os imports.
  - Export `logs.CodexMainLimit` (logs/codex.go:601-602) and compare with it in codexLimits' sort (probe/codex.go:320-321). D18 keeps it in codexBuckets.
  - Keep probe.parseTime.
- **Tests:** TestGrokLongLines and TestGrokLastLineWithoutNewline stay.
- **Behavior:** none. **Risk:** low. **Net:** −30. **Depends on:** none.
- **Check:** `go build ./... && go test ./internal/probe/ ./internal/logs/`. TestGrokErrors "unreadable" still reports the read error.

#### C6. One provider list: `snapshot.Providers` · P2

- **Findings:** dup-05, collect-13 (provider half), cmd-07 (`known` half).
- **Files:** `internal/snapshot/snapshot.go`; `internal/collect/discover.go`, `doc.go`, `collect.go`; `internal/view/build.go`, `team.go`, `ui.go` (or card.go after C13), `helpers_test.go`; `cmd/ai-usage/home.go`, `alias.go`, `alias_test.go`; `internal/logs/logs_test.go`; `scripts/demo/main.go`.
- **Change:**
  - snapshot's own knownProvider loop (snapshot.go:460) becomes the exported `KnownProvider(p string) bool { return slices.Contains(Providers, p) }`, and its validation callers (306, 323, 337, 355, 409) use it.
  - Delete collect.Providers, collect.knownProvider (doc.go:466) and cmd `known` (home.go:159; callers home.go:75 and alias.go:158 and 261). Every caller uses `snapshot.Providers` and `snapshot.KnownProvider`. This removes the three modernize hits A6 left.
  - SortAccounts and alias.go order providers with `slices.Index(snapshot.Providers, p)` in place of a rank map.
  - view.homeOf loops over `snapshot.Providers` with "."+p, so defaultHomes goes.
- **Tests:** logs_test.go and view helpers use `snapshot.Providers`.
- **Behavior:** none; the list and its order are the same. **Risk:** low. **Net:** −40. **Depends on:** A1 (TestWireFormat still passes).
- **Check:** `go build ./... && go vet ./...`. Then `go test ./internal/... ./cmd/...`, and `go run ./scripts/demo <tmp>` diffed against `docs/demo`. `modernize -test ./...` no longer lists snapshot.go, doc.go or home.go.

#### C7. Claude takes the remembered config folder and the last use as parameters · P2

- **Findings:** probe-01, probe-02, collect-04. probe-01 and probe-02 are chosen over collect-04's `Env.ClaudeConfigDirs` map, because explicit parameters beat a hidden field (Owner decision 5).
- **Files:** `internal/probe/claude.go`, `probe.go`, `claude_test.go`; `internal/collect/collect.go`, `discover.go`, `claudeenv_test.go`, `collect_test.go`, `orca_test.go`.
- **Change:**
  - `func Claude(ctx context.Context, env Env, home, remembered string, lastUse time.Time) (Reading, error)`.
  - `claudeConfigDir(home, remembered string)` (claude.go:82) loops over the variable and then the remembered value: the first that names this home wins, as itself when absolute, else as the home. Then the default and custom fallbacks follow unchanged. Merge claudeEnv's comment (why the exact string is remembered) into its doc.
  - Delete lastUseKey, WithLastUse and LastUse (claude.go:616-628). Their users are collect.go:448 (WithLastUse) and collect_test.go:128 (LastUse).
  - `Options.Ask` and askHarness's func become `func(ctx, provider, home string, lastUse time.Time) (probe.Reading, error)`. askHarness calls `probe.Claude(ctx, env, home, homeEnv["claude"][home], lastUse)`. askAll keeps its three-argument `ask`, and the closure passes `lastUse[home]`.
  - Delete collect.claudeEnv (its WithEnv and Getenv calls at collect.go:132 and 135), collect.samePath and probe `Env.Getenv` (probe.go:166-168). Unexport `WithEnv` (172-175); C8 deletes it.
- **Tests:**
  - Delete TestClaudeEnvUsesRememberedValueWhenUnset.
  - TestClaudeConfigDir gets a `remembered` column and four rows: variable names another home plus remembered, variable unset plus remembered, nothing remembered at the default home, and variable naming this home beats remembered.
  - collect_test.go `w.ask` records lastUse directly, and the orca_test.go wrappers pass it on.
  - Probe tests call `Claude(ctx, env, home, "", x)`.
  - TestSchedulerRunProbesClaudeWithRememberedConfigDir stays as the end-to-end check.
- **Behavior:** none. **Risk:** low. **Net:** −55. **Depends on:** C3.
- **Check:** `go test ./internal/probe/ ./internal/collect/` and `GOOS=windows go vet ./internal/collect/ ./internal/probe/`. TestAskTellsWhenTheHomeWasLastUsed, TestHomesSharingClaudeLogsAreNotToldTheirUse, TestClaudeStopsAskingOnceTheHomeIsIdle and TestRememberEnvKeepsClaudeConfigDirVerbatim pass. `/usr/bin/grep -rn 'claudeEnv\|WithLastUse' internal` prints nothing.

#### C8. One harness command builder · P2

- **Findings:** probe-05.
- **Files:** `internal/probe/probe.go`, `claude.go`, `codex.go`.
- **Change:**
  - `func (e Env) command(ctx context.Context, bin string, set []string, args ...string) *exec.Cmd` (today probe.go:131-141) sets Dir, ownGroup, `cmd.Env = e.pathFor(e.harnessEnv(set...), bin)` and `cmd.WaitDelay = time.Second`. It takes over the comment "A child the CLI leaves behind can hold stdout open after the kill." (claude.go:155). codexAt then sets `cmd.WaitDelay = 2 * time.Second`.
  - Add `environ()`, which gives e.Environ or os.Environ(). getenv and harnessEnv use it.
  - `harnessEnv(set ...string)` (180-196) takes key/value pairs, drops matching names with sameEnvName, and appends non-empty values.
  - Callers: claudeAuthStatus (149-173) passes `CLAUDE_CONFIG_DIR`. claudeRefresh (257-277) passes `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` "" and `DISABLE_AUTOUPDATER` "1", in place of its WithEnv chain at 262-263. codexAt passes `CODEX_HOME`.
  - Delete WithEnv and both `cmd.Stdin = nil` lines (claude.go:154 and 265, with the comment at 264), keeping "no input" in claudeRefresh's doc. Delete both callers' WaitDelay lines (156, 269).
  - DefaultEnv (104-117) keeps Environ, HomeDir, SystemBinDirs, AppDirs and ClaudeManaged. The other Env fields stay as test seams, and Env's doc says a zero field means the real thing.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −14. **Depends on:** C7.
- **Check:** `go test ./internal/probe/ ./cmd/ai-usage/` and `GOOS=windows go vet ./internal/probe/`. TestClaudeRefreshesUsage checks the child env and "0 bytes" of stdin, TestCodexTimeoutKillsServer stays under 1.9s, and TestHarnessEnv and TestPathFor pass.

#### C9. An exported sentinel for "not logged in" · P2

- **Findings:** probe-11.
- **Files:** `internal/probe/probe.go`, `probe_test.go`, `claude_test.go`, `codex_test.go`; `internal/collect/collect.go`, `collect_test.go`.
- **Change:** Replace the notLoggedIn type and its two methods with `var ErrNotLoggedIn = errors.New("not logged in")` and `func notLoggedIn(harness string) error { return fmt.Errorf("%s: %w", harness, ErrNotLoggedIn) }`. The texts stay "claude: not logged in" and "codex: not logged in", and the probe call sites do not change. joinErrors' doc says errors.Is. In collect, delete isLoggedOut and use `errors.Is(err, probe.ErrNotLoggedIn)` at collect.go:480, 483 and 818.
- **Tests:** delete probe's saysLoggedOut and use errors.Is in the five tests that call it. collect_test.go's copied type becomes `func notLoggedIn(p string) error { return fmt.Errorf("%s: %w", p, probe.ErrNotLoggedIn) }`, so its callers stay.
- **Behavior:** none; stored error text is unchanged. **Risk:** low. **Net:** −14. **Depends on:** none.
- **Check:** `go test ./internal/probe/ ./internal/collect/ ./internal/view/`. collect_test.go:671 still checks "codex: not logged in".

#### C10. One output path for collect, report, `report --from` and status · P2

- **Findings:** cmd-03, tui-04, tui-05, dup-14. cmd-03 with tui-04 and tui-05 is chosen over dup-14's printJSON, because it also builds the report once and removes the endpoint parameter.
- **Files:** `cmd/ai-usage/main.go`, `interactive.go`, `interactive_test.go`, `interactive_unix_test.go`.
- **Change:**
  - `reportAt(d state.Dir, res *collect.Result, now time.Time) view.Report` drops `endpoint` and sets `RelayURL: relayURL(res.Config)`.
  - Add `writeJSON(w io.Writer, v any) error` (an Encoder with two-space indent) and use it at main.go:572-574, 654-656 and 704-706.
  - Add `(d *display) tuiConfig(stdout io.Writer, r view.Report) tui.Config`, which gives the Report, the Options with Width set, and the Profile. Move viewConfig's comment about escapes and COLORFGBG onto it.
  - `viewConfig(d, res, disp, offline, stdout)` starts from `disp.tuiConfig(stdout, reportAt(d, res, clock().UTC()))` and sets Load, Refresh, Stopping, Watch and Now.
  - `printReport(stdout, r view.Report, jsonOut, guide bool, disp)` loses its own view.Build.
  - Delete showView. cmdCollect and cmdReport call `tui.Run(ctx, viewConfig(...), os.Stdin, stdout)` when interactive, else `printReport(stdout, reportAt(...), ...)`.
  - reportFrom calls `tui.Run(ctx, disp.tuiConfig(stdout, r), os.Stdin, stdout)` or printReport. cmdStatus uses reportAt and writeJSON.
- **Tests:** drop the `""` endpoint argument at interactive_test.go:61, 65, 74, 77, 90 and 172 and interactive_unix_test.go:57. TestReportFrom already covers `--from` as text, as JSON, and with a schema mismatch.
- **Behavior:** none. cmdCollect's report takes the relay URL from `res.Config`, which is the same config.json reloaded under the lock, as cmdReport already does. **Risk:** low. **Net:** −35. **Depends on:** none.
- **Check:** `go test -count=1 ./cmd/ai-usage/`. Output of `report --plain`, `report --json` and `status --json` on a test device is byte-identical before and after.

#### C11. One collection runner for `collect` and the view's refresh · P2

- **Findings:** cmd-04, tui-03. cmd-04 is chosen over tui-03's collectOptions, because it also folds runCollect's recover and deletes collectNow.
- **Files:** `cmd/ai-usage/main.go` (collect.go after E1), `interactive.go`, `main_test.go`.
- **Change:**
  - Add `type collection struct { d state.Dir; offline, scheduled bool; waiting func(); locked func(*state.State) }` and `func (c collection) run(ctx context.Context) (res *collect.Result, err error)`.
  - run registers the rescue defer first, then the recover defer moved from runCollect, so a panic becomes `err` before the rescue reads it.
  - It loads the config and builds the Options: Dir, Version, Probe, Hostname, OSUser and Now. Relay is set when a relay URL is configured and the run is not offline. Scheduled runs set PullEvery to an hour; other runs set Wait to lockWait and Waiting to c.waiting.
  - After runs housekeeping, marks it done, and calls c.locked.
  - Delete runCollect and collectNow. cmdCollect builds a collection, with the stderr "another run … waiting for its result" line and the GuideDue take as closures, and keeps its comments.
  - The view's Refresh calls `collection{d: d, offline: offline}.run(ctx)`.
  - lockWait becomes a const.
- **Tests:** TestStopAfterSample calls `collection{d: state.Dir(d.dir)}.run(ctx)`.
- **Behavior:** none in normal runs. Two edge cases change: a waited run whose report fails to print no longer runs rescue (updateFloor made that a no-op), and a panic in LoadConfig or deviceName is now recovered and rescued. **Risk:** medium. **Net:** −30. **Depends on:** C10.
- **Check:** `go test -count=1 -race ./cmd/ai-usage/ -run 'TestCollectWhileAnotherRunCollects|TestUpdateWhenCollectionFails|TestPanicIsRecordedAndStillUpdates|TestStopAfterSample|TestQuitDuringRefresh|TestGuideAfterInstall|TestTwoDevicesShareATeam|TestUpdateCheckEveryScheduledRun'`.

#### C12. Drop NewServer's Limits parameter · P2

- **Findings:** relay-09.
- **Files:** `relay/server.go`, `relay/server_test.go`; `api/relay.go`; `cmd/ai-usage/main.go`, `alias_test.go`, `main_test.go`; `internal/collect/team_test.go`.
- **Change:** `func NewServer(store Store) *Server` uses `DefaultLimits()`. Delete withDefaults. Update the 7 outside call sites: main.go:869, api/relay.go:48, alias_test.go:214, main_test.go:821, 1288 and 1605, and collect/team_test.go:30. Keep allow()'s `max(int64(window/time.Second), 1)` guard and its comment.
- **Tests:** delete TestNewServerFillsUnsetLimits. `newRelay(t)` drops its limits argument, and each test sets the fields it needs, for example `e.srv.Limits.DevicesPerTeam = 2`. TestDeviceCap also sets RecordTTL to 24h.
- **Behavior:** none. **Risk:** low. **Net:** −40. **Depends on:** none.
- **Check:** `go build ./... && go test ./relay/ ./api/ ./internal/collect/ ./cmd/ai-usage/`.

#### C13. Draw `ai-usage status` and the guide with the page renderer · P2

- **Findings:** view-render-03 (the safety version), view-render-04, dup-17 (not taken; see Owner decision 6).
- **Files:** new `internal/view/card.go`; `ui.go` (deleted), `status.go`, `guide.go`, `format.go`, `paint.go`, `render.go`, `attention.go`, `projects.go`.
- **Change:**
  - card.go: `type card struct { *page; lines []chunks }`. newCard keeps the card's own marks on its page copy: ✕ for a failure unless ASCII, and " - " as the ASCII separator. It has emit, blank, String and `clockAt(t, layout)`.
  - One `hang(lead chunks, parts []string, ink)` routine serves para, kv, kvLead and the former kvIndent and kvPath callers. Move wrapWords and wrapAfter to card.go.
  - paint.go gets the marks ok, warn, partial and staged; skip becomes marks.none. It also gets `(p *page) ink(s string, fg color.Color, bold bool) chunk`, which paints with lipgloss.Red, Green, Yellow, Cyan and BrightBlack. So the 16 base colors stay, and there is no bold without color.
  - format.go gets `wrapItems(items []string, sep string, first, rest int) []string`, used by the status problems and by the legend's wrap.
  - StatusText and Guide become `c := newCard(&r, o); …; return c.String()`. status, sources, stamp and homeName become card methods.
  - Move minWidth, maxWidth, homeOf and claudeAppHome to render.go, and failed to attention.go.
  - Delete ui.go and the old style, seg, line and glyphs code in format.go.
- **Tests:** team-status-80 and single-status-80 are unchanged. The ✕ expectations at view_test.go:1238-1243 and main_test.go:627 stay.
- **Behavior:** the only byte change is the SGR reset in `ai-usage status` and the guide: `\x1b[0m` becomes `\x1b[m`. Nothing on screen changes. **Risk:** medium. **Net:** −165. **Depends on:** A3, C6.
- **Check:** `go test ./internal/view ./cmd/ai-usage`. Compare `go run ./cmd/ai-usage status --color=always | cat -v` before and after: only the reset differs. Check again with NO_COLOR and `--ascii`.

#### C14. Write `ago` on top of `dur` · P2

- **Findings:** view-render-05.
- **Files:** `internal/view/format.go`, `render.go`, `header.go`, `format_test.go`.
- **Change:** Delete `span`. `ago(d)` returns "just now" under a minute. From 10 days it truncates to days, and from 10 hours to hours; then it returns `dur(d) + " ago"`. Move dur next to age and ago. Rename header.go's closure `ago` to `since`, so nothing shadows the function.
- **Tests:** TestDurations drops the span field. Every row gives the same output.
- **Behavior:** none. **Risk:** low. **Net:** −18. **Depends on:** C13 (the card uses ago).
- **Check:** `go test ./internal/view -run 'TestDurations|TestGolden|TestCollectorSection'`. The status goldens keep "(2h 12m ago)".

#### C15. Multi-key sorts as `slices.SortFunc` with `cmp.Or` · P2

- **Findings:** view-team-09, dup-13.
- **Files:** `internal/view/team.go`, `build.go`, `devices.go`; `internal/collect/collect.go`, `discover.go`, `doc.go`; `internal/logs/codex.go`, `claude.go`; `cmd/ai-usage/alias.go`.
- **Change:** Convert `sort.Slice` to `slices.SortFunc`, and `sort.SliceStable` to `slices.SortStableFunc`, returning `cmp.Or(...)` with the same keys:
  - descending keys use `cmp.Compare(b.X, a.X)`, and times use `a.T.Compare(b.T)`;
  - sites: collect.go:266, discover.go:67, doc.go:176, 327 (SortAccounts) and 445 (aliases), logs/codex.go:212, logs/claude.go:119, view/build.go:168, view/team.go:284 (buildTeam's device sort), 673 (matrix), 712 (sortTeamAccounts), 803 (linkedList) and 818 (sortPerDevice), devices.go:36, and alias.go:217, with C6's `slices.Index`;
  - a bool key keeps one explicit `if a.X != b.X { … }` before the cmp.Or (discover.go:69, doc.go:332, team.go:285, logs/claude.go:126);
  - leave attention()'s sort (attention.go:101-119, a switch on kind) and the one-key closures. C26 converts doc.go:119 and 185, and D18 replaces probe/codex.go's id sort (319-325).
- **Tests:** none change.
- **Behavior:** none; every key set is a total order. **Risk:** low. **Net:** −48. **Depends on:** B1, C6.
- **Check:** `go test ./internal/... ./cmd/...` with goldens unchanged. `dupl -t 60 ./...` reports nothing.

#### C16. Shared DEVICES helpers for the grid and the status view · P2

- **Findings:** view-team-12 (the value version).
- **Files:** `internal/view/devices.go` (the grid and usage()), `devicestatus.go`.
- **Change:**
  - Delete devicestatus.go:43-52, the loop that adds rows for devices missing from the matrix, with its `has` map. No device reaches it.
  - Add `deviceLead(r Row, nameW int)`, `totalLead(w int)`, `groupHead(name string, span int)` and `pillsAt(t, pills chunks, edge int) (chunks, bool)`. The grid, the status view and usage use them.
  - deviceStatus draws the header, rows and TOTAL with one `line` builder.
  - Split deviceStatus into `statusColumns(rows, devs, grand)` and `fitStatus(cols, note, devs, lead)`.
  - Add no nameWidth, grandTotal or heading struct.
- **Tests:** none change.
- **Behavior:** none. **Risk:** medium. **Net:** −40. **Depends on:** B1, A12.
- **Check:** `go test ./internal/view ./internal/tui`. `go test ./internal/view -update` leaves every golden unchanged, and gocyclo lists no part of deviceStatus over 15.

#### C17. Measure key-bar items by drawing them · P2

- **Findings:** tui-06.
- **Files:** `internal/tui/keybar.go`.
- **Change:** Delete item.width and barWidth. Move keyBar's drawing into `func (m Model) drawBar(its []item) string`, with the doc "drawBar draws its keys: a leading space, and ` · ` between keys." keyBar tests `ansi.StringWidth(m.drawBar(its)) > m.width` in both places, and ends `return fit(m.drawBar(its), m.width)`.
- **Tests:** none change. TestKeyBar and TestDeviceViews compare exact bars at every breakpoint.
- **Behavior:** none; the measure is the drawing. **Risk:** low. **Net:** −11. **Depends on:** A5.
- **Check:** `go test ./internal/tui`. keyBar's gocognit drops from 28 to 16.

#### C18. A per-run sampler instead of 7 to 9 values passed around · P2

- **Findings:** collect-06.
- **Files:** `internal/collect/collect.go`.
- **Change:** Add `type sampler struct { Options; st *state.State; now, since, prevRun time.Time; growth map[string]snapshot.Tokens; paths paths; quotaFrom map[string]map[string]string }`, built in Run after the waited-run check. It holds today's lines 227-234.
  - Delete the hidden `quotaFrom` and `paths` fields from Options.
  - collectSource, collectProvider, linkedAccount, linkHermes and linkGrowth become the methods `source`, `provider`, `linkedAccount`, `linkHermes` and `linkGrowth`.
  - attribute, applyReading, applyRejected, claimUnknown and prune keep their signatures, since doc_test calls them.
  - syncTeam is called with `s.Options`.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −20. **Depends on:** B2, B3, C7.
- **Check:** `go build ./... && go test ./internal/collect/`.

#### C19. `fsutil.RealPath` and `fsutil.Tilde` · P2

- **Findings:** dup-03 (the value version), dup-04, cmd-10. dup-04's `fsutil.Tilde` is chosen over cmd-10's `collect.Tilde`, because the leaf package needs no new import edge.
- **Files:** `internal/fsutil/fsutil.go`; `internal/probe/probe.go`, `claude.go`; `cmd/ai-usage/main.go`, `home.go`; `internal/selfupdate/selfupdate.go`; `internal/logs/hermes.go`; `internal/collect/collect.go`; `internal/view/render.go`.
- **Change:**
  - `RealPath(p string) string` returns the EvalSymlinks result, or p. Delete probe.realPath with its comment (probe.go:388-394) and use RealPath at probe.go:349 and 352 (bins) and claude.go:433 (claudeVersion). cmd `executable()` stays, with the body `exe, err := os.Executable(); if err != nil { return "", err }; return fsutil.RealPath(exe), nil`. selfupdate.fill (126-135) keeps its error check and sets `u.Exe = fsutil.RealPath(exe)` in place of its EvalSymlinks block (131-133). hermes.go:39-41 and collect.go:1220-1223 use it too.
  - `Tilde(p, home string) string` returns p for an empty home, "~" for the home itself, and "~"+rest under the home with `/` or `\`. Delete cmd `tilde` (home.go:351-356). home.go, collect.homeErrors and `page.path` call fsutil.Tilde.
  - cmd/home.go's samePath stays: it resolves links on purpose.
- **Tests:** none change.
- **Behavior:** none. On Unix a home followed by a backslash would now shorten, which real paths never have. **Risk:** low. **Net:** −40. **Depends on:** C4.
- **Check:** `go build ./... && GOOS=windows go vet ./...`, then `go test ./internal/probe/ ./internal/selfupdate/ ./internal/logs/ ./internal/collect/ ./internal/view/ ./cmd/...` with goldens unchanged. `/usr/bin/grep -rn 'realPath(' internal/probe` prints nothing.

#### C20. Small probe cleanups · P3

- **Findings:** dup-06 (the DefaultHome half), probe-07, probe-08.
- **Files:** `internal/probe/probe.go`, `claude.go`, `codex.go`, `grok.go`, `codex_test.go`; `internal/collect/discover.go`, `collect.go`; `cmd/ai-usage/home.go`.
- **Change:**
  - Add `probe.DefaultHome(userHome, provider string) string` to probe.go, "" for an empty userHome. Delete isDefaultHome (claude.go:69-74); its probe callers (claude.go:90, codex.go:59) compare with DefaultHome. discover.go:40-43 and 214, collect.go:1288-1291 and home.go:201 and 233 call it too. samePath stays unexported, since C7 removed collect's copy.
  - Grok: add `newestUser(keep func(grokAuth) bool) string` for the two newest-sign-in loops. Delete grokBilling.at, and let grokBillingLine set `pid`.
  - Stop sanitizing in probe what collect sanitizes again:
    - claudeLimitName returns raw names, without the PlainLabel calls at claude.go:763 and 771;
    - grokBillingLine uses `c.SubscriptionTier`;
    - codexLimits skips only nil windows, dropping the MaxWindows cap at probe/codex.go:343;
    - printedLines (probe/codex.go:143-157) uses `strings.TrimSpace(snapshot.Printable(line))` in place of its control-character map. Printable maps control characters to spaces the same way and also drops hidden ones.

    Keep `snapshot.PlainLabel` on the Codex account type, which collect does not sanitize.
- **Tests:** delete TestCodexLimitsCapsWindows; collect_test.go:1312 caps windows.
- **Behavior:** none; applyReading sanitizes the same fields. **Risk:** low. **Net:** −46. **Depends on:** C7.
- **Check:** `go build ./... && GOOS=windows go vet ./...`, then `go test ./internal/probe/ ./internal/collect/ ./cmd/...`, including TestLastLineSaysTheError. `/usr/bin/grep -rn 'isDefaultHome' internal cmd` prints nothing.

#### C21. Validate each PUT once, and share the relay request builder · P3

- **Findings:** relay-05, relay-21.
- **Files:** `internal/snapshot/snapshot.go`, `snapshot_test.go`; `relay/server.go`, `relay/client.go`; `internal/team/team_test.go`; `internal/collect/collect_test.go`, `hermes_test.go`.
- **Change:**
  - `func (d Doc) Validate() error` has no clock and no future branch. Add `func (d Doc) FromFuture(now time.Time) bool`. put() rejects `doc.FromFuture(s.now())` with "collected_at is in the future", then runs the canonical check as now.
  - Add `(c *Client) request(ctx, method, device string, body []byte)`. Publish and signed use it and set their own headers.
- **Tests:** callers use `Validate()` with no argument. TestValidateFutureCollectedAt becomes TestFromFuture: the same five rows, asserting `d.FromFuture(t0) == !c.ok`.
- **Behavior:** none; TestPutRejects "collected in the future" is still 422 with the same message. **Risk:** low. **Net:** −16. **Depends on:** A1, A2.
- **Check:** `go test ./relay/ ./api/ ./internal/snapshot/ ./internal/team/ ./internal/collect/`, with TestWireFormat unchanged.

#### C22. Small view cleanups · P3

- **Findings:** view-team-03, view-team-04, view-team-05, view-team-10, view-render-06.
- **Files:** `internal/view/forecast.go`, `build.go`, `team.go`, `subscriptions.go`, `attention.go`, `render.go`, `header.go`, `devices.go` (usage.go), `status.go`.
- **Change:**
  - Move `known()` beside mainWindow and add `knownMain(q *Quota) *Window`, used in buildTeam's provider loop (team.go:262), in the matrix (608), and in `left()`.
  - Add `quotaOf(q Quota, provider string, rs []reading, at, now time.Time) (*Quota, string)` and delete quotaView. build.go and teamAccount.quota call it.
  - Build: drop values that attention(), buildTeam, teamName, matrix(), names() and linkedList() always overwrite. Move the account literal into `accountView(st, provider, homes, a, now) Account`.
  - Idioms:
    - `Duration.Abs` replaces absDuration;
    - `stateOrder` and `attentionOrder` slices with `slices.Index` replace the rank switches;
    - sameReading uses `slices.EqualFunc`;
    - `slices.ContainsFunc` replaces anyStale;
    - `mainIndex(rs []reading)`;
    - `x.ta.Link = cmp.Or(x.local, x.qLink)`.
  - Add `shownLabel(a *TeamAccount) string` next to shortID for the header, subscriptions and usage. homeName reuses shortID.
- **Tests:** TestMainWindow calls `mainIndex(readings(ws, now))`.
- **Behavior:** none. **Risk:** low. **Net:** −57. **Depends on:** B1, C13.
- **Check:** `go test ./internal/view ./internal/tui` with goldens unchanged, including TestRefusalReading, TestSubscriptionsGoWorstFirst, TestAttention, TestTeamLinkFromTheWire, TestJSONFieldNamesAreStable and TestShortNames.

#### C23. Small CLI helpers · P3

- **Findings:** tui-07, cmd-05 (the value version), cmd-08.
- **Files:** `cmd/ai-usage/term.go`, `main.go`, `home.go`, `alias.go`.
- **Change:**
  - `(d *display) parse(fs, args)` runs the flag parse and then the color and width checks. It replaces the pair at the three drawing commands.
  - Add `subcommand(args) (string, []string)`, used by cmdTeam, cmdRelay, cmdName and cmdHome, and `loadConfig() (state.Dir, state.Config, error)`, used by cmdRelay, cmdName, cmdSchedule and cmdAlias. Delete `dir()` and call `state.DefaultDir()`. Add no noArgs.
  - Add `tabulate(rows [][]string) string`, with the same tabwriter settings and trailing spaces trimmed, for listHomes, the alias table and the alias list.
- **Tests:** none change.
- **Behavior:** none; `ai-usage home` and `ai-usage alias` output is byte-identical. **Risk:** low. **Net:** −33. **Depends on:** C10.
- **Check:** `go test -count=1 ./cmd/ai-usage/`. Compare `ai-usage home` and `ai-usage alias` on a test device before and after.

#### C24. Share the alias label and newest-wins rules through `snapshot` · P3

- **Findings:** cmd-09.
- **Files:** `internal/snapshot/alias.go`; `cmd/ai-usage/alias.go`; `internal/view/team.go`.
- **Change:** Add `snapshot.LabelKey(label string) string` (lower case when the label holds an `@`) and `snapshot.AliasWins(at time.Time, device string, cur time.Time, curDevice string) bool`. Delete cmd's sameLabel and person. alias.go and view's aliasKey and alias merge use them.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −8. **Depends on:** A1.
- **Check:** `go test ./internal/snapshot/ ./internal/view/ ./cmd/ai-usage/ -run 'Alias|Matrix|Team|WireFormat'`.

#### C25. State file names and the empty state defined once · P3

- **Findings:** dup-15, cmd-11, dup-18. dup-15's `Dir.StateFile()` and `Dir.TeamCacheFile()` are chosen over cmd-11's exported collect constant, because they sit beside KeyFile and SamplesDir.
- **Files:** `internal/state/state.go`; `internal/collect/team.go`, `doc_test.go`; `cmd/ai-usage/main.go`, `interactive.go`; `internal/view/helpers_test.go`; `scripts/demo/main.go`.
- **Change:**
  - Add `(d Dir) StateFile()` and `(d Dir) TeamCacheFile()`. LoadState, SaveState, collect/team.go (drop teamCacheFile), main.go:838 and interactive.go:68 use them.
  - Add `state.NewState()` and an unexported `fill()`, which LoadState calls after decoding and on the damaged-file path. view's emptyState, the empty-map literals in collect/doc_test.go, and the demo use it. Leave literals with session contents alone.
- **Tests:** as listed.
- **Behavior:** none. **Risk:** low. **Net:** −23. **Depends on:** none.
- **Check:** `go test ./internal/state/ ./internal/collect/ ./internal/view/ ./cmd/...`, and `go run ./scripts/demo <tmp>` diffed against `docs/demo`.

#### C26. Collect: relay block, totals, Hermes parts and hours · P3

- **Findings:** collect-05, collect-10, dup-12, collect-12, dup-10. collect-10's embedded `usage` struct is chosen over dup-12's `ProjectTotals.add`, because it serves accounts and projects alike.
- **Files:** `internal/collect/team.go`, `collect.go`, `doc.go`, `doc_test.go`; `internal/logs/logs.go`, `rollup.go`.
- **Change:**
  - Add `keepReadError(st, c TeamCache)` and `(s *sampler) publish(ctx, key, device, res) string`, which holds Run's relay block. Run appends a non-empty result to its problems.
  - Add `type usage struct { Sessions int; Tokens snapshot.Tokens; Hours map[int64]int64; LastActive time.Time }` with `add(tok, hours, last)`, embedded in AccountTotals and ProjectTotals. Add `sortProjects`, and convert the providers and aliases sorts, with slices.SortFunc and cmp.Or. SortAccounts stays.
  - Add `partsOf(s logs.Session, label string) map[string]snapshot.Tokens`. attribute and linkHermes use it, and hermesParts goes.
  - Add `logs.AddHours(a, b)`. Delete collect.addHours and logs.addHours; their callers use AddHours.
- **Tests:** TestSortAccounts literals use `usage: usage{Tokens: tok(1000)}`.
- **Behavior:** none; `ai-usage report --json` on a copied state folder is unchanged. **Risk:** low. **Net:** −51. **Depends on:** C18, B3, C1.
- **Check:** `go build ./... && go test ./internal/collect/ ./internal/logs/ ./internal/view/ ./cmd/ai-usage/`.

#### C27. Logs: gateway loop, optional columns, time parsing, InOut and the querier · P3

- **Findings:** logs-04 (the value version), logs-05, logs-06 (the value version), logs-07, logs-10.
- **Files:** `internal/logs/logs.go`, `hermes.go`, `grok.go`, `scan.go`, `codex.go`; `internal/snapshot/snapshot.go`; `internal/collect/collect.go`, `doc.go`, tests; `internal/view/team.go`.
- **Change:**
  - Keep ReadHomes' switch. Merge the Hermes gateway loop into the fitHours loop, and keep the gateway comment.
  - Add `columnOr(cols map[string]bool, name, expr, fallback string) string` and build both Hermes select lists with it. Delete the optional and count closures, and keep the comments on optional columns, time reading and the typeof split.
  - Move parseTime to scan.go and add `looseTime(v any) time.Time` for Unix seconds, milliseconds and ISO strings, keeping the year-5138 comment.
    - grok: `grokLine.Timestamp any`, and delete the unixTime type.
    - hermes: delete hermesTime; `hermesSessionUsage.last` becomes a `time.Time`.
    - Drop the math and strconv imports.
  - Add `snapshot.Tokens.InOut()` and delete logs.InOut. view/team.go drops its logs import.
  - Delete the Hermes querier interface: the readers take `*sql.Tx`, and one dbState per attempt flows into hermesURI and snapshotSQLite.
- **Tests:** none change. TestGrokHours, TestHermesTimesAsText, TestHermesActivityTime, TestHermesSplitsUsageByBillingProvider, TestHermesOlderSchemas, TestHermesUsageOlderSchemas, TestHermesUnusableDatabase and TestHermesReadsInPlace pass.
- **Behavior:** none for valid data. Garbage values only: a Hermes number of 1e11 or more reads as milliseconds, and Grok numeric strings now parse. **Risk:** medium. **Net:** −51. **Depends on:** A4, A6, C2.
- **Check:** `go build ./... && go vet ./... && go test -count=1 ./internal/logs/ ./internal/collect/ ./internal/view/ ./internal/snapshot/`, with goldens and TestWireFormat unchanged. `go run mvdan.cc/unparam@latest ./internal/logs/` prints nothing.

#### C28. One silence threshold: `collect.SilentAfter` · P3

- **Findings:** critic-03 (without its latestVersion part; see section 8).
- **Files:** `internal/collect/team.go`; `internal/view/report.go`, `team.go`.
- **Change:** collect's `silentAfter = 24 * time.Hour` (team.go:40-42), whose doc says it is how long before the views call a device silent, and view's `SilentAfter = 24 * time.Hour` (report.go:21-22) are one rule kept twice. Rename collect's to the exported `SilentAfter`, keep its doc, and use it in behindSince (team.go:214). Delete view.SilentAfter and its comment line from the const block. buildTeam's device literal (view/team.go:124) becomes `Silent: now.Sub(d.CollectedAt) > collect.SilentAfter,`. view already imports collect. latestVersion and behindSince's version loop stay: one walks devices and the other snapshots.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −2. **Depends on:** none.
- **Check:** `go build ./... && go test ./internal/collect/ ./internal/view/ ./internal/tui/`, goldens unchanged. `/usr/bin/grep -rnE '\b[Ss]ilentAfter\b' --include='*.go' internal cmd scripts` lists only the definition in collect/team.go, its use in behindSince, and view/team.go's use.

#### C29. One text cut with an ellipsis: `snapshot.Truncate` · P3

- **Findings:** critic-05.
- **Files:** `internal/snapshot/snapshot.go`; `internal/collect/collect.go`, `doc.go`; `internal/selfupdate/selfupdate.go`.
- **Change:** selfupdate.reason's cut (selfupdate.go:458-464) is collect.truncate (collect.go:1518-1528) line for line: it keeps s when it fits in n bytes, else cuts at `n - len("…")`, steps back to a rune start and appends "…".
  - Add `func Truncate(s string, n int) string` to snapshot.go next to Printable, with collect.truncate's body and the doc "Truncate keeps the start of s in at most n bytes, cut on a rune boundary, ending in … when cut." snapshot imports unicode/utf8.
  - Delete collect.truncate. Its callers (collect.go:295, 396, 545 and 1515, and lastError at doc.go:358) call snapshot.Truncate. Drop collect.go's unicode/utf8 import (23), which nothing else there uses.
  - reason's lines 458-464 become `s = snapshot.Truncate(s, maxReason)`. selfupdate imports internal/snapshot, which imports only the standard library, and drops unicode/utf8 (30).
  - probe.truncate (no ellipsis, Owner decision 7) and collect's clip (doc.go:515, keeps the end) stay.
- **Tests:** none change. TestRefusalReason "long" and collect's doc tests cover the cut.
- **Behavior:** none. **Risk:** low. **Net:** −9. **Depends on:** none.
- **Check:** `go build ./... && GOOS=windows go vet ./internal/selfupdate/ ./internal/collect/`, then `go test ./internal/selfupdate/ ./internal/collect/ ./internal/snapshot/`. `/usr/bin/grep -rn 'utf8.RuneStart' --include='*.go' internal` lists only snapshot.go and probe's truncate.
### Phase D. Shorter functions

Phase D breaks up the functions that gocyclo lists over 15 or gocognit over 20. Each step moves lines into named functions and keeps the order of every effect. Output does not change, except for the error texts named in D4. Earlier phases move the line numbers, so each step also names the code it moves.

#### D1. buildTeam becomes a merger with one method per step · P2

- **Findings:** view-team-06, with the release-check parts v0.2.5 added to buildTeam (critic issue on D1 and D7).
- **Files:** `internal/view/team.go`.
- **Change:** buildTeam (team.go:75-295; gocognit 124) becomes a short driver. Add `type merger struct { in Input; now time.Time; local map[string]*Link; byProv map[string]map[string]*teamAccount; linked map[string]map[string]*LinkedUsage; devs []*device; aliases map[string]snapshot.Alias; aliasBy map[string]string; behind map[string]collect.Behind }` with these methods:
  - `raw(s) string` and `open(s) string`, the two closures at 88-96, with raw's comment;
  - `addDoc(d snapshot.Doc)`, which calls teamDevice, addAliases and addAccounts;
  - `teamDevice(d snapshot.Doc) *TeamDevice`, the device literal and its sources (111-133). It calls `lastErrors(d, this)`, which owns the one `snapshot.SplitLastError(m.raw(d.LastError))` call (112) and returns the printable LastError and, for another device only, the UpdateError (122, 126-129, with the comment that this device's own update shows in the header and ATTENTION);
  - `addAliases(d)` (136-149);
  - `account(provider, label) *teamAccount`, the get-or-create at 164-179;
  - `addAccounts(dv *device, d snapshot.Doc)` (151-226, minus the pieces below);
  - `hermesActivity()` (228-238);
  - `markOld(latest *string)`: the loop at 241-247 that sets Old and, when the team cache's `behind` entry names the device's release, BehindSince;
  - `provider(p string) (TeamProvider, bool)` (249-279).

  Add `func (x *teamAccount) addReading(a snapshot.Account, dev string, link *Link)` for the window merge at 206-219. buildTeam keeps the doc selection with PulledAt and `m.behind = in.Team.Behind` (76-87), `for d: m.addDoc(d)`, m.hermesActivity(), `t.Latest` (240) and m.markOld, the provider loop, the matrix and the device sort, in about 35 lines.
- **Tests:** none change.
- **Behavior:** none. **Risk:** medium. **Net:** +18. **Depends on:** B1, C22, C28.
- **Check:** `go test ./internal/view ./internal/tui ./cmd/ai-usage` with goldens unchanged, including TestTeamDeviceUpdate. `gocyclo -over 15 ./internal/view` and `gocognit -over 20 ./internal/view` list neither buildTeam nor a merger method.

#### D2. collect.Run as a list of named steps · P2

- **Findings:** collect-07. Both verifiers gave a version. The value version's names and signatures are used, and the safety version's order of effects is the constraint.
- **Files:** `internal/collect/collect.go`, `discover.go`.
- **Change:** Run (collect.go:161-341) becomes about 50 lines:
  1. `func (o *Options) lock(ctx context.Context) (unlock func(), waited bool, lastRun time.Time, err error)`: lines 163-174.
  2. Load the config, the key and the state, then `homes := Discover(...)`.
  3. `rememberHomes(o Options, cfg state.Config, homes map[string][]string) (state.Config, error)` in discover.go: lines 194-214 (unremembered, the Remember and RememberEnv closure, EditConfig).
  4. `inputs := runInputs(o, cfg, homes)`, then `if waited { if res := reuseWaited(o, cfg, key, st, inputs, lastRun); res != nil { return res, nil } }`. reuseWaited holds 216-226, keeps its own BuildDoc at st.LastRunAt, and returns nil when this run must collect.
  5. Build the sampler (C18).
  6. `problems, failed := s.collect(ctx, homes)`: lines 235-250 (Damage, the source loop, prune).
  7. `sample := sampleOf(st, s.growth, now)`: lines 252-272, sorted with `slices.SortFunc` and `cmp.Or(strings.Compare(a.Provider, b.Provider), strings.Compare(a.Label, b.Label))`.
  8. `noteProblems(st *state.State, problems []string, now time.Time)` replaces the closure at 293-298.

  The effects keep this order:
  1. the ctx.Err() check before AppendSample and PruneSamples;
  2. LastRunAt, LastRunInputs and LastSuccessAt;
  3. noteProblems;
  4. `BuildDoc` at now, then `res.Team, _ = LoadTeamCache(o.Dir)`;
  5. s.publish (C26), appending its problem and noting again;
  6. After, when ctx is live;
  7. `return res, o.Dir.SaveState(st)`.
- **Tests:** none change.
- **Behavior:** none. **Risk:** medium. **Net:** −15. **Depends on:** C18, C26.
- **Check:** `go test ./internal/collect/ ./cmd/ai-usage/`, including TestRunWaitsForTheRunItOverlaps, TestWaitedRunThatSkippedTheTeamRead, TestAfterHookSeesTheStateBeforeSave, TestRunFailsWhileLocked and TestRunDoesNotBringBackARemovedHome. gocyclo reports Run under 15. On a copied state folder, `ai-usage collect && ai-usage report --json` matches the output from before the step.

#### D3. collectProvider splits into provider, harness and Hermes paths · P2

- **Findings:** collect-08.
- **Files:** `internal/collect/collect.go`, `claudeapp.go`, new `internal/collect/hermes.go`.
- **Change:** Keep one short `s.provider(ctx, p)`: skip, ReadLogs, read problems, then `if p == "hermes" { s.hermes(...) } else { s.harness(...) }`, then applyRejected, keepCurrent and the source status.
  - s.harness holds the Claude apps (through a claudeApps helper in claudeapp.go), lastUses, askAll, labels, homeErrors, claimUnknown, labelling and attribute.
  - s.hermes, in the new hermes.go, holds the label (the account, or Unknown), touch, attribute, linkGrowth, markHermesCurrent, linkHermes and probed.
- **Tests:** none change.
- **Behavior:** none. **Risk:** medium. **Net:** −10. **Depends on:** B3, C18.
- **Check:** `go test ./internal/collect/`, including all of hermes_test.go, orca_test.go and claudeapp_test.go. gocyclo and gocognit report each new function under 15 and 20. `/usr/bin/grep -c 'p == "hermes"' internal/collect/collect.go` prints 1.

#### D4. Snapshot validation as small validators with one list helper · P2

- **Findings:** relay-06. Both versions agree; the text below merges them.
- **Files:** `internal/snapshot/snapshot.go`, `snapshot_test.go`.
- **Change:**
  - Add `func checkList[T any](name string, items []T, limit int, check func(T) error) error`. Past the limit it returns `fmt.Errorf("%d %s, limit %d", len(items), name, limit)`. Otherwise it returns the first item error, wrapped as `fmt.Errorf("%s[%d]: %w", name, i, err)`.
  - Add unexported validators that return today's texts unwrapped:
    - `(w Window) validate`: name, percent (including NaN), minutes;
    - `(p Project) validate`: path, checkCounts;
    - `(r Recent) validate`: window, start, tokens;
    - `(l Linked) validate(owner string)`: provider known and not the owner, label, checkCounts;
    - `(a Alias) validate`: 'unknown provider', label, optional name, 'at is missing';
    - `(s Source) validate`: 'unknown provider', 'unknown status', optional error;
    - `checkDay(n int64) error`: 'day count out of range'.
  - Doc.Validate keeps v, team and device. It then checks device_label and os_user as two sealed checks in that order, replacing the map loop at 274-278. Then come last_error, collector_version, collected_at and the accounts/sources nil check, and checkList for accounts, aliases and sources.
  - Account.validate keeps provider, label, plan, the windows/projects nil check, 'windows without quota_at' and the quota_from rule. It then calls `checkCounts(a.Sessions, a.Tokens)` and checkList for windows, projects, days (checkDay), recent, and linked (`func(l Linked) error { return l.validate(a.Provider) }`).
  - Replace checkSealed and checkPlain with `type textRule struct{ re *regexp.Regexp; max int; what string }`, `sealedText = textRule{sealedRe, MaxSealed, "sealed label"}`, `plainText = textRule{plainRe, MaxPlain, "short label"}` and `func (t textRule) check(name, s string, required bool) error`. The messages stay '%s is missing' and '%s is not a %s'. PlainLabel keeps plainRe.
  - The validators call C6's `KnownProvider`.
- **Tests:** TestDurationName, TestPlainLabel and FuzzPlainLabel (snapshot_test.go:397, 420 and 431) call `plainText.check`. TestWireFormat does not change.
- **Behavior:** two error texts change, and only for documents a release never sends. A bad day count now reads 'days[i]: day count out of range'. When a document breaks several rules, the first one reported can differ: 9 windows without quota_at now report 'windows without quota_at'. The relay returns 422 in both cases, as before. **Risk:** low. **Net:** −30. **Depends on:** A1, A2, C6, C21.
- **Check:** `go test ./internal/snapshot/ ./relay/`. gocyclo -over 15 and gocognit -over 20 list no snapshot function.

#### D5. Server.put splits along its seams, with `*ErrStatus` as the handler result · P2

- **Findings:** relay-10. Both versions agree; the text below merges them.
- **Files:** `relay/server.go`, `protocol.go`.
- **Change:** Move ErrStatus to protocol.go. The server uses the concrete `*ErrStatus` as its failure value, never through the error interface.
  - Add `errUnavailable = &ErrStatus{Code: 503, Msg: "store unavailable"}` and `errTeamFull = &ErrStatus{Code: 403, Msg: "team has the most devices allowed"}`. They replace 9 and 3 literals.
  - Handlers have the type `func(http.ResponseWriter, *http.Request) *ErrStatus`. `s.route(pattern, h)` replaces perIP. It runs `s.allow(w, r, "ip:"+s.clientKey(r, 64), s.Limits.RequestsPerIP, s.Limits.Window)`, then h, and calls `fail(w, e.Code, e.Msg)` on a non-nil result. Health writes its JSON and returns nil.
  - `allow(w, r, key, limit, window) *ErrStatus` returns nil, errUnavailable, or `&ErrStatus{429, "rate limit"}` after it sets Retry-After.
  - `verifyRequest(r, teamFP, device) *ErrStatus` replaces signedRequest, with the same codes and texts. A publicKey error becomes `&ErrStatus{403, err.Error()}`.
  - `devicePath(r) (teamFP, device string, e *ErrStatus)` returns 404 'no such team or device'. put and del use it.
  - Split put:
    - `readSnapshot(w, r, teamFP, device) (Record, snapshot.Doc, *ErrStatus)`: lines 232-268. It covers the key, the read deadline, the body, the size (413), the signature (401), Decode, FromFuture and the canonical check (422 with err.Error()), and the team and device match (422). It returns `Record{Body, Sig}`.
    - `putAgain(ctx, teamFP, device string, rec Record, doc snapshot.Doc, prev *Record) *ErrStatus`: lines 284-302, then Put with `rec.Since = prev.Since`. An identical body returns nil without a write.
    - `putNew(w, r, teamFP, device string, rec Record) *ErrStatus`: lines 304-323, then Put with `rec.Since = s.now()`, then the pastCap block (330-351) under `context.WithoutCancel`. On a pastCap error it deletes the record and returns errUnavailable.
    - put itself: devicePath, readSnapshot, the team allow, Get (errUnavailable), the dispatch on prev, then writeJSON `{"stored":true}`.
- **Tests:** none change.
- **Behavior:** none; every status code, message and Retry-After value stays. **Risk:** medium. **Net:** −20. **Depends on:** C12, C21.
- **Check:** `go test ./relay/ ./api/ ./internal/collect/ ./cmd/ai-usage/`, with TestPutRejects, TestSignedRead, TestDelete, TestTeamWriteLimit, TestStoreFailuresAre503 and the cap tests unchanged. gocyclo -over 15 ./relay lists no non-test function.

#### D6. matrix(): columns without the −1 sentinel, in their own function · P3

- **Findings:** view-team-07 (the value version).
- **Files:** `internal/view/team.go`.
- **Change:** In matrix (team.go:594), replace the −1 sentinel block (617-635) with `noQuota := map[string]bool{}`, set for a.provider on every device account where `billsTo(a, byProv) == nil`. Then `for _, p := range snapshot.Providers { if noQuota[p] { index[colKey{p, "", true}] = len(cols); cols = append(cols, Column{Provider: p, Name: p, NoQuota: true, State: StateUnknown}) } }`. Move colKey to package level. Extract 595-635 into `func matrixColumns(providers []TeamProvider, byProv map[string]map[string]*teamAccount, devs []*device) ([]Column, map[colKey]int)`. matrix() keeps the row loop, the column totals, the shares and the sort. Add no matrixRow or setShares.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −8. **Depends on:** B1, C6.
- **Check:** `go test ./internal/view` (TestMatrix, TestMatrixSplitsHermesByLogin, TestTeamLink*) with goldens unchanged.

#### D7. attention() splits into quota, device and order parts · P3

- **Findings:** view-team-11 (the value version).
- **Files:** `internal/view/attention.go`.
- **Change:** Move 27-64 into `quotaAttention(t Team, now time.Time) []Attention`. There, replace the two `pct := w.Forecast.Percent; at.Percent = &pct` copies with one after the switch: `if at.Kind != AttentionOut { pct := w.Forecast.Percent; at.Percent = &pct }`. Move 65-91 into `deviceAttention(t Team, c Collector) []Attention`. attention() becomes `out := append(append([]Attention{}, quotaAttention(t, now)...), deviceAttention(t, c)...)`, which stays non-nil for a JSON `[]`, followed by the existing sort.SliceStable with C22's attentionOrder. Add no windowAttention or attentionLess.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** +6. **Depends on:** C22.
- **Check:** `go test ./internal/view` (TestAttention, TestAttentionOverAtResetOrder, TestHeaderFailuresAreInAttention, TestFailedRunIsInAttention, TestWindowThatHasResetIsUnknown) with goldens unchanged.

#### D8. grid() splits along its layout seams · P3

- **Findings:** view-team-13 (the value version).
- **Files:** `internal/view/devices.go`.
- **Change:** Extract `(p *page) fitColumns(cols []gridCol, lead, totalW int) (shown []int, hidden, edge int)` from 160-193. It also sets p.matrixColumns and p.matrixShown. Make `more(n int) string` a package func, since the headings' "+N more" uses it too. Extract `(p *page) gridTitle(rows, edge int) chunks` from 195-211, using C16's pillsAt. value, text and totalOf stay closures in grid. Add no gridCells struct, gridColumns or gridNames.
- **Tests:** none change.
- **Behavior:** none. **Risk:** medium; grid decides what fits on a narrow terminal. **Net:** −5. **Depends on:** B1, C16.
- **Check:** `go test ./internal/view ./internal/tui` (TestMatrixMore, TestPageHeat, TestWidths, TestPageFits, TestPinnedHead*) with goldens unchanged.

#### D9. subscriptions() and bar() split; subHeader reads a column table · P3

- **Findings:** view-render-07.
- **Files:** `internal/view/subscriptions.go`.
- **Change:**
  - Extract `(p *page) subRows(a *TeamAccount) []subRow` from 87-121. rows[0] is the account's row, with win set to its main window (nil without a quota). Then comes one row for each window where limitsMore holds. subscriptions() calls it for each subscription account, counts a.State and noReading (`!known(rows[0].win)`) as the switch at 93-98 does, and appends the rows.
  - Extract `(p *page) subTitle(total, noReading int, counts map[string]int) chunks` from 128-136.
  - subHeader keeps the bold provider cell, then loops over `[]struct{ head string; w int; right, show bool }{{"PLAN", l.plan, false, l.showPlan}, {"THIS WEEK", l.bar, false, true}, {"LEFT", l.left, true, true}, {"RESETS", l.resets(), false, true}, {"AT RESET", l.at, true, true}, {"USERS", l.users(), false, true}, {"LAST", l.last, true, l.showLast}}`. Each shown column appends `p.space(2)` and `p.right(p.muted(head), w)` or `p.left(p.muted(head), w)`.
  - Extract the pure `func barSplit(w Window, n int) (used, tick int)` from bar() 384-405, with tick −1 when elapsed is unknown. The name barCells is taken by the const at :65. bar keeps the known() check, the drawing and the marks.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −20. **Depends on:** C22.
- **Check:** `go test ./internal/view` (TestBarTickFollowsTheForecast, TestSubscriptionsCountNoReadingByTheMainWindow, TestSubscriptionsGoWorstFirst) with every golden byte-identical. gocognit no longer lists subscriptions.

#### D10. PROJECTS columns carry their own ink; add `p.align` · P3

- **Findings:** view-render-08.
- **Files:** `internal/view/projects.go`, `paint.go`, `devicestatus.go`.
- **Change:**
  - Declare `type projCol struct { head string; right bool; cells []string; w int; ink func(s string, w int) chunk }` at file scope. The period and 90D columns ink with `p.cell(s)`, SESS with `p.plain(s)`, VIA with `p.muted(truncEnd(s, w, g.ell))`. LAST calls `p.mark("none")` when s is g.none and returns `p.muted(s)` otherwise.
  - Extract `(p *page) projectCols(ps []Project, per Period) (cols []*projCol, via *projCol)`. It builds the columns and their cells (32-39 and the cell appends at 48-58) and sets each c.w (the width part of 61-65).
  - projects() keeps the paths and pathW in their own loop over ps (40-47) and sums `rest = Σ(2+c.w)`. It drops VIA with `cols = slices.DeleteFunc(cols, func(c *projCol) bool { return c == via })` and `rest -= 2 + via.w`.
  - Add to paint.go `func (p *page) align(c chunks, w int, right bool) chunks`. Right gives `chunks{p.space(w - c.width())}` followed by c; left gives `c.padTo(w)`. The head loop appends `p.space(2)` and `p.align(chunks{p.muted(c.head)}, c.w, c.right)`. The row loop appends `p.space(2)` and `p.align(chunks{c.ink(c.cells[i], c.w)}, c.w, c.right)`.
  - deviceStatus uses align for its two padding blocks (devicestatus.go:225-230 and 239-244).
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −12. **Depends on:** C16.
- **Check:** `go test ./internal/view`: the projects-all, team-* and single-* goldens are byte-identical, and TestProjectPathsArePrintable, TestPageHeadersDim and TestProjectsSortByPeriod pass.

#### D11. The status card splits into its blocks · P3

- **Findings:** view-render-09 (the value version).
- **Files:** `internal/view/status.go`, or the card file from C13.
- **Change:** Split status (status.go:23-146) into card methods whose bodies are the current lines, unchanged:
  - statusVerdict(): 26-48, the title and the healthy or problems line, using problems() from A3;
  - statusIdentity(dir): 51-60, the version, device, team and directory;
  - statusRun(): 63-83;
  - statusRelay(): 85-105;
  - statusSchedule(): 106-122;
  - statusUpdate(): 123-143.

  status(dir) becomes statusVerdict, blank, statusIdentity, blank, statusRun, statusRelay, statusSchedule, statusUpdate, blank, sources. The locals at line 50 become three card methods, ok(), fail() and warn(), and a package value info. sources() uses ok() and fail(). On C13's card they return chunks through p.ink.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** +8. **Depends on:** C13.
- **Check:** `go test ./internal/view ./cmd/ai-usage`: the status goldens, TestCollectorSection, TestStatusUpdateLine and TestWidths are byte-identical. gocyclo -over 15 no longer lists status.

#### D12. Ledger helpers for claimUnknown, prune and linkHermes · P3

- **Findings:** collect-09. Both verifiers gave a version. The value version is used: it brings the three functions under 20 with four helpers. The safety version's withProfiles, candidates and hermesUses move code without lowering a function over the limit, and keepReadError is already in C26.
- **Files:** `internal/collect/collect.go` (ledger.go after E2).
- **Change:**
  - `moveShare(s *state.Session, from, to string)`: claimUnknown's block at 854-869 (By, ByHours, Last). claimUnknown calls `moveShare(s, UnknownAccount, label)` after its `s == nil` check. The `ok` check on By moves into the helper, which returns early.
  - `dropShare(s *state.Session, label string)`: prune's 1466-1475 (delete By, Last and ByHours, and subtract its hours from Hours).
  - `pruneAccounts(st *state.State, used map[string]bool, cutoff time.Time)`: prune's 1484-1499. prune becomes the session pass, with dropShare, followed by pruneAccounts.
  - `mostUsed(uses map[state.Link]*use) *state.Link`: linkHermes' pick at 1356-1364, with the same tie-break (tokens, then newest, then label). Move `type use` to package level beside it.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −5. **Depends on:** B3, C26.
- **Check:** `go test ./internal/collect/`, including TestClaimTakesTheUnknownHours, TestHermesQuotaFromNamedHome and the prune and hours tests. gocognit reports claimUnknown, prune and linkHermes under 20.

#### D13. fitDoc: one `longest` helper for its two loops · P3

- **Findings:** collect-11.
- **Files:** `internal/collect/doc.go`.
- **Change:** Add `longest(accts []snapshot.Account, n func(snapshot.Account) int, above int) int`. It returns the index of the first account with the largest n(a) above `above`, or −1. Both loops in fitDoc (465-486) call it: `longest(doc.Accounts, func(a snapshot.Account) int { return len(a.Projects) }, 0)` and `longest(doc.Accounts, func(a snapshot.Account) int { return len(a.Days) }, 7)`. In aliases (419-422), fold the Name assignment into the literal: `sa := snapshot.Alias{Provider: parts[0], Label: parts[1], Name: a.Name, At: a.At.UTC()}`. The name is still copied.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −12. **Depends on:** none.
- **Check:** `go test ./internal/collect/ -run 'TestBuildDocFitsTheSizeLimit|TestFitDocDropsAccounts|TestClip|TestDocCarries'`.

#### D14. parseClaude splits along its three line kinds · P3

- **Findings:** logs-11.
- **Files:** `internal/logs/claude.go`.
- **Change:** Add `type claudeParse struct { f *claudeFile; id, parent, own string; index map[string]int; anon, malformed int; sidechainOf string; sawSession bool }`. parseClaude builds it with `own = cmp.Or(parent, id)`, calls `forEachLine(path, p.line)`, applies the sidechain rule (330-333, comment kept) and returns. The methods are:
  - `(p *claudeParse) line(line []byte)`: the marker filter, the unmarshal, then the dispatch;
  - `costState(row claudeLine)`: 270-274;
  - `header(row claudeLine)`: 276-288;
  - `message(row claudeLine)`: 292-328.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** +8. **Depends on:** A6.
- **Check:** `go test -count=1 ./internal/logs/ -run 'Claude|ReadHomes|LineTooLong'`. gocyclo -over 15 ./internal/logs no longer lists parseClaude.

#### D15. countClaude: claimMessages plus the shared merge · P3

- **Findings:** logs-12.
- **Files:** `internal/logs/claude.go`.
- **Change:** Add `func claimMessages(files []*claudeFile) (copied map[string]bool)`. It takes `order := slices.Clone(files)` and sorts it with `slices.SortStableFunc` by start, then native first, then path, keeping the "copy that kept the original times" comment. Then it runs the claim loop from 133-160. Key building moves to `func (m claudeMsg) key(session string) string`, from 138-144. countClaude becomes `copied := claimMessages(files)`, the rows from f.sess, and `return addTracked(mergeByID(rows, addCopy), files, copied)`.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −6. **Depends on:** C1, C15.
- **Check:** `go test -count=1 ./internal/logs/ -run 'Claude|ReadHomesCountsAcrossHomesOnce'`.

#### D16. countCodex: families, per-family counting and the file's row · P3

- **Findings:** logs-13.
- **Files:** `internal/logs/codex.go`.
- **Change:** Add two fields to codexFile, `own Tokens` and `hours map[int64]int64`, with the comment "what this file counts once replays are matched". Then add:
  - `func codexFamilies(files []*codexFile) [][]*codexFile`: 197-222. Groups keep first-seen order, and each group is sorted with `slices.SortStableFunc` by start, then id, then path.
  - `func countFamily(family []*codexFile)`: 222-250, writing f.own and f.hours.
  - `func (f *codexFile) session() Session`: 259-262.

  countCodex runs countFamily on each family, then returns `mergeByID(<session() of each fresh file>, addCopy)`.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −4. **Depends on:** C1, C15.
- **Check:** `go test -count=1 ./internal/logs/ -run 'Codex|ReadHomes'`.

#### D17. claudeCachedUsage: one function per cache shape · P3

- **Findings:** probe-04.
- **Files:** `internal/probe/claude.go`.
- **Change:** Add `func claudeLimitWindows(raw json.RawMessage) []snapshot.Window` (the body of 388-400) and `func claudeKeyWindows(u map[string]json.RawMessage) []snapshot.Window` (402-413). quota() becomes: nil from cache() gives nil; `ws := claudeLimitWindows(c.Utilization["limits"]); if len(ws) == 0 { ws = claudeKeyWindows(c.Utilization) }`; no windows gives nil; otherwise `&Quota{At: time.UnixMilli(c.FetchedAtMs).UTC(), Source: "cache", Windows: ws}`.
- **Tests:** TestClaudeCachedUsage's table does not change.
- **Behavior:** none. **Risk:** low. **Net:** −2. **Depends on:** C3.
- **Check:** `go test ./internal/probe/ -run TestClaudeCachedUsage`. gocognit -over 20 ./internal/probe no longer lists claudeCachedUsage.

#### D18. codexLimits: bucket order and window conversion · P3

- **Findings:** probe-09. Both versions agree; the text below merges them.
- **Files:** `internal/probe/codex.go`.
- **Change:**
  - Add `func codexBuckets(single *codexBucket, byID map[string]*codexBucket) []*codexBucket`. When byID is empty it returns `[]*codexBucket{single}` if single is not nil, and nil otherwise. Otherwise it ranges over `slices.Sorted(maps.Keys(byID))`, skips nil buckets, sets `b.LimitID = cmp.Or(b.LimitID, id)`, and puts id "codex" first. A byID whose entries are all nil gives no buckets and does not fall back to single, as now.
  - Add `func (b *codexBucket) windows() []snapshot.Window`, the Primary and Secondary loop from 327-341: Name `logs.CodexWindowName(b.LimitID, mins)`, Percent, Minutes, and ResetsAt only when it is above 0. C20 already removed the MaxWindows cap.
  - codexLimits becomes: unmarshal, with the same error; then `for _, b := range codexBuckets(res.RateLimits, res.ByLimitID) { plan = cmp.Or(plan, b.PlanType); ws = append(ws, b.windows()...) }`; `nil, plan, nil` when ws is empty; otherwise `&Quota{At: now, Source: "harness", Windows: ws}, plan, nil`.
  - Drop the `sort` import. logs/codex.go and logs.CodexWindowName do not change.
- **Tests:** TestCodexLimits does not change.
- **Behavior:** none. **Risk:** low. **Net:** −5. **Depends on:** C20.
- **Check:** `go test ./internal/probe/ -run 'CodexLimits|CodexChatGPT' && go test ./internal/logs/`. gocognit -over 20 ./internal/probe no longer lists codexLimits.

#### D19. The Codex conversation: the RPC numbers its own calls · P3

- **Findings:** probe-10 (the value version).
- **Files:** `internal/probe/codex.go`, `codex_test.go`.
- **Change:**
  - Add a `next int` field to codexRPC. `call(ctx, method string, params any)` does `c.next++; id := c.next`. Drop the literal ids at 219, 229 and 262; the ids stay 1, 2 and 3 in order.
  - In call's select, the ctx.Done case is empty, and the c.done case breaks out of the select when ctx.Err() is not nil, keeping the comment that the context also kills the process. Both reach one `return nil, fmt.Errorf("codex %s: no answer in time", method)` after the select. The EOF and shortErr mapping and the reply matching stay inline.
  - Add `func codexAccount(raw json.RawMessage) (Reading, error)` with 233-258: 'codex account/read: unexpected result' on bad JSON, `notLoggedIn("codex")` for a null account, and otherwise the label and plan switch.
  - codexConversation becomes a run of early returns: initialize; initialized; account/read (returning `Reading{}, err`); codexAccount; `if err != nil || r.Account == "" { return r, err }`, so an account type that labels to "" still skips the rate-limit read; account/rateLimits/read (returning `r, err`); codexLimits (returning `r, err`); `r.Quota, r.Plan = q, cmp.Or(r.Plan, plan)`; `return r, nil`. Delete the errs slice and its joinErrors call.
- **Tests:** drop the literal ids in TestCodexRPCAbandonedCall, TestCodexRPCErrorAnswer, TestCodexRPCLastLineWithoutNewline and TestCodexRPCReaderPanicIsTheCallError.
- **Behavior:** none. **Risk:** low. **Net:** −3. **Depends on:** C9, D18.
- **Check:** `go test -race ./internal/probe/ -run Codex`. gocyclo -over 15 and gocognit -over 20 no longer list call.

#### D20. `relay serve` leaves cmdRelay · P3

- **Findings:** cmd-12.
- **Files:** `cmd/ai-usage/main.go` (relay.go after E1).
- **Change:** Move main.go:858-906 into `func relayServe(ctx context.Context, args []string, stderr io.Writer) error`, with its comments. cmdRelay starts `sub, args := subcommand(args); if sub == "serve" { return relayServe(ctx, args, stderr) }`. The URL check at 928-931 stays inline.
- **Tests:** TestRelayServeClientIPHeader and TestRelayShutdownPanicIsItsError do not change.
- **Behavior:** none. **Risk:** low. **Net:** +3. **Depends on:** C23.
- **Check:** `go test ./cmd/ai-usage/ -run TestRelay`. gocognit no longer lists cmdRelay over 20.

#### D21. cmdTeam, cmdName and cmdSchedule split along their subcommands · P3

- **Findings:** cmd-13.
- **Files:** `cmd/ai-usage/main.go` (team.go, name.go and schedule.go after E1).
- **Change:**
  - Add `showTeam(d state.Dir, stdout io.Writer) error` from 723-739.
  - Add `forgetDevice(ctx context.Context, d state.Dir, id string, stdout, stderr io.Writer) error` from 772-795. The two argument checks at 766-771 stay in cmdTeam.
  - joinTeam takes `(ctx, d, args []string, stdin io.Reader, stdout, stderr io.Writer)` and starts with the 0/1/else key-line switch from 751-763, so cmdTeam's join case is one line.
  - Add `showName(cfg state.Config, stdout io.Writer)` from 1111-1131. The argument check stays.
  - Add `scheduleStatus(ctx context.Context, d state.Dir, s schedule.Scheduler, exe, home string, stdout io.Writer) error` from 997-1016, without the Duplicate case that B5 removes.

  Each command body becomes a switch of short cases.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** +5. **Depends on:** B5, C23.
- **Check:** `go test ./cmd/ai-usage/ -run 'TestTeam|TestTwoDevices|TestDeviceName|TestSchedule|TestCommentedOut|TestRelayCommands'`. gocognit -over 20 ./cmd/... lists none of these commands.

#### D22. cmdHome and listHomes split · P3

- **Findings:** cmd-14 (the value version).
- **Files:** `cmd/ai-usage/home.go`.
- **Change:**
  - Add `homeTargets(sub string, rest []string) (p string, homes []string, err error)` from 71-85: the argument count, the known provider and homePath of each.
  - Add `quotaRefs(sub, p string, qs []string) ([]homeRef, error)` from 86-106.
  - Add `forgetSessions(ctx context.Context, d state.Dir, p string, homes, kept []string, stdout, stderr io.Writer) error` from 144-154: the Forget call, its wrapped error and the session/sessions line.
  - The EditConfig closure (120-139) stays inline.
  - Split listHomes into `homeRows(cfg state.Config, found map[string][]string, userHome string) [][]string` from 280-299 and `missingRows(cfg, found, userHome) [][]string` from 302-328. listHomes becomes Discover, `rows := append(homeRows(…), missingRows(…)...)` and `fmt.Fprint(stdout, tabulate(rows))`.

  cmd-06 (flags anywhere) stays out (Owner decision 9).
- **Tests:** none change.
- **Behavior:** none; `ai-usage home` output is byte-identical. **Risk:** low. **Net:** +5. **Depends on:** A6, C6, C23.
- **Check:** `go test -count=1 ./cmd/ai-usage/ -run 'TestHome|TestCollectWhileAnotherRunCollects'`. gocognit shows cmdHome and listHomes under 20.

#### D23. loadAliasBook and cmdAlias get shorter · P3

- **Findings:** cmd-15.
- **Files:** `cmd/ai-usage/alias.go`.
- **Change:**
  - Move `seen map[string]bool` into aliasBook. The closures add and note become `(b *aliasBook) addAccount(p, l string)` and `(b *aliasBook) noteName(p, l string, a aliasSet)`. noteName uses C24's `snapshot.AliasWins`.
  - Extract `(b *aliasBook) addTeam(d state.Dir, self string)` from 184-212, with the open closure inside it.
  - Extract `aliasDone(stdout io.Writer, targets []account, name, was string, clear bool)` from 98-113. The EditConfig block (87-97) stays inline.
  - The sort at 213-223 is C15's, and orUnknownName is A6's `cmp.Or`. This step does not touch them again.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** −10. **Depends on:** A6, C15, C24.
- **Check:** `go test -count=1 ./cmd/ai-usage/ -run TestAlias`. gocognit shows loadAliasBook and cmdAlias under 20.

#### D24. selfupdate.Check splits at the lookup and install seam · P3

- **Findings:** platform-07 (the value version: extract only, one file; Owner decision 17).
- **Files:** `internal/selfupdate/selfupdate.go`.
- **Change:** Move 167-207 into `func (u *Updater) install(ctx context.Context, repo, tag string) (downloaded bool, err error)`. It holds the writable check, the checksums.txt download and lookup, the binary download (set `downloaded = err == nil || errors.Is(err, errTooLarge)` right after it), the sha256 compare, stage, starts (removing tmp on failure) and swap (removing tmp on failure). Check ends with `res.Downloaded, err = u.install(ctx, repo, tag); res.Installed = err == nil; return res, err`. The file and selfupdate_test.go stay whole.
- **Tests:** none change.
- **Behavior:** none; asset names, the checksum format and the Windows `.old` swap stay. **Risk:** low. **Net:** +5. **Depends on:** none.
- **Check:** `go test ./internal/selfupdate/ ./cmd/ai-usage/ -run 'Update|Release|Housekeeping'`. gocyclo -over 15 ./internal/selfupdate no longer lists Check.
### Phase E. Split the large files

Phase E moves declarations into files named for their concern. Each step is one commit, done after the steps that edit the same code, so the diff shows moved blocks only. Where a step also edits code, it says so. The net figures count the package clause and imports that each new file adds.

Every step in this phase uses the same move check:

- Before and after, `go test <pkg> -list '.*' | sort` prints the same list.
- `/usr/bin/git diff -M --color-moved=zebra` shows only moved blocks, plus the edits the step names.
- `gofmt -l .` prints nothing.

#### E1. main.go splits by command · P2

- **Findings:** cmd-16. Both versions agree on the layout. The value version is used, with the names the earlier steps give (C10, C11, C23, D20, D21). There is no noArgs, per C23.
- **Files:** `cmd/ai-usage/main.go`, `alias.go`, `interactive.go`, and the new files below.
- **Change:** One commit that only moves code:
  - main.go (about 170 lines): the package doc, version and defaultRelay, the test hooks (clock, probeEnv, newScheduler, newUpdater), main, run, usageError, argError (moved from alias.go:21-26), flags, parse, parseMixed, subcommand, loadConfig and writeJSON.
  - name.go: maxName, deviceName, envName, validName, hostname, osUser, cmdName and showName.
  - collect.go: cmdCollect, takeGuide, collection and its run, panicError, rescue, lockWait and waitLock.
  - update.go: updateFloor, updateTimeout, retryFailed, housekeeping, updateIfDue, noteUpdate and cmdUpdate.
  - schedule.go: disabledByHand, ensureSchedule, job, executable, cmdSchedule, scheduleStatus, scheduleRun, liveSchedule, collectOnce and sleepCtx.
  - report.go: loadResult, reportAt (from interactive.go), printReport, cmdReport, reportFrom and cmdStatus.
  - team.go: cmdTeam, showTeam, forgetDevice and joinTeam.
  - relay.go: relayURL, cmdRelay and relayServe.
  - interactive.go keeps stdinTTY, interactive, openView, tuiConfig and viewConfig.
- **Tests:** none move here. F splits main_test.go.
- **Behavior:** none. **Risk:** low. **Net:** +70. **Depends on:** every C and D step in cmd/ai-usage (C10, C11, C19, C23, C24, C25, D20 to D23).
- **Check:** `go build ./... && GOOS=windows go vet ./cmd/... && GOOS=linux go vet ./cmd/... && go test ./cmd/ai-usage/`. main.go is under 200 lines, and no other non-test file in cmd/ai-usage is over 250.

#### E2. collect.go and doc.go split by concern, and their tests with them · P2

- **Findings:** collect-15, tests-05 (superseded: its test split is folded in here).
- **Files:** `internal/collect/collect.go`, `doc.go`, `team.go`, `hermes.go` (from D3), `collect_test.go`, `doc_test.go`, `claudeenv_test.go`, and the new files below.
- **Change:** One commit that only moves code:
  - collect.go (about 400 lines): Options, Result, Run and its steps, the sampler, provider, keepCurrent, shortErr, truncate and dedupe.
  - ask.go: askHarness, answer, askAll, applyReading, homeErrors, sharedClaudeLogs and harness.
  - quota.go: minResetsAt, RejectionSource, setQuota, applyRejected, soleAccount, allReset and clampWindows.
  - ledger.go: touchAccount, attribute, partsOf, seenNow, claimUnknown, moveShare, dropShare, pruneAccounts, servedBy, resetSkew, sameReset and prune.
  - hours.go: placeHours through dropHours, plus labelHours, accountHours, DaysOf, dayNumber and sinceStart, moved from doc.go.
  - hermes.go gains the rest of the Hermes block (linkHermes, mostUsed, markHermesCurrent, linkGrowth).
  - team.go gains LoadKey and publish.
  - doc.go splits into totals.go (the usage struct, AccountTotals, ProjectTotals, Totals, Projects, linkQuota, lastActive, SortAccounts, sortProjects, isCurrent) and doc.go (BuildDoc, recent, aliases, longest, fitDoc and clip).
  - Tests move the same way:
    - world_test.go: type world and its methods, newWorld, sess, quota, run, totalsFor, hasTotals, lastSample, growthOf and tok (collect_test.go:28-203);
    - claim_test.go: TestFirstRunAttributesHistoryToCurrentAccount through TestClaimOnceAfterTheAccountAgesOut (205-585);
    - quota_test.go: TestProbeErrorsNameTheirHome through TestClaudeRejectionGoesToTheSessionsAccount, with notLoggedIn, codexLimits and rejectedSession (618-896), and the clamp tests;
    - ledger_test.go: the prune and attribution tests;
    - hours_test.go and totals_test.go: the hours and totals tests from doc_test.go;
    - hermes_test.go gains TestHermesAttributesByBillingProvider (898-945);
    - discover_test.go: the discovery tests and TestRememberEnvKeepsClaudeConfigDirVerbatim. TestSchedulerRunProbesClaudeWithRememberedConfigDir moves to collect_test.go, and claudeenv_test.go is deleted.
  - collect_test.go keeps isolation, partial reads, retention, homes, samples, damaged state, and wait, lock and After.
- **Tests:** moves only; the count stays at 102 runs.
- **Behavior:** none. **Risk:** low. **Net:** +60. **Depends on:** B2, B3, B6, C9, C18, C26, D2, D3, D12, D13.
- **Check:** the move check, then `go build ./... && go vet ./... && go test ./internal/collect/`.

#### E3. view/team.go splits into team, names, matrix and hermes · P3

- **Findings:** view-team-08.
- **Files:** `internal/view/team.go`, `build.go`, new `names.go`, `matrix.go`, `hermes.go`.
- **Change:** Pure moves:
  - team.go: teamAccount, winReading, devAccount, device, the merger and buildTeam (D1), subscription, deviceError, sourceError, latestVersion, active, quota, sortTeamAccounts and left.
  - names.go: merger.addAliases, names, aliasKey, ShortName, idLike, and teamName from build.go:110-124.
  - matrix.go: matrix and matrixColumns (D6), colKey, shareOf, billsTo, users and devAccount.sinceStart.
  - hermes.go: login, devAccount.split, shares, wireLink, sameReading, addLinked and linkedList.
  - sameTime (766-771), which attention.go also uses, moves to build.go beside timeOf and timePtr.
- **Tests:** none here; F21 splits view_test.go along the same files.
- **Behavior:** none. **Risk:** low. **Net:** +20. **Depends on:** B1, C15, C22, C24, D1, D6.
- **Check:** the move check, then `go build ./... && go vet ./internal/view && go test ./internal/view`.

#### E4. The ATTENTION section leaves header.go · P3

- **Findings:** view-render-10.
- **Files:** `internal/view/header.go`, `render.go`, `forecast.go`, new `attentionlines.go`.
- **Change:**
  - attentionlines.go holds badgeOf, attentionSubject, attentionLines (from render.go), (*page).attention, subject, versions, attentionText, pace, window, windowName and windowLength.
  - trimLength and lengthPrefix move to forecast.go next to mainWindow, since subscriptions.go:119 uses them too.
  - header.go keeps healthItem, header and health, about 135 lines.
  - One code edit: in attentionText, the forms closure becomes `(p *page) forms(parts ...string) []chunks`, and the kinds get `(p *page) overForms(a Attention, old string)`, `underForms(a Attention)`, `silentForms(a Attention, room int)` and `oldForms(a Attention, room int)`. attentionText computes old and dispatches; OUT and ERROR stay inline.
- **Tests:** none change.
- **Behavior:** none. **Risk:** low. **Net:** +6. **Depends on:** C13, D7.
- **Check:** `go test ./internal/view`: TestPageAttentionIsCut, TestPageSilentDevice, TestPageOverLine, TestPageOverAtReset, TestPageOldLine, TestAttention and every golden byte-identical.

#### E5. Each view declaration in the file of its concern · P3

- **Findings:** view-render-11 (the value version).
- **Files:** `internal/view/render.go`, `format.go`, `projects.go`, `paint.go`, `subscriptions.go`, new `period.go`, `window.go`; `format_test.go`, `page_test.go`.
- **Change:**
  - The Period block (the type and consts, Periods, String, Of, share and Next) moves from render.go to period.go.
  - shortID (render.go:404-411), with C22's shownLabel, moves to format.go next to uuidRe. topProjects (render.go:263-264) moves to projects.go next to minPath and maxPath.
  - The page's legend() (projects.go:134-206) moves to paint.go after the marks tables, since it explains the marks of every section.
  - subscriptions.go:290-454 moves to window.go: known, leftText, leftCell, resetsText, atResetText, atReset, elapsed, bar and D9's barSplit. That file draws one window as a bar and its LEFT, RESETS and AT RESET cells. subscriptions.go keeps subRow, subGroup, subLayout, the constants, subscriptions, layoutSubs, subHeader and subLine.
  - One code edit: delete unread (subscriptions.go:294-296). known becomes `w != nil && !w.Reset && !w.Unread`, and leftCell's first case becomes `w != nil && w.Unread`.
  - TestPageWideCharacters (format_test.go:143-168) moves to page_test.go.
- **Tests:** the one move above.
- **Behavior:** none. **Risk:** low. **Net:** 0. **Depends on:** B1, C13, C14, C22, D9, E4.
- **Check:** the move check, then `go build ./... && go test ./internal/view ./internal/tui`.

#### E6. logs/codex.go splits into threads, the rollout file and limits · P3

- **Findings:** logs-15.
- **Files:** `internal/logs/codex.go`, `codex_test.go`, new `codexfile.go`, `codexlimits.go`, `codexlimits_test.go`.
- **Change:** Pure moves:
  - codex.go keeps readCodexHomes and its doc, codexFiles, walkCodex, sameFiles and sameFile, linkedCodexFiles, countCodex with codexFamilies and countFamily (D16), and unionFind.
  - codexfile.go: codexFile, codexRecord, parseCodex, meta, count, record, settle, codexCount, codexEvent, codexUsage (since, tokens), spawnParent, historyThread, codexNameRe, codexNameID and codexLine.
  - codexlimits.go: codexLimitSkew, codexLimits, codexMainLimit, codexLogLimits, codexLogWindow, reading and CodexWindowName.
  - parseTime is already in scan.go after C27.
  - TestCodexLimits and TestCodexWindowName (codex_test.go:394-535) move to codexlimits_test.go.
- **Tests:** the one move above.
- **Behavior:** none. **Risk:** low. **Net:** +16. **Depends on:** C1, C2, C27, D16.
- **Check:** the move check, then `go build ./... && go vet ./internal/logs/ && go test -count=1 ./internal/logs/`.

#### E7. A read-only SQLite reader apart from the Hermes schema · P3

- **Findings:** logs-16 (the value version).
- **Files:** `internal/logs/hermes.go`, `hermes_test.go`, new `sqlite.go`, `sqlite_test.go`.
- **Change:**
  - sqlite.go holds `func readSQLite(dbPath string, read func(tx *sql.Tx) error) error`: the EvalSymlinks step with its comment, then readHermes' retry loop (42-57) with its comment. It calls readSQLiteOnce once per attempt.
  - `func readSQLiteOnce(dbPath string, st dbState, read func(tx *sql.Tx) error) (live bool, err error)` is readHermesDB's body (96-117) with `err = read(tx)` in place of readHermesTx. It stays its own function so its deferred Close and Rollback run after each attempt. The st parameter comes from C27.
  - hermesAttempts, afterHermesRead, dbState, statDB, hermesURI, exists, snapshotSQLite, copyFile, sqliteURI and tableColumns move unchanged, with their names.
  - hermes.go keeps readHermes, which stats state.db and calls `readSQLite(dbPath, func(tx *sql.Tx) (err error) { sessions, malformed, err = readHermesTx(tx, since); return err })`, then returns `(sessions, HomeRead{Err: err, Malformed: malformed})`. It also keeps betweenHermesQueries, readHermesTx, hermesSessionUsage, parts and hermesUsage.
- **Tests:** hermes_test.go:347-504 (TestHermesReadsUncheckpointedWAL through TestHermesRereadsADatabaseOpenedDuringTheRead, with liveHermesDB) and 531-626 (TestHermesStopsRereadingAChangingDatabase through TestHermesHomeWithURICharacters) move to sqlite_test.go. TestHermesReadsOneCommit stays, since it uses the query hook.
- **Behavior:** none. **Risk:** low. **Net:** +14. **Depends on:** C27.
- **Check:** `go vet ./internal/logs/ && go test -count=1 ./internal/logs/ -run 'Hermes|SQLite'`.

#### E8. probe helpers in find.go, proc.go and codexrpc.go · P3

- **Findings:** probe-12.
- **Files:** `internal/probe/probe.go`, `codex.go`, `claude.go` and their tests; new `find.go`, `proc.go`, `codexrpc.go`, `fake_test.go`, `find_test.go`, `proc_test.go`, `codexrpc_test.go`.
- **Change:** Moves only:
  - find.go: find, systemBinDirs, extraBinDirs, binNames, isExecutable, appDirs, bins, bundled, realPath and Find (probe.go:227-379).
  - proc.go: lastLine, terminalCodes, errorLine, hint, waitOrKill, rescue, shortErr and truncate (A14).
  - codexrpc.go: errExited, errUnsupported, unsupported, codexRPC, newCodexRPC, read, close, notify, send, codexReply and call.
  - isDefaultHome and parseTime move into probe.go.
  - codex.go keeps Codex, codexAt, codexExitGrace, codexConversation, codexAccount (D19) and the limits code. claude.go keeps the Claude code.
- **Tests:**
  - fake_test.go: fakeEnv, helperEnviron, helperRecord, readRecord, readRuns, methods, TestHelperProcess, fakeHarness, hangWithChild, stdinState, fakeClaudeUsage, fakeCodex, sameDir, waitGone, processGone, waitNoGoroutine and errText.
  - find_test.go: exeName, writeExe, notOnPath, TestFind* and TestBins (from codex_test.go).
  - proc_test.go: TestLastLineSaysTheError and TestWaitOrKillPanicIsItsError.
  - codexrpc_test.go: pipeServer, buggyReader and TestCodexRPC*.
  - TestCodexScriptFindsItsInterpreter moves to codex_test.go.
- **Behavior:** none. **Risk:** low. **Net:** +12. **Depends on:** A14, C3, C7, C8, C9, C20, D17 to D19.
- **Check:** the move check, then `go build ./... && go vet ./... && GOOS=windows go vet ./internal/probe/ && go test ./internal/probe/`.

#### E9. snapshot splits into the document and its validation · P3

- **Findings:** relay-07. Both verifiers gave a version. The value version is used: alias.go stays, since C24 adds LabelKey and AliasWins to it and relay-08 is not done by default (Owner decision 8), so no text.go is needed.
- **Files:** `internal/snapshot/snapshot.go`, `snapshot_test.go`, new `validate.go`, `validate_test.go`.
- **Change:** Pure moves:
  - validate.go: teamRe, deviceRe, sealedRe, plainRe, statuses, ValidTeam, ValidDevice, Decode, Doc.Validate, FromFuture, the per-type validate methods, checkDay, checkList, checkCounts, textRule, sealedText and plainText, and PlainLabel, which is built on plainRe.
  - snapshot.go keeps the package doc, Version, the limits, Providers, the types and their methods (Window.Length and Start, windowLength, the Tokens methods), Printable, hidden and DurationName.
- **Tests:** validate_test.go gets TestWireFormat, TestDecodeTrailingAndSize, TestDecodeRejectsUnknownFields, TestValidate, repeat, TestFromFuture, TestValidTeamAndDevice, FuzzDecode, TestPlainLabel and FuzzPlainLabel. snapshot_test.go keeps t0, sealed, validDoc, encode, TestTokens, TestDurationName, TestPrintable and TestWindowLengthAndStart.
- **Behavior:** none. **Risk:** low. **Net:** +10. **Depends on:** C6, C21, C24, D4.
- **Check:** the move check, then `go build ./... && go test ./internal/snapshot/`.

#### E10. Rate limits and the client address leave relay/server.go · P3

- **Findings:** relay-11 (the value version).
- **Files:** `relay/server.go`, `protocol.go`, new `relay/limits.go`; `docs/relay.md`, `docs/relay-protocol.md`.
- **Change:** Pure moves:
  - limits.go: Limits, DefaultLimits with its rationale comment, allow, overLimit and its has, add and sweep methods, maxOverLimit, clientKey, clientAddr and rateKey.
  - ListResponse and ListedDevice move to protocol.go, beside ErrStatus (D5).
  - server.go keeps bodyTimeout, Server, NewServer, route, ServeHTTP, now, the handlers and their helpers (devicePath, verifyRequest, readSnapshot, putAgain, putNew, pastCap, firstDevices, recordTTL, canonical), writeJSON and fail.
  - docs/relay.md:150 and docs/relay-protocol.md:486 say relay/limits.go in place of relay/server.go.
- **Tests:** none move.
- **Behavior:** none. **Risk:** low. **Net:** +5. **Depends on:** A9, C12, D5.
- **Check:** `go build ./... && go test ./relay/`.

#### E11. relay/store.go splits into memory and KV · P3

- **Findings:** relay-17 (the value version).
- **Files:** `relay/store.go`, `store_test.go`, new `relay/kv.go`, `kv_test.go`.
- **Change:** Pure moves. kv.go gets KV, docKey, setKey, KV's methods, decodeRecord, maxKVResponse and pipeline. store.go keeps Record, Store, StoreFromEnv and Memory.
- **Tests:** kv_test.go gets fakeKV, newFakeKV and its methods, errWrongType, newKV, TestKVPutGetDelete, TestKVListPrunesExpired, TestKVSetOutlivesTheLastWrite, TestKVListFullTeam, TestKVCount, TestKVErrors and TestRelayOverKV. store_test.go keeps TestMemoryTTL, TestMemoryCountAndSweep and TestStoreFromEnv.
- **Behavior:** none; the KV key names do not change. **Risk:** low. **Net:** +15. **Depends on:** none. F5 and F7 later edit the moved helpers in kv_test.go.
- **Check:** the move check, then `go build ./... && go test ./relay/`.

#### E12. schedule.go splits into cron.go and task.go · P3

- **Findings:** platform-08.
- **Files:** `internal/schedule/schedule.go`, `schedule_test.go`, new `cron.go`, `task.go`, `cron_test.go`, `task_test.go`.
- **Change:**
  - schedule.go keeps the package doc, Marker, Interval, State, Runner, Scheduler, Default, Name, execRunner and xmlText, which the plist and the task share.
  - One code edit: Install, Remove and Lookup each become `switch s.GOOS { case "darwin": …Agent; case "windows": …Task }; return …Cron`, with Install's line-break check kept first.
  - cron.go: maxLine, cronPath, Line, cronLine, shCommand, args, pathDirs, installCron, removeCron, lookupCron, cronLines, noCrontab, writeCron, isOurs, commented, withoutMarker, shq and cronEscape.
  - task.go: installTask, TaskXML, jobToken, winQuote, utf16LE, decodeText, removeTask and lookupTask.
- **Tests:** schedule_test.go:18-404 moves to cron_test.go and 406-577 to task_test.go. TestExecRunner and its helpers stay. TestWindows splits into TestTaskInstall (449-503), TestTaskLookup (505-541) and TestTaskRemove (543-565), each with its own fakeTasks.
- **Behavior:** none; the cron line, the plist and the task XML stay byte-identical. **Risk:** low. **Net:** +10. **Depends on:** A6, A13, B5, C4.
- **Check:** `go test ./internal/schedule/`, `GOOS=windows go vet ./internal/schedule/` and `GOOS=darwin go vet ./internal/schedule/`. gocyclo -over 15 ./internal/schedule no longer lists TestWindows.

#### E13. state.go splits into config.go and lock.go · P3

- **Findings:** platform-09.
- **Files:** `internal/state/state.go`, `state_test.go`, new `config.go`, `lock.go`, `config_test.go`, `lock_test.go`, `samples_test.go`.
- **Change:**
  - config.go: Config, Alias, LoadConfig, SaveConfig and EditConfig.
  - lock.go: ErrBusy, Lock, ScheduleLock, Foreground, lock, lockPoll, LockWait and lockWait, with checkWait as a const. It sits beside lock_unix.go and lock_windows.go.
  - state.go keeps the doc, Retention, Dir, DefaultDir, Path, KeyFile, SamplesDir, StateFile and TeamCacheFile (C25), State and its nested types, Key, SplitKey, NewState, LoadState, SaveState, readJSON and writeJSON.
- **Tests:** lock_test.go gets TestHoldLock, TestLockEndsWithItsProcess, TestLockWait and TestForegroundAnswersAtOnce. config_test.go gets TestEditConfigKeepsConcurrentEdits and the three TestLoadConfig* tests. samples_test.go gets sampleAt and the three sample tests. state_test.go keeps tempDir, checkPrivate, TestDefaultDirHonorsEnv, TestLoadState* and TestStateRoundTrip. Delete TestLockIsExclusive: TestHoldLock and TestLockWait cover the same exclusion.
- **Behavior:** none. **Risk:** low. **Net:** −18. **Depends on:** B4, C4, C25.
- **Check:** `go test ./internal/state/ && GOOS=windows go vet ./internal/state/`.
### Phase F. Tests

Phase F merges tests that check the same thing, shares repeated setup, and splits the largest test files. No production code changes, except the one line in F1. A merged test keeps every assertion the tests it replaces made, unless the step names a check another test already makes. Coverage per package must not drop below the baseline. Line numbers are at d460afa; phase E moves many tests first, so find each site by the test's name.

#### F1. Pass GOCOVERDIR to the probe helper processes · P2

- **Findings:** tests-08. Raised from P3 to P2, because the gates in section 6 run `go test -cover`, and the probe package fails under it today. It lands before phase A, beside H3, although it is listed here.
- **Files:** `internal/probe/probe_test.go`.
- **Change:** Add "GOCOVERDIR" to the passthrough list in helperEnviron (probe_test.go:44).
- **Tests:** this line only.
- **Behavior:** none. **Risk:** low. **Net:** +1. **Depends on:** none.
- **Check:** `go test -cover ./internal/probe > <scratch>/t08.txt 2>&1` and `go test -coverprofile=<scratch>/c.out ./... > <scratch>/t08all.txt 2>&1` both pass.

#### F2. One panic test and one waited-run table in collect · P2

- **Findings:** collect-16, tests-10. Both fold the same waited-run test. tests-10's table fields are used, with collect-16's PulledAt checks.
- **Files:** `internal/collect/collect_test.go`, `orca_test.go` (E2 leaves both tests in these files).
- **Change:**
  - Replace TestPanicInOneSourceDoesNotStopOthers (collect_test.go:977-1008) and TestPanicInProbeIsItsSourceError (orca_test.go:258-280) with TestPanicIsItsSourceError in collect_test.go. It has two subtests: read (wraps o.ReadLogs) and probe (wraps o.Ask). Both panic with "a parser bug" for claude and give codex session c1. Both assert the union of today's checks: the claude source has status error with "stopped by a bug"; codex is ok, bob is current, with tok(10); LastError holds "stopped by a bug"; the saved state's codex source is ok.
  - Delete TestWaitedRunThatSkippedTheTeamRead (1479-1529) and keep its first comment sentence in TestRunWaitsForTheRunItOverlaps's doc. In that test:
    1. add `relay, readTeam bool` to the table, with two rows: {"skipped the team read", collects: true, relay: true, waited: false} and {"read the team too", collects: true, relay: true, readTeam: true, waited: true};
    2. when tc.relay, set `o.Relay = newRelay(t, w).client(w)` before the first run;
    3. in the Waiting goroutine, after saving LastRunAt: when tc.readTeam, load the team cache, set PulledAt to t0+15m and save it;
    4. after the LastRunAt and Doc check, when tc.relay, assert res.Team.PulledAt: w.now when the team was not read, t0+15m when it was.
  - Delete the dead Skip guards at 1128-1130 and 1150-1152.
- **Tests:** as above. A6 already made the range-over-int edits, and A7 the staticcheck ones.
- **Behavior:** none. **Risk:** low. **Net:** −60. **Depends on:** A6, A7.
- **Check:** `go test -race -count=5 -run 'Wait|Panic' ./internal/collect > <scratch>/t10.txt 2>&1`. Disabling the recover in collectSource makes both panic subtests fail. `staticcheck ./internal/collect/` reports nothing.

#### F3. Merge the overlapping two-file log tests · P2

- **Findings:** logs-17.
- **Files:** `internal/logs/codex_test.go`, `claude_test.go`, `hermes_test.go`.
- **Change:**
  - Codex: delete TestCodexOneThreadInTwoFiles. In the TestCodexHours subtest, age the page to now−3h, live to now−2h and archived to now−1h, then assert one session, `Tokens == rootOwn.Add(Tokens{Input: 100, CacheRead: 400, Output: 10})` and `Updated == now−1h` before checkHours.
  - Claude: delete the "one session in two projects" subtest. Add checkHours to TestClaudeOneSessionInTwoProjects with `{"2026-09-20T10:00:00Z": 38, "2026-09-21T10:00:00Z": 51}`: 33 and 44 timed, with the 12 untimed tokens spread in proportion and the remainder to the largest hour. Confirm the numbers by one run.
  - Hermes: remove `before := tree(...)` and the sameTree call at 115-118 and 709-711.
  - Flatten TestCodexForkCountsOnlyItsOwnRequests into a plain test body.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −40. **Depends on:** C1.
- **Check:** `go test -count=1 -cover ./internal/logs/` stays at 94.0% or more. `go test -count=1 -run 'CodexHours|ClaudeOneSessionInTwoProjects' -v ./internal/logs/` shows the merged checks run.

#### F4. Codex fallback and Claude home tests as tables · P2

- **Findings:** probe-13. Both verifiers gave a version. The value version is used: the three find tests differ in their Env and OS filters, so a table saves little, and the plan checks are TestCodexChatGPT's.
- **Files:** `internal/probe/codex_test.go`, `claude_test.go`, and E8's `fake_test.go`.
- **Change:**
  1. Replace TestCodexFallsBackToBundled, TestCodexWithoutAccountReadGivesWay, TestCodexKeepsTheAccountWhenItExitsAfter, TestCodexNoneServes and TestCodexAnswerEndsTheSearch (codex_test.go:225-302) with TestCodexTriesEachBinary. It is a table of {name, mode string; bundles []string (paths under HomeDir, newest first; bundle i gets mod time testNow − i hours); ran int (how many of [fake/bin/codex, bundles...] started, in that order); account, wantErr string; loggedOut bool}. Rows:
     - an old codex on PATH gives way to the newest bundle (codex-old-on-path, [vscodeExt, chatGPTApp], 2, the fixture account);
     - no account/read gives way (codex-no-account-read-on-path, [chatGPTApp], 2, the fixture account);
     - an exit after naming the account, without and with a bundle (codex-exit-after-account, nil and [chatGPTApp], 1, the fixture account, "exited without answering: no backend");
     - none serves (codex-exit-says, [chatGPTApp], 2, "", "exited without answering: unrecognized subcommand 'app-server'");
     - logged out ends the search (codex-logged-out, [chatGPTApp], 1, "", "not logged in", true).

     Each row asserts the exact binaries run, r.Account, the error text (none when wantErr is empty), `errors.Is(err, ErrNotLoggedIn) == loggedOut`, and `(r.Quota != nil) == (tc.wantErr == "")`. Keep TestCodexOnlyBundled.
  2. Replace TestClaudeDefaultHome, TestClaudeDefaultHomeNamedByEnv and TestClaudeCustomHome (claude_test.go:216-282) with TestClaudeHomes, a table of {name, leaf string; named bool} with rows (".claude", false), (".claude", true) and ("work-claude", false). Environ gets CLAUDE_CONFIG_DIR=home+separator when named, and CLAUDE_CONFIG_DIR=/stale otherwise. The child's variable must be home+separator when named, home for the custom leaf, and unset otherwise. The 12% cache goes to home/.claude.json when the variable is set, and to HomeDir/.claude.json otherwise; a 99% decoy goes to the other place. Every row asserts the account, plan max, one 12% window at fetchedAtMs, `claude auth status --json`, the variable, and `sameDir(rec.Dir, env.HomeDir)`. Delete the cross-reference comments at claude_test.go:158-160 and 173-174.
  3. In TestClaudeAnswers, drop the wantQuota field and assert `r.Quota == nil`.
  4. In TestClaudeUsageRefreshTimeout, delete the child-pid block (600-615) and the PROBE_RELEASE setup, and make fakeClaudeUsage's "hang" mode `time.Sleep(time.Minute); return 0`. TestClaudeTimeout keeps the process-group check, which after C8 covers the one command builder both calls use.
- **Tests:** as above. The rows use the fixture account the tests use today.
- **Behavior:** none. **Risk:** low. **Net:** −95. **Depends on:** C8, C9.
- **Check:** `go test -race ./internal/probe/` and `GOOS=windows go vet ./internal/probe/`. `go test -cover ./internal/probe/` does not drop below 93.8%.

#### F5. One test store and two publish helpers in the relay tests · P2

- **Findings:** relay-13, tests-15 (its helper half; its split is F23's). relay-13's value version is used for TestRejectedRequestsSkipTheStore: it keeps its own NewServer, because F6 needs a server with default settings to show that forwarding headers are ignored.
- **Files:** `relay/server_test.go`, `kv_test.go` (from E11).
- **Change:**
  - Replace racing, failing, brokenStore and countingStore (server_test.go:623-636, 665-689, 1050-1059, 1270-1318) with `type testStore struct { Store; hideFirstList bool; lists int; fail map[string]bool; counts atomic.Int64 }`, kept next to errBroken.
    - Get, Put, Delete, List, Size and Count return errBroken when fail[op] is set ("get", "put", "delete", "list", "size", "count"), and otherwise call Store.
    - List counts its calls. With hideFirstList, its first call returns an empty map before any fail check, as racing and failing do.
    - Count adds 1 to counts first.
  - Uses:
    - TestFirstWritePastTheCapGivesWay: `e.srv.Store = &testStore{Store: e.mem, hideFirstList: true}`.
    - TestFirstWritePastTheCapFailsClosed: `&testStore{Store: e.mem, hideFirstList: true, fail: map[string]bool{broken: true}}`, inside today's loop over "list" and "delete".
    - TestStoreFailuresAre503: each subtest runs `e := newRelay(t)`, `e.srv.Store = &testStore{Store: e.mem, fail: map[string]bool{op: true}}`, and uses `e.client(k)`.
    - TestRelayOverKV: `kv, f, _ := newKV(t)`, `e := newRelay(t)`, `e.srv.Store = kv`, `e.srv.Limits.DevicesPerTeam = 2`, and `e.client(k)`. newKV does not change, and both clocks stay at t0.
    - TestRejectedRequestsSkipTheStore: `store := &testStore{Store: NewMemory()}` and its own NewServer and clock, as now, reading store.counts.
  - Add two relayEnv methods:
    - `publish(t, c *Client, k *team.Key, dev string) error` publishes `docFor(k, dev, e.clock.Now())`;
    - `join(t, c *Client, k *team.Key, devs ...string)` moves the clock one minute before each device, publishes it, and fails the test on any error.
  - Use e.join for the fill loops at 602-607, 643-648 and 700-705 and for each "clock.Add(minute), publish, t.Fatal" run (658-662, 732-741, 755-758). Use e.publish for the other checked publishes (651, 708, 714, 749, 896, 902) and the closures at 569 and 936.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −90. **Depends on:** C12.
- **Check:** `go test ./relay ./api -list '.*' | sort` prints the same names before and after. `go test -race ./relay ./api > <scratch>/t15.txt 2>&1` passes.

#### F6. Delete relay tests that repeat others · P2

- **Findings:** relay-14 (the value version). It folds TestCannotWriteAnotherTeam (Owner decision 21).
- **Files:** `relay/server_test.go`.
- **Change:**
  - Delete TestPublishSignsTheExactBody, TestForwardingHeadersAreIgnoredByDefault, TestCannotWriteAnotherTeam and TestPublishLinkedQuota.
  - In TestRejectedRequestsSkipTheStore, give the i-th /v1/health request `X-Real-Ip: 203.0.113.<i>` and `X-Forwarded-For: 198.51.100.<i>`. Its "5 allowed, 45 limited" check then also shows that a default server ignores forwarding headers; its comment says so.
  - Add to TestDelete: a request signed by another team's key on this team's device path (403), and a request signed by the other key whose HeaderKey is this team's public key (401).
  - Add to TestSignedRead's table: {"signed by another key", HeaderSig set to the other key's signature of the same GET, 401}.
  - Extend docFor to a full snapshot as v0.2.3 builds it: LastError sealed; on the codex account Days {1200, 0, 300}, Recent [{5h, at−2h, 900}] and Linked [{hermes, sealed, 2, {Input: 7}}]; a second account {Provider: hermes, Label: sealed, QuotaAt, QuotaFrom: codex, Windows: [the 5h window], Projects: []}; Aliases [{codex, sealed label, sealed name, at}]. Every relay test then publishes every current member.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −100. **Depends on:** F5, and the owner's answer (default: fold).
- **Check:** `go test -cover ./relay/`. Coverage of server.go and client.go does not drop below 96.4%.

#### F7. fakeKV.run answers only what KV sends · P2

- **Findings:** relay-16 (the value version).
- **Files:** `relay/kv_test.go` (from E11).
- **Change:** Keep fakeKV.run as a switch and delete the branches KV never reaches:
  - need() and its calls, and "ERR empty command";
  - SET's "invalid expire time" and "syntax error" branches. SET always gets EX, so it reads `secs, _ := strconv.Atoi(args[3])` and sets the expiry;
  - WRONGTYPE on SADD, SREM, SCARD and INCR;
  - INCR's non-integer error (`n, _ := strconv.ParseInt`);
  - EXPIRE's integer-parse error and the "Unsupported option" default; match NX and GT only.

  Keep WRONGTYPE on GET and SMEMBERS, which TestKVErrors' "wrong type" and "broken device set" reach. Keep the Authorization check and the "ERR unknown command" fallback, so a new store command fails loudly.
- **Tests:** this helper only.
- **Behavior:** none. **Risk:** low. **Net:** −70. **Depends on:** none.
- **Check:** `go test -cover ./relay/`. KV coverage in store.go is unchanged, and gocyclo lists no store test function over 15.

#### F8. One fake GitHub release server for the cmd update tests · P2

- **Findings:** cmd-18, tests-14 (its server half). cmd-18's in-package fakeGitHub is chosen over platform-11's exported selfupdatetest package, because a test-support package would be a new layer (Owner decision 23), and over tests-14's gitHub type, which is the same design with fields set directly.
- **Files:** `cmd/ai-usage/main_test.go`.
- **Change:**
  - Replace fakeRelease (main_test.go:1365-1401) with `type fakeGitHub struct { *httptest.Server; mu sync.Mutex; tag, badSum string; busy bool; lookups, downloads int; bin []byte }` and `newFakeGitHub(t, tag)`. It sets AIU_FAKE_RELEASE=tag with t.Setenv and serves, under mu:
    - `…/releases/latest`: counts a lookup; 503 "busy" when busy, else a redirect to `/tag/<tag>`;
    - `/download/<tag>/<asset>`: counts a download and writes bin;
    - `/download/<tag>/checksums.txt`: 64 zeros when tag equals badSum, else the real sum.
  - Add `(g *fakeGitHub) set(func(*fakeGitHub))` and `counts() (lookups, downloads int)`, both under mu.
  - releaseBuild (1505-1520) becomes `releaseBuild(t, running) (exe string, g *fakeGitHub)`.
  - TestUpdateCheckEveryScheduledRun uses `releaseBuild(t, "v1.3.0")` in place of 1676-1696, drops its own newUpdater (a copy of hermetic's 108-110) and its path guard, sets busy with g.set, reads g.counts(), and asserts downloads == 0 at the end.
  - TestFailedReleaseIsNotDownloadedAgain uses `exe, g := releaseBuild(t, "v1.2.0"); g.set(func(g *fakeGitHub) { g.badSum = "v1.3.0" })` in place of 1746-1797. The tag switch at 1842-1845 becomes `g.set` with tag "v1.3.1", plus the existing t.Setenv.
  - Callers read g.bin and g.counts() in place of `*downloads` and bin (1408-1446, 1661-1664, 1867-1879, 1902-1923).
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −45. **Depends on:** none.
- **Check:** `go test -count=1 -race ./cmd/ai-usage/ -run 'Update|Housekeeping|Panic|Schedule|Guide|FailedRelease'`.

#### F9. Check the launch agent plist with plutil only · P2

- **Findings:** platform-10 (Owner decision 18: the plist is checked only on macOS).
- **Files:** `internal/schedule/launchd_test.go`.
- **Change:** Delete readPlist and plistValues' non-darwin branch. TestAgentPlist starts with `if _, err := exec.LookPath("plutil"); err != nil { t.Skip("plutil reads the plist as launchd does; CI runs this on macOS") }`. Drop the imports this frees (encoding/xml, io). xmlText stays covered on every OS by the task XML tests.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −70. **Depends on:** B5.
- **Check:** on macOS, `go test ./internal/schedule/ -run TestAgentPlist -v` passes and does not skip. On Linux it skips. The CI macOS test job passes.

#### F10. Shared setup helpers in the collect tests · P3

- **Findings:** collect-17 (the value version).
- **Files:** `internal/collect/world_test.go` (from E2), and the test files E2 made.
- **Change:** Add to world_test.go:
  - `mkdirs(t *testing.T, dirs ...string)`: MkdirAll 0o700 on each, failing the test on error. It replaces both `mk` closures (TestDiscoverAndRemember, TestDiscoverFindsHermesProfiles), the MkdirAll loops at hermes_test.go:250-254 and 337-341 and collect_test.go:522-526, and the single MkdirAll-then-check blocks.
  - `hermesProfile(t *testing.T, dir string)`: writes the empty state.db marker. Use it at collect_test.go:1698, hermes_test.go:255 and 342, and in place of the `db` closure.
  - `(w *world) logout(p, home string)`: sets `readings[k] = probe.Reading{}` and `askErr[k] = notLoggedIn(p)`. Use it at collect_test.go:373-374, 478-479, 549-550 and 571-572, hermes_test.go:117-118 and orca_test.go:208-209. collect_test.go:656-657 wraps the error, so it stays.
  - `editConfig(t *testing.T, d state.Dir, edit func(*state.Config))` for LoadConfig, edit and SaveConfig. Use it at collect_test.go:514-521 and hermes_test.go:263-271 and 349-357.
  - `findTotals(st *state.State, provider, label string) (AccountTotals, bool)`. totalsFor calls it and fails when it is missing; hasTotals returns its bool.

  Add no other helpers.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −70. **Depends on:** E2, F2.
- **Check:** `go test ./internal/collect/` passes with the same test count.

#### F11. Reuse liveHermesDB, add mustTime, use snapshot.Providers in the logs tests · P3

- **Findings:** logs-18.
- **Files:** `internal/logs/sqlite_test.go` (from E7), `helpers_test.go`, `codexlimits_test.go` (from E6), `grok_test.go`, `logs_test.go`.
- **Change:**
  - TestHermesReadsUncheckpointedWAL uses `db := liveHermesDB(t, home); defer db.Close()`. It checks only the tokens, so the row's cwd is not needed.
  - Add `func mustTime(t *testing.T, s string) time.Time` (RFC3339Nano) to helpers_test.go, used in TestCodexLimits, TestGrokHours and checkHours.
  - fullHomes, TestReadersNeverWriteToTheHome and TestReadMissingHome use snapshot.Providers in place of `var providers` (C6 made the change in logs_test.go's list; this removes the local copy).
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −17. **Depends on:** C6.
- **Check:** `go test -count=1 ./internal/logs/`.

#### F12. Fewer probe test helpers and mode switches · P3

- **Findings:** probe-14 (the value version).
- **Files:** `internal/probe/fake_test.go`, `find_test.go` (both from E8), `codex_test.go`.
- **Change:**
  1. Add `func recordLines(t *testing.T, path string) []string`: read the file, fail when it is missing, and split the trimmed body on newlines. readRecord and readRuns use it.
  2. fakeEnv's Command passes the full `name` after `--`, not filepath.Base(name). fakeHarness records `Name: filepath.Base(args[0]), Path: args[0]`, with a new `Path string` field (json "path") on helperRecord. The "-on-path" remap then needs no special Command. Delete withRan (codex_test.go:197-207); F4's table and TestCodexOnlyBundled collect `r.Path` from readRuns.
  3. Delete exeName (find_test.go after E8) and use `binNames(name)[0]`.
  4. writeExe calls `writeFile(t, path, "#!/bin/sh\n")` and then `os.Chmod(path, 0o755)`.

  fakeHarness's claude switch and fakeCodex's account/read switch stay.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −25. **Depends on:** F4.
- **Check:** `go test ./internal/probe/`. gocyclo scores fakeHarness and fakeCodex lower.

#### F13. TestPutRejects' rows without repeated closures · P3

- **Findings:** relay-15.
- **Files:** `relay/server_test.go`.
- **Change:** Replace signedBy with `signed := func(b []byte) req { return req{fp, dev, pub, b, sig(k, b)} }` and `edited := func(edit func(*snapshot.Doc)) req { return signed(withDoc(edit)) }`. Rows become, for example, `{"names another device", edited(func(d *snapshot.Doc) { d.Device = "other-device" }), 422}` and `{"pretty printed", signed(pretty), 422}`.
- **Tests:** this test only.
- **Behavior:** none. **Risk:** low. **Net:** −25. **Depends on:** F5.
- **Check:** `go test -run TestPutRejects -v ./relay/` lists the same 28 subtests as before (26 table rows and the two trailing t.Run cases).

#### F14. View test fixtures in one place · P3

- **Findings:** view-team-15, tests-06. They list the same helpers. view-team-15's `attentionList` name is used, because tests-06's `attentionLines` is already a function in package view.
- **Files:** `internal/view/helpers_test.go`, `view_test.go`.
- **Change:**
  - Move spend (view_test.go:21-33) and docWith (34-46) to helpers_test.go. otherDoc becomes `return docWith(t, key, state.Config{Device: device}, host, at, st)`.
  - Add `(f *fixture) seal(at time.Time)`, used by newFixture and the 7 sites that seal by hand.
  - Add `weekQuota(at time.Time, pct float64) *state.Quota`: a weekly window begun 3 days before now, Source harness.
  - Add `spendVia(st *state.State, label, project, provider, to string, n int64, at time.Time)`: spend plus Via.
  - Add `attentionList(r Report) []string`, one "Kind devices Message" string per item. TestHeaderFailuresAreInAttention and TestFailedRunIsInAttention use it (668-670, 728-730).
  - Use `column(t, mx, label)` in place of the hand loops at 809-814, 948-956 (the col<0 check goes) and 1119-1130 (the c.Percent check stays).
- **Tests:** as above. Goldens stay unchanged.
- **Behavior:** none. **Risk:** low. **Net:** −60. **Depends on:** B1.
- **Check:** `go test ./internal/view -list '.*' | sort` prints the same names before and after. `/usr/bin/git status internal/view/testdata` is clean.

#### F15. Page test helpers: one body-line finder · P3

- **Findings:** view-render-12.
- **Files:** `internal/view/page_test.go`, `helpers_test.go`.
- **Change:** Add `bodyLine(t *testing.T, p Page, from string, match func(text string) bool) string` next to pageSection, with the doc "bodyLine is the first line of p's body, as drawn, from the one whose text starts with from on, whose text matches." TestPageHeat's row and TestPageHeadersDim's line call it. Inline moreAttention into TestPageAttentionIsCut. newFixture passes cfg to collect.BuildDoc.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −15. **Depends on:** none.
- **Check:** `go test ./internal/view -run 'TestPage|TestStatus' -v` shows the same number of passing tests.

#### F16. hermetic clears the relay variables and restores every hook · P3

- **Findings:** cmd-19 (the value version), tests-14 (its KV-clearing item).
- **Files:** `cmd/ai-usage/main_test.go`.
- **Change:**
  - hermetic (main_test.go:61-111) clears KV_REST_API_URL and KV_REST_API_TOKEN with the other variables, and saves and restores collectOnce and sleepCtx with the other hooks.
  - Delete 740-742 and 769-771 (the relay tests' own KV clearing) and 1174-1175 (TestScheduleRun's save and restore).
  - Add `(d *device) env()` with the three Setenvs from run (222-224); run calls it. TestTeamJoinReadsOneLine:713 uses `b.env()` in place of `b.run("", "version")`. TestScheduleRunStartsACollection:1259-1260 uses `d.env()`; AIU_AS_CLI stays.

  Add no memRelay, lockFree or usage helpers.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −10. **Depends on:** F8.
- **Check:** `go test -count=1 ./cmd/ai-usage/`.

#### F17. Leave page layout to the view goldens in the cmd tests · P3

- **Findings:** cmd-20 (Owner decision 11).
- **Files:** `cmd/ai-usage/main_test.go`, `term_test.go`.
- **Change:**
  - In TestCollectOfflineThenViews, keep three markers: the header with the team fingerprint (main_test.go:596), "\nSUBSCRIPTIONS  2 · 2 over\n" (598) and the /work/app project row (606). Delete the other 9 layout strings (597, 599-605, 607).
  - Use view.SchemaVersion in place of the literal 4 at main_test.go:554, 618 and 641 and term_test.go:103. view_test.go and the smoke test keep a literal (I2 changes the smoke test).
  - In TestTwoDevicesShareATeam (879), keep the "\nDEVICES  2 · by 7d" and no-matrix checks and drop the column-header regexp. The regexp import stays for TestUsageSections.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −12. **Depends on:** the owner's answer (default: do it).
- **Check:** `go test ./cmd/ai-usage/ -run 'TestCollectOfflineThenViews|TestTwoDevicesShareATeam' && go test ./internal/view/ -run Golden`.

#### F18. Drop TestViewConfig's color-profile loop · P3

- **Findings:** tui-10.
- **Files:** `cmd/ai-usage/interactive_test.go`.
- **Change:** Delete interactive_test.go:81-94 and the colorprofile import (line 15). Keep 48-79: it does not ask before opening, COLORFGBG, and the --devices and --projects wiring. TestViewColor and TestTerminalDetection cover the profiles.
- **Tests:** this test only.
- **Behavior:** none. **Risk:** low. **Net:** −15. **Depends on:** C10.
- **Check:** `go test -run 'TestViewConfig|TestViewColor|TestTerminalDetection' ./cmd/ai-usage`.

#### F19. One openPTY for macOS and Linux tests · P3

- **Findings:** tui-11.
- **Files:** new `cmd/ai-usage/pty_unix_test.go`; `pty_darwin_test.go`, `pty_linux_test.go`, `background_unix_test.go`.
- **Change:** pty_unix_test.go (`//go:build darwin || linux`) holds control, answering and one openPTY. openPTY opens /dev/ptmx, gets the name through `control(m, func(fd int) (err error) { name, err = ptsName(fd); return })`, opens the other end with O_NOCTTY, sets the 120x40 window size and registers the Cleanup; this absorbs ptySlave. pty_darwin_test.go keeps `ptsName` with TIOCPTYGRANT, TIOCPTYUNLK and TIOCPTYGNAME. pty_linux_test.go keeps ptsName with TIOCSPTLCK and TIOCGPTN, returning /dev/pts/N. background_unix_test.go keeps only TestQueryBackground.
- **Tests:** as above.
- **Behavior:** none. **Risk:** low. **Net:** −9. **Depends on:** A7.
- **Check:** `go test -run 'TestQueryBackground|TestViewColor' ./cmd/ai-usage` on macOS and `GOOS=linux go vet ./cmd/ai-usage`. The Linux CI job runs it for real.

#### F20. onlyFiles in TestCleanupOldUnderBrackets · P3

- **Findings:** platform-12 (the value version).
- **Files:** `internal/selfupdate/selfupdate_test.go`.
- **Change:** Replace the ReadDir, names and DeepEqual block (703-713) with `onlyFiles(t, dir, "ai-usage.exe", "other.old-1")`, and drop the reflect import if nothing else uses it. The Windows swap tests and the state ReadDir loops stay.
- **Tests:** this test only.
- **Behavior:** none. **Risk:** low. **Net:** −10. **Depends on:** none.
- **Check:** `go test ./internal/selfupdate/ -run TestCleanupOldUnderBrackets`.

#### F21. view_test.go splits by the file each test covers · P3

- **Findings:** view-team-16, tests-13 (superseded: view-team-16's layout follows E3's files).
- **Files:** `internal/view/view_test.go` (deleted), new `build_test.go`, `team_test.go`, `names_test.go`, `matrix_test.go`, `hermes_test.go`, `attention_test.go`, `status_test.go`; `forecast_test.go`.
- **Change:** Pure moves:
  - build_test.go: TestAccountOfThisDevice, TestProjectsSortByPeriod, and TestJSONFieldNamesAreStable with keys and jsonFields.
  - team_test.go: TestUsageShiftsByTheDaysSinceADeviceReported, TestTeamAddsTokensAndTakesNewestReadingOfEachWindow, TestTeamCacheOfAnotherTeamIsIgnored, TestUnreadableLabels, TestSubscriptionsGoWorstFirst, TestDeviceErrors and TestTeamPlanIsTheNewestAndPerDeviceIsByTokens.
  - names_test.go: TestShortNames and the four TestAlias* tests.
  - matrix_test.go: TestMatrix, TestUsersSinceTheWindowBegan and TestMatrixSplitsHermesByLogin.
  - hermes_test.go: the five TestTeamLink* tests and TestLastActivityThroughHermes.
  - attention_test.go: TestAttention, TestAttentionOverAtResetOrder, TestHeaderFailuresAreInAttention and TestFailedRunIsInAttention.
  - forecast_test.go gains TestWindowThatHasResetIsUnknown and TestRefusalReading.
  - status_test.go: TestStatusFoldsManyHomes, TestClaudeAppHomeIsNotTheUsersHome, TestStatusUpdateLine and TestCollectorSection.
- **Tests:** moves only.
- **Behavior:** none. **Risk:** low. **Net:** +30. **Depends on:** E3, F14.
- **Check:** the move check from phase E, then `go test ./internal/view`.

#### F22. main_test.go splits to match the command files · P3

- **Findings:** cmd-17, tests-14 (superseded: its split follows the same files). Both cmd-17 versions agree except for TestCollectOfflineThenViews. The value version puts it in collect_test.go, because the test starts with `collect`.
- **Files:** `cmd/ai-usage/main_test.go`, `help_test.go`, and new `cli_test.go`, `report_test.go`, `collect_test.go`, `team_test.go`, `name_test.go`, `relay_test.go`, `home_test.go`, `schedule_test.go`, `update_test.go`.
- **Change:** Pure moves, with no build tag on any new file:
  - cli_test.go: TestMain, hermetic, fakeProbeEnv, TestHelperProcess, fakeCodexAppServer, device, newDevice, result and the device methods (run, env, ok, report, state, config, write, claude, codex), provider, fullest and fingerprint.
  - help_test.go gains TestVersionHelpAndUsageErrors, plainHelp, helpShown and TestUsageSections.
  - report_test.go: TestReportBeforeAnyRunWritesNothing and TestReportFrom.
  - collect_test.go: TestCollectWhileAnotherRunCollects, TestCollectOfflineThenViews, TestGuideAfterInstall and TestStopAfterSample.
  - team_test.go: TestTeamKeyAndJoin, TestTeamJoinReadsOneLine and TestTwoDevicesShareATeam.
  - name_test.go: TestDeviceName.
  - relay_test.go: TestRelayServeClientIPHeader, buggyContext, TestRelayShutdownPanicIsItsError and TestRelayCommands.
  - home_test.go: TestHomeCommands and TestHomeRemoveForgetsItsSessions.
  - schedule_test.go: fakeCrontab, TestScheduleCommands, TestScheduleRun, TestScheduleRunStartsACollection, TestScheduledRunsUseTheSameFolder, TestCommentedOutScheduleIsLeftAlone and TestStoppedHousekeeping.
  - update_test.go: fakeGitHub and releaseBuild (F8), TestHousekeepingOnReleaseBuild, TestUpdateCommandRecordsResult, TestUpdateCheckAfterClockRanAhead, TestUpdateCheckEveryScheduledRun, TestFailedReleaseIsNotDownloadedAgain, TestUpdateWhenCollectionFails and TestPanicIsRecordedAndStillUpdates.
  - main_test.go is deleted when empty.
- **Tests:** moves only.
- **Behavior:** none. **Risk:** low. **Net:** +90. **Depends on:** E1, F8, F16, F17.
- **Check:** `go test -list '.*' ./cmd/ai-usage/` prints the same names on macOS before and after. `GOOS=windows go vet ./cmd/... && GOOS=linux go vet ./cmd/... && go test -count=1 ./cmd/ai-usage/`.

#### F23. relay/server_test.go splits by subject · P3

- **Findings:** relay-12, tests-15 (superseded: its split).
- **Files:** `relay/server_test.go`, new `helpers_test.go`, `client_test.go`, `cap_test.go`, `limits_test.go`.
- **Change:** Pure moves:
  - helpers_test.go: t0, clock, relayEnv, newRelay, client, clientFrom, headerTransport, newKey, docFor, marshal, statusOf, putRequest, signedRequest, send, stored, testStore and errBroken.
  - client_test.go: TestPublishPullRoundTrip, TestPullCountsDocumentsThatDoNotVerify, TestPullKeepsOneDocumentPerDevice, TestPullRejectsAMalformedTeamRead and TestClientErrors.
  - cap_test.go: TestDeviceCap, TestTeamReadListsNoMoreThanTheCap, TestFirstWritePastTheCapGivesWay, TestFirstWritePastTheCapFailsClosed, TestDevicePushedPastTheCapIsTold, TestFullTeamReadFitsVercel and TestRecordLifetimeGrowsWithTheDevice.
  - limits_test.go: TestTeamWriteLimit, TestPerIPLimit, TestNewTeamsPerIP, TestNewDevicesPerIP, TestClientAddr, TestRejectedRequestsSkipTheStore and TestOverLimitIsBounded.
  - server_test.go keeps TestPutRejects, TestStaleAndRepeatedWrites, TestSignedRead, TestDelete, TestHealth, TestStoreFailuresAre503, TestCollectorEncodingIsCanonical and TestSlowBodyTimesOut.
- **Tests:** moves only.
- **Behavior:** none. **Risk:** low. **Net:** +25. **Depends on:** E10, F5, F6, F13.
- **Check:** `go test ./relay/`. The count from `go test -list . ./relay/` equals the count before F5, minus the tests C12 and F6 delete.

#### F24. tui_test.go splits by subject; tui.go stays whole · P3

- **Findings:** tui-12 (the value version: tests only; Owner decision 22), tests-16 (superseded).
- **Files:** `internal/tui/tui_test.go`, new `keybar_test.go`, `scroll_test.go`, `help_test.go`, `run_test.go`.
- **Change:** Moves, with imports fixed per file by goimports. Line numbers are today's.
  - tui_test.go keeps the shared helpers (fakeRender, report, model, update, press, keys, screen, bar, quits, refresher; 1-130 and 157), ansiStrip (904-910), teamReport (442-454), TestFrame, TestNarrowAndShortTerminals, TestQuit and TestBackground.
  - keybar_test.go: TestKeyBar, TestPills, TestPeriods, TestShare, viewsRender, TestDeviceViewsAt80 and B1's rewritten TestDeviceViewsDrawn. TestDeviceViews splits at its comment seams into TestDeviceViews (331-363: the `s` round trip, share, scroll and top), TestDeviceViewsStart (365-384: opened in status, `s` waits in the help, the help text, one device has one view) and TestDeviceViewsBar (386-425: the ASCII and color forms, narrow widths). Each part already builds its own model.
  - scroll_test.go: bigTeam, sight, TestPinnedHead, TestPinnedHeadSideways, TestMatrixScroll and TestScroll.
  - help_test.go: TestHelp.
  - run_test.go: immediate (131-155), TestRefresh, TestReload, TestJobs and TestRun.
- **Tests:** the count is today's, minus TestOlderDevice (B1), plus the two new TestDeviceViews parts.
- **Behavior:** none. **Risk:** low. **Net:** +20. **Depends on:** B1, A5, C17.
- **Check:** `go vet ./internal/tui && go test -v ./internal/tui`.
### Phase G. Comments

The audit found few noisy comments: most comments in this code say why. Phase G deletes the ones that only restate a name or tell history, and fixes the ones the share rework left stale. Other comment edits ride with the step that changes their code (for example C1, C7, C8 and B-phase deletions). Declarations are named, not numbered, since phase E moves them.

#### G1. Fix the stale comments in the view tests · P3

- **Findings:** view-team-17 (its code half; H5 takes its ai-report.md lines).
- **Files:** `internal/view/golden_test.go`, `devicestatus_test.go`.
- **Change:**
  - golden_test.go:17 becomes "// loadReport loads a report from testdata."
  - Delete golden_test.go:78, so the case reads as the share mode in color.
  - devicestatus_test.go:11-13 becomes "// statusRows are the device names of DEVICES' rows, in order, in either view."
- **Tests:** comments only.
- **Behavior:** none. **Risk:** low. **Net:** −2. **Depends on:** B1.
- **Check:** `go vet ./internal/view`, and a read of the diff.

#### G2. Delete doc comments in cmd that only restate the name · P3

- **Findings:** cmd-21.
- **Files:** `cmd/ai-usage/help.go`, `home.go`, `alias.go`, `main.go` (collect.go after E1).
- **Change:** Delete these 7 comment lines, and keep every comment that says why:
  - help.go: "helpIntro is the first line of the help.", "helpSection is a heading of the help and its lines.", "helpSectionNamed is whether a section of the help is titled s." and "helpPunct is the punctuation of what to type.";
  - home.go: "homeRef is one harness home.";
  - alias.go: "account is one account this device knows of.";
  - main.go: "panicError is a collection that panicked."
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −7. **Depends on:** none.
- **Check:** `go vet ./cmd/...`.

#### G3. Delete the history and restating comments in probe · P3

- **Findings:** probe-15.
- **Files:** `internal/probe/codex_test.go`, `codex.go` (codexrpc.go after E8), `probe.go` (find.go after E8).
- **Change:**
  - TestCodexRPCAbandonedCall's comment keeps its first sentence and loses "The old reader-per-call design raced here and lost it."
  - Delete "unsupported is an error answer that is errUnsupported." and "realPath is path with its links resolved, or path itself when that fails."
  - The claude.go comment "The null device: there is no prompt to wait for." goes with its `cmd.Stdin = nil` line in C8. The duplicate getenv and Getenv comments go with Env.Getenv in C7.
- **Tests:** comments only.
- **Behavior:** none. **Risk:** low. **Net:** −3. **Depends on:** C7, C8.
- **Check:** `go vet ./internal/probe/`.
### Phase H. Documents

The docs keep today's behavior and drop history the code no longer has. The B steps already remove every doc sentence about the compatibility they delete. Phase H removes the rest of the history, the copies of text that lives elsewhere, and the statements the code no longer matches. Line numbers are at d460afa.

#### H1. Remove the report-redesign history from design.md · P2

- **Findings:** docs-09.
- **Files:** `design.md`.
- **Change:**
  - Delete the last sentence of design.md:3, 'Its section "Next: report redesign" lists what the collector and the relay must add for this design.'
  - Delete the last sentence of design.md:160, "This replaces the 6-hour pace …".
  - Delete 317-334, "## What goes" and "## Build order".
  - design.md:189 becomes "The status view is DEVICES as a table of each device's state: which devices report, on which release, and what fails on them. `s` in the interactive view switches to it and back; `--devices` prints it and opens the interactive view on it." B1 already dropped its older-collector clause.
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −21. **Depends on:** B1.
- **Check:** `/usr/bin/grep -nE 'What goes|Build order|before v0.2.0|came back|Next: report' design.md` prints nothing.

#### H2. design.md points to README's example page, which is the golden file · P2

- **Findings:** docs-10. Both versions agree; the text below merges them.
- **Files:** `design.md`, `internal/view/golden_test.go`.
- **Change:**
  - Replace design.md:28-79, the caption line and the fenced page, with: "The page at 160 columns is the example at the top of [README.md](README.md), which is `internal/view/testdata/team-160.golden`; the golden files are exact where this text is not."
  - Move the key-bar line (design.md:78, " ↑↓ scroll · ←→ matrix · s ‹usage› status · p period ‹7d› · % share · r refresh · ? help · q quit") into "## The interactive view", as a fenced block just before "Keys:".
  - design.md:5 ends its second sentence at "… show the result." and drops ": the matrix rows go by total, and OVER lines by when they run out".
  - golden_test.go:34 says "the zone of README.md's sample page" in place of "design.md's".
- **Tests:** one comment.
- **Behavior:** none. **Risk:** low. **Net:** −48. **Depends on:** H1.
- **Check:** `diff <(/usr/bin/sed -n 15,73p README.md) internal/view/testdata/team-160.golden` prints nothing. design.md has two page blocks left: the status view and the key bar.

#### H3. Rewrite the refactoring entry in ai-report.md's backlog · P3

- **Findings:** docs-01. It lands in the first commit of phase A, beside this file, although it is listed here.
- **Files:** `ai-report.md`.
- **Change:** Replace ai-report.md:132-139 with one bullet:

  "- **A refactoring review for short, expressive code.** In progress; [refactoring.md](refactoring.md) lists every change, its order, and how it is checked. It cuts repeated logic, long functions, files that grew too large, layers, options, and branches nothing needs, dead code, comments that restate the code, and tests that repeat others or guard what goes. It drops everything that exists only for collectors, snapshots, relay records, state files, schedules, or installs from before v0.2.3, since every device runs v0.2.3 or later before it ships. It keeps what deployed releases still need: the release file names, `checksums.txt`, and the `releases/latest` lookup they update through; the relay's acceptance of v0.2.3 snapshots and requests, since the relay deploys before a release; the key, signature, and sealing formats; and the snapshot, the relay protocol, and the JSON (`schema_version` 4) as they are. The current release's behavior does not change, except where refactoring.md says so and the owner agreed."

  When the refactor is done, the entry becomes one line under Implementation status (Owner decision 13).
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −6. **Depends on:** none.
- **Check:** the link resolves. `/usr/bin/grep -n 'schema_version. 3' ai-report.md` finds only line 196, which H5 fixes.

#### H4. Delete done and obsolete roadmap, backlog and status entries in ai-report.md · P3

- **Findings:** docs-11, compat-10. compat-10's other items are elsewhere: 139 is H3's, 196 is H5's, and the last sentence of 204 and the bullet at 233 are B10's. compat-10's rewrite of 105 is not needed, because 105 goes.
- **Files:** `ai-report.md`.
- **Change:**
  - 93 becomes "Decided on 2026-09-23 with the owner. The periods and day buckets are built; the day and week tables and the utilization history are still to do."
  - 97 becomes "The report needs a breakdown by day and by quota week beside its periods."
  - Delete 105, the day-bucket sketch that is built, and 110-120, "Report redesign (done)".
  - Replace 140-144 with: "- **Say why an account has no reading.** The report shows only `?`. Saying why, in the status view's NOTE and in the JSON, would let a teammate's device be diagnosed from the team view." The Codex rate-limits timeout is added back only if the owner says it still happens (Owner decision 14). The new text names no device and no release.
  - Move the official-relay sentence from the "Relay deployment (done)" bullet at 225 into the Relay bullet at 208: where the relay runs, that it deploys on every push to `main`, and that the repository variable `AI_USAGE_RELAY_URL` builds it into release builds. Delete the 225 bullet.
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −24. **Depends on:** B10, H1.
- **Check:** `/usr/bin/grep -nE 'Report redesign|v0\.1\.[0-9]|\(done\)|32 KB|older than this format|Deploy order' ai-report.md` prints nothing, and no doc links a heading that is gone.

#### H5. Fix the doc and help statements the code no longer matches · P3

- **Findings:** docs-12, view-team-17 (its ai-report.md half), compat-10 (item 196). For line 197, view-team-17's value text is used, because it names the share mode as d155357 defined it. For line 196, docs-12's text is used, because a line without the version number cannot go stale again.
- **Files:** `ai-report.md`, `cmd/ai-usage/help.go`.
- **Change:**
  - ai-report.md:196 becomes "- **Views**: the console report of [design.md](design.md) and JSON per [docs/json-schema.md](docs/json-schema.md). Both include the other devices in the team."
  - In ai-report.md:197, "the DEVICES × SUBSCRIPTIONS matrix of tokens or estimated share per device and account" becomes "the DEVICES × SUBSCRIPTIONS matrix of each device's tokens per account, or its share of each column's tokens".
  - help.go:80, the AI_USAGE_HOME row, reads "the state folder; `ai-usage status` shows where it is".
  - help.go:56 becomes "run a relay (Upstash for Redis from `KV_REST_API_URL` and `KV_REST_API_TOKEN`, else memory)".
  - releasing.md's CI list is I1's.
- **Tests:** none change; TestHelp passes.
- **Behavior:** two lines of `ai-usage help` change wording. **Risk:** low. **Net:** 0. **Depends on:** H3.
- **Check:** `go test ./cmd/ai-usage -run TestHelp`. `/usr/bin/grep -nE 'estimated share|schema_version. 3|Vercel KV from|OS config dir' ai-report.md cmd/ai-usage/help.go` prints nothing.

#### H6. README's state folder table lists every file · P3

- **Findings:** docs-13 (the value version), platform-15 (its README half). Both change the lock row; docs-13's wording is used, and platform-15 adds the `.bad` row.
- **Files:** `README.md`.
- **Change:**
  - README.md:196 becomes "| `run.lock`, `config.lock`, `schedule.lock` | keep two runs from overlapping, two commands from changing `config.json` at once, and tell other runs that `schedule run` schedules this folder; held with the system's file lock, which ends with its process |".
  - Add the row "| `state.json.bad` | a `state.json` that did not parse, kept when a run started again from an empty state |".
  - `report --from` stays out of README and help: it is for the demo only (Owner decision 15).
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** +3. **Depends on:** B4.
- **Check:** `/usr/bin/grep -c 'schedule.lock' README.md` prints 1, and the table renders with one row per file in a state folder.

#### H7. README's container section becomes a summary · P3

- **Findings:** docs-16 (the value version).
- **Files:** `README.md`.
- **Change:** Keep the heading at README.md:318, which 152 links. Replace 320-337 with: "The collector runs in a container as on any Linux machine: install it inside, as the user whose tools it should read, and each container is a machine in the team. Keep that user's home on a volume, name the machine with `AI_USAGE_NAME`, and run `ai-usage schedule run` beside the main process, since containers rarely have cron. [docs/containers.md](docs/containers.md) has the install command, an entrypoint, a Dockerfile, and an s6-overlay service."
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −15. **Depends on:** none.
- **Check:** `/usr/bin/grep -c 'docker exec -i' README.md` prints 0, and the `#in-a-container` link resolves.

#### H8. relay.md stops copying the protocol · P3

- **Findings:** docs-17.
- **Files:** `docs/relay.md`, `docs/relay-protocol.md`.
- **Change:**
  - Delete relay.md:182-186 (the header list) and 200-210 (the status table). Keep the request table (175-180) and line 188; line 173 already says relay-protocol.md specifies every field and rule. B10 already points relay.md:144 to relay-protocol.md §11.
  - In relay-protocol.md:496, delete "The reference collector writes 4 times an hour … 250 requests an hour." and "The device cap follows from the team read: … a Vercel Function may return." End the paragraph with "[relay.md](relay.md#limits) explains how these limits were chosen."
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −15. **Depends on:** B10, E10.
- **Check:** the relay.md API section is about 10 lines, and relay-protocol.md §6.8 still defines every status code.

#### H9. Samples stay; the docs stop saying pace reads them · P3

- **Findings:** platform-16 (Owner decision 20: keep samples).
- **Files:** `ai-report.md`.
- **Change:** The last sentence of ai-report.md:235 becomes 'Samples written while the account was unknown keep it unknown; nothing reads samples yet, and the day and week tables in "Next: usage over time" will.' Delete the bullet at 237 ("Pace uses only this device's samples"). The code does not change: LoadSamples is the read path those tables need and the growth oracle in the collect tests.
- **Tests:** none.
- **Behavior:** none. **Risk:** low. **Net:** −1. **Depends on:** H4, which deletes the sketch the old sentence pointed to.
- **Check:** `/usr/bin/grep -n 'Pace uses' ai-report.md` prints nothing.

#### H10. ai-report.md's Implementation status keeps only what no other doc says · P3

- **Findings:** docs-14 (the value version). Done only if the owner agrees (Owner decision 16; default: not done).
- **Files:** `ai-report.md`.
- **Change:** The rule: README.md is the source of user-facing behavior and design.md of the report. ai-report's Implementation status keeps requirements, reasons found nowhere else, and limits. Edits:
  - 220: "Each behavior in this document keeps a test" becomes "Each behavior this document, README.md, or design.md states keeps a test", so the deletions below free no test.
  - 154 becomes "`install.sh` picks the binary's folder and edits a shell profile as [README](README.md#install) says. It replaces only a plain file of this program, which it tells by the module path Go builds into the binary."
  - Delete 155, 157, 161, 162, 165, 166, 168, 169, 172, 189, 198, 199 and 202. They restate README.md's install, schedule, update and discovery sections and design.md's forecast, widths and short names.
  - 163 becomes "The latest release is the tag `releases/latest` redirects to; a redirect to `releases/latest` under a new owner or name is followed up to three times. A run gives the whole check 7 minutes."
  - 164 keeps only its last sentence.
  - 171 keeps the home and record paths and "Only the email and `userSelectedFolders` are decoded, never the title, the first message, or the system prompt."
  - 192 keeps the macOS local-host-name sentence.
  - 218 becomes "**Guide after install.** A new device prints a short guide under the first report a person reads, once ([README](README.md#first-run)). The binary prints it, so every installer gets it; `state.json` keeps it due until a report is printed as text."
  - 148 takes the refactor's date.
- **Tests:** none.
- **Behavior:** none. **Risk:** medium: a deleted sentence may hold a rule found nowhere else. **Net:** −13. **Depends on:** B4, B5, B8, H5, and the owner's answer.
- **Check:** each deleted behavior is still stated in README.md or design.md; grep for '6 hours', 'once on wake', 'Sharing settings' and 'remembered for scheduled runs'.
### Phase I. CI and lint gates

Phase I keeps the result from drifting back. It lands last, when the tree is clean under every linter it adds.

#### I1. CI runs vet, staticcheck and deadcode for every release OS, and go mod tidy · P2

- **Findings:** platform-13 (the value version: one step in the test job, not a new job), docs-12 (its releasing.md line), platform-15 (its CI list half).
- **Files:** `.github/workflows/ci.yml`, `docs/releasing.md`.
- **Change:**
  - Delete the lint job's `- run: go vet ./...` (ci.yml:34). The lint job keeps `go build ./...` on go.mod's Go.
  - In the test job (stable Go), replace `- run: go vet ./...` (ci.yml:61) with:

    ```yaml
          - if: runner.os == 'Linux'
            name: vet, staticcheck and deadcode for every release OS; go mod tidy
            run: |
              go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
              go install golang.org/x/tools/cmd/deadcode@v0.50.0
              bin=$(go env GOPATH)/bin
              for os in linux darwin windows; do
                GOOS=$os go vet ./...
                GOOS=$os "$bin/staticcheck" ./...
                d=$(GOOS=$os "$bin/deadcode" -test ./...)
                [ -z "$d" ] || { echo "$d"; exit 1; }
              done
              go mod tidy -diff
    ```

    The GOOS=darwin run replaces the macOS vet that goes. The tools are pinned and bumped by hand (Owner decision 19). `deadcode -test` counts tests as roots, so api/relay.go (reached from its test) and the typeahead helpers (reached from typeahead_test.go) are not reported.
  - docs/releasing.md's CI list becomes:
    - "`gofmt` and `go build` with the oldest Go that `go.mod` allows";
    - "`go vet`, `staticcheck` and `deadcode -test` for Linux, macOS, and Windows, and `go mod tidy -diff`, with the stable Go";
    - "`go test` on Linux and macOS, plus `go test -race` on Linux; Windows is cross-compiled and installed by the smoke test, but its unit tests are not a gate";
    - the shell, PowerShell, cross-compile and installer lines as they are.
- **Tests:** none.
- **Behavior:** none for users; CI fails on new lint. **Risk:** low. **Net:** +6. **Depends on:** A3, A7 (the lint prerequisites), A13, B4, B5.
- **Check:** run the loop locally from the repository root with the tools installed into a scratch GOBIN; it exits 0. Push a branch and see the test job pass on both runners.

#### I2. The install smoke test stops hard-coding the schema version · P3

- **Findings:** platform-14.
- **Files:** `.github/scripts/install-smoke.sh`.
- **Change:** Replace line 101 with `grep -Eq '"schema_version": [0-9]+,' "$work/report.json" || fail "report --json has no schema_version"`.
- **Tests:** this script only.
- **Behavior:** none. **Risk:** low. **Net:** 0. **Depends on:** none.
- **Check:** `shellcheck -s sh .github/scripts/install-smoke.sh`. The CI install job passes on all three runners.
## 6. Verification gates

After every step, besides its own check:

1. `gofmt -l .` prints nothing. `go build ./...` passes, and `go vet ./...` passes with GOOS set to linux, darwin and windows.
2. `go test ./...` passes, and `go test -race` passes on the packages the step touched. Write test output to a file and read its tail.
3. `/usr/bin/git diff --stat HEAD~1 -- internal/view/testdata internal/snapshot/testdata` prints nothing. Only two steps touch golden files: A1 adds `v1.json`, and B1 deletes the four `team-older-*` goldens. No step regenerates a golden to pass.
4. `go test -cover ./...` shows no package below its baseline in section 4, unless the step deleted the code a test covered. This gate needs F1.
5. For a step that moves code, `go test <pkg> -list '.*' | sort` prints the same names before and after, and `/usr/bin/git diff -M --color-moved=zebra` shows only moved blocks and the edits the step names.

At the end of each phase:

6. staticcheck and `deadcode -test` with GOOS set to linux, darwin and windows report nothing new. After phase A they report nothing at all.
7. On a copy of a v0.2.4 state folder, `AI_USAGE_HOME=<copy> ai-usage report --json` and `report --plain` print the same before and after the phase. The report the relay pulls from the team stays readable: a v0.2.4 binary publishes to `ai-usage relay serve` built from the tree, and the tree's binary pulls and draws it.
8. On a Mac, `ai-usage schedule` shows the launch agent as Active after a `collect`, and the crontab stays empty.

Before the first release built from this tree:

9. CI is green on all three runners, including the install smoke test.
10. A v0.2.4 binary finds and installs the new release with `ai-usage update`. This checks the asset names, the checksum file and the `version` output together.

## 7. Owner decisions

Each decision has a recommendation and a default. The default is what the implementation does if the owner does not answer.

1. **Keep `schema_version` 4 while `unknown` and `window_unknown` leave the JSON (B1).** No v0.2.x report ever set them for a current device, and no reader outside this repository exists before launch. Recommendation: yes. Default: yes.
2. **A pre-v0.2.0 record still on the relay shows 0 tokens, not `?` or `≥` (B1).** Records expire 7 to 90 days after their last write, and every device has run v0.2.3 or later since 2026-09-24. Recommendation: accept. Default: accept.
3. **Who runs the device checks under Preconditions, and when (B2, B3, B5).** Recommendation: the owner runs them on every device, or asks each device's user to, before B2. Default: B2, B3 and B5 wait until every device passes; the rest of the plan goes ahead.
4. **The new leaf package is named `internal/fsutil` (C4, C19).** It holds WriteFile, RealPath and Tilde. Recommendation: fsutil. Default: fsutil.
5. **`collect.Options.Ask` takes the home's last use as a parameter (C7).** This widens an internal function type instead of hiding the value in a context. Recommendation: yes. Default: yes.
6. **`ai-usage status` and the guide keep their look (C13).** They keep `✕`, " - " and the base-16 colors when drawn by the page renderer. dup-17 would move them to the page theme's colors. Recommendation: keep. Default: keep.
7. **Probe error text is cut without an ellipsis (A14).** dup-08 would add one, which changes stored error text. Recommendation: no ellipsis. Default: not done.
8. **Alias names measured with the view's width (relay-08).** This changes which names are accepted at the edges, including names teammates already published. Recommendation: no. Default: not done.
9. **`home` and `alias` parse flags anywhere with the flag package (cmd-06).** The refusal texts would change to Go's standard wording. It saves about 10 lines. Recommendation: not in this refactor. Default: not done.
10. **`-h` for every command, and stray arguments refused with the standard wording (cmd-22).** This is a user-visible change to `update`, `relay show`, `relay clear`, `team key`, `name show`, `name clear` and `home list`. Recommendation: a separate change after this plan. Default: not done.
11. **cmd tests check wiring and a few markers, and leave layout to the view goldens (F17).** Recommendation: yes. Default: yes.
12. **docs/json-schema.md keeps only "Changes from version 3" (B7).** Recommendation: yes. Default: yes.
13. **The backlog's refactoring entry becomes one line under Implementation status when the plan is done (H3).** Recommendation: one line. Default: one line.
14. **The Codex rate-limits timeout in the backlog (H4).** It was last seen on a device that ran an old release. Recommendation: drop it unless it happens on the current release. Default: dropped; the open item is restated without device names.
15. **`report --from` is for the demo only (H6).** Recommendation: demo only, left out of README and help. Default: demo only.
16. **ai-report.md's Implementation status keeps only requirements, reasons found nowhere else, and limits (H10).** Recommendation: not in this refactor; it changes what the document is, and a deleted sentence may hold a rule. Default: not done.
17. **selfupdate.Check only extracts `install`, and the file stays whole (D24).** Recommendation: extract only. Default: extract only.
18. **The launch agent plist is checked only on macOS, with plutil (F9).** CI runs the tests on macOS, and the task XML tests cover xmlText everywhere. Recommendation: yes. Default: yes.
19. **staticcheck and deadcode are pinned, and deadcode is a gate (I1).** Recommendation: pinned versions bumped by hand; deadcode `-test` as a gate. Default: as recommended.
20. **Samples stay, and only the docs change (H9).** The day and week tables will read them, and the collect tests use them. Recommendation: keep. Default: keep.
21. **TestCannotWriteAnotherTeam is folded into TestDelete and TestSignedRead (F6).** Every case it checks stays, under the tests of each endpoint. Recommendation: yes. Default: yes.
22. **Only tui_test.go splits; tui.go stays whole (F24).** tui.go is about 600 lines after A5 and C17 and reads top to bottom. Recommendation: tests only. Default: tests only.
23. **No exported test-support packages (F8).** platform-11 proposed `selfupdatetest` and `scheduletest`. Recommendation: no; F8's in-package fake is enough. Default: not done.

## 8. Considered and not done

### Rejected findings

| Finding | Proposal | Why not |
|---|---|---|
| collect-18 | Trim TestBuildDocDecodesAndOpens to the envelope and sealing checks | The lines it would cut check BuildDoc's mapping of totals into the snapshot: the sealed label, Current and Plan, the quota's windows, LastActiveAt and sealed project paths. No other test covers that mapping. |
| collect-19 | Reword the "old ledger" comments and shorten applyRejected's comment | The comments are accurate. Hours and ByHours first shipped in v0.2.0, and the ledger keeps v0.1.x sessions for 90 days. applyRejected's comment is the only statement of its refusal rules. |
| compat-12 | Reword comments that justify code by ledgers from before hours or an older collector | Same premise as collect-19, and wrong for the same reason: live v0.2.3 ledgers still hold such sessions. |
| logs-14 | Split readGrok into the walk and one session | After C2, A4 and A6, readGrok is about 45 linear lines. The two helpers would each have one caller and thread an out-parameter. |
| tests-09 | Run the view width sweeps in parallel and shorten a TUI test wait | Test speed is not a goal, and the code grows. The shorter wait makes TestReload flaky under `-race` on a loaded runner. B1 already shrinks the sweeps. |
| docs-15 | Describe the Claude /usage probe once and shorten README's paragraph | The cut would drop v0.2.3's rules on API keys, symlinks and refusals, two sentences about attribution, and the freshness limits in ai-report.md. |

### Parts of kept findings that are not taken

| Finding | Part not taken | Why |
|---|---|---|
| dup-08 | `truncate` with an ellipsis | A14 keeps stored error text byte-identical (Owner decision 7). |
| dup-09 | `absorb` and `logFile` | C1's mergeByID and C2's walkLogs cover the same code with one helper each. |
| dup-12 | `ProjectTotals.add` | C26's embedded `usage` serves accounts and projects alike. |
| dup-14 | `printJSON` | C10 also builds the report once and drops a parameter. |
| dup-17 | Theme colors for status and the guide | Owner decision 6. |
| dup-03, cmd-10 | `collect.Tilde` | C19 puts Tilde in fsutil beside RealPath. |
| platform-05 | `internal/atomicfile` | C4's fsutil also holds C19's helpers (Owner decision 4). |
| platform-11 | Exported test packages | F8's in-package fake (Owner decision 23). |
| platform-12 | dirNames, windowsRelease, winUpdater | F20 takes only onlyFiles; the rest saves little. |
| platform-13 | A separate staticcheck job | I1 adds one step to the existing Linux test job. |
| tui-01 | Delete TestDeviceViewsDrawn | B1 rewrites it on the existing fixture; it is the only draw test of the status view. |
| tui-03 | `collectOptions` | C11's collection runner also folds the recover and deletes collectNow. |
| tui-12 | Split tui.go | Owner decision 22. |
| collect-04 | `Env.ClaudeConfigDirs` | C7's explicit parameters beat a hidden field. |
| collect-09 | withProfiles, candidates, hermesUses | D12's four helpers bring the long functions under the limit; these only move code. |
| collect-14 | Unexport LoadKey | Taste only; A10 takes the bool half. |
| probe-06 | The window cap change and its test deletions | C5 takes only the shared line scanner. |
| probe-13 | One table for the three find tests | They differ in Env and OS filters; F4 leaves them. |
| relay-02 | Change Record.Since's tag | Zero-since records may still be in KV; B10 changes only the comments. |
| relay-07 | text.go | alias.go stays; E9 moves validation only. |
| relay-08 | CheckAlias in the view | Owner decision 8. |
| relay-19 | Delete TestSealedFitsSnapshot with SealedLen | A8 keeps the test; it guards the sealed-label limit. |
| tests-05, tests-13, tests-14, tests-15, tests-16 | Their file splits | E2, F21, F22, F23 and F24 split along the new source files. |
| tests-12 | readHome | A4 and A8 take the parts that remove dead code. |
| view-team-07, view-team-13 | matrixRow, setShares, gridCells | Named helpers with one caller; D6 and D8 split only at real seams. |
| cmd-06, cmd-22 | Flag parsing and `-h` changes | Owner decisions 9 and 10. |
| cmd-19 | memRelay, lockFree, usage | F16 takes the hermetic fix and `env()`; the rest saves little. |
| compat-08 | Rewrite of relay.md:198 | B10 points to relay-protocol.md §11, which states the rule. |
| compat-10 | Rewrite of ai-report.md:105 | H4 deletes the line; the sketch is built. |
| docs-13 | `report --from` in README | Owner decision 15. |
| docs-14 | The Implementation status cut | Owner decision 16; H10 is ready if the owner agrees. |

### Kept as is

The audit also checked these and left them, because they serve current behavior:

- **Harness formats.** Claude Code's legacy `.config.json`, top-level sidechain files, and the cache's fallback keys; Codex token counts without `last_token_usage`, rate limits without `limit_id`, `resets_in_seconds`, and session_meta fallbacks; Hermes' optional columns, ISO-text times and missing tables; Grok's billing-period fallback and its millisecond and RFC 3339 times. The fleet update does not control the harnesses.
- **Hours fallbacks.** labelHours' untimed branch, placeHours' first branch, and lastActive's fallback to Seen. v0.1.x sessions stay in live ledgers until about 2026-12-22.
- **Platform code.** Windows build tags, the console VT and typeahead helpers, the `.old` swap, Task Scheduler code pages and job token, launchd's `disabled()` reading both spellings, BusyBox crontab, the freebsd and openbsd stubs, `sqliteURI`'s leading slash, and `team.blank` trimming a byte-order mark.
- **Entry points and seams.** api/relay.go (the Vercel function, one file by necessity), `relay serve` and the Memory store, the Updater's and Env's zero-value defaults, the tui test seams, the Hermes read hooks, and `claudeOwned`.
- **Current features.** The Old flag and its `↓` mark, the team cache check against another team's key, `team.key.previous`, rescue and housekeeping, the waited-run reuse (`LastRunInputs`), collectSource's recover, the clock guards in setQuota and clampWindows, the damaged state kept as `state.json.bad`, `AI_USAGE_NO_SCHEDULE`, and `InAgent`.
- **JSON contract.** report.go as one file of types, Column.Percent and WindowTokens (json-schema.md tells readers to derive the old share from them), and the optional snapshot members that v0.2.3 sends.
- **Look-alikes with different rules.** probe's and collect's truncate and shortErr, cmd's samePath (it resolves links) and collect's, validName and CheckAlias, the matrix row sort and the grid's sortRows, the key bar's pill and the grid's, the per-provider switches, the process runners and HTTP clients, and the three TestHelperProcess copies.
- **Small helpers in separate packages.** clamp, firstLine, mustKey, newKey and the file writers of each test package. A shared test-utility package would cost more than it saves.
- **Tests with their own contract.** TestWidths and TestPageFits, the mono and color goldens, TestStatusColumns, TestFullTeamReadFitsVercel and TestKVListFullTeam, TestRelayOverKV, TestLockEndsWithItsProcess, TestExecRunner, and the only tests of the pty background query, relay shutdown, scheduled collection and stopped housekeeping.
- **Long functions that read best whole.** tui's Model.key and Update (flat switches), truncPath (a table-tested algorithm), rollup with its cycle guard, and draw()'s render-to-fit loop.

Some steps override an audit keep item, each for a reason the audit did not have:

- A13 deletes the cron test case for an entry without a state folder, because A13 removes the empty-home branch that case exercised.
- A1 replaces TestQuotaFromIsOptional with the wire golden, which pins the same bytes and every other member.
- A7 adds a `lint:ignore` for the pty test's deprecated call, which the audit left because staticcheck was not in CI; I1 puts it there.
- F6 folds TestCannotWriteAnotherTeam into TestDelete and TestSignedRead. Every case it checked stays (Owner decision 21).
- F19 keeps the per-OS pty files but gives them one openPTY; only `ptsName` differs by OS.
- H2 replaces design.md's page example, which the compatibility audit kept as example text, with a pointer to README's copy, which is the golden file.
- H9 rewrites one limit in ai-report.md's "Deferred or not done" and deletes another, because they say pace reads samples, and nothing does.

## 9. Expected outcome

| Phase | Steps | Net lines |
|---|---|---|
| A. Safety nets, dead code and lint | 14 | −449 |
| B. Remove compatibility with releases before v0.2.0 | 10 | −1,303 |
| C. One copy of each rule, and fewer layers | 27 | −1,074 |
| D. Shorter functions | 24 | −112 |
| E. Split the large files | 13 | +220 |
| F. Tests | 24 | −672 |
| G. Comments | 3 | −12 |
| H. Documents | 10 | −140 (−127 without H10) |
| I. CI and lint gates | 2 | +6 |
| **Total** | **127** | **−3,536 (−3,523 without H10), about −3,500** |

The tree goes from about 45,100 lines of Go and Markdown docs to about 41,500. Phase E adds lines on purpose: each new file has its own package clause and imports. The estimates come from the findings and from trial edits on a scratch copy; each step's check, not the estimate, decides.

After the plan:

- staticcheck and `deadcode -test` report nothing on linux, darwin and windows, and CI keeps it so.
- Each function that C16, C17 or a phase D step splits drops out of gocyclo over 15 and gocognit over 20, as that step's check says. The long functions kept on purpose stay (section 8).
- `dupl -t 60` reports nothing.
- No Go file is over about 600 lines. cmd/ai-usage/main.go is under 200 lines, and no other non-test file in cmd/ai-usage is over 250.

Files split, and their new names:

| Old file | New files | Step |
|---|---|---|
| cmd/ai-usage/main.go | main.go, name.go, collect.go, update.go, schedule.go, report.go, team.go, relay.go | E1 |
| internal/collect/collect.go, doc.go | collect.go, ask.go, quota.go, ledger.go, hours.go, hermes.go, team.go (gains), totals.go, doc.go | D3, E2 |
| internal/view/team.go | team.go, names.go, matrix.go, hermes.go | E3 |
| internal/view/header.go | header.go, attentionlines.go | E4 |
| internal/view/render.go, subscriptions.go | render.go, period.go, subscriptions.go, window.go | E5 |
| internal/logs/codex.go | codex.go, codexfile.go, codexlimits.go | E6 |
| internal/logs/hermes.go | hermes.go, sqlite.go | E7 |
| internal/probe/probe.go, codex.go | probe.go, find.go, proc.go, codex.go, codexrpc.go | E8 |
| internal/snapshot/snapshot.go | snapshot.go, validate.go | E9 |
| relay/server.go | server.go, limits.go | E10 |
| relay/store.go | store.go, kv.go | E11 |
| internal/schedule/schedule.go | schedule.go, cron.go, task.go | E12 |
| internal/state/state.go | state.go, config.go, lock.go | E13 |
| (new) | internal/fsutil/fsutil.go | C4 |

Test files split, and their new names:

| Old file | New files | Step |
|---|---|---|
| cmd/ai-usage/main_test.go | cli_test.go, help_test.go (gains), report_test.go, collect_test.go, team_test.go, name_test.go, relay_test.go, home_test.go, schedule_test.go, update_test.go | F22 |
| cmd/ai-usage/pty_darwin_test.go, pty_linux_test.go, background_unix_test.go | pty_unix_test.go (shared) | F19 |
| internal/collect/collect_test.go, doc_test.go, claudeenv_test.go | collect_test.go, world_test.go, claim_test.go, quota_test.go, ledger_test.go, hours_test.go, totals_test.go, discover_test.go, doc_test.go | E2 |
| internal/view/view_test.go, older_test.go | build_test.go, team_test.go, names_test.go, matrix_test.go, hermes_test.go, attention_test.go, status_test.go, forecast_test.go (gains) | B1, F21 |
| internal/tui/tui_test.go | tui_test.go, keybar_test.go, scroll_test.go, help_test.go, run_test.go | F24 |
| relay/server_test.go, store_test.go | server_test.go, helpers_test.go, client_test.go, cap_test.go, limits_test.go, store_test.go, kv_test.go | E11, F23 |
| internal/probe tests | fake_test.go, find_test.go, proc_test.go, codexrpc_test.go | E8 |
| internal/logs tests | codexlimits_test.go, sqlite_test.go | E6, E7 |
| internal/snapshot/snapshot_test.go | snapshot_test.go, validate_test.go | E9 |
| internal/schedule/schedule_test.go | schedule_test.go, cron_test.go, task_test.go | E12 |
| internal/state/state_test.go | state_test.go, config_test.go, lock_test.go, samples_test.go | E13 |
| (new) | internal/fsutil/fsutil_test.go, internal/snapshot/testdata/v1.json | C4, A1 |
