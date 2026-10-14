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
equal("team attention", team.attention.map(\.kind.rawValue), ["out", "out", "over", "over", "tight", "error", "error", "no_reading", "silent", "old", "idle"])
// IDLE, a paid subscription no team device has used for a week, gets no
// chip, and the verdict does not count it.
equal("idle kind", team.attention.last?.kind, .idle)
// Each entry has the subject and text its console line says in full; an
// entry from an older ai-usage has neither.
equal("attention words", [team.attention[2].subject, team.attention[2].text],
      ["codex bots@studio.dev", "runs out ~Fri 08:59 at this week's pace, 2d 17h before reset"])
equal("device notes", ["courier", "mira-mbp"].map { team.teamDevice(named: $0)?.note }, ["silent since Wed 10:28", nil])
equal("team devices", team.team.devices.count, 12)
equal("team providers", team.team.providers.map(\.provider), ["claude", "codex", "grok", "hermes"])
equal("team matrix", [team.team.matrix.columns.count, team.team.matrix.rows.count], [7, 12])
equal("team projects", team.projects.count, 6)
// Projects next to each other in one folder show as a group under its name;
// the ones on their own follow as "Other", in the report's order.
let sections = ProjectSection.sections(team.projects, home: "/Users/mira")
equal("project sections", sections.map { "\($0.title ?? "-") \($0.grouped) \($0.projects.count)" }, ["orbit true 3", "Home true 2", "Other false 1"])
equal("project group folder", sections.first?.folder, "~/src/orbit")
let homeRun = team.projects.filter { !$0.path.contains("/src/") }
equal("home group", ProjectSection.sections(homeRun, home: "/Users/mira").map { $0.title ?? "-" }, ["Home"])
equal("no group", ProjectSection.sections(Array(team.projects.suffix(2)), home: "/Users/mira").map { $0.title ?? "-" }, ["-"])
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
// window's reset alone leaves the account known. The tokens no login was
// known for have no reading, so no row of their own: the report's reason
// shows in the line of those not read.
check("demo quotas known", team.team.providers.allSatisfy { $0.accounts.allSatisfy { $0.tightest != nil || $0.label == "unknown" } })
check("unknown has no row", team.teamAccount(provider: "claude", label: "unknown").map { $0.quota == nil && LimitLine($0) == nil && $0.noReading != nil } == true)
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
equal("days", [days.count, days.first ?? 0], [90, 1_805_817])
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
      ["claude mira 5h", "claude leo 7d", "claude unknown -", "codex leo 7d", "codex bots 7d", "codex mira 7d", "grok 7f3b9c21 7d", "hermes openai-codex 7d"])
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
equal("team verdict", [teamVerdict.title, teamVerdict.subline], ["Claude mira · 5h is out", "Back in 1h 25m · Thu 17:05 · +3 more"])
check("team verdict opens its account", teamVerdict.kind == .out && teamVerdict.more == 3 && teamVerdict.provider == "claude" && teamVerdict.label == "mira@studio.dev")
// The alerts list: every entry in the report's order, a subscription as
// this app names it, a device as the report does, the text as the report
// has it, and what a click opens.
let alerts = AlertLine.lines(team)
equal("alerts order", alerts.map(\.attention.kind.rawValue), team.attention.map(\.kind.rawValue))
equal("alert subjects", alerts.map(\.text.subject), ["Claude mira · 5h", "Codex leo", "Codex bots", "Claude mira", "Codex mira", "build-01", "leo-air",
                                                     "Claude unknown", "courier", "1 device on v0.3.4", "Grok 7f3b9c21"])
