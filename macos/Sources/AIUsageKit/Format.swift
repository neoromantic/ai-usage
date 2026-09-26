import Foundation

/// How the app prints the report's numbers and times.
public enum Format {
    /// What a table's cell shows for nothing.
    public static let none = "–"

    /// Tokens in whole millions, as the console's tables print them, with
    /// the unit in the table's caption: 603, 1210, "<1" under a million, and
    /// the none mark for none. Cells of one size line up and compare.
    public static func millions(_ n: Int) -> String {
        switch n {
        case ...0: return none
        case ..<1_000_000: return "<1"
        default: return String(Int((Double(n) / 1e6).rounded()))
        }
    }

    /// The same for VoiceOver: "104 million", "under a million", "none".
    public static func spokenMillions(_ n: Int) -> String {
        switch n {
        case ...0: return "none"
        case ..<1_000_000: return "under a million"
        default: return millions(n) + " million"
        }
    }

    /// Tokens in three significant figures at most, for a sentence such as
    /// a tooltip's: 480K, 12.3M, 130M, 1.7B.
    public static func tokens(_ n: Int) -> String {
        if n < 1000 { return String(max(n, 0)) }
        var value = Double(n)
        for unit in ["K", "M", "B", "T"] {
            value /= 1000
            let tenths = (value * 10).rounded() / 10
            if tenths < 100 {
                let s = String(format: "%.1f", tenths)
                return (s.hasSuffix(".0") ? String(s.dropLast(2)) : s) + unit
            }
            if value.rounded() < 1000 || unit == "T" {
                return String(Int(value.rounded())) + unit
            }
        }
        return String(n)
    }

    /// A duration in at most two units: 34m, 7h 5m, 1d 23h, 12d.
    public static func duration(_ seconds: TimeInterval) -> String {
        guard seconds >= 60 else { return "<1m" }
        let m = Int(seconds / 60)
        let (days, hours, mins) = (m / 1440, m / 60 % 24, m % 60)
        switch (days, hours, mins) {
        case (1..., 1..., _): return "\(days)d \(hours)h"
        case (1..., _, _): return "\(days)d"
        case (_, 1..., 1...): return "\(hours)h \(mins)m"
        case (_, 1..., _): return "\(hours)h"
        default: return "\(mins)m"
        }
    }

    /// How long ago in its largest unit, as the console's LAST column has
    /// it: "now", "7m", "3h", "2d".
    public static func age(_ date: Date, now: Date) -> String {
        let m = Int(now.timeIntervalSince(date) / 60)
        switch m {
        case ..<1: return "now"
        case ..<60: return "\(m)m"
        case ..<1440: return "\(m / 60)h"
        default: return "\(m / 1440)d"
        }
    }

    /// How long ago: "just now", "7m ago", "1d 5h ago".
    public static func ago(_ date: Date, now: Date) -> String {
        let d = now.timeIntervalSince(date)
        return d < 60 ? "just now" : duration(d) + " ago"
    }

    /// A time within six days as a weekday and a 24-hour clock, "Sun 01:00";
    /// else as a date, "2 Oct 15:04".
    public static func clock(_ date: Date, now: Date, timeZone: TimeZone = .current) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = abs(date.timeIntervalSince(now)) < 6 * 86400 ? "EEE HH:mm" : "d MMM HH:mm"
        return f.string(from: date)
    }

    /// What is left of a window: a whole percent, "~" before it for a stale
    /// reading, "?" when how full it is now is not known.
    public static func left(_ w: Quota.Window?) -> String {
        guard let w, w.known else { return "?" }
        let s = "\(percentLeft(w))%"
        return w.stale ? "~" + s : s
    }

    public static func percentLeft(_ w: Quota.Window) -> Int {
        max(100 - Int(w.percent.rounded(.down)), 0)
    }

    /// A share in whole percents, as the console prints it, with "%" in the
    /// table's caption: "<1" under one, ">99" short of all of it, and the
    /// none mark for none.
    public static func share(_ v: Double?) -> String {
        guard let v, v > 0 else { return none }
        if v < 1 { return "<1" }
        if v > 99 && v < 100 { return ">99" }
        return String(Int(v.rounded()))
    }

    /// A path under home with "~".
    public static func tilde(_ path: String, home: String) -> String {
        guard !home.isEmpty, path == home || path.hasPrefix(home + "/") else { return path }
        return "~" + path.dropFirst(home.count)
    }

    /// A project's path as its name and the folder it is in, with "~" for
    /// home: "~/src/orbit/web" is "web" in "~/src/orbit". A path of one
    /// part, such as "~" or "unknown", is its own name, in no folder.
    public static func pathParts(_ path: String, home: String) -> (name: String, folder: String) {
        var p = tilde(path, home: home)
        while p.count > 1 && p.hasSuffix("/") {
            p.removeLast()
        }
        guard p.count > 1, let slash = p.lastIndex(of: "/") else { return (p, "") }
        let folder = p[..<slash]
        return (String(p[p.index(after: slash)...]), folder.isEmpty ? "/" : String(folder))
    }

    /// The provider as a person names it: "Claude", "Codex".
    public static func provider(_ id: String) -> String {
        id.prefix(1).uppercased() + id.dropFirst()
    }
}

