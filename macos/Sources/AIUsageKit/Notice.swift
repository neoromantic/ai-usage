import Foundation

/// Which notifications the person wants, as Settings sets them.
public struct NoticeSettings: Equatable, Sendable {
    /// When a window that will run out before its reset gets a notification.
    /// A forecast early in a long window can say it runs out days ahead and
    /// change by the next reading, so by default only one that runs out
    /// within a day does.
    public enum RunsOut: String, CaseIterable, Sendable {
        case day, always, never
    }

    /// Whose subscriptions get notifications.
    public enum Scope: String, CaseIterable, Sendable {
        /// The subscriptions logged in on this Mac.
        case mac
        /// Every subscription of the team, bots' included.
        case team
    }

    public static let outKey = "notifyOut"
    public static let runsOutKey = "notifyRunsOut"
    public static let scopeKey = "notifySubscriptions"
    /// How soon a window runs out for `RunsOut.day`.
    public static let soon: TimeInterval = 86400

    /// A window that is out.
    public var out = true
    public var runsOut = RunsOut.day
    public var scope = Scope.mac

    public init(out: Bool = true, runsOut: RunsOut = .day, scope: Scope = .mac) {
        (self.out, self.runsOut, self.scope) = (out, runsOut, scope)
    }

    /// The settings saved in defaults, each at its default when not set.
    public init(_ d: UserDefaults) {
        out = d.object(forKey: Self.outKey) as? Bool ?? true
        runsOut = d.string(forKey: Self.runsOutKey).flatMap(RunsOut.init) ?? .day
        scope = d.string(forKey: Self.scopeKey).flatMap(Scope.init) ?? .mac
    }

    /// No notification at all.
    public var off: Bool { !out && runsOut == .never }

    /// Whether a notice is one of those asked for.
    public func wants(_ n: WindowNotice, now: Date) -> Bool {
        switch (n.kind, runsOut) {
        case (.out, _): return out
        case (.over, .always): return true
        case (.over, .never): return false
        case (.over, .day): return n.runsOutAt.map { $0.timeIntervalSince(now) <= Self.soon } ?? false
        }
    }
}

/// A notification about a window of a subscription: it is out, or it runs
/// out before its reset. The report's attention decides which windows
/// those are and when; a notice only says it.
public struct WindowNotice: Equatable, Sendable {
    /// What the window is, the less urgent first.
    public enum Kind: String, Codable, Sendable {
        case over, out

        var rank: Int { self == .out ? 1 : 0 }
    }

    public let provider: String
    /// The account's label.
    public let label: String
    public let window: String
    public let kind: Kind
    /// When the window resets, which ends the period the notice is about.
    public let resetsAt: Date?
    /// When a window that runs out does: the report's forecast, or its
    /// reset when it runs out then.
    public let runsOutAt: Date?
    /// How long the window is, which tells its periods apart.
    public let length: TimeInterval?
    /// "Claude mira · 5h is out".
    public let title: String
    /// "Back Thu 17:05 (in 1h 25m)".
    public let body: String

    public init(provider: String, label: String, window: String, kind: Kind, resetsAt: Date?, runsOutAt: Date? = nil,
                length: TimeInterval? = nil, title: String = "", body: String = "") {
        (self.provider, self.label, self.window, self.kind) = (provider, label, window, kind)
        (self.resetsAt, self.runsOutAt, self.length, self.title, self.body) = (resetsAt, runsOutAt, length, title, body)
    }

    /// The window: "claude/mira@studio.dev/5h".
    public var key: String { "\(provider)/\(label)/\(window)" }
    /// The account, which groups the notices about its windows.
    public var thread: String { "\(provider)/\(label)" }

