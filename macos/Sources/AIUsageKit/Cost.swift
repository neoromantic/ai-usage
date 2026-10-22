import Foundation

/// What the team pays for its subscriptions a month, how much of it the
/// team used over the last 4 weeks, and who used it, as `cost` in the
/// report: a budget, not accounting, in dollars. The app shows it;
/// ai-usage works out every number, question, and event. Only the fields
/// the app shows are here.
public struct Cost: Decodable, Sendable {
    /// The price a month of every subscription that has not ended and has
    /// a price; withoutPrice counts those that have none.
    public let totalUsd: Double
    public let withoutPrice: Int
    /// What the team used of the subscriptions whose Used is known, that
    /// over their prices, and the rest; nil when none is known.
    public let usedUsd: Double?
    public let usedPct: Double?
    public let unusedUsd: Double?
    public let usedEstimated: Bool
    /// The soonest charge a person confirmed.
    public let nextCharge: Charge?
    public let prices: Prices
    /// By provider, those no device has any more after the others, and
    /// those that ended last.
    public let subscriptions: [CostSubscription]
    /// What each device used, the most first.
    public let devices: [Device]
    /// What this Mac asks a person.
    public let questions: [Question]
    /// What happened in the last 30 days, oldest first.
    public let events: [Event]

    public struct Charge: Decodable, Sendable {
        public let provider: String
        public let account: String
        public let at: Date
        /// Its price: a year's for a yearly subscription.
        public let usd: Double?
    }

    /// The price list in use: the day it was last checked, and where it
    /// came from, `built in` or `relay`.
    public struct Prices: Decodable, Sendable {
        public let updated: String
        public let source: String
    }

    public struct Device: Decodable, Sendable {
        public let device: String
        public let deviceId: String
        /// Nil when no price of the subscriptions it used is known.
        public let usd: Double?
    }

    public struct Question: Decodable, Sendable {
        /// The same for as long as the question stands.
        public let id: String
        /// `renewal`, `price`, or `still_paid`.
        public let kind: String
        public let provider: String
        public let account: String
        public let text: String
        /// For a renewal, a YYYY-MM-DD day.
        public let suggestion: String?
        /// `monthly` and `yearly`, for a renewal of a plan the list prices
        /// yearly too.
        public let choices: [String]
    }

    public struct Event: Decodable, Sendable {
        /// The same in every report, so that it is told once.
        public let id: String
        /// `new`, `plan_changed`, `price_changed`, `ended`,
        /// `charges_soon`, or `unconfirmed`.
        public let kind: String
        public let at: Date
        public let provider: String
        public let account: String
        public let text: String
    }

    public func subscription(provider: String, label: String) -> CostSubscription? {
        subscriptions.first { $0.provider == provider && $0.account == label }
    }
}

/// One subscription's cost.
public struct CostSubscription: Decodable, Sendable {
    public let provider: String
    /// Its label.
    public let account: String
    /// As on its team account; one no device has any more goes by its
    /// short name.
    public let name: String
    public let alias: String?
    /// Logged in on this Mac.
    public let current: Bool
    public let plan: String?
    /// The plan as the price list calls it, as `Max 20x`.
    public let planName: String?
    /// Its price a month; nil when unknown.
    public let priceUsd: Double?
    /// `list`, `typed`, `stale` (the list's, as a person typed one for
    /// typedFor), or `unknown`.
    public let priceSource: String
    public let typedFor: String?
    /// `monthly` or `yearly`.
    public let cadence: String
    /// Its next charge, which a person gave; nil when nobody did.
    public let renewsAt: Date?
    public let usedUsd: Double?
    public let usedPct: Double?
    /// usedUsd is its whole price split by tokens, as no week of its Used
    /// is known.
    public let usedEstimated: Bool
    public let unusedUsd: Double?
    /// `active`, `idle`, `unconfirmed`, or `ended`.
    public let state: String
    public let idleSince: Date?
    public let endedAt: Date?
    public let planConfirmedAt: Date?
    /// `App Store` or `Google Play`, where the price may differ.
    public let billedThrough: String?
    /// The devices that used it, the most first.
    public let consumers: [Consumer]
    public let usdPerMWeighted: Double?