extension Report {
    /// A report of one device: its USAGE takes the place of the team's.
    public var solo: Bool { team.devices.count <= 1 }

    public var thisDevice: TeamDevice? { team.devices.first(where: \.thisDevice) }

    public func teamAccount(provider: String, label: String) -> TeamAccount? {
        team.providers.first { $0.provider == provider }?.accounts.first { $0.label == label }
    }

    public func teamDevice(named label: String) -> TeamDevice? {
        team.devices.first { $0.label == label }
    }
}

extension TeamAccount {
    /// How full the account is now is known for a window that limits it:
    /// the main one, or another that limits it more. Without one there is
    /// nothing to show of it but why: it was never read, or each such
    /// window has reset since its reading or is not in it.
    public var quotaKnown: Bool {
        quota?.windows.contains { ($0.main || $0.limits) && $0.known } ?? false
    }
}

/// What the menu bar shows: the window with the least left among the
/// subscriptions logged in on this device, of the windows that limit each
/// one, since any full one stops work. Those are the windows the report
/// marks with `limits`, or with a report that does not mark them, the main
/// ones.
public struct MenuSummary: Equatable, Sendable {
    public let provider: String
    public let label: String
    /// The account's short name, as the team sees it.
    public let name: String
    public let window: String
    public let percentLeft: Int
    public let used: Double
    public let stale: Bool

    public init(provider: String, label: String, name: String, window: String, percentLeft: Int, used: Double, stale: Bool) {
        (self.provider, self.label, self.name, self.window) = (provider, label, name, window)
        (self.percentLeft, self.used, self.stale) = (percentLeft, used, stale)
    }

    public static func pick(_ r: Report) -> MenuSummary? {
        var best: MenuSummary?
        for p in r.team.providers {
            for a in p.accounts where a.subscription && a.current {
                for w in a.quota?.windows ?? [] where w.known && (w.limits || w.main) {
                    let s = MenuSummary(provider: p.provider, label: a.label, name: a.name, window: w.name,
                                        percentLeft: Format.percentLeft(w), used: w.percent, stale: w.stale)
                    if best.map({ s.percentLeft < $0.percentLeft }) ?? true {
                        best = s
                    }
                }
            }
        }
        return best
    }

    /// The percent left as the menu bar shows it: "42%", "~42%" for a stale
    /// reading.
    public var text: String { (stale ? "~" : "") + "\(percentLeft)%" }

    /// What VoiceOver says: "Claude mira, 5h window: 0% left".
    public var spoken: String {
        "\(Format.provider(provider)) \(name), \(window) window: \(percentLeft)% left" + (stale ? ", from an old reading" : "")
    }

    /// The gauge symbol whose needle is nearest to how much is used.
    public static func symbol(used: Double?) -> String {
        let steps = [0, 33, 50, 67, 100]
        let u = min(max(used ?? 0, 0), 100)
        let step = steps.min { abs(Double($0) - u) < abs(Double($1) - u) }!
        return "gauge.with.dots.needle.\(step)percent"
    }
}
