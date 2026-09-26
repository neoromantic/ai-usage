// The checks of AIUsageKit: xcrun swift run AIUsageChecks [DEMO_DIR]
// DEMO_DIR holds team.json and solo.json; it defaults to the repository's
// docs/demo. Every check prints a line; any failure exits 1.
import AIUsageKit
import Foundation

var failures = 0

func check(_ name: String, _ ok: @autoclosure () throws -> Bool) {
    let passed = (try? ok()) ?? false
    print(passed ? "ok    " : "FAIL  ", name)
    if !passed { failures += 1 }
}

func equal<T: Equatable>(_ name: String, _ got: T, _ want: T) {
    check("\(name) = \(want)" + (got == want ? "" : " (got \(got))"), got == want)
}

func time(_ s: String) -> Date { parseTime(s)! }

let demo = CommandLine.arguments.count > 1
    ? URL(fileURLWithPath: CommandLine.arguments[1])
    : URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("../../../docs/demo").standardized

// Decoding.
let team = try Report.decode(Data(contentsOf: demo.appendingPathComponent("team.json")))
let solo = try Report.decode(Data(contentsOf: demo.appendingPathComponent("solo.json")))
equal("team schema", team.schemaVersion, 4)
equal("team generated_at", team.generatedAt, time("2026-09-24T13:40:00Z"))
equal("team attention", team.attention.map(\.kind.rawValue), ["out", "out", "over", "error", "silent", "old"])
equal("team devices", team.team.devices.count, 12)
equal("team providers", team.team.providers.map(\.provider), ["claude", "codex", "grok", "hermes"])
equal("team matrix", [team.team.matrix.columns.count, team.team.matrix.rows.count], [6, 12])
equal("team projects", team.projects.count, 6)
check("team is not solo", !team.solo)
check("solo is solo", solo.solo && solo.team.pulledAt == nil)
equal("solo this device", solo.thisDevice?.label, "mira-mbp")
let over = team.attention[2]
check("nanoseconds kept", abs(over.at!.timeIntervalSince(time("2026-09-25T06:59:45Z")) - 0.542168674) < 1e-6)
equal("forecast runs out", team.teamAccount(provider: "codex", label: "bots@studio.dev")?.quota?.main?.forecast?.runsOutAt, over.at)
check("reset window unknown", team.teamAccount(provider: "codex", label: "leo@studio.dev")?.quota?.windows.first?.known == false)
equal("nullable share", team.team.matrix.rows[0].cells[0].share.week != nil, true)
equal("short window name", solo.teamAccount(provider: "claude", label: "mira@studio.dev").map { q in q.quota!.windows.map { q.quota!.shortName($0.name) } }, ["5h", "7d", "Fable"])
equal("health", team.collector.health.map { "\($0.item) \($0.status) \($0.state)" }, ["collection ok ok", "relay ok ok", "update ok ok"])
equal("health at", team.collector.health.map(\.at), [team.collector.lastSuccessAt, team.collector.relay.lastPushAt, team.collector.update.checkedAt])
check("health release", team.collector.health.allSatisfy { $0.release == nil })
equal("limits", team.teamAccount(provider: "claude", label: "leo@studio.dev")?.quota?.windows.map(\.limits), [false, true])
equal("limits beside the main window", solo.teamAccount(provider: "claude", label: "mira@studio.dev")?.quota?.windows.map(\.limits), [false, true, true])

/// The demo report as JSON, changed by edit, decoded.
func edited(_ name: String, _ edit: (Any) -> Any) throws -> Report {
    let json = try JSONSerialization.jsonObject(with: Data(contentsOf: demo.appendingPathComponent(name)))
    return try Report.decode(JSONSerialization.data(withJSONObject: edit(json)))
}

/// Every object in v, changed by edit.
func objects(_ v: Any, _ edit: ([String: Any]) -> [String: Any]) -> Any {
    if let o = v as? [String: Any] { return edit(o).mapValues { objects($0, edit) } }
    if let a = v as? [Any] { return a.map { objects($0, edit) } }
    return v
}

// A report from an ai-usage before health and limits.
let older = try edited("team.json") { objects($0) { $0.filter { $0.key != "health" && $0.key != "limits" } } }
check("older report has no health", older.collector.health.isEmpty)
check("older report limits nothing", older.team.providers.allSatisfy { $0.accounts.allSatisfy { $0.quota?.windows.allSatisfy { !$0.limits } ?? true } })

