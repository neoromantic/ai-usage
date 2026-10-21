import Foundation

/// The Team window's matrix: each device of the team against each
/// subscription, and the tokens with no quota, as the console's DEVICES ×
/// SUBSCRIPTIONS has them, in a period, as tokens or as each device's share
/// of its column. Its rows, columns, order, and numbers are the report's
/// matrix; it only lays them out, and sorts the rows when asked.
public struct TeamMatrix: Sendable {
    /// What the cells show, as the console's `%` switches it.
    public enum Mode: String, CaseIterable, Identifiable, Sendable {
        /// Each device's tokens on each column.
        case tokens
        /// Each device's part of each column's tokens, and of the team's.
        case share

        public var id: Self { self }
        public var title: String { self == .tokens ? "Tokens" : "Share" }
    }

    /// The columns of a provider's subscriptions, or of the tokens with no
    /// quota, side by side under one heading.
    public struct Group: Equatable, Sendable {
        /// "Claude", or "No Quota".
        public let title: String
        /// Its columns' places in `columns`.
        public let columns: Range<Int>
    }

    public struct Column: Sendable {
        /// The column across reports: "claude/mira@studio.dev", or
        /// "hermes/" for Hermes' tokens with no quota.
        public let id: String
        public let provider: String
        /// What its heading says under its group's: the account's short
        /// name, or the provider of the tokens with no quota.
        public let title: String
        /// The subscription as the app names it: "Claude mira", "Hermes
        /// (no quota)".
        public let name: String
        /// The subscription's state, which its heading's color says; nil
        /// for the tokens with no quota and for an unknown state.
        public let state: String?
        /// The account's label, or what the tokens with no quota are.
        public let help: String
        public let noQuota: Bool
    }

    /// A number of the matrix, as it reads and as it sorts.
    public struct Cell: Equatable, Sendable {
        /// "153", "<1", "44", the none mark; "~" before an estimate.
        public let text: String
        /// Its tokens, or its share in percent: what it sorts and shades by.
        public let value: Double
        /// Its step of the heat map, from 1 to 5, as the console's: a step
        /// for every half of a tenfold below the largest cell. 0 for none,
        /// and for totals.
        public let heat: Int
        /// The tooltip: "build-01 · Codex bots: 131M in 7 Days · 46% of
        /// column · 87M this window".
        public let help: String
        /// The same for VoiceOver.
        public let spoken: String
    }

    /// A device's row.
    public struct Line: Sendable {
        public let device: String
        public let deviceId: String
        public let cells: [Cell]
        public let total: Cell
    }

    /// What the rows can be sorted by.
    public enum SortKey: Hashable, Sendable {
        case device
        /// A column, by its place in `columns`.
        case column(Int)
        case total
    }

    public let period: Period
    public let mode: Mode
    public let groups: [Group]
    public let columns: [Column]
    /// The devices, in the report's order.
    public let lines: [Line]
    /// TOTAL: each column's, and all of the team's.
    public let totals: [Cell]
    public let total: Cell
    /// What the numbers are, for the window's header: "M weighted tokens",
    /// "% of column total".
    public let unit: String

