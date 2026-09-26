import Foundation

/// How the app prints the report's numbers and times.
public enum Format {
    /// Tokens in three significant figures at most: 480K, 12.3M, 130M, 1.7B.
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

    /// A share in whole percents: "<1" under one, ">99" short of all of it.
    public static func share(_ v: Double) -> String {
        switch v {
        case ..<1: return "<1%"
        case 99..<100: return ">99%"
        default: return "\(Int(v.rounded()))%"
        }
    }

    /// A path under home with "~".
    public static func tilde(_ path: String, home: String) -> String {
        guard !home.isEmpty, path == home || path.hasPrefix(home + "/") else { return path }
        return "~" + path.dropFirst(home.count)
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