equal("alert texts", alerts.map(\.text.detail), team.attention.map(\.text))
check("alert targets", alerts.map(\.target) == [
    .limits(provider: "claude", label: "mira@studio.dev"), .limits(provider: "codex", label: "leo@studio.dev"),
    .limits(provider: "codex", label: "bots@studio.dev"), .limits(provider: "claude", label: "mira@studio.dev"),
    .limits(provider: "codex", label: "mira@studio.dev"), .devices(["build-01"]), .devices(["leo-air"]), .notRead(provider: "claude"),
    .devices(["courier"]), .devices(["leo-air"]), .limits(provider: "grok", label: "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64"),
])
// The list's last line is the subscription the calm verdict names.
equal("most room", Room.most(team).map { "\($0.text) \($0.account.label)" }, "Most room: Grok 7f3b9c21 · 100% 7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64")
let soloVerdict = Verdict(solo, now: solo.generatedAt, timeZone: berlin)
equal("solo verdict", [soloVerdict.title, soloVerdict.subline],
      ["Claude mira · Fable runs out ~Thu 23:55", "2d 1h before its reset · +1 more"])
// A narrow header cuts how long before its reset first, then moves the
// time it runs out from the title to the line.
equal("solo verdict short", soloVerdict.shortSubline, "+1 more")
equal("solo verdict compact", [soloVerdict.compactTitle, soloVerdict.compactSubline],
      ["Claude mira · Fable runs out", "~Thu 23:55 · +1 more"])
check("solo verdict is over", soloVerdict.kind == .over)
// An old reading's age stays in every form of the line, ahead of the count
// of the others.
let ranOut = try edited("solo.json") { json in
    var o = json as! [String: Any]
    var attention = o["attention"] as! [[String: Any]]
    attention[0]["at"] = "2026-09-24T03:18:00Z"
    attention[0]["reading_age_seconds"] = 129_600
    o["attention"] = attention
    return o
}
let ranOutVerdict = Verdict(ranOut, now: ranOut.generatedAt, timeZone: berlin)
equal("stale verdict", [ranOutVerdict.title, ranOutVerdict.subline, ranOutVerdict.shortSubline],
      ["Claude mira · Fable ran out ~Thu 05:18", "2d 19h before its reset · reading 1d 12h old · +1 more", "reading 1d 12h old · +1 more"])
equal("stale verdict compact", [ranOutVerdict.compactTitle, ranOutVerdict.compactSubline],
      ["Claude mira · Fable ran out", "~Thu 05:18 · reading 1d 12h old · +1 more"])
let calm = try edited("team.json") { json in
    var o = json as! [String: Any]
    o["attention"] = (o["attention"] as! [[String: Any]]).filter { !["out", "over", "tight"].contains($0["kind"] as? String) }
    return o
}
let calmVerdict = Verdict(calm, now: calm.generatedAt, timeZone: berlin)
equal("calm verdict", [calmVerdict.title, calmVerdict.subline, calmVerdict.label], ["All on track", "Most room: Grok 7f3b9c21 · 100%", "7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64"])
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
      ["2 errors xmark.circle.fill out [\"build-01\", \"leo-air\"]", "1 not reporting antenna.radiowaves.left.and.right.slash tight [\"courier\"]"])
check("team chip kinds", chips.map(\.kind) == [.errors, .silent] && chips.map(\.short) == ["2", "1"])
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
equal("failing relay said once", Chip.chips(broken).map(\.text), ["Relay failing", "2 errors", "1 not reporting"])
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
// An over account counts down to the window that runs out first, among
// the one shown and the others that limit it, the one shown first of
// equals; an account another window stops, and one not over, count down to
// their resets.
let runOut = { (r: Report, p: String, l: String) in r.teamAccount(provider: p, label: l).flatMap(LimitLine.init)?.runsOut }
equal("run-out countdown", runOut(team, "codex", "bots@studio.dev").map { "\($0.window.name) \($0.at == team.attention[2].at)" }, "7d true")
equal("first window to run out", runOut(solo, "claude", "mira@studio.dev").map { "\($0.window.name) \($0.at == solo.attention[0].at)" }, "7d Fable true")
check("no run-out when stopped or not over", runOut(team, "claude", "mira@studio.dev") == nil && runOut(team, "codex", "mira@studio.dev") == nil)
let sameRunOut = try edited("solo.json") { objects($0) { o in
    guard var f = o["forecast"] as? [String: Any], f["runs_out_at"] is String else { return o }
    f["runs_out_at"] = "2026-09-25T06:00:00Z"
    return o.merging(["forecast": f]) { $1 }
} }
equal("run-out tie goes to the shown window", runOut(sameRunOut, "claude", "mira@studio.dev")?.window.name, "7d")

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
equal("v0.3.4 attention", v034.attention.map(\.kind.rawValue), ["out", "out", "over", "over", "tight", "error", "error", "no_reading", "silent", "old", "idle"])

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
// An entry from an ai-usage that does not word its entries: the subject
// is the devices it is about, and the text its message, or nothing.
let texts = v04.attention.map { AttentionText($0, report: v04) }
equal("attention subjects", texts.map(\.subject), ["Claude mira · 5h", "Claude mira", "Codex mira", "Claude leo", "courier", "leo-air", ""])
equal("attention texts", texts.dropFirst().map(\.detail), ["", "", "leo-air runs v0.3.0: reinstall with install.sh", "courier-2", "v0.4.0",
                                                           "a kind from a later release"])