    public struct Consumer: Decodable, Sendable {
        public let device: String
        public let deviceId: String
        /// Its part of usedUsd; nil when the price is unknown.
        public let usd: Double?
        /// Its part of the weighted tokens, in percent.
        public let share: Double
    }

    /// The account as the CLI takes it: `claude:ann@acme.dev`.
    public var target: String { provider + ":" + account }

    /// "Claude ann".
    public var title: String { Format.provider(provider) + " " + name }

    /// The plan as the price list names it, else as its tool reports it.
    public var planTitle: String? { planName ?? plan.map(Format.plan) }

    public var ended: Bool { state == "ended" }

    /// Its price a month, "$200/mo", "?" when unknown.
    public var priceText: String {
        priceUsd.map { Format.usd($0) + "/mo" } ?? "?"
    }

    /// Where the price comes from, in words: "list price", "typed",
    /// "list price; typed for Plus", "no price".
    public var priceWords: String {
        switch priceSource {
        case "typed": return "typed"
        case "stale": return "list price; typed for " + (typedFor.map(Format.plan) ?? "another plan")
        case "unknown": return "no price"
        default: return priceUsd == 0 ? "free" : "list price"
        }
    }

    /// How full its main window got over the last 4 weeks: "used 91%",
    /// or what an estimate split, "used ~$300"; nil when unknown.
    public var usedText: String? {
        if let p = usedPct { return "used \(Int(p.rounded()))%" }
        if usedEstimated, let u = usedUsd { return "used ~" + Format.usd(u) }
        return nil
    }

    /// The state when it is not active, as the console's COST says it:
    /// "idle 23d", "still paid?", "ended".
    public func stateText(now: Date) -> String? {
        switch state {
        case "idle": return "idle" + (idleSince.map { " " + Format.age($0, now: now) } ?? "")
        case "unconfirmed": return "still paid?"
        case "ended": return "ended"
        default: return nil
        }
    }

    /// Its cost in a line, as Limits' details show it: "$200/mo · used
    /// 91% · next charge Fri 25 Sep · idle 23d".
    public func line(now: Date, timeZone: TimeZone = .current) -> String {
        let price = priceUsd.map { Format.usd($0) + "/mo" + (priceSource == "typed" ? " typed" : "") } ?? "no price"
        let next = renewsAt.map { "next charge " + Format.day($0, timeZone: timeZone) }
        return ([price, usedText, next, stateText(now: now)] as [String?]).compactMap { $0 }.joined(separator: " · ")
    }

    /// The same in sentences, for its tooltip.
    public func help(now: Date, timeZone: TimeZone = .current) -> String {
        var lines: [String] = []
        let plan = planTitle ?? "its plan"
        switch (priceUsd, priceSource) {
        case (nil, _): lines.append("No price: the price list has none for \(plan); type one in Settings, Cost")
        case (let p?, "typed"): lines.append("\(Format.usd(p)) a month, as a person typed it" + (cadence == "yearly" ? ", billed yearly" : ""))
        case (let p?, "stale"): lines.append("\(Format.usd(p)) a month, the list price of \(plan): the typed price was for "
            + (typedFor.map(Format.plan) ?? "another plan"))
        case (let p?, _): lines.append("\(Format.usd(p)) a month, the list price of \(plan)" + (cadence == "yearly" ? ", billed yearly" : ""))
        }
        if let u = usedUsd, let p = usedPct {
            lines.append("Used \(Format.usd(u)) of it over the last 4 weeks: its main window got \(Int(p.rounded()))% full")
        } else if usedEstimated, let u = usedUsd {
            lines.append("Used ~\(Format.usd(u)) over the last 4 weeks, an estimate: no week of its main window is known")
        }
        lines.append(renewsAt.map { "Next charge \(Format.day($0, timeZone: timeZone))" } ?? "Nobody gave its next charge")
        if let m = usdPerMWeighted {
            lines.append("\(m < 1 ? String(format: "$%.2f", m) : Format.usd(m)) a month for a million of the team's weighted tokens on it")
        }
        if let b = billedThrough {
            lines.append("Billed through \(b); the price may differ from the list's")
        }
        switch state {
        case "idle": lines.append("Paid and unused" + (idleSince.map { " since \(Format.day($0, timeZone: timeZone))" } ?? ""))
        case "unconfirmed": lines.append("Nobody can say it is still paid" + (planConfirmedAt.map { "; last confirmed \(Format.day($0, timeZone: timeZone))" } ?? ""))
        case "ended": lines.append("Ended" + (endedAt.map { " \(Format.day($0, timeZone: timeZone))" } ?? "") + "; in no total")
        default: break
        }
        return lines.joined(separator: "\n")
    }
}

