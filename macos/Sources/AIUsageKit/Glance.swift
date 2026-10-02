import Foundation

/// The popover's first line: the most urgent thing the report's attention
/// says about the subscriptions, or that all is on track and which one has
/// the most room. It only picks among what the report has decided.
public struct Verdict: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        /// A window is out.
        case out
        /// A window runs out before its reset.
        case over
        /// Nothing is out or runs out.
        case calm
        /// No subscription has a window read.
        case none
    }

    public let kind: Kind
    public let title: String
    public let subline: String
    /// The title without the time it runs out, which leads the subline
    /// instead, for a header too narrow for the whole title.
    public let compactTitle: String
    public let compactSubline: String
    /// The other out and over entries the title leaves out.
    public let more: Int
    /// The account the verdict is about, to open on a click.
    public let provider: String?
    public let label: String?

    public init(_ r: Report, now: Date, timeZone: TimeZone = .current) {
        let clock = { (t: Date) in Format.clock(t, now: now, timeZone: timeZone) }
        let urgent = r.attention.filter { $0.kind == .out || $0.kind == .over }
        let first = urgent.first { $0.kind == .out } ?? urgent.first
        if let a = first {
            let account = a.provider.flatMap { r.teamAccount(provider: $0, label: a.account ?? "") }
            var subject = Format.provider(a.provider ?? "") + " " + (a.name ?? a.account ?? "")
            if let w = a.window {
                subject += " · " + (account?.quota?.shortName(w) ?? w)
            }
            var parts: [String] = []
            var compact: (title: String, lead: String)?
            if a.kind == .out {
                kind = .out
                title = subject + " is out"
                parts.append(a.at.map { "Back in \(Format.duration($0.timeIntervalSince(now))) · \(clock($0))" } ?? "Back at its reset")
            } else {
                kind = .over
                if let at = a.at {
                    title = subject + (at > now ? " runs out ~" : " ran out ~") + clock(at)
                    compact = (subject + (at > now ? " runs out" : " ran out"), "~" + clock(at))
                    // The reset's clock is the row's countdown, and the
                    // verdict's tooltip.
                    if let reset = a.resetsAt {
                        parts.append("\(Format.duration(reset.timeIntervalSince(at))) before its reset")
                    }
                } else {
                    title = subject + " runs out at reset"
                    if let reset = a.resetsAt {
                        parts.append("Resets \(clock(reset))")
                    }
                }
            }
            // The count of the others comes before the reading's age, which
            // the line cuts first.
            more = urgent.count - 1
            if more > 0 {
                parts.append("+\(more) more")
            }
            if let age = a.readingAgeSeconds {
                parts.append("reading \(Format.duration(TimeInterval(age))) old")
            }
            subline = parts.joined(separator: " · ")
            compactTitle = compact?.title ?? title
            compactSubline = compact.map { ([$0.lead] + parts).joined(separator: " · ") } ?? subline
            (provider, label) = (a.provider, a.account)
            return
        }
        more = 0
        let accounts = r.team.providers.flatMap { p in p.accounts.filter(\.subscription).map { (p.provider, $0) } }
        guard accounts.contains(where: { $0.1.tightest != nil }) else {
            kind = .none
            title = "No limits read yet"
            subline = "Claude Code, Codex and Grok report them after first use"
            (compactTitle, compactSubline) = (title, subline)
            (provider, label) = (nil, nil)
            return
        }
        kind = .calm
        title = "All on track"
        var room: (provider: String, account: TeamAccount, window: Quota.Window)?
        for (p, a) in accounts where a.state != "out" && a.state != "over" {
            guard let w = a.tightest else { continue }
            if room.map({ Format.percentLeft(w) > Format.percentLeft($0.window) }) ?? true {
                room = (p, a, w)
            }
        }
        var parts: [String] = []
        if let room {
            parts.append("Most room: \(Format.provider(room.provider)) \(room.account.name) · \(Format.left(room.window))")
        }
        // A window of this Mac's that ends nearly full comes before the
        // ones left mostly unused.
        let tight = r.attention.filter { $0.kind == .tight }.count
        if tight > 0 {
            parts.append("\(tight) tight")
        }
        let under = r.attention.filter { $0.kind == .under }.count
        if under > 0 {
            parts.append("\(under) underused")
        }
        subline = parts.joined(separator: " · ")
        (compactTitle, compactSubline) = (title, subline)
        (provider, label) = (room?.provider, room?.account.label)
    }
}