// A 5h window forecast at its recent pace says the recent, as the report does.
let tight5h = try edited("team.json") { json in
    var o = objects(json) { o in
        var o = o
        if o["name"] as? String == "5h", o["percent"] != nil, o["state"] != nil {
            o["forecast"] = ["percent": 95, "elapsed": 0.5, "basis": "recent", "pace": 10]
        }
        return o
    } as! [String: Any]
    o["attention"] = [["kind": "tight", "provider": "claude", "account": "mira@studio.dev", "name": "mira", "window": "5h", "resets_at": "2026-09-24T12:00:00Z", "percent": 95]]
    return o
}
equal("5h window's recent pace", tight5h.teamAccount(provider: "claude", label: "mira@studio.dev")?.quota?.windows.first { $0.name == "5h" }?.recentPace, "the recent")
// The pace a forecast takes, as the report names it: the recent one, else
// this week's, or this window's when it is not a week long.
let paces = { (r: Report, p: String, l: String) in r.teamAccount(provider: p, label: l)?.quota?.windows.map(\.pace) ?? [] }
equal("forecast paces", paces(tight5h, "claude", "mira@studio.dev") + paces(team, "codex", "bots@studio.dev"),
      ["the recent", "the last day's", "this window's", "this week's"])
let chips04 = Chip.chips(v04)
equal("v0.4 chips", chips04.map { "\($0.text) \($0.state) \($0.devices)" },
      ["1 needs reinstall tight [\"leo-air\"]", "1 reinstalled unknown [\"courier\"]"])
check("v0.4 chip kinds", chips04.map(\.kind) == [.reinstall, .duplicate])
let calm04 = try edited("team.json") { json in
    var o = json as! [String: Any]
    o["attention"] = (o["attention"] as! [[String: Any]]).filter { !["out", "over", "tight"].contains($0["kind"] as? String) }
        + [["kind": "tight", "provider": "claude", "account": "mira@studio.dev", "name": "mira", "resets_at": "2026-09-26T23:00:00Z", "percent": 88]]
    return o
}
equal("calm verdict with a tight window", Verdict(calm04, now: calm04.generatedAt, timeZone: berlin).subline,
      "Most room: Grok 7f3b9c21 · 100% · 1 tight")

// Notifications: the out and over windows of this Mac's subscriptions, as
// attention lists them, with the main window named; another device's are
// its user's to watch, unless Settings asks for the team's.
let always = NoticeSettings(runsOut: .always)
let teamNotices = WindowNotice.notices(team, always, now: team.generatedAt, timeZone: berlin)
equal("team notices", teamNotices.map { "\($0.kind) \($0.key)" }, ["out claude/mira@studio.dev/5h", "over claude/mira@studio.dev/7d"])
equal("out notice", teamNotices.first.map { [$0.title, $0.body, $0.thread] }, ["Claude mira · 5h is out", "Back Thu 17:05 (in 1h 25m)", "claude/mira@studio.dev"])
check("out notice's period", teamNotices.first?.resetsAt == time("2026-09-24T15:05:00Z") && teamNotices.first?.length == 5 * 3600)
equal("over notice", teamNotices.last.map { [$0.title, $0.body] }, ["Claude mira · 7d runs out ~Sat 10:28", "14h 31m before its reset · resets Sun 01:00"])
equal("over notice's reset", teamNotices.last?.resetsAt, time("2026-09-26T23:00:00Z"))
equal("over notice's run-out", teamNotices.last?.runsOutAt, time("2026-09-26T08:28:35.1159618Z"))
equal("solo notices", WindowNotice.notices(solo, always, now: solo.generatedAt, timeZone: berlin).map(\.title),
      ["Claude mira · 7d Fable runs out ~Thu 23:55", "Claude mira · 7d runs out ~Sat 12:46"])