    /// The notices the report's attention calls for, of those the settings
    /// ask for: each window that is out or runs out before its reset, in
    /// the report's order, of the subscriptions logged in on this Mac or of
    /// the whole team. A team's notice says which device uses the account
    /// most, as Limits does.
    public static func notices(_ r: Report, _ settings: NoticeSettings = NoticeSettings(), now: Date,
                               timeZone: TimeZone = .current) -> [WindowNotice] {
        let clock = { (t: Date) in Format.clock(t, now: now, timeZone: timeZone) }
        return r.attention.compactMap { a -> WindowNotice? in
            let kind: Kind
            switch a.kind {
            case .out: kind = .out
            case .over: kind = .over
            default: return nil
            }
            guard let provider = a.provider, let label = a.account,
                  let account = r.teamAccount(provider: provider, label: label),
                  account.current || settings.scope == .team else { return nil }
            // The report names the window only when it is not the main one.
            let name = a.window ?? account.quota?.main?.name ?? ""
            let window = account.quota?.windows.first { $0.name == name }
            let subject = Format.provider(provider) + " " + (a.name ?? label) + (name.isEmpty ? "" : " · " + name)
            let title: String
            var parts: [String] = []
            let resetsAt: Date?, runsOutAt: Date?
            if kind == .out {
                (resetsAt, runsOutAt) = (a.at, nil)
                title = subject + " is out"
                parts.append(a.at.map { "Back \(clock($0)) (in \(Format.duration($0.timeIntervalSince(now))))" } ?? "Back at its reset")
            } else {
                (resetsAt, runsOutAt) = (a.resetsAt, a.at ?? a.resetsAt)
                if let at = a.at, let reset = a.resetsAt, reset.timeIntervalSince(at) >= 60 {
                    title = subject + (at > now ? " runs out ~" : " ran out ~") + clock(at)
                    parts.append("\(Format.duration(reset.timeIntervalSince(at))) before its reset · resets \(clock(reset))")
                } else {
                    title = subject + " runs out at its reset"
                    if let reset = a.resetsAt {
                        parts.append("Resets \(clock(reset))")
                    }
                }
            }
            if settings.scope == .team, let busiest = account.busiest {
                parts.append("used on " + (account.users > 1 ? "\(busiest) +\(account.users - 1)" : busiest))
            }
            if let age = a.readingAgeSeconds {
                parts.append("reading \(Format.duration(TimeInterval(age))) old")
            }
            let n = WindowNotice(provider: provider, label: label, window: name, kind: kind, resetsAt: resetsAt, runsOutAt: runsOutAt,
                                 length: window?.length.map { TimeInterval($0 * 60) }, title: title, body: parts.joined(separator: " · "))
            return settings.wants(n, now: now) ? n : nil
        }
    }
}

/// What was said of each window, so that it is said once a period and
/// state: once when it runs out before its reset, once more when it is out,
/// and again only after it resets. The app keeps it in its settings, so a
/// restart says nothing twice.
public struct NoticeLedger: Codable, Equatable, Sendable {
    public struct Said: Codable, Equatable, Sendable {
        public var kind: WindowNotice.Kind
        /// The reset of the period it was said in.
        public var resetsAt: Date?
    }

    /// By window key.
    public private(set) var said: [String: Said] = [:]

    public init() {}

    /// Records the notices a report calls for, and returns those not said
    /// yet. A window is forgotten once it resets, or, with no reset time,
    /// once the report no longer calls for it. So a forecast that drops
    /// under 100% and rises again is not said twice before the reset.
    public mutating func record(_ notices: [WindowNotice], now: Date) -> [WindowNotice] {
        let called = Set(notices.map(\.key))
        said = said.filter { key, s in s.resetsAt.map { $0 > now } ?? called.contains(key) }
        var due: [WindowNotice] = []
        for n in notices {
            // A reset that has passed is an old reading's: the window is
            // back, or will be at the next reading.
            if let r = n.resetsAt, r <= now { continue }
            if let s = said[n.key], Self.samePeriod(s.resetsAt, n), s.kind.rank >= n.kind.rank { continue }
            said[n.key] = Said(kind: n.kind, resetsAt: n.resetsAt)
            due.append(n)
        }
        return due
    }

    /// Whether a notice is of the period a reset was said in. The next
    /// period resets a whole length of the window later, and readings of
    /// one period can differ by seconds, so a reset less than half the
    /// window's length later is the same period; without the length, one
    /// within a minute, as the report matches the readings of a period. A
    /// provider that resets a window early starts a period too.
    static func samePeriod(_ said: Date?, _ n: WindowNotice) -> Bool {
        guard let said, let r = n.resetsAt else { return true }
        return r.timeIntervalSince(said) < max((n.length ?? 0) / 2, 60)
    }
}