    public init(_ r: Report, period p: Period, mode: Mode) {
        let m = r.team.matrix
        (period, self.mode) = (p, mode)
        let label = r.tokenLabel
        let during = " in " + p.title
        let spokenDuring = " in " + p.title.lowercased()
        columns = m.columns.map { c in
            let provider = Format.provider(c.provider)
            return Column(id: "\(c.provider)/\(c.noQuota ? "" : c.label ?? c.name)", provider: c.provider,
                          title: c.noQuota ? provider : c.name, name: c.noQuota ? "\(provider) (no quota)" : "\(provider) \(c.name)",
                          state: c.noQuota || c.state == "unknown" ? nil : c.state,
                          help: c.noQuota ? "\(provider)'s tokens through no subscription" : c.label ?? c.name, noQuota: c.noQuota)
        }
        // The report puts each provider's subscriptions side by side, and
        // the columns with no quota after them.
        var groups: [Group] = []
        for (i, c) in m.columns.enumerated() {
            let title = c.noQuota ? "No Quota" : Format.provider(c.provider)
            if let last = groups.last, last.title == title {
                groups[groups.count - 1] = Group(title: title, columns: last.columns.lowerBound..<i + 1)
            } else {
                groups.append(Group(title: title, columns: i..<i + 1))
            }
        }
        self.groups = groups
        let value = { (c: Matrix.Cell) in mode == .share ? c.shownShare.value[p] ?? 0 : Double(c.shown.value[p]) }
        let top = m.rows.flatMap { $0.cells.map(value) }.max() ?? 0
        let estimated = { (s: String, on: Bool) in on && s != Format.none ? "~" + s : s }
        let cols = columns
        lines = m.rows.map { row in
            let cells = cols.indices.map { i -> Cell in
                guard i < row.cells.count else {
                    return Cell(text: Format.none, value: 0, heat: 0, help: "", spoken: "\(cols[i].name): none")
                }
                let c = row.cells[i], col = m.columns[i], tokens = c.shown.value[p], share = c.shownShare.value[p]
                var help = [tokens > 0 ? Format.tokensM(tokens) + during : "none" + during]
                var spoken = [Format.spokenMillions(tokens) + " \(c.shown.label)" + spokenDuring]
                if tokens > 0, let s = share {
                    help.append(Format.share(s) + "% of column")
                    spoken.append(Format.share(s) + "% of column")
                }
                if !col.noQuota, c.shownWindow.value > 0 {
                    help.append(Format.tokensM(c.shownWindow.value) + " this window")
                    spoken.append(Format.spokenMillions(c.shownWindow.value) + " this window")
                }
                let v = value(c)
                return Cell(text: mode == .share ? Format.share(share) : Format.millions(tokens), value: v, heat: Self.heatStep(v, top: top),
                            help: "\(row.device) · \(cols[i].name): " + help.joined(separator: " · "),
                            spoken: "\(cols[i].name): " + spoken.joined(separator: ", "))
            }
            let tokens = row.shown.value[p], share = row.shownShare.value[p]
            var help = "\(row.device): " + (tokens > 0 ? Format.tokensM(tokens) : "none") + during
                + (tokens > 0 ? share.map { " · \(Format.share($0))% of the team" } ?? "" : "")
            if row.burnEstimated {
                help += "\n~ " + (r.team.estimatedReason ?? "Estimated from input and output")
            }
            let total = Cell(text: estimated(mode == .share ? Format.share(share) : Format.millions(tokens), row.burnEstimated),
                             value: mode == .share ? share ?? 0 : Double(tokens), heat: 0, help: help,
                             spoken: "Total: \(Format.spokenMillions(tokens)) \(row.shown.label)" + spokenDuring
                                + (tokens > 0 ? share.map { ", \(Format.share($0))% of the team" } ?? "" : "")
                                + (row.burnEstimated ? ", estimated" : ""))
            return Line(device: row.device, deviceId: row.deviceId, cells: cells, total: total)
        }
        // A column's share of itself is all of it, where it has any tokens.
        let whole = { (tokens: Int) in mode == .share ? (tokens > 0 ? "100" : Format.none) : Format.millions(tokens) }
        totals = m.columns.enumerated().map { i, c in
            let tokens = c.shown.value[p]
            var help = ["\(cols[i].name): " + (tokens > 0 ? Format.tokensM(tokens) : "none") + during]
            if !c.noQuota, c.shownWindow.value > 0 {
                help.append(Format.tokensM(c.shownWindow.value) + " this window")
            }
            return Cell(text: whole(tokens), value: Double(tokens), heat: 0, help: help.joined(separator: " · "),
                        spoken: "Total: \(Format.spokenMillions(tokens)) \(c.shown.label)" + spokenDuring)
        }
        let all = m.rows.reduce(0) { $0 + $1.shown.value[p] }
        let someEstimated = m.rows.contains(where: \.burnEstimated)
        total = Cell(text: estimated(whole(all), someEstimated), value: Double(all), heat: 0,
                     help: "The team: " + (all > 0 ? Format.tokensM(all) : "none") + during
                        + (someEstimated ? "\n~ " + (r.team.estimatedReason ?? "Some devices' tokens are estimated") : ""),
                     spoken: "Team total: \(Format.spokenMillions(all)) \(label)" + spokenDuring + (someEstimated ? ", estimated" : ""))
        unit = mode == .share ? "% of column total" : "M " + label
    }

    /// The step of the heat map a value takes against the largest, from 1
    /// to 5: a step for every half of a tenfold below it, as the console's
    /// matrix shades its cells; 0 for none.
    public static func heatStep(_ v: Double, top: Double) -> Int {
        guard v > 0, top > 0 else { return 0 }
        return min(max(5 - Int((log10(top / v) * 2).rounded(.down)), 1), 5)
    }

    /// The rows sorted by a column, ties in the report's order; nil keeps
    /// the report's order.
    public func lines(sortedBy key: SortKey?, ascending: Bool) -> [Line] {
        guard let key else { return lines }
        return Self.sorted(lines, ascending: ascending) { a, b in
            switch key {
            case .device: return a.device.localizedStandardCompare(b.device)
            case .column(let i):
                return Self.compare(i < a.cells.count ? a.cells[i].value : 0, i < b.cells.count ? b.cells[i].value : 0)
            case .total: return Self.compare(a.total.value, b.total.value)
            }
        }
    }

    static func compare<T: Comparable>(_ a: T, _ b: T) -> ComparisonResult {
        a < b ? .orderedAscending : a > b ? .orderedDescending : .orderedSame
    }