let atReset = try edited("team.json") { json in
    var o = json as! [String: Any]
    o["attention"] = [["kind": "over", "provider": "claude", "account": "mira@studio.dev", "name": "mira", "resets_at": "2026-09-26T23:00:00Z",
                       "percent": 100, "reading_age_seconds": 7200]]
    return o
}
let atResetNotices = WindowNotice.notices(atReset, always, now: atReset.generatedAt, timeZone: berlin)
equal("over at its reset", atResetNotices.map { [$0.title, $0.body] }, [["Claude mira · 7d runs out at its reset", "Resets Sun 01:00 · reading 2h old"]])
equal("over at its reset runs out then", atResetNotices.first?.runsOutAt, time("2026-09-26T23:00:00Z"))
check("no notices in calm", WindowNotice.notices(calm, always, now: calm.generatedAt).isEmpty)

// By default, a window that will run out gets one only when it runs out
// within a day, as early forecasts of a long window change.
equal("within a day by default", WindowNotice.notices(team, now: team.generatedAt, timeZone: berlin).map(\.key), ["claude/mira@studio.dev/5h"])
equal("solo within a day", WindowNotice.notices(solo, now: solo.generatedAt, timeZone: berlin).map(\.title), ["Claude mira · 7d Fable runs out ~Thu 23:55"])
equal("never", WindowNotice.notices(solo, NoticeSettings(runsOut: .never), now: solo.generatedAt).count, 0)
equal("not when out", WindowNotice.notices(team, NoticeSettings(out: false, runsOut: .always), now: team.generatedAt).map(\.key), ["claude/mira@studio.dev/7d"])
let day: TimeInterval = 86400
let soon = WindowNotice(provider: "claude", label: "ann@acme.dev", window: "7d", kind: .over, resetsAt: team.generatedAt + 3 * day,
                        runsOutAt: team.generatedAt + day)
check("a day ahead", NoticeSettings().wants(soon, now: team.generatedAt) && !NoticeSettings().wants(soon, now: team.generatedAt - 1))
check("off", NoticeSettings(out: false, runsOut: .never).off && !NoticeSettings(out: false).off && !NoticeSettings(runsOut: .never).off)

// The team's: every account with a reading, bots' too, with the device that
// uses it most.
let teamWide = WindowNotice.notices(team, NoticeSettings(scope: .team), now: team.generatedAt, timeZone: berlin)
equal("the team's notices", teamWide.map { "\($0.kind) \($0.key)" },
      ["out claude/mira@studio.dev/5h", "out codex/leo@studio.dev/7d", "over codex/bots@studio.dev/7d"])
equal("the team's notices' text", teamWide.map { [$0.title, $0.body] }, [
    ["Claude mira · 5h is out", "Back Thu 17:05 (in 1h 25m) · used on mira-mbp +1"],
    ["Codex leo · 7d is out", "Back Fri 11:12 (in 19h 32m) · used on leo-air"],
    ["Codex bots · 7d runs out ~Fri 08:59", "2d 17h before its reset · resets Mon 02:40 · used on build-01 +9"],
])

