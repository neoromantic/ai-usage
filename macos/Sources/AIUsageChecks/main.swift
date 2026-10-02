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
// Projects next to each other in one folder show as a group under its name;
// the ones on their own follow as "Other", in the report's order.
let sections = ProjectSection.sections(team.projects, home: "/Users/mira")
equal("project sections", sections.map { "\($0.title ?? "-") \($0.grouped) \($0.projects.count)" }, ["orbit true 3", "Other false 3"])
equal("project group folder", sections.first?.folder, "~/src/orbit")
let homeRun = team.projects.filter { !$0.path.contains("/src/") }
equal("home group", ProjectSection.sections(homeRun, home: "/Users/mira").map { $0.title ?? "-" }, ["Home"])
equal("no group", ProjectSection.sections(Array(team.projects.suffix(3)), home: "/Users/mira").map { $0.title ?? "-" }, ["-"])
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

// A report from an ai-usage before health, limits, folders, and not_updating.
let older = try edited("team.json") { objects($0) { $0.filter { !["health", "limits", "folders", "not_updating", "days"].contains($0.key) } } }
check("older report has no days", older.providers.allSatisfy { $0.accounts.allSatisfy(\.days.isEmpty) })
check("older report has no health", older.collector.health.isEmpty)
check("older report limits nothing", older.team.providers.allSatisfy { $0.accounts.allSatisfy { $0.quota?.windows.allSatisfy { !$0.limits } ?? true } })
equal("folders", team.projects.map(\.folders), [5, 1, 1, 1, 1, 1])
equal("older report's folders", older.projects.map(\.folders), [0, 0, 0, 0, 0, 0])
let stuck = try edited("team.json") { objects($0) { $0["device"] as? String == team.team.devices[1].device ? $0.merging(["not_updating": true]) { $1 } : $0 } }
equal("not updating", stuck.team.devices.map(\.notUpdating), team.team.devices.indices.map { $0 == 1 })
check("older report updates", older.team.devices.allSatisfy { !$0.notUpdating })

// The subscriptions with no window known now: never read, or each window
// that limits it has reset since its reading or is not in it. Another
// window's reset alone leaves the account known.
check("demo quotas known", team.team.providers.allSatisfy { $0.accounts.allSatisfy { $0.tightest != nil } })
func withQuota(_ label: String, _ edit: @escaping ([String: Any]) -> Any) throws -> TeamAccount? {
    try edited("team.json") { objects($0) { o in
        guard o["label"] as? String == label, o["busiest"] != nil, let q = o["quota"] as? [String: Any] else { return o }
        return o.merging(["quota": edit(q)]) { $1 }
    } }.teamAccount(provider: "claude", label: label)
}
let windows = { (q: [String: Any], edit: ([String: Any]) -> [String: Any]) in q.merging(["windows": (q["windows"] as! [[String: Any]]).map(edit)]) { $1 } }
equal("never read", try withQuota("leo@studio.dev") { _ in NSNull() }.map { $0.tightest != nil }, false)
equal("main window reset", try withQuota("leo@studio.dev") { q in windows(q) { $0.merging(["reset": true]) { $1 } } }.map { $0.tightest != nil }, false)
equal("main window not read", try withQuota("leo@studio.dev") { q in windows(q) { $0.merging(["unread": true]) { $1 } } }.map { $0.tightest != nil }, false)
equal("another window known", try withQuota("mira@studio.dev") { q in windows(q) { $0["main"] as? Bool == true ? $0.merging(["reset": true]) { $1 } : $0 } }.map { $0.tightest != nil }, true)
equal("another window not limiting", try withQuota("leo@studio.dev") { q in windows(q) { $0["main"] as? Bool == true ? $0.merging(["reset": true]) { $1 } : $0 } }.map { $0.tightest != nil }, false)

