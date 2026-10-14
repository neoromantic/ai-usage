import Foundation

/// What a subscription's details in Limits say besides its windows, as the
/// report has it: who spent the main window since it began, where the
/// window that runs out is headed and at what pace, when the subscription
/// was last used, and how old its reading is and who took it, when that
/// says something. It only words what the report decided.
public struct SubscriptionDetails: Sendable {
    /// The devices that spent the main window since it began, busiest
    /// first, in forms that drop what a narrow line has no room for, the
    /// longest first: "build-01 84M, scout 35M, +8 more · since 21 Sep
    /// 02:40". Alone, the tokens. The tooltip names every device.
    public struct Spent: Equatable, Sendable {
        public let forms: [String]
        public let help: String
    }

    /// How old the reading is, "4m old", and where it came from: ", from
    /// scout (hermes) via the Codex login". The age is stale when a
    /// window's reading is.
    public struct Reading: Equatable, Sendable {
        public let age: String
        public let source: String
        public let stale: Bool

        public var text: String { age + source }
    }

    public let thisWindow: Spent?
    /// "164% at reset · this week's pace", the window's name before it
    /// when it is not the one shown, "would reach" when nobody spent on
    /// it since it would have run out.
    public let forecast: String?
    /// "20m ago".
    public let lastUsed: String?
    /// Only when it says more than that the reading is this Mac's, and
    /// recent: an old reading, one another device took, one a forecast
    /// that stopped stands on, or another login's.
    public let reading: Reading?

    public init(provider: String, account: TeamAccount, line: LimitLine, report: Report, now: Date, timeZone: TimeZone = .current) {
        thisWindow = Self.spent(provider: provider, account: account, report: report, now: now, timeZone: timeZone)
        forecast = Self.forecast(account, line)
        lastUsed = account.lastActiveAt.map { Format.ago($0, now: now) }
        reading = Self.reading(account, report: report, now: now)
    }

    /// Who spent the main window, from the report's matrix: each device's
    /// tokens since the window began, as the column has its start.
    private static func spent(provider: String, account: TeamAccount, report: Report, now: Date, timeZone: TimeZone) -> Spent? {
        let m = report.team.matrix
        guard let col = m.columns.firstIndex(where: { !$0.noQuota && $0.provider == provider && $0.label == account.label }) else { return nil }
        let column = m.columns[col]
        let used = m.rows.enumerated().compactMap { i, r -> (i: Int, device: String, tokens: Int)? in
            guard col < r.cells.count, r.cells[col].shownWindow.value > 0 else { return nil }
            return (i, r.device, r.cells[col].shownWindow.value)
        }.sorted { $0.tokens != $1.tokens ? $0.tokens > $1.tokens : $0.i < $1.i }
        guard !used.isEmpty else { return nil }
        let items = used.map { "\($0.device) \(Format.tokensM($0.tokens))" }
        let since = column.windowStart.map { "since " + sinceText($0, now: now, timeZone: timeZone) }
        var leads: [String]
        if report.solo {
            leads = [Format.tokensM(column.shownWindow.value)]
        } else {
            // The two busiest, then the busiest alone, with the rest as a
            // count.
            let list = { (n: Int) in (items.prefix(n) + (items.count > n ? ["+\(items.count - n) more"] : [])).joined(separator: ", ") }
            leads = [list(2)]
            if items.count > 1 {
                leads.append(list(1))
            }
        }
        let forms = since.map { s in [leads[0] + " · " + s] } ?? []
        let window = account.quota?.main?.name ?? "This"
        let head = "\(window) window" + (since.map { " \($0)" } ?? "") + ", \(column.shownWindow.label):"
        return Spent(forms: forms + leads, help: ([head] + items).joined(separator: "\n"))
    }

    /// A start within the last day as the clock, else as the date: "21 Sep
    /// 02:40", so that it does not read as the reset's weekday.
    private static func sinceText(_ t: Date, now: Date, timeZone: TimeZone) -> String {
        guard now.timeIntervalSince(t) >= 86400 else { return Format.clock(t, now: now, timeZone: timeZone) }
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "d MMM HH:mm"
        return f.string(from: t)
    }

    /// The window that runs out, or else the first over one, as the row
    /// orders them.
    private static func forecast(_ a: TeamAccount, _ line: LimitLine) -> String? {
        let shown = line.window
        let over = line.runsOut?.window ?? ([shown] + line.others).first { $0.state == "over" && $0.forecast?.runsOutAt != nil }
        guard let w = over, let f = w.forecast else { return nil }
        let name = w.name == shown.name ? "" : (a.quota?.shortName(w.name) ?? w.name) + " "
        // Nobody spent on it since it would have run out: where it was
        // headed, not where it is.
        return name + (f.stopped ? "would reach " : "") + "\(Int(f.percent))% at reset · \(w.pace) pace"
    }

    private static func reading(_ a: TeamAccount, report: Report, now: Date) -> Reading? {
        guard let q = a.quota else { return nil }
        // Another device's reading. A report that does not say which device
        // read it names one: of a team, that may be another.
        let elsewhere = q.device != nil && (q.deviceId.map { $0 != report.thisDevice?.device } ?? !report.solo)
        let stopped = q.windows.contains { $0.forecast?.stopped == true }
        guard q.stale || stopped || elsewhere || q.from != nil else { return nil }
        var source = elsewhere ? q.device.map { ", from \($0)" } ?? "" : ""
        if let from = q.from {
            source += " via the \(Format.provider(from)) login"
        }
        return Reading(age: "\(Format.duration(now.timeIntervalSince(q.observedAt))) old", source: source, stale: q.stale)
    }
}