// Times.
equal("time with offset", parseTime("2026-09-24T15:40:00+02:00"), time("2026-09-24T13:40:00Z"))
equal("time with millis", parseTime("2026-09-24T13:40:00.250Z"), time("2026-09-24T13:40:00Z").addingTimeInterval(0.25))
check("not a time", parseTime("yesterday") == nil && parseTime("2026-09-24T13:40:00.Z") == nil)
let v3 = #"{"schema_version": 3, "generated_at": "2026-09-24T13:40:00Z"}"#
check("schema 3 refused", {
    do { _ = try Report.decode(Data(v3.utf8)); return false } catch { return error as? ReportError == .schema(3) }
}())

// Formatters.
for (n, want) in [(0, "0"), (999, "999"), (9_540, "9.5K"), (480_000, "480K"), (999_960, "1M"), (12_300_000, "12.3M"),
                  (99_960_000, "100M"), (130_000_000, "130M"), (1_658_119_321, "1.7B")] {
    equal("tokens \(n)", Format.tokens(n), want)
}
let durations: [(TimeInterval, String)] = [(59, "<1m"), (34 * 60, "34m"), (7 * 3600 + 5 * 60, "7h 5m"), (3600, "1h"), (47 * 3600 + 59, "1d 23h"), (12 * 86400, "12d"), (12 * 86400 + 7200, "12d 2h")]
for (s, want) in durations {
    equal("duration \(Int(s))s", Format.duration(s), want)
}
let berlin = TimeZone(identifier: "Europe/Berlin")!
equal("clock this week", Format.clock(time("2026-09-26T23:00:00Z"), now: team.generatedAt, timeZone: berlin), "Sun 01:00")
equal("clock later", Format.clock(time("2026-10-02T13:04:00Z"), now: team.generatedAt, timeZone: berlin), "2 Oct 15:04")
equal("ago", Format.ago(time("2026-09-24T13:37:00Z"), now: team.generatedAt), "3m ago")
equal("ago now", Format.ago(team.generatedAt, now: team.generatedAt), "just now")
let ages: [(TimeInterval, String)] = [(0, "now"), (59, "now"), (7 * 60, "7m"), (3 * 3600 + 1200, "3h"), (2 * 86400 + 23 * 3600, "2d")]
for (s, want) in ages {
    equal("age \(Int(s))s", Format.age(team.generatedAt.addingTimeInterval(-s), now: team.generatedAt), want)
}
let mira = team.teamAccount(provider: "claude", label: "mira@studio.dev")!
equal("left", Format.left(mira.quota?.main), "42%")
equal("left unknown", Format.left(team.teamAccount(provider: "codex", label: "leo@studio.dev")?.quota?.windows.first), "?")
equal("share", [0.4, 26.6, 99.5, 100].map(Format.share), ["<1%", "27%", ">99%", "100%"])
equal("tilde", Format.tilde("/Users/mira/src/web", home: "/Users/mira"), "~/src/web")
equal("tilde elsewhere", Format.tilde("/Users/miranda/x", home: "/Users/mira"), "/Users/miranda/x")

// The menu bar summary: the fullest window of this device's subscriptions
// among those that limit them, not only a main one.
let summary = MenuSummary.pick(team)
equal("team summary", [summary?.provider, summary?.label, summary?.window, summary?.text], ["claude", "mira@studio.dev", "5h", "0%"])
equal("team summary spoken", summary?.spoken, "Claude mira, 5h window: 0% left")
let soloSummary = MenuSummary.pick(solo)
equal("solo summary", [soloSummary?.window, soloSummary?.text], ["7d Fable", "7%"])
// A window the report does not mark as limiting does not count, however
// full; without the marks, the main windows alone count.
let unmarked = try edited("team.json") { objects($0) { o in o["main"] as? Bool == false ? o.merging(["limits": false]) { $1 } : o } }
let unmarkedSummary = MenuSummary.pick(unmarked)
equal("summary of limiting windows", [unmarkedSummary?.label, unmarkedSummary?.window, unmarkedSummary?.text], ["mira@studio.dev", "7d", "42%"])
let olderSummary = MenuSummary.pick(older)
equal("summary of an older report", [olderSummary?.label, olderSummary?.window, olderSummary?.text], ["mira@studio.dev", "7d", "42%"])
let stale = MenuSummary(provider: "codex", label: "a", name: "a", window: "7d", percentLeft: 42, used: 58, stale: true)
equal("stale summary", stale.text, "~42%")
equal("symbols", [nil, 10, 58, 71, 100].map(MenuSummary.symbol).map { $0.replacingOccurrences(of: "gauge.with.dots.needle.", with: "") },
      ["0percent", "0percent", "50percent", "67percent", "100percent"])