// Times.
equal("time with offset", parseTime("2026-09-24T15:40:00+02:00"), time("2026-09-24T13:40:00Z"))
equal("time with millis", parseTime("2026-09-24T13:40:00.250Z"), time("2026-09-24T13:40:00Z").addingTimeInterval(0.25))
check("not a time", parseTime("yesterday") == nil && parseTime("2026-09-24T13:40:00.Z") == nil)
let v3 = #"{"schema_version": 3, "generated_at": "2026-09-24T13:40:00Z"}"#
check("schema 3 refused", {
    do { _ = try Report.decode(Data(v3.utf8)); return false } catch { return error as? ReportError == .schema(3) }
}())

// Formatters.
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
for (n, want) in [(0, "–"), (999_999, "<1M"), (1_000_000, "1M"), (167_400_000, "167M"), (1_975_000_000, "1,975M")] {
    equal("tokensM \(n)", Format.tokensM(n), want)
}
let spokenSeconds: [TimeInterval] = [30, 5100, 3600, 205_200, 86400]
equal("spoken durations", spokenSeconds.map { Format.spokenDuration($0) },
      ["under a minute", "1 hour 25 minutes", "1 hour", "2 days 9 hours", "1 day"])
// The sparkline is the last 14 days, oldest first, zeros where the list
// does not reach.
equal("sparkline", Format.sparkline([5, 4, 3]), Array(repeating: 0, count: 11) + [3, 4, 5])
let days = solo.providers[0].accounts[0].days
equal("sparkline of a report", Format.sparkline(days), Array(days.prefix(14).reversed()))
equal("days", [days.count, days.first ?? 0], [90, 17_750_013])
// Whole millions and whole percents, as the console prints them.
for (n, want) in [(0, "–"), (-5, "–"), (1, "<1"), (999_999, "<1"), (1_000_000, "1"), (1_499_999, "1"), (1_500_000, "2"),
                  (603_000_000, "603"), (1_658_119_321, "1658")] {
    equal("millions \(n)", Format.millions(n), want)
}
equal("spoken millions", [0, 480_000, 104_400_000].map(Format.spokenMillions), ["none", "under a million", "104 million"])
equal("share", [nil, 0, 0.4, 26.6, 99, 99.5, 100].map(Format.share), ["–", "–", "<1", "27", "99", ">99", "100"])
equal("tilde", Format.tilde("/Users/mira/src/web", home: "/Users/mira"), "~/src/web")
equal("tilde elsewhere", Format.tilde("/Users/miranda/x", home: "/Users/mira"), "/Users/miranda/x")
let paths = ["/Users/mira/src/orbit/web", "/Users/mira", "/Users/mira/notes/", "/private/tmp/x", "/x", "/", "unknown", "/Users/mira/.codex/worktrees/*/app"]
equal("path parts", paths.map { Format.pathParts($0, home: "/Users/mira") }.map { [$0.name, $0.folder] },
      [["web", "~/src/orbit"], ["~", ""], ["notes", "~"], ["x", "/private/tmp"], ["x", "/"], ["/", ""], ["unknown", ""], ["app", "~/.codex/worktrees/*"]])

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
// The window the menu bar picks carries its state and reset: an out one
// shows when it is back, in either mode that shows words.
equal("team summary state", [summary?.state, summary?.resetsAt.map { "\($0)" }], ["out", "\(time("2026-09-24T15:05:00Z"))"])
equal("out menu text", [MenuBarShows.percent, .time, .icon].map { summary?.text($0, now: team.generatedAt) }, ["1h 25m", "1h 25m", nil])
let resets = MenuSummary(provider: "codex", label: "a", name: "a", window: "7d", percentLeft: 42, used: 58, stale: false,
                         state: "ok", resetsAt: team.generatedAt.addingTimeInterval(2 * 86400 + 9 * 3600))