extension Report {
    public func costSubscription(provider: String, label: String) -> CostSubscription? {
        cost?.subscription(provider: provider, label: label)
    }
}

extension Format {
    /// Dollars as the console prints them: "$1,000", "<$1" under a dollar,
    /// "$0".
    public static func usd(_ v: Double) -> String {
        if v <= 0 { return "$0" }
        if v < 1 { return "<$1" }
        let f = NumberFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.numberStyle = .decimal
        f.usesGroupingSeparator = true
        f.groupingSeparator = ","
        f.groupingSize = 3
        f.maximumFractionDigits = 0
        f.roundingMode = .halfEven
        return "$" + (f.string(from: NSNumber(value: v)) ?? String(Int(v.rounded())))
    }

    /// Dollars in a table's cell: "?" when unknown, the none mark for none,
    /// "~" before an estimate.
    public static func dollars(_ v: Double?, estimated: Bool = false) -> String {
        guard let v else { return "?" }
        if v <= 0 { return none }
        return (estimated ? "~" : "") + usd(v)
    }

    /// A day as the console's NEXT has it: "Fri 25 Sep".
    public static func day(_ date: Date, timeZone: TimeZone = .current) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "EEE d MMM"
        return f.string(from: date)
    }
}

/// The popover's money: the team's total a month, which ends the header,
/// as it ends the console's.
public struct CostTotal: Equatable, Sendable {
    /// "$1,000/mo".
    public let text: String
    /// "The team's subscriptions cost $1,000 a month, +1 without a price ·
    /// used ~$787 (79%) over the last 4 weeks · next charge Fri 25 Sep,
    /// $200".
    public let help: String

    /// Nil when no subscription has a price.
    public init?(_ r: Report, timeZone: TimeZone = .current) {
        guard let c = r.cost, c.totalUsd > 0 else { return nil }
        text = Format.usd(c.totalUsd) + "/mo"
        var parts = [(r.solo ? "The subscriptions cost " : "The team's subscriptions cost ") + Format.usd(c.totalUsd) + " a month"
            + (c.withoutPrice > 0 ? ", +\(c.withoutPrice) without a price" : "")]
        if let u = c.usedUsd, let p = c.usedPct {
            parts.append("used \(c.usedEstimated ? "~" : "")\(Format.usd(u)) (\(Int(p.rounded()))%) over the last 4 weeks")
        }
        if let n = c.nextCharge {
            parts.append("next charge \(Format.day(n.at, timeZone: timeZone))" + (n.usd.map { ", " + Format.usd($0) } ?? ""))
        }
        help = parts.joined(separator: " · ")
    }
}