/// A small button under the verdict for a problem that is not about a
/// subscription: the report that could not be read, a part of the
/// collector that fails, and devices that fail, stopped reporting, no
/// longer update themselves, need a reinstall, or were reinstalled.
/// Subscriptions have the verdict and their rows.
public struct Chip: Equatable, Sendable {
    public enum Kind: Equatable, Sendable {
        /// The last refresh failed while an older report shows.
        case refresh
        /// A collector health item that fails, or is not scheduled.
        case health(item: String, status: String)
        case errors
        case silent
        /// Devices on an older release that do not update themselves.
        case notUpdating
        /// Devices whose release cannot do something, such as read Claude's
        /// quota: they need a reinstall.
        case reinstall
        /// Devices that were reinstalled, whose old record stays.
        case duplicate
        /// With this Mac alone, its errors and silence in one.
        case collector
    }

    public let kind: Kind
    public let text: String
    /// What the chip says where the row is too narrow for every chip's
    /// words: its count, or nothing beside its symbol.
    public let short: String
    public let symbol: String
    /// The state whose color the chip takes; "unknown" for a quiet chip.
    public let state: String
    /// The attention entries behind the chip.
    public let entries: [Attention]
    /// The devices the chip is about, in the report's order.
    public let devices: [String]
    /// The devices on an older release that update themselves, which a
    /// not updating chip leaves out.
    public let updating: Int

    init(_ kind: Kind, _ text: String, short: String = "", symbol: String, state: String, entries: [Attention] = [],
         devices: [String]? = nil, updating: Int = 0) {
        (self.kind, self.text, self.short, self.symbol, self.state, self.entries) = (kind, text, short, symbol, state, entries)
        self.devices = devices ?? Self.devices(entries)
        self.updating = updating
    }

    private static func devices(_ entries: [Attention]) -> [String] {
        var seen = Set<String>()
        return entries.flatMap { $0.devices ?? [] }.filter { seen.insert($0).inserted }
    }

    public static func == (a: Chip, b: Chip) -> Bool {
        a.kind == b.kind && a.text == b.text && a.short == b.short && a.symbol == b.symbol && a.state == b.state && a.devices == b.devices
    }

    /// The report's chips, in the order they show.
    public static func chips(_ r: Report, refreshFailed: Bool = false) -> [Chip] {
        var out: [Chip] = []
        if refreshFailed {
            out.append(Chip(.refresh, "Can't read report", symbol: "exclamationmark.triangle.fill", state: "over"))
        }
        let c = r.collector
        var covered: [(Attention) -> Bool] = []
        for h in c.health where h.state == "error" || h.status == "unscheduled" {
            let text: String
            switch (h.item, h.status) {
            case ("collection", "never"): text = "Never collected"
            case ("collection", "failed"): text = "Last run failed"
            case ("collection", "unscheduled"): text = "Not scheduled"
            case ("relay", "failing"): text = "Relay failing"
            case ("update", "failed"): text = "Update failed"
            default: text = Format.provider(h.item) + " " + h.status
            }
            out.append(Chip(.health(item: h.item, status: h.status), text, symbol: "exclamationmark.triangle.fill",
                            state: h.state == "error" ? "out" : "tight"))
            // The report names this Mac's failing relay, update check, or
            // run as an error of its own too; the health chip says it once.
            switch (h.item, h.status) {
            case ("relay", "failing"): covered.append { ($0.message ?? "").hasPrefix("relay") }
            case ("update", "failed"): covered.append { ($0.message ?? "").hasPrefix("update") }
            case ("collection", "failed"): covered.append { $0.message != nil && $0.message == c.lastError }
            default: break
            }
        }
        let of = { (k: Attention.Kind) in r.attention.filter { $0.kind == k } }
        let errors = of(.error).filter { a in !(a.devices == [c.deviceLabel] && covered.contains { $0(a) }) }
        let silent = of(.silent)
        if r.solo {
            if !(errors + silent).isEmpty {
                out.append(Chip(.collector, collectorText(r, errors: errors, silent: silent), symbol: "xmark.circle.fill",
                                state: "out", entries: errors + silent))
            }
        } else {
            if !errors.isEmpty {
                out.append(Chip(.errors, errors.count == 1 ? "1 error" : "\(errors.count) errors", short: "\(errors.count)",
                                symbol: "xmark.circle.fill", state: "out", entries: errors))
            }
            if !silent.isEmpty {
                let n = devices(silent).count
                out.append(Chip(.silent, "\(n) not reporting", short: "\(n)", symbol: "antenna.radiowaves.left.and.right.slash",
                                state: "tight", entries: silent))
            }
        }
        // The devices whose release lacks something have an old entry of
        // their own, first; a reinstall is their fix, whether or not they
        // update themselves.
        let lacking = of(.old).filter { !$0.limitations.isEmpty }
        if !lacking.isEmpty {
            let n = devices(lacking).count
            out.append(Chip(.reinstall, n == 1 ? "1 needs reinstall" : "\(n) need reinstall", short: "\(n)",
                            symbol: "arrow.down.circle.fill", state: "tight", entries: lacking))
        }
        // A device on an older release that updates itself is lag after a
        // release, not a problem: only those the report says do not update
        // themselves get a chip.
        let old = of(.old).filter(\.limitations.isEmpty)
        if let first = old.first {
            let all = devices(old)
            let stuck = all.filter { r.teamDevice(named: $0)?.notUpdating == true }
            if !stuck.isEmpty {
                out.append(Chip(.notUpdating, "\(stuck.count) not updating", short: "\(stuck.count)", symbol: "arrow.up.circle.fill",
                                state: "tight", entries: [first], devices: stuck, updating: all.count - stuck.count))
            }
        }
        let duplicate = of(.duplicate)
        if !duplicate.isEmpty {
            let n = devices(duplicate).count
            out.append(Chip(.duplicate, "\(n) reinstalled", short: "\(n)", symbol: "square.on.square", state: "unknown",
                            entries: duplicate))
        }
        return out
    }