// Settings read from defaults, each at its default until it is set.
let suite = "ai-usage-checks-\(ProcessInfo.processInfo.processIdentifier)"
let defaults = UserDefaults(suiteName: suite)!
equal("default settings", NoticeSettings(defaults), NoticeSettings())
defaults.set(false, forKey: NoticeSettings.outKey)
defaults.set("always", forKey: NoticeSettings.runsOutKey)
defaults.set("team", forKey: NoticeSettings.scopeKey)
equal("saved settings", NoticeSettings(defaults), NoticeSettings(out: false, runsOut: .always, scope: .team))
defaults.set("someday", forKey: NoticeSettings.runsOutKey)
equal("an unknown setting", NoticeSettings(defaults).runsOut, .day)
defaults.removePersistentDomain(forName: suite)

// Each window is said once a period and state, over a restart too.
let t0 = team.generatedAt
var ledger = NoticeLedger()
equal("notices said", ledger.record(teamNotices, now: t0).map(\.key), teamNotices.map(\.key))
equal("not said at the next run", ledger.record(teamNotices, now: t0.addingTimeInterval(900)).count, 0)
var restarted = try JSONDecoder().decode(NoticeLedger.self, from: JSONEncoder().encode(ledger))
equal("not said after a restart", restarted.record(teamNotices, now: t0.addingTimeInterval(1800)).count, 0)
let hour: TimeInterval = 3600
let reset = t0.addingTimeInterval(3 * hour)
func ann5h(_ kind: WindowNotice.Kind, _ resetsAt: Date) -> WindowNotice {
    WindowNotice(provider: "claude", label: "ann@acme.dev", window: "5h", kind: kind, resetsAt: resetsAt, length: 5 * hour)
}
let over5h = ann5h(.over, reset)
let out5h = ann5h(.out, reset.addingTimeInterval(20))
var steps = NoticeLedger()
equal("over", steps.record([over5h], now: t0).map(\.kind.rawValue), ["over"])
equal("over again", steps.record([over5h], now: t0 + 900).count, 0)
equal("forecast under 100%", steps.record([], now: t0 + 1800).count, 0)
equal("over in the same period", steps.record([over5h], now: t0 + 2700).count, 0)
equal("then out", steps.record([out5h], now: t0 + hour).map(\.kind.rawValue), ["out"])
equal("out again", steps.record([out5h], now: t0 + hour + 900).count, 0)
equal("over after out", steps.record([over5h], now: t0 + 2 * hour).count, 0)
equal("a reset read 10 minutes later", steps.record([ann5h(.out, reset.addingTimeInterval(600))], now: t0 + 2 * hour + 900).count, 0)
equal("a passed reset", steps.record([out5h], now: reset + 60).count, 0)
check("forgotten after the reset", steps.said.isEmpty)
equal("the next period", steps.record([ann5h(.over, reset + 5 * hour)], now: reset + hour).map(\.kind.rawValue), ["over"])
var early = NoticeLedger()
_ = early.record([over5h], now: t0)
equal("a period after an early reset", early.record([ann5h(.over, reset + 4 * hour)], now: t0 + hour).map(\.kind.rawValue), ["over"])
func grok(_ resetsAt: Date) -> WindowNotice {
    WindowNotice(provider: "grok", label: "7f3b9c21", window: "7d", kind: .over, resetsAt: resetsAt)
}
var unsized = NoticeLedger()
_ = unsized.record([grok(reset)], now: t0)
equal("without its length, a minute", unsized.record([grok(reset + 50)], now: t0 + 60).count, 0)
equal("without its length, the next period", unsized.record([grok(reset + hour)], now: t0 + 120).count, 1)
let unknownReset = WindowNotice(provider: "codex", label: "leo@acme.dev", window: "7d", kind: .out, resetsAt: nil)
var unknown = NoticeLedger()
equal("no reset", unknown.record([unknownReset], now: t0).count, 1)
equal("no reset again", unknown.record([unknownReset], now: t0 + 900).count, 0)
equal("no reset, back", unknown.record([], now: t0 + 1800).count, 0)
equal("no reset, out again", unknown.record([unknownReset], now: t0 + 2700).count, 1)