/// The Team window's Cost: each device against each subscription that has
/// not ended, in dollars used over the last 4 weeks, as the console's `$`
/// mode of DEVICES × SUBSCRIPTIONS has them, with what nobody used and each
/// one's price under them. Its numbers are the report's cost; it only lays
/// them out, and sorts the rows when asked.
public struct CostMatrix: Sendable {
    public let groups: [TeamMatrix.Group]
    public let columns: [TeamMatrix.Column]
    /// The devices, in the report's order.
    public let lines: [TeamMatrix.Line]
    /// What nobody used of each column, and of every subscription.
    public let unused: [TeamMatrix.Cell]
    public let unusedTotal: TeamMatrix.Cell
    /// Each column's price a month, and the team's total.
    public let totals: [TeamMatrix.Cell]
    public let total: TeamMatrix.Cell
    public let unit = "$ used, unused, and price a month"

    /// Nil for a report without a cost.
    public init?(_ r: Report) {
        guard let c = r.cost else { return nil }
        let subs = c.subscriptions.filter { !$0.ended }
        columns = subs.map { s in
            TeamMatrix.Column(id: "\(s.provider)/\(s.account)", provider: s.provider, title: s.name, name: s.title, state: nil,
                              help: ([s.account, s.planTitle, s.priceText + " " + s.priceWords, s.stateText(now: r.generatedAt)] as [String?])
                                  .compactMap { $0 }.joined(separator: " · "),
                              noQuota: false)
        }
        var groups: [TeamMatrix.Group] = []
        for (i, s) in subs.enumerated() {
            let title = Format.provider(s.provider)
            if let last = groups.last, last.title == title {
                groups[groups.count - 1] = TeamMatrix.Group(title: title, columns: last.columns.lowerBound..<i + 1)
            } else {
                groups.append(TeamMatrix.Group(title: title, columns: i..<i + 1))
            }
        }
        self.groups = groups
        let cols = columns
        let used = { (s: CostSubscription, id: String) in s.consumers.first { $0.deviceId == id } }
        let top = subs.flatMap { $0.consumers.compactMap(\.usd) }.max() ?? 0
        lines = r.team.matrix.rows.map { row in
            let cells = subs.enumerated().map { i, s -> TeamMatrix.Cell in
                guard let u = used(s, row.deviceId) else {
                    return TeamMatrix.Cell(text: Format.none, value: 0, heat: 0, help: "\(row.device) · \(cols[i].name): none",
                                           spoken: "\(cols[i].name): none")
                }
                let text = Format.dollars(u.usd, estimated: s.usedEstimated)
                let of = s.priceUsd.map { " of " + Format.usd($0) + "/mo" } ?? ""
                let share = "\(Format.share(u.share))% of its tokens"
                return TeamMatrix.Cell(text: text, value: u.usd ?? 0, heat: TeamMatrix.heatStep(u.usd ?? 0, top: top),
                                       help: "\(row.device) · \(cols[i].name): \(text)\(of) · \(share)" + (s.usedEstimated ? "\n~ An estimate: no week of its main window is known" : ""),
                                       spoken: "\(cols[i].name): \(text == "?" ? "unknown" : text), \(share)")
            }
            // A device's total is an estimate when one of its parts is.
            let estimated = subs.contains { $0.usedEstimated && used($0, row.deviceId) != nil }
            let d = c.devices.first { $0.deviceId == row.deviceId }
            let text = d.map { Format.dollars($0.usd, estimated: estimated) } ?? Format.none
            return TeamMatrix.Line(device: row.device, deviceId: row.deviceId, cells: cells,
                                   total: TeamMatrix.Cell(text: text, value: d?.usd ?? 0, heat: 0,
                                                          help: "\(row.device): \(text == Format.none ? "none" : text) over the last 4 weeks",
                                                          spoken: "Total: \(text == Format.none ? "none" : text)"))
        }
        unused = subs.enumerated().map { i, s in
            let text = s.unusedUsd.map { Format.dollars($0, estimated: s.usedEstimated) } ?? (s.priceUsd == nil ? "?" : Format.none)
            return TeamMatrix.Cell(text: text, value: s.unusedUsd ?? 0, heat: 0, help: "\(cols[i].name): \(text) unused",
                                   spoken: "Unused: \(text == Format.none ? "none" : text)")
        }
        let unusedText = Format.dollars(c.unusedUsd, estimated: c.usedEstimated)
        unusedTotal = TeamMatrix.Cell(text: unusedText, value: c.unusedUsd ?? 0, heat: 0, help: "Nobody used \(unusedText) over the last 4 weeks",
                                      spoken: "Unused in all: \(unusedText)")
        totals = subs.enumerated().map { i, s in
            let text = s.priceUsd.map { Format.dollars($0) } ?? "?"
            return TeamMatrix.Cell(text: text, value: s.priceUsd ?? 0, heat: 0, help: "\(cols[i].name): \(s.priceText), \(s.priceWords)",
                                   spoken: "Price: \(text == "?" ? "unknown" : text + " a month")")
        }
        let totalText = Format.usd(c.totalUsd) + (c.withoutPrice > 0 ? " +?" : "")
        total = TeamMatrix.Cell(text: totalText, value: c.totalUsd, heat: 0,
                                help: "The team's subscriptions: \(Format.usd(c.totalUsd)) a month"
                                    + (c.withoutPrice > 0 ? ", +\(c.withoutPrice) without a price" : ""),
                                spoken: "Team total: \(Format.usd(c.totalUsd)) a month")
    }