    /// A solo chip's words: the tool that fails, when every error names the
    /// same one, as "codex: app-server exited" does.
    private static func collectorText(_ r: Report, errors: [Attention], silent: [Attention]) -> String {
        guard silent.isEmpty else { return "Collector error" }
        let tools = Set(errors.map { a -> String? in
            guard let m = a.message, let colon = m.firstIndex(of: ":") else { return nil }
            let tool = String(m[..<colon])
            return r.providers.contains { $0.provider == tool } ? tool : nil
        })
        guard tools.count == 1, let tool = tools.first ?? nil else { return "Collector error" }
        return Format.provider(tool) + " failing"
    }
}

/// A subscription's row in Limits, as the report has it: the window its bar
/// and percent show, which is the main one, or the one that limits the
/// account when the main one has no reading; the account's state, which the
/// report takes from every window that limits it; and the other windows
/// that limit it. It only picks among what the report has decided.
public struct LimitLine: Sendable {
    /// The window the bar and percent show.
    public let window: Quota.Window
    /// The account's state, as the report has it.
    public let state: String
    /// The other known windows that limit the account, the one with the
    /// least left first.
    public let others: [Quota.Window]
    /// Another window that is out, when the one shown is not: the account
    /// is out until it is back.
    public let blocking: Quota.Window?
    /// The main window, when it has no reading and another is shown.
    public let unreadMain: Quota.Window?

    /// Nil for an account with no window known that limits it.
    public init?(_ a: TeamAccount) {
        let main = a.quota?.main
        guard let shown = main?.known == true ? main : a.tightest else { return nil }
        window = shown
        state = a.state
        let ws = (a.quota?.windows ?? []).filter { $0.limits && $0.known && $0.name != shown.name }
        others = ws.enumerated().sorted { x, y in
            Format.percentLeft(x.element) != Format.percentLeft(y.element)
                ? Format.percentLeft(x.element) < Format.percentLeft(y.element) : x.offset < y.offset
        }.map(\.element)
        blocking = shown.state == "out" ? nil : others.first { $0.state == "out" }
        unreadMain = main?.known == true ? nil : main
    }
}

/// A run of the Projects list: the projects next to each other in one
/// folder, under its name, or the ones on their own, under no name at the
/// top and "Other" after a group. It only lays out the report's order.
public struct ProjectSection: Sendable {
    public let title: String?
    /// The folder a group is in, "~/src/orbit"; empty for the ones on
    /// their own.
    public let folder: String
    public let grouped: Bool
    public let projects: [Project]

    public static func sections(_ projects: [Project], home: String) -> [ProjectSection] {
        var runs: [(folder: String, projects: [Project])] = []
        for p in projects {
            let folder = Format.pathParts(p.path, home: home).folder
            if let last = runs.last, last.folder == folder, !folder.isEmpty {
                runs[runs.count - 1].projects.append(p)
            } else {
                runs.append((folder, [p]))
            }
        }
        var out: [ProjectSection] = []
        for run in runs {
            if run.projects.count > 1 {
                let title = run.folder == "~" ? "Home" : run.folder.split(separator: "/").last.map(String.init) ?? run.folder
                out.append(ProjectSection(title: title, folder: run.folder, grouped: true, projects: run.projects))
            } else if let last = out.last, !last.grouped {
                out[out.count - 1] = ProjectSection(title: last.title, folder: "", grouped: false, projects: last.projects + run.projects)
            } else {
                out.append(ProjectSection(title: out.isEmpty ? nil : "Other", folder: "", grouped: false, projects: run.projects))
            }
        }
        return out
    }
}

/// What an attention entry is about and what to know, in a sentence. The
/// kinds since v0.4.0 say it in the words of the report's ATTENTION lines,
/// in their fullest form.
public struct AttentionText: Sendable {
    public var subject = ""
    /// What asks for something, first: how long the devices on an older
    /// release have not updated.
    public var alert: String?
    public var detail = ""