equal("menu texts", [MenuBarShows.percent, .time, .icon].map { resets.text($0, now: team.generatedAt) }, ["42%", "2d 9h", nil])
equal("menu text without a reset", stale.text(.time, now: team.generatedAt), "–")
equal("menu spoken", [summary?.spoken(now: team.generatedAt), resets.spoken(now: team.generatedAt)],
      ["Claude mira, 5h window: 0% left, back in 1 hour 25 minutes", "Codex a, 7d window: 42% left, resets in 2 days 9 hours"])
// The tightest window of each account is the one the summary picks from,
// the first of equals.
equal("tightest", team.team.providers.flatMap { p in p.accounts.map { "\(p.provider) \($0.name) \($0.tightest?.name ?? "-")" } },
      ["claude mira 5h", "claude leo 7d", "codex leo 7d", "codex bots 7d", "codex mira 7d", "grok 7f3b9c21 7d", "hermes openai-codex 7d"])
equal("tightest of an older report", older.teamAccount(provider: "claude", label: "mira@studio.dev")?.tightest?.name, "7d")
equal("solo tightest", solo.teamAccount(provider: "claude", label: "mira@studio.dev")?.tightest?.name, "7d Fable")
// Settings from before the menu bar had modes: the percent switched off
// becomes the ring alone, once.
equal("migration", [MenuBarShows.migrated(showPercent: false, shows: nil), MenuBarShows.migrated(showPercent: true, shows: nil),
                    MenuBarShows.migrated(showPercent: false, shows: "time"), MenuBarShows.migrated(showPercent: nil, shows: nil)],
      [.icon, nil, nil, nil])

// The verdict: the first out entry, else the first over one, with how
// many more there are; else the subscription with the most room.
let teamVerdict = Verdict(team, now: team.generatedAt, timeZone: berlin)
equal("team verdict", [teamVerdict.title, teamVerdict.subline], ["Claude mira · 5h is out", "Back in 1h 25m · Thu 17:05 · +2 more"])
check("team verdict opens its account", teamVerdict.kind == .out && teamVerdict.more == 2 && teamVerdict.provider == "claude" && teamVerdict.label == "mira@studio.dev")
let soloVerdict = Verdict(solo, now: solo.generatedAt, timeZone: berlin)
equal("solo verdict", [soloVerdict.title, soloVerdict.subline],
      ["Claude mira · Fable runs out ~Thu 23:55", "2d 1h before its reset · +1 more"])
equal("solo verdict compact", [soloVerdict.compactTitle, soloVerdict.compactSubline],
      ["Claude mira · Fable runs out", "~Thu 23:55 · 2d 1h before its reset · +1 more"])