// The ring, of the team's subscriptions: every device's, not only this
// Mac's.
let elsewhere = try edited("team.json") { objects($0) { o in o["current"] != nil ? o.merging(["current": false]) { $1 } : o } }
check("no subscription of this Mac's", MenuSummary.pick(elsewhere) == nil)
let teamRing = MenuSummary.pick(elsewhere, tracks: .team)
equal("the team's ring", [teamRing?.provider, teamRing?.label, teamRing?.window, teamRing?.text], ["claude", "mira@studio.dev", "5h", "0%"])
equal("the team's ring alone", MenuSummary.pick(solo, tracks: .team)?.window, soloSummary?.window)

// The Team window's matrix: the report's columns under their providers,
// its devices in its order, and its numbers.
let matrix = TeamMatrix(team, period: .week, mode: .tokens)
equal("matrix groups", matrix.groups.map { "\($0.title) \($0.columns)" }, ["Codex 0..<3", "Claude 3..<6", "Grok 6..<7"])
equal("matrix columns", matrix.columns.map(\.id), ["codex/bots@studio.dev", "codex/mira@studio.dev", "codex/leo@studio.dev", "claude/mira@studio.dev",
                                                   "claude/leo@studio.dev", "claude/unknown", "grok/7f3b9c21-4e8a-4d6b-a1c5-2e9f0b7d8a64"])
equal("matrix titles", matrix.columns.map(\.title), ["bots", "mira", "leo", "mira", "leo", "unknown", "7f3b9c21"])
equal("matrix names", matrix.columns.map(\.name), ["Codex bots", "Codex mira", "Codex leo", "Claude mira", "Claude leo", "Claude unknown", "Grok 7f3b9c21"])
equal("matrix states", matrix.columns.map(\.state), ["over", "tight", "out", "out", "ok", nil, "under"])
equal("matrix rows", matrix.lines.map(\.device), team.team.matrix.rows.map(\.device))
equal("matrix row", matrix.lines[0].cells.map(\.text), ["131", "–", "–", "24", "–", "7", "–"])
equal("matrix cell help", matrix.lines[0].cells[0].help, "build-01 · Codex bots: 131M in 7 Days · 46% of column · 84M this window")
equal("matrix cell spoken", matrix.lines[0].cells[0].spoken, "Codex bots: 131 million weighted tokens in 7 days, 46% of column, 84 million this window")
equal("matrix empty cell help", matrix.lines[0].cells[1].help, "build-01 · Codex mira: none in 7 Days")
// A step of the heat map for every half of a tenfold below the largest
// cell, as the console's.
equal("heat steps", [1, 0.5, 0.3, 0.1, 0.03, 0.01, 0.001, 0].map { TeamMatrix.heatStep($0, top: 1) }, [5, 5, 4, 3, 2, 1, 1, 0])
equal("matrix heat", matrix.lines.map { $0.cells[0].heat }, [5, 0, 0, 5, 4, 4, 4, 4, 3, 3, 2, 2])
equal("matrix totals", matrix.totals.map(\.text), ["285", "56", "22", "128", "62", "7", "–"])
// TOTAL of all is the rows', as the console's; leo-air's is estimated.
equal("matrix total", [matrix.total.text, matrix.lines[2].total.text], ["~560", "~84"])
check("matrix estimate said", matrix.lines[2].total.help.hasSuffix("\n~ leo-air runs v0.3.4: it updates itself within 15 minutes"))
equal("matrix unit", matrix.unit, "M weighted tokens")
// Share: each device's part of each column, as the report has it.
let shares = TeamMatrix(team, period: .week, mode: .share)
equal("share row", shares.lines[0].cells.map(\.text), ["46", "–", "–", "19", "–", "100", "–"])
equal("share of a column alone", shares.lines[2].cells[2].text, "100")
equal("share totals", shares.totals.map(\.text), ["100", "100", "100", "100", "100", "100", "–"])
equal("share row total", shares.lines[0].total.text, "29")
equal("share unit", shares.unit, "% of column total")
equal("share heat", shares.lines[0].cells.map(\.heat), [5, 0, 0, 4, 0, 5, 0])
// Another period takes the report's numbers of it.
equal("today", TeamMatrix(team, period: .today, mode: .tokens).lines[0].cells[0].text, "13")
// The tokens with no quota head their own group, with no state.
let noQuota = try edited("team.json") { json in
    var o = json as! [String: Any], t = o["team"] as! [String: Any], m = t["matrix"] as! [String: Any]
    var columns = m["columns"] as! [[String: Any]]
    columns[6] = columns[6].merging(["provider": "hermes", "name": "hermes", "no_quota": true, "state": "unknown"]) { $1 }
    columns[6].removeValue(forKey: "label")
    m["columns"] = columns
    t["matrix"] = m
    o["team"] = t
    return o
}
let hermes = TeamMatrix(noQuota, period: .week, mode: .tokens)
equal("no quota group", hermes.groups.last.map { "\($0.title) \($0.columns)" }, "No Quota 6..<7")
equal("no quota column", hermes.columns.last.map { [$0.id, $0.title, $0.name, $0.state ?? "-"] }, ["hermes/", "Hermes", "Hermes (no quota)", "-"])