    /// The rows sorted by a column, ties in the report's order; nil keeps
    /// the report's order.
    public func lines(sortedBy key: TeamMatrix.SortKey?, ascending: Bool) -> [TeamMatrix.Line] {
        guard let key else { return lines }
        return TeamMatrix.sorted(lines, ascending: ascending) { a, b in
            switch key {
            case .device: return a.device.localizedStandardCompare(b.device)
            case .column(let i):
                return TeamMatrix.compare(i < a.cells.count ? a.cells[i].value : 0, i < b.cells.count ? b.cells[i].value : 0)
            case .total: return TeamMatrix.compare(a.total.value, b.total.value)
            }
        }
    }
}

/// The `ai-usage cost` commands the app runs to answer: it stores and works
/// out nothing itself.
public enum CostCommand {
    public static func confirm(_ s: String) -> [String] { ["cost", "confirm", s] }
    public static func cancel(_ s: String) -> [String] { ["cost", "cancel", s] }
    public static func reset(_ s: String) -> [String] { ["cost", "reset", s] }
    public static func skip(_ s: String, _ kind: String) -> [String] { ["cost", "skip", s, kind] }

    /// `cost set` with what was given; nil with nothing.
    public static func set(_ s: String, price: String? = nil, cadence: String? = nil, renews: String? = nil) -> [String]? {
        var args = ["cost", "set", s]
        if let price { args += ["--price", price] }
        if let cadence { args.append("--" + cadence) }
        if let renews { args += ["--renews", renews] }
        return args.count > 3 ? args : nil
    }

    /// The command for an answer typed to a renewal or price question, as
    /// in a notification's field: a day, "2026-10-04" or "4", or dollars,
    /// "$20", and "monthly" or "yearly" among the words. Nil when it says
    /// nothing the question asks; ai-usage checks the rest.
    public static func answer(kind: String, target: String, text: String) -> [String]? {
        var cadence: String?, value: String?
        for word in text.lowercased().split(whereSeparator: { $0 == " " || $0 == "," || $0 == "\n" }) {
            switch word {
            case "monthly", "yearly": cadence = String(word)
            default: value = value ?? String(word)
            }
        }
        switch kind {
        case "price":
            guard let v = value.map({ $0.hasPrefix("$") ? String($0.dropFirst()) : $0 }), Double(v) != nil else { return nil }
            return set(target, price: v, cadence: cadence)
        case "renewal":
            return set(target, cadence: cadence, renews: value)
        default:
            return nil
        }
    }
}