    public init(_ a: Attention, report r: Report, now: Date, timeZone: TimeZone = .current) {
        let clock = { (t: Date) in Format.clock(t, now: now, timeZone: timeZone) }
        let from = { (t: Date) in Format.duration(abs(t.timeIntervalSince(now))) }
        let stale = a.readingAgeSeconds.map { " · reading \(Format.duration(TimeInterval($0))) old" } ?? ""
        let devices = a.devices ?? []
        switch a.kind {
        case .out, .over, .tight, .under, .outside, .noReading:
            let account = a.provider.flatMap { r.teamAccount(provider: $0, label: a.account ?? "") }
            subject = Format.provider(a.provider ?? "") + " " + (a.name ?? a.account ?? "")
            if let w = a.window {
                subject += " · " + (account?.quota?.shortName(w) ?? w)
            }
        default:
            subject = devices.joined(separator: ", ")
        }
        switch a.kind {
        case .out:
            detail = (a.at.map { "back \(clock($0)) (in \(from($0)))" } ?? "until its reset") + stale
        case .over:
            if let at = a.at, let reset = a.resetsAt, reset.timeIntervalSince(at) >= 60 {
                detail = (at > now ? "runs out" : "ran out") + " ~\(clock(at)), \(Format.duration(reset.timeIntervalSince(at))) before its reset"
                    + " · resets \(clock(reset))" + stale
            } else {
                detail = "runs out at its reset" + stale
            }
        case .tight:
            let at = "~\(Int(a.percent ?? 0))% at reset" + (a.resetsAt.map { " " + clock($0) } ?? "")
            detail = at + " at \(Self.pace(a, r)) pace" + stale
        case .outside:
            let points = "+\(Int((a.percent ?? 0).rounded())) points"
            detail = points + (a.at.map { " in " + Format.age($0, now: now) } ?? "") + ", no team device spent"
        case .under:
            let unused = a.percent.map { "~\(max(100 - Int($0), 0))% would go unused" } ?? "some would go unused"
            detail = unused + (a.resetsAt.map { " · resets \(clock($0))" } ?? "") + stale
        case .error:
            detail = a.message ?? "failing"
        case .noReading:
            detail = a.message ?? "no reading"
        case .silent:
            detail = a.at.map { "no report since \(clock($0)) (\(from($0)) ago)" } ?? "no report for a day"
            if let m = a.message {
                detail += " · last error: \(m)"
            }
        case .duplicate:
            detail = "reinstalled as " + (a.message ?? "another device")
            if let id = a.deviceId {
                detail += ": " + Self.forgetCommand(id)
            }
        case .old:
            let versions = Set(devices.compactMap { r.teamDevice(named: $0)?.collectorVersion }).sorted()
            let on = devices.count == 1 && versions.count == 1
                ? "on \(versions[0])"
                : "\(devices.count) devices on " + (versions.count == 1 ? versions[0] : "older releases")
            if let lack = Self.lack(a.limitations) {
                detail = on + " · " + lack + "; reinstall with install.sh"
                return
            }
            detail = on + " · " + (a.message.map { "latest \($0)" } ?? "a newer release is out")
            // The report sets the time once a device has had the runs to
            // update and has not.
            if let at = a.at {
                alert = "Not updated for " + (devices.count > 1 ? "up to " : "") + from(at)
            }
        case .other:
            detail = a.message ?? ""
        }
    }

    /// The command that forgets a reinstalled device's old record, with
    /// the first 8 hex digits of its id.
    public static func forgetCommand(_ id: String) -> String {
        let hex = id.hasPrefix("d-") ? id.dropFirst(2) : Substring(id)
        return "ai-usage team forget-device " + hex.prefix(8)
    }

    /// The first of a release's limitations in words, as the report's
    /// ATTENTION line says it.
    public static func lack(_ limitations: [String]) -> String? {
        if limitations.contains("no_claude_quota") { return "cannot read Claude quota" }
        if limitations.contains("no_update_errors") { return "does not report update errors" }
        return nil
    }

    /// The pace a tight line forecasts at: the recent one, when the
    /// forecast takes it, else the week's, or the window's when it is not a
    /// week long.
    private static func pace(_ a: Attention, _ r: Report) -> String {
        let q = a.provider.flatMap { r.teamAccount(provider: $0, label: a.account ?? "") }?.quota
        let named = a.window.map { name in q?.windows.first { $0.name == name } }
        guard let w = named ?? q?.main else { return "this week's" }
        if w.forecast?.basis == "recent" { return w.recentPace }
        return (w.minutes ?? (w.name == "7d" ? 10080 : 0)) == 10080 ? "this week's" : "this window's"
    }
}