// Sorting: by a column, ties in the report's order; none keeps it.
let byBots = matrix.lines(sortedBy: .column(0), ascending: false).map(\.device)
equal("sorted by a column", byBots, ["build-01", "scout", "herald", "sentry", "editor", "archivist", "tutor", "courier", "critic", "concierge", "mira-mbp", "leo-air"])
equal("sorted up", matrix.lines(sortedBy: .column(0), ascending: true).map(\.device).prefix(3), ["mira-mbp", "leo-air", "concierge"])
equal("sorted by device", matrix.lines(sortedBy: .device, ascending: true).map(\.device).prefix(4), ["archivist", "build-01", "concierge", "courier"])
equal("sorted by total", matrix.lines(sortedBy: .total, ascending: false).map(\.device), matrix.lines.map(\.device))
equal("not sorted", matrix.lines(sortedBy: nil, ascending: true).map(\.device), matrix.lines.map(\.device))
equal("sorted by share", shares.lines(sortedBy: .column(3), ascending: false).map(\.device).prefix(3), ["mira-mbp", "build-01", "leo-air"])

// The status: each device as the report has it, a line each in the
// matrix's order.
let status = DeviceStatusTable(team)
equal("status rows", status.lines.map(\.device), matrix.lines.map(\.device))
let leoLine = status.lines[2]
equal("status line", [leoLine.user, leoLine.version, leoLine.note], ["leo", "v0.3.4", "codex: app-server exited without answering"])
check("status behind", leoLine.behind && status.lines.filter(\.behind).count == 1)
equal("status tools", status.lines.prefix(3).map { $0.tools.map { $0.provider + ($0.failing ? " ×" : "") } },
      [["claude ×", "codex", "hermes"], ["claude", "codex", "grok"], ["claude", "codex ×"]])
equal("status tokens", [leoLine.tokens.value.week, status.total.week], [83_872_070, 559_618_448])
check("status estimated", leoLine.estimated && status.estimated)
equal("status seen", status.lines[1].seen, time("2026-09-24T13:37:00Z"))
equal("sorted by version", status.lines(sortedBy: .version, ascending: true).map(\.device).prefix(2), ["leo-air", "build-01"])
equal("sorted by seen", status.lines(sortedBy: .seen, ascending: true).map(\.device).prefix(2), ["mira-mbp", "scout"])
equal("seen longest ago", status.lines(sortedBy: .seen, ascending: true).last?.device, "courier")
equal("sorted by today", status.lines(sortedBy: .tokens(.today), ascending: false).first?.device,
      team.team.matrix.rows.max { $0.shown.value.today < $1.shown.value.today }?.device)

// The header counts the devices as the console's status view does.
equal("team header", TeamHeader(team, period: .week, unit: matrix.unit).text, "12 devices · 2 errors · 1 not reporting · 7 Days UTC · M weighted tokens")
equal("team header share", TeamHeader(team, period: .today, unit: shares.unit).text, "12 devices · 2 errors · 1 not reporting · Today UTC · % of column total")