// The locator, on a made-up home.
let fm = FileManager.default
let tmp = fm.temporaryDirectory.appendingPathComponent("ai-usage-checks-\(ProcessInfo.processInfo.processIdentifier)")
defer { try? fm.removeItem(at: tmp) }
let bin = tmp.appendingPathComponent("opt/ai-usage")
let local = tmp.appendingPathComponent(".local/bin/ai-usage")
for dir in [bin.deletingLastPathComponent(), local.deletingLastPathComponent(), tmp.appendingPathComponent("Library/LaunchAgents")] {
    try fm.createDirectory(at: dir, withIntermediateDirectories: true)
}
let script = """
#!/bin/sh
case "$1" in
big) /usr/bin/head -c 200000 /dev/zero | /usr/bin/tr '\\0' a; /usr/bin/head -c 100000 /dev/zero | /usr/bin/tr '\\0' b >&2 ;;
fail) echo "ai-usage: boom" >&2; exit 3 ;;
usage) printf 'a name cannot contain spaces\n\nai-usage collects AI harness usage\n\nUSAGE\n  ai-usage\n' >&2; exit 2 ;;
wait) printf 'ai-usage: waiting for another run, such as the scheduled one, to finish\nai-usage: no account goes by "x"; the accounts:\n  claude  mira\n' >&2; exit 2 ;;
echo) /bin/cat ;;
env) printf '%s|%s' "$AI_USAGE_HOME" "$PATH" ;;
sleep) exec /bin/sleep 10 ;;
esac
"""
for exe in [bin, local] {
    try Data(script.utf8).write(to: exe)
    try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: exe.path)
}
// Only folders in the made-up home, so an ai-usage installed on this Mac,
// as in /opt/homebrew/bin, does not count.
let dirs = [local.deletingLastPathComponent().path, tmp.appendingPathComponent("bin").path]
equal("standard folders", CLILocation.standardDirs(home: tmp).prefix(2).map { $0 }, dirs)
equal("locate standard folder", CLILocation.locate(environment: [:], home: tmp, dirs: dirs), CLILocation(executable: local))
let plist: [String: Any] = [
    "Label": CLILocation.agentLabel,
    "ProgramArguments": [bin.path, "collect", "--quiet", "--home", "/state/ai-usage"],
    "EnvironmentVariables": ["PATH": "/usr/bin:/bin"],
]
try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
    .write(to: tmp.appendingPathComponent("Library/LaunchAgents/\(CLILocation.agentLabel).plist"))
let agent = CLILocation(executable: bin, stateHome: "/state/ai-usage", path: "/usr/bin:/bin")
equal("locate from LaunchAgent", CLILocation.locate(environment: [:], home: tmp, dirs: dirs), agent)
equal("locate AI_USAGE_BIN", CLILocation.locate(environment: ["AI_USAGE_BIN": local.path], home: tmp, dirs: dirs)?.executable, local)
try fm.removeItem(at: bin)
equal("locate past a missing program", CLILocation.locate(environment: [:], home: tmp, dirs: dirs)?.executable, local)
try fm.removeItem(at: local)
check("locate nothing", CLILocation.locate(environment: [:], home: tmp, dirs: dirs) == nil)

// The runner.
try Data(script.utf8).write(to: local)
try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: local.path)
let cli = CLI(location: CLILocation(executable: local, stateHome: "/state/ai-usage", path: "/usr/bin:/bin"))
equal("large output", try cli.runSync(["big"]).count, 200_000)
check("error carries stderr", {
    do { _ = try cli.runSync(["fail"]); return false } catch { return error as? CLIError == .failed(status: 3, message: "boom") }
}())
// A usage error is followed by the whole help; a run that waited for the
// lock says so first.
check("usage error without the help", {
    do { _ = try cli.runSync(["usage"]); return false } catch { return error as? CLIError == .failed(status: 2, message: "a name cannot contain spaces") }
}())
check("error without the wait", {
    do { _ = try cli.runSync(["wait"]); return false } catch { return error as? CLIError == .failed(status: 2, message: "no account goes by \"x\"; the accounts:\n  claude  mira") }
}())
equal("message of nothing", CLI.message(stderr: "\n"), "")
equal("stdin", String(decoding: try cli.runSync(["echo"], input: Data("key\n".utf8)), as: UTF8.self), "key\n")
equal("environment", String(decoding: try cli.runSync(["env"]), as: UTF8.self), "/state/ai-usage|/usr/bin:/bin")
let start = Date()
check("timeout", {
    do { _ = try cli.runSync(["sleep"], timeout: 0.5); return false } catch { return error as? CLIError == .timedOut(seconds: 0.5) }
}() && Date().timeIntervalSince(start) < 5)
let asyncResult = await Task { try await cli.run(["echo"], input: Data("async".utf8)) }.result
equal("async run", try String(decoding: asyncResult.get(), as: UTF8.self), "async")

print(failures == 0 ? "all checks passed" : "\(failures) checks failed")
exit(failures == 0 ? 0 : 1)