check("solo verdict is over", soloVerdict.kind == .over)
let calm = try edited("team.json") { json in
    var o = json as! [String: Any]
    o["attention"] = (o["attention"] as! [[String: Any]]).filter { !["out", "over"].contains($0["kind"] as? String) }
    return o
}
let calmVerdict = Verdict(calm, now: calm.generatedAt, timeZone: berlin)
equal("calm verdict", [calmVerdict.title, calmVerdict.subline, calmVerdict.label], ["All on track", "Most room: Grok 7f3b9c21 · 94%", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64"])
let unread = try edited("team.json") { json in
    var o = objects(json) { o in o["quota"] != nil ? o.merging(["quota": NSNull()]) { $1 } : o } as! [String: Any]
    o["attention"] = (o["attention"] as! [[String: Any]]).filter { !["out", "over", "under"].contains($0["kind"] as? String) }
    return o
}
let noneVerdict = Verdict(unread, now: unread.generatedAt, timeZone: berlin)
check("verdict without windows", noneVerdict.kind == .none && noneVerdict.title == "No limits read yet" && noneVerdict.provider == nil)

// Chips: what asks for something besides the subscriptions. A device on
// an older release that updates itself gets none.
let chips = Chip.chips(team)
equal("team chips", chips.map { "\($0.text) \($0.symbol) \($0.state) \($0.devices)" },
      ["1 error xmark.circle.fill out [\"leo-air\"]", "1 not reporting antenna.radiowaves.left.and.right.slash tight [\"courier\"]"])
check("team chip kinds", chips.map(\.kind) == [.errors, .silent] && chips.map(\.short) == ["1", "1"])
equal("refresh chip first", Chip.chips(team, refreshFailed: true).first?.text, "Can't read report")
equal("solo chips", Chip.chips(solo).count, 0)
let leoAir = team.teamDevice(named: "leo-air")!.device
let notUpdating = try edited("team.json") { objects($0) { $0["device"] as? String == leoAir && $0["os_user"] != nil ? $0.merging(["not_updating": true]) { $1 } : $0 } }
equal("not updating chip", Chip.chips(notUpdating).last.map { "\($0.text) \($0.state) \($0.devices) \($0.updating)" }, "1 not updating tight [\"leo-air\"] 0")
let broken = try edited("team.json") { json in
    var o = objects(json) { o in
        guard o["item"] as? String == "relay" else { return o }
        return o.merging(["status": "failing", "state": "error"]) { $1 }
    } as! [String: Any]
    let this = (o["collector"] as! [String: Any])["device_label"] as! String
    o["attention"] = (o["attention"] as! [[String: Any]]) + [["kind": "error", "devices": [this], "message": "relay: relay returned 503"]]
    return o
}
equal("health chip", Chip.chips(broken).first.map { "\($0.text) \($0.state)" }, "Relay failing out")
// This Mac's failing relay is the health chip's alone, not an error too.
equal("failing relay said once", Chip.chips(broken).map(\.text), ["Relay failing", "1 error", "1 not reporting"])
let toolFails = try edited("solo.json") { json in
    var o = json as! [String: Any]
    let this = (o["collector"] as! [String: Any])["device_label"] as! String
    o["attention"] = (o["attention"] as! [[String: Any]]) + [["kind": "error", "devices": [this], "message": "codex: app-server exited without answering"]]
    return o
}
equal("solo chip names the tool", Chip.chips(toolFails).map(\.text), ["Codex failing"])

// Limits rows: the account's state is the report's, which an out window
// other than the main one sets; the bar and percent stay on the main one.
let miraLine = LimitLine(mira)
equal("out row", [miraLine?.state, miraLine?.window.name, miraLine?.blocking?.name], ["out", "7d", "5h"])
let codexLeo = LimitLine(team.teamAccount(provider: "codex", label: "leo@studio.dev")!)
equal("main out row", [codexLeo?.state, codexLeo?.window.name, codexLeo?.blocking?.name], ["out", "7d", nil])
equal("solo over row", LimitLine(solo.teamAccount(provider: "claude", label: "mira@studio.dev")!).map { [$0.state, $0.window.name, $0.others.first?.name] },
      ["over", "7d", "7d Fable"])
// A rejection reading: the main window is not in it and the 5h one is out.
// The row shows the 5h window, not the line of accounts not read.
let rejected = try withQuota("leo@studio.dev") { q in windows(q) { w in
    w["main"] as? Bool == true ? w.merging(["unread": true, "state": "unknown"]) { $1 }
        : w.merging(["percent": 100, "state": "out", "limits": true]) { $1 }
} }
let rejectedLine = rejected.flatMap(LimitLine.init)
equal("rejection row", [rejectedLine?.window.name, rejectedLine?.unreadMain?.name, rejectedLine?.blocking?.name], ["5h", "7d", nil])
check("never read has no row", try withQuota("leo@studio.dev") { _ in NSNull() }.flatMap(LimitLine.init) == nil)

// Two reports of the same team: one as v0.3.4 printed it, with no
// weighted tokens, and one as v0.4.0 prints it, with every field it added.
// The lists show what the report sent, and say which it is. An entry's
// device_id is new too, but a matrix row has had one all along.
let added: Set<String> = ["burn", "fresh", "burn_days", "burn_share", "window_burn", "covered_since", "active_sessions", "transcript_gaps",
                          "estimated", "held_back_by", "estimated_reason", "burn_estimated", "gap", "replaced_by", "limitations",
                          "no_reading", "outside", "basis", "pace"]
let stripped = { (json: Any) in objects(json) { $0.filter { !added.contains($0.key) } } }
let v034 = try edited("team.json", stripped)
let v034Solo = try edited("solo.json", stripped)
equal("v0.3.4 label", v034.tokenLabel, "tokens in+out")
equal("v0.3.4 solo label", v034Solo.tokenLabel, "tokens in+out")
check("v0.3.4 tooltip", v034.tokenHelp.hasPrefix("Input + output tokens"))
let row034 = v034.team.matrix.rows[0]
check("v0.3.4 row falls back", row034.shown.value == row034.usage && row034.shown.label == "tokens in+out")
check("v0.3.4 share falls back", row034.shownShare.value == row034.share && row034.cells[0].shownShare.value == row034.cells[0].share)
check("v0.3.4 window falls back", v034.team.matrix.columns.allSatisfy { $0.shownWindow.value == $0.windowTokens && $0.shown.value == $0.usage })
check("v0.3.4 cells fall back", row034.cells.allSatisfy { $0.shown.value == $0.usage && $0.shownWindow.value == $0.windowTokens })
check("v0.3.4 days fall back", v034Solo.providers[0].accounts[0].shownDays.value == v034Solo.providers[0].accounts[0].days)
check("v0.3.4 projects fall back", v034.projects.allSatisfy { $0.shown.value == $0.usage && $0.activeSessions == nil })
check("v0.3.4 devices fall back", v034.team.devices.allSatisfy { $0.shown.value == $0.usage && !$0.burnEstimated && $0.limitations.isEmpty })
check("v0.3.4 accounts fall back", v034.team.providers.allSatisfy { $0.accounts.allSatisfy { a in
    a.shown.value == a.usage && a.noReading == nil && a.outside == nil && a.perDevice.allSatisfy { $0.shown.value == $0.usage }
} })
check("v0.3.4 team not estimated", !v034.team.estimated && v034.team.estimatedReason == nil && v034.team.heldBackBy.isEmpty)
check("v0.3.4 forecast basis", v034.teamAccount(provider: "claude", label: "mira@studio.dev")?.quota?.main?.forecast?.basis == nil)
equal("v0.3.4 attention", v034.attention.map(\.kind.rawValue), ["out", "out", "over", "error", "silent", "old"])

/// Usage, or a share, of every period times f.
func scaled(_ v: Any?, _ f: Double) -> Any {
    guard let o = v as? [String: Any] else { return NSNull() }
    return o.mapValues { x -> Any in (x as? NSNumber).map { f == 3 ? $0.intValue * 3 as Any : $0.doubleValue * f as Any } ?? NSNull() }
}
let v04 = try edited("team.json") { json in
    var o = objects(stripped(json)) { o in
        var o = o
        if o["usage"] != nil { o["burn"] = scaled(o["usage"], 3) }
        if let days = o["days"] as? [Int] { o["burn_days"] = days.map { $0 * 3 } }
        if o["share"] != nil { o["burn_share"] = scaled(o["share"], 0.5) }
        if let w = o["window_tokens"] as? Int { o["window_burn"] = w * 3 }
        if o["path"] != nil, o["sessions"] != nil {
            o["fresh"] = o["usage"]
            o["active_sessions"] = ["today": 1, "7d": 2, "30d": 3, "90d": 4]
        }
        if o["homes"] != nil { o["covered_since"] = "2026-07-01T09:00:00Z" }
        if o["matrix"] != nil {
            o["estimated"] = true
            o["held_back_by"] = ["leo-air"]
            o["estimated_reason"] = "leo-air runs v0.3.0, which sends no weighted tokens: reinstall it with install.sh"
        }
        let device = o["os_user"] != nil ? o["device"] : o["cells"] != nil ? o["device_id"] : nil
        if device as? String == leoAir {
            o["burn_estimated"] = true
            if o["os_user"] != nil { o["limitations"] = ["no_claude_quota"] }
        }
        if o["label"] as? String == "courier", o["os_user"] != nil {
            o["replaced_by"] = "courier-2"
            o["gap"] = ["seconds": 43200, "at": "2026-09-23T08:28:00Z"]
            o["last_error_at"] = "2026-09-23T08:20:00Z"
        }
        if o["label"] as? String == "leo@studio.dev", o["busiest"] != nil, (o["quota"] as? [String: Any])?["source"] as? String != "codex" {
            o["no_reading"] = "leo-air runs v0.3.0: reinstall with install.sh"
        }
        if o["main"] as? Bool == true, o["forecast"] is [String: Any] {
            o["forecast"] = (o["forecast"] as! [String: Any]).merging(["basis": "recent", "pace": 1.5]) { $1 }
        }
        return o
    } as! [String: Any]
    let attention = o["attention"] as! [[String: Any]]
    o["attention"] = [attention[0],
        ["kind": "tight", "provider": "claude", "account": "mira@studio.dev", "name": "mira", "resets_at": "2026-09-26T23:00:00Z", "percent": 88],
        ["kind": "outside", "provider": "codex", "account": "mira@studio.dev", "name": "mira", "at": "2026-09-24T04:40:00Z", "percent": 12.4],
        ["kind": "no_reading", "provider": "claude", "account": "leo@studio.dev", "name": "leo", "message": "leo-air runs v0.3.0: reinstall with install.sh"],
        ["kind": "duplicate", "devices": ["courier"], "device_id": "d-3f9a1c2e7b5d", "message": "courier-2", "at": "2026-09-23T08:28:00Z"],
        ["kind": "old", "devices": ["leo-air"], "message": "v0.4.0", "limitations": ["no_claude_quota"]],
        ["kind": "someday", "message": "a kind from a later release"]]
    return o
}
equal("v0.4 label", v04.tokenLabel, "weighted tokens")
check("v0.4 tooltip", v04.tokenHelp.hasPrefix("Weighted tokens"))
let row04 = v04.team.matrix.rows[0], col04 = v04.team.matrix.columns[0]
equal("v0.4 row", [row04.shown.label, row04.shownShare.label, col04.shownWindow.label], ["weighted tokens", "weighted tokens", "weighted tokens"])
equal("v0.4 row burn", row04.shown.value.week, row04.usage.week * 3)
equal("v0.4 window burn", col04.shownWindow.value, col04.windowTokens * 3)
equal("v0.4 cell burn", [row04.cells[0].shown.value.week, row04.cells[0].shownWindow.value], [row04.cells[0].usage.week * 3, row04.cells[0].windowTokens * 3])
equal("v0.4 share", row04.shownShare.value.week, row04.share.week.map { $0 * 0.5 })
let account04 = v04.providers[0].accounts[0]
equal("v0.4 days", account04.shownDays.value, account04.days.map { $0 * 3 })
equal("v0.4 days label", account04.shownDays.label, "weighted tokens")
equal("v0.4 project", [v04.projects[0].shown.value.quarter, v04.projects[0].activeSessions?.week], [v04.projects[0].usage.quarter * 3, 2])
equal("v0.4 covered since", v04.providers[0].coveredSince, time("2026-07-01T09:00:00Z"))
let leo04 = v04.teamDevice(named: "leo-air")!, courier04 = v04.teamDevice(named: "courier")!
check("v0.4 estimated device", leo04.burnEstimated && leo04.limitations == ["no_claude_quota"] && leo04.shown.value.week == leo04.usage.week * 3)
check("v0.4 estimated row", v04.team.matrix.rows.filter(\.burnEstimated).map(\.device) == ["leo-air"])
equal("v0.4 reinstalled device", [courier04.replacedBy, courier04.gap.map { "\($0.seconds)" }], ["courier-2", "43200"])
equal("v0.4 last error at", courier04.lastErrorAt, time("2026-09-23T08:20:00Z"))
check("v0.3.4 last error at", v034.teamDevice(named: "courier")?.lastErrorAt == nil)
equal("v0.4 estimated team", [v04.team.estimated ? "estimated" : "-", v04.team.heldBackBy.joined(), v04.team.estimatedReason ?? "-"],
      ["estimated", "leo-air", "leo-air runs v0.3.0, which sends no weighted tokens: reinstall it with install.sh"])
equal("v0.4 no reading", v04.teamAccount(provider: "claude", label: "leo@studio.dev")?.noReading, "leo-air runs v0.3.0: reinstall with install.sh")
equal("v0.4 per device", v04.teamAccount(provider: "claude", label: "mira@studio.dev").map { a in a.perDevice.map { $0.shown.label } }?.first,
      "weighted tokens")
let forecast04 = v04.teamAccount(provider: "claude", label: "mira@studio.dev")?.quota?.main?.forecast
equal("v0.4 forecast", [forecast04?.basis, forecast04?.pace.map { "\($0)" }], ["recent", "1.5"])
equal("v0.4 attention", v04.attention.map(\.kind.rawValue), ["out", "tight", "outside", "no_reading", "duplicate", "old", "someday"])
check("v0.4 kinds", v04.attention.map(\.kind) == [.out, .tight, .outside, .noReading, .duplicate, .old, .other("someday")])
equal("v0.4 attention fields", [v04.attention[4].deviceId, v04.attention[5].limitations.first], ["d-3f9a1c2e7b5d", "no_claude_quota"])
// Each new kind in the words of the report's ATTENTION line.
let texts = v04.attention.map { AttentionText($0, report: v04, now: v04.generatedAt, timeZone: berlin) }
equal("attention subjects", texts.map(\.subject), ["Claude mira · 5h", "Claude mira", "Codex mira", "Claude leo", "courier", "leo-air", ""])
equal("attention texts", texts.dropFirst().map(\.detail), [
    "~88% at reset Sun 01:00 at the last day's pace",
    "+12 points in 9h, no team device spent",
    "leo-air runs v0.3.0: reinstall with install.sh",
    "reinstalled as courier-2: ai-usage team forget-device 3f9a1c2e",
    "on v0.3.0 · cannot read Claude quota; reinstall with install.sh",
    "a kind from a later release",
])
check("reinstall has no update alert", texts[5].alert == nil)
// The pace a tight line names: this week's without the recent basis.
let tightWeek = AttentionText(v034.attention[0], report: v034, now: v034.generatedAt, timeZone: berlin)
equal("out text unchanged", tightWeek.detail, "back Thu 17:05 (in 1h 25m)")
let chips04 = Chip.chips(v04)
equal("v0.4 chips", chips04.map { "\($0.text) \($0.state) \($0.devices)" },
      ["1 needs reinstall tight [\"leo-air\"]", "1 reinstalled unknown [\"courier\"]"])
check("v0.4 chip kinds", chips04.map(\.kind) == [.reinstall, .duplicate])
let calm04 = try edited("team.json") { json in
    var o = json as! [String: Any]
    o["attention"] = (o["attention"] as! [[String: Any]]).filter { !["out", "over"].contains($0["kind"] as? String) }
        + [["kind": "tight", "provider": "claude", "account": "mira@studio.dev", "name": "mira", "resets_at": "2026-09-26T23:00:00Z", "percent": 88]]
    return o
}
equal("calm verdict with a tight window", Verdict(calm04, now: calm04.generatedAt, timeZone: berlin).subline,
      "Most room: Grok 7f3b9c21 · 94% · 1 tight")

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