// The Team window is a team's alone: a button beside the popover's tabs and
// an item of the menu, with ⌘D. One device has neither, nor the device
// status view in Terminal.
equal("team buttons", AppMenu.buttons(team), [.team])
equal("solo buttons", AppMenu.buttons(solo), [])
equal("team menu", AppMenu.sections(team).map { "\($0.title): " + $0.actions.map(\.title).joined(separator: ", ") }, [
    "Actions: Collect Now, Team Window…",
    "Open in Terminal: Report, Device Status, All Projects",
    "AI Usage: Settings…, Quit AI Usage",
])
equal("solo menu", AppMenu.sections(solo).map(\.actions), [[.collect], [.report, .projects], [.settings, .quit]])
equal("menu without a report", AppMenu.sections(nil).map(\.actions), [[.settings, .quit]])
equal("menu shortcuts", AppMenu.sections(team).flatMap(\.actions).map(\.key), ["r", "d", "", "", "", ",", "q"])
check("menu symbols", AppMenu.sections(team).flatMap(\.actions).allSatisfy { !$0.symbol.isEmpty })
equal("terminal views", [AppAction.report, .devices, .projects].map { $0.terminal?.args.joined(separator: " ") }, ["report", "report --devices", "report --projects"])
// A release to install comes after Collect Now.
let available = try edited("team.json") { objects($0) { $0["item"] as? String == "update" ? $0.merging(["status": "available", "state": "info", "release": "v0.7.0"]) { $1 } : $0 } }
equal("menu update", AppMenu.sections(available, update: available.collector.health.first { $0.item == "update" }).first?.actions.map(\.title),
      ["Collect Now", "Update ai-usage to v0.7.0…", "Team Window…"])
// The menu's entries stop short of the screen: past 8, 7 and a line of the
// rest.
equal("menu entries capped", [3, 8, 9, 12].map { n -> String in
    let c = AppMenu.capped(Array(0..<n))
    return "\(c.shown.count)+\(c.more)"
}, ["3+0", "8+0", "7+2", "7+5"])

// About says the app's name and version once, and the command-line tool's
// release only when it is another one, or the tool is missing.
let about = { (app: String, cli: String?, found: Bool) -> [String?] in
    let a = AboutVersion(app: app, cli: cli, found: found)
    return [a.version, a.note]
}
equal("about one release", about("0.6.0", "v0.6.0", true), ["0.6.0", nil])
equal("about another release", about("0.6.0", "v0.5.1", true), ["0.6.0", "Command-line tool v0.5.1: updates with Check for Updates"])
equal("about a build from source", about("dev", "v0.6.0", true), ["dev", "Command-line tool v0.6.0: updates with Check for Updates"])
equal("about before the tool's version", about("0.6.0", nil, true), ["0.6.0", nil])
equal("about without the tool", about("0.6.0", nil, false), ["0.6.0", "Command-line tool not found"])

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
// The script Terminal opens runs the command as the collector does, with
// its folder and arguments quoted; without the LaunchAgent's PATH, the
// shell's and the standard folders.
let quoted = CLI(location: CLILocation(executable: local, stateHome: "/state/mira's ai-usage", path: "/usr/bin:/bin"))
let command = tmp.appendingPathComponent("AI Usage Report.command")
try Data(quoted.terminalScript(["env"]).utf8).write(to: command)
let sh = Process(), shOut = Pipe()
sh.executableURL = URL(fileURLWithPath: "/bin/sh")
sh.arguments = [command.path]
sh.standardOutput = shOut
try sh.run()
sh.waitUntilExit()
equal("terminal script", String(decoding: shOut.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self), "/state/mira's ai-usage|/usr/bin:/bin")
equal("terminal script without the agent", CLI(location: CLILocation(executable: URL(fileURLWithPath: "/opt/ai usage/ai-usage")))
        .terminalScript(["report", "--devices"]).split(separator: "\n").map(String.init).dropFirst().map { $0.hasPrefix("export PATH=\"$PATH\":'") ? "path" : $0 },
      ["path", "exec '/opt/ai usage/ai-usage' 'report' '--devices'"])

print(failures == 0 ? "all checks passed" : "\(failures) checks failed")
exit(failures == 0 ? 0 : 1)