    /// xs sorted by order, stably: equals keep their places.
    static func sorted<T>(_ xs: [T], ascending: Bool, by order: (T, T) -> ComparisonResult) -> [T] {
        xs.enumerated().sorted { x, y in
            let o = order(x.element, y.element)
            if o == .orderedSame { return x.offset < y.offset }
            return ascending == (o == .orderedAscending)
        }.map(\.element)
    }
}

/// The Team window's status: each device's release, last report, its
/// tools, tokens in each period, and the report's NOTE, a line each in the
/// matrix's order, as the console's status view has them. It takes each
/// from the report and only sorts the lines when asked.
public struct DeviceStatusTable: Sendable {
    public struct Tool: Equatable, Sendable {
        public let provider: String
        /// Its tool fails on the device, or reads only part of what it has.
        public let failing: Bool
        public let error: String?
    }

    public struct Line: Sendable {
        public let device: String
        public let deviceId: String
        /// The device as the team has it; nil for a row it does not have.
        public let team: TeamDevice?
        public let version: String
        /// It runs an older release than the latest.
        public let behind: Bool
        /// When it last reported.
        public let seen: Date?
        /// The tools it reads, those it skips left out.
        public let tools: [Tool]
        public let tokens: Measured<Usage>
        public let estimated: Bool
        /// The report's NOTE, in full.
        public let note: String?
    }

    public enum SortKey: Hashable, Sendable {
        case device, version, seen
        case tokens(Period)
    }

    public let lines: [Line]
    /// TOTAL: the team's tokens in each period.
    public let total: Usage
    public let estimated: Bool

    public init(_ r: Report) {
        lines = r.team.matrix.rows.map { row in
            let d = r.team.devices.first { $0.device == row.deviceId }
            return Line(device: row.device, deviceId: row.deviceId, team: d, version: d?.collectorVersion ?? "",
                        behind: d?.old ?? false, seen: d?.collectedAt,
                        tools: (d?.sources ?? []).filter { $0.status != "skipped" }.map {
                            Tool(provider: $0.provider, failing: $0.status == "error" || $0.status == "partial", error: $0.error)
                        },
                        tokens: row.shown, estimated: row.burnEstimated, note: d?.note.flatMap { $0.isEmpty ? nil : $0 })
        }
        let rows = r.team.matrix.rows
        total = Usage(today: rows.reduce(0) { $0 + $1.shown.value.today }, week: rows.reduce(0) { $0 + $1.shown.value.week },
                      month: rows.reduce(0) { $0 + $1.shown.value.month }, quarter: rows.reduce(0) { $0 + $1.shown.value.quarter })
        estimated = rows.contains(where: \.burnEstimated)
    }

    /// The lines sorted by a column, ties in the report's order; nil keeps
    /// the report's order. The oldest report sorts last by seen ascending.
    public func lines(sortedBy key: SortKey?, ascending: Bool) -> [Line] {
        guard let key else { return lines }
        return TeamMatrix.sorted(lines, ascending: ascending) { a, b in
            switch key {
            case .device: return a.device.localizedStandardCompare(b.device)
            case .version: return a.version.compare(b.version, options: .numeric)
            case .seen: return TeamMatrix.compare(b.seen ?? .distantPast, a.seen ?? .distantPast)
            case .tokens(let p): return TeamMatrix.compare(a.tokens.value[p], b.tokens.value[p])
            }
        }
    }
}

/// The Team window's header: how many devices the team has, how many fail
/// and are not reporting, as the console's status view counts them, the
/// period and the days it counts, UTC days but for a today that counts from
/// this Mac's midnight, and what the numbers are: "12 devices · 2 errors ·
/// 1 not reporting · 7 Days UTC · M weighted tokens".
public struct TeamHeader: Equatable, Sendable {
    public let devices: Int
    /// The devices that fail and still report.
    public let errors: Int
    /// The devices that stopped reporting; a silent device's error is the
    /// one it last reported.
    public let silent: Int
    public let period: String
    public let unit: String

    public init(_ r: Report, period: Period, unit: String) {
        let ds = r.team.devices
        devices = ds.count
        silent = ds.filter(\.silent).count
        errors = ds.filter { !$0.silent && $0.error != nil }.count
        self.period = period == .today && r.team.todayLocal ? period.title : period.title + " UTC"
        self.unit = unit
    }

    public var devicesText: String { devices == 1 ? "1 device" : "\(devices) devices" }
    public var errorsText: String? { errors == 0 ? nil : errors == 1 ? "1 error" : "\(errors) errors" }
    public var silentText: String? { silent == 0 ? nil : "\(silent) not reporting" }

    public var text: String {
        [devicesText, errorsText, silentText, period, unit].compactMap { $0 }.joined(separator: " · ")
    }
}
