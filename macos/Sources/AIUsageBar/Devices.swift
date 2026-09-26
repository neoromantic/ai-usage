import AIUsageKit
import SwiftUI

/// Before a device's name: this Mac, a silence, an error, or an old release.
private struct DeviceMark: View {
    let device: TeamDevice?

    var body: some View {
        ZStack {
            if let d = device {
                if d.thisDevice {
                    ThisMacMark()
                } else if d.silent {
                    Image(systemName: "moon.zzz.fill").foregroundStyle(Palette.text("tight")).help("No report for a day")
                        .accessibilityLabel("Silent")
                } else if let e = d.error {
                    Image(systemName: "xmark.circle.fill").foregroundStyle(Palette.text("out")).help(e)
                        .accessibilityLabel("Error")
                } else if d.old {
                    Image(systemName: "arrow.up.circle.fill").foregroundStyle(.secondary).help("Runs an older release")
                        .accessibilityLabel("Outdated")
                }
            }
        }
        .font(.caption)
        .frame(width: 16)
    }

    /// The mark in a word, for VoiceOver.
    static func spoken(_ d: TeamDevice?) -> String? {
        guard let d else { return nil }
        if d.thisDevice { return "this Mac" }
        if d.silent { return "silent" }
        if d.error != nil { return "error" }
        return d.old ? "outdated" : nil
    }
}

/// Each period's tokens, for VoiceOver: "today 17 million, 7 days 167 million".
private func spokenPeriods(_ usage: Usage) -> String {
    Period.allCases.map { "\($0.title.lowercased()) \(Format.spokenMillions(usage[$0]))" }.joined(separator: ", ")
}

/// The team's devices against its subscriptions: the tokens each device
/// spent through each in a period, or its share of each.
struct UsageMatrixView: View {
    @EnvironmentObject private var store: Store
    let report: Report

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Picker("Show", selection: $store.share) {
                    Text("Tokens").tag(false)
                    Text("Share").tag(true)
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .fixedSize()
                .help("Tokens in and out, or each device's part of each column's total")
                Picker("Period", selection: $store.period) {
                    ForEach(Period.allCases) { Text($0.title).tag($0) }
                }
                .labelsHidden()
                .fixedSize()
                Spacer(minLength: 8)
                Caption(text: store.share ? "% of column total" : "M tokens in + out")
            }
            .controlSize(.small)
            MatrixView(report: report, period: store.period, share: store.share)
        }
    }
}

/// The matrix, in whole millions or whole percents. The devices on the left
/// and the totals on the right stay in place; subscriptions that do not fit
/// between them scroll sideways. Every row has one height, so the three
/// parts line up.
private struct MatrixView: View {
    let report: Report
    let period: Period
    let share: Bool

    /// The narrowest a column is, wide enough for any cell: the heads and
    /// the totals size the columns, and each cell fills its column.
    private static let cellWidth: CGFloat = 40
    private static let groupHeight: CGFloat = 18
    private static let headHeight: CGFloat = 18
    private static let rowHeight: CGFloat = 20
    /// The room of the rule over the totals.
    private static let ruleHeight: CGFloat = 9

    var body: some View {
        let m = report.team.matrix
        // A table that fits spans the popover, the device column taking
        // the room left.
        ViewThatFits(in: .horizontal) {
            layout(m, scrolls: false)
            layout(m, scrolls: true)
        }
        .overlay(alignment: .top) {
            Rectangle()
                .fill(.separator)
                .frame(height: 1)
                .offset(y: Self.groupHeight + Self.headHeight + CGFloat(m.rows.count) * Self.rowHeight + Self.ruleHeight / 2)
        }
        .font(.callout.monospacedDigit())
        .accessibilityElement(children: .contain)
        .accessibilityLabel(share ? "Share of each column's total, \(period.title)" : "Tokens by device and subscription, \(period.title)")
        .accessibilityChildren {
            VStack {
                ForEach(m.rows, id: \.deviceId) { r in
                    Text(spoken(r, m))
                }
                Text(spokenTotals(m))
            }
        }
    }

    private func layout(_ m: Matrix, scrolls: Bool) -> some View {
        HStack(alignment: .top, spacing: 8) {
            deviceColumn(m)
                .fixedSize(horizontal: true, vertical: false)
                .frame(maxWidth: scrolls ? nil : .infinity, alignment: .leading)
            if scrolls {
                SideScroll { columns(m) }
            } else {
                columns(m)
            }
            totalColumn(m)
                .fixedSize(horizontal: true, vertical: false)
        }
    }

    private func device(_ r: Matrix.Row) -> TeamDevice? {
        report.team.devices.first { $0.device == r.deviceId }
    }

    private func deviceColumn(_ m: Matrix) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Color.clear.frame(width: 0, height: Self.groupHeight)
            Text("Device")
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.leading, 20)
                .frame(height: Self.headHeight)
            ForEach(m.rows, id: \.deviceId) { r in
                HStack(spacing: 4) {
                    DeviceMark(device: device(r))
                    Text(r.device).lineLimit(1).frame(maxWidth: 110, alignment: .leading)
                }
                .frame(height: Self.rowHeight)
            }
            Text("Total")
                .foregroundStyle(.secondary)
                .padding(.leading, 20)
                .frame(height: Self.rowHeight)
                .padding(.top, Self.ruleHeight)
        }
    }

    /// The subscriptions' columns under their providers' headings.
    private func columns(_ m: Matrix) -> some View {
        let groups = columnGroups(m.columns)
        let top = m.rows.flatMap { $0.cells.map(value) }.max() ?? 0
        return Grid(alignment: .trailing, horizontalSpacing: 6, verticalSpacing: 0) {
            GridRow {
                ForEach(groups, id: \.start) { g in
                    VStack(spacing: 3) {
                        Text(g.title).font(.caption.weight(.semibold)).foregroundStyle(.secondary).lineLimit(1)
                        Rectangle().fill(.separator).frame(height: 1)
                    }
                    .frame(height: Self.groupHeight, alignment: .bottom)
                    .gridCellUnsizedAxes(.horizontal)
                    .gridCellColumns(g.count)
                }
            }
            GridRow {
                ForEach(m.columns.indices, id: \.self) { i in
                    columnHead(m.columns[i]).frame(height: Self.headHeight)
                }
            }
            .font(.caption)
            ForEach(m.rows, id: \.deviceId) { r in
                GridRow {
                    ForEach(m.columns.indices, id: \.self) { i in
                        let v = i < r.cells.count ? value(r.cells[i]) : 0
                        Text(i < r.cells.count ? text(r.cells[i]) : Format.none)
                            .foregroundStyle(v > 0 ? .primary : .tertiary)
                            .padding(.horizontal, 5)
                            .frame(maxWidth: .infinity, alignment: .trailing)
                            .frame(height: Self.rowHeight - 3)
                            .background(RoundedRectangle(cornerRadius: 4).fill(Color.primary.opacity(heat(v, top: top))))
                            .frame(height: Self.rowHeight)
                            .gridCellUnsizedAxes(.horizontal)
                    }
                }
            }
            GridRow {
                ForEach(m.columns.indices, id: \.self) { i in
                    Text(columnTotal(m.columns[i].usage[period]))
                        .fontWeight(.medium)
                        .padding(.horizontal, 5)
                        .frame(minWidth: Self.cellWidth, alignment: .trailing)
                        .fixedSize()
                        .frame(height: Self.rowHeight)
                        .padding(.top, Self.ruleHeight)
                }
            }
        }
    }

    private func totalColumn(_ m: Matrix) -> some View {
        VStack(alignment: .trailing, spacing: 0) {
            Color.clear.frame(width: 0, height: Self.groupHeight)
            Text("Total")
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(height: Self.headHeight)
            ForEach(m.rows, id: \.deviceId) { r in
                Text(total(r))
                    .fontWeight(.medium)
                    .foregroundStyle(total(r) == Format.none ? .tertiary : .primary)
                    .frame(height: Self.rowHeight)
            }
            Text(columnTotal(m.rows.reduce(0) { $0 + $1.usage[period] }))
                .fontWeight(.medium)
                .frame(height: Self.rowHeight)
                .padding(.top, Self.ruleHeight)
        }
        .frame(minWidth: 36, alignment: .trailing)
    }

    private struct ColumnGroup {
        let title: String
        let start: Int
        let count: Int
    }

    /// Runs of columns under one provider, the no-quota columns under one.
    private func columnGroups(_ cols: [Matrix.Column]) -> [ColumnGroup] {
        var out: [ColumnGroup] = []
        for (i, c) in cols.enumerated() {
            let title = c.noQuota ? "No quota" : Format.provider(c.provider)
            if let last = out.last, last.title == title {
                out[out.count - 1] = ColumnGroup(title: title, start: last.start, count: last.count + 1)
            } else {
                out.append(ColumnGroup(title: title, start: i, count: 1))
            }
        }
        return out
    }

    /// A subscription's name, after a dot in its state's color when it is
    /// tight or worse.
    private func columnHead(_ c: Matrix.Column) -> some View {
        HStack(spacing: 3) {
            if !c.noQuota, let color = Palette.fill(c.state) {
                Circle().fill(color).frame(width: 5, height: 5)
            }
            Text(c.noQuota ? Format.provider(c.provider) : c.name).lineLimit(1)
        }
        .foregroundStyle(.secondary)
        .padding(.horizontal, 5)
        .frame(minWidth: Self.cellWidth, maxWidth: 80, alignment: .trailing)
        .fixedSize()
        .help(c.noQuota
            ? "\(Format.provider(c.provider)) tokens with no subscription, such as on an API key"
            : "\(c.label ?? c.name): \(c.percent.map { "\(Int($0))% used" } ?? "no reading"), \(Palette.label(c.state))")
    }

    private func value(_ cell: Matrix.Cell) -> Double {
        share ? cell.share[period] ?? 0 : Double(cell.usage[period])
    }

    private func text(_ cell: Matrix.Cell) -> String {
        share ? Format.share(cell.share[period]) : Format.millions(cell.usage[period])
    }

    private func total(_ r: Matrix.Row) -> String {
        share ? Format.share(r.share[period]) : Format.millions(r.usage[period])
    }

    /// A column's total; in shares, all of it when it has any tokens.
    private func columnTotal(_ tokens: Int) -> String {
        guard tokens > 0 else { return Format.none }
        return share ? "100" : Format.millions(tokens)
    }

    /// The tint of a cell, a neutral gray, a step darker for every half of
    /// a tenfold nearer to the largest cell.
    private func heat(_ v: Double, top: Double) -> Double {
        guard v > 0, top > 0 else { return 0 }
        let step = min(max(5 - Int((log10(top / v) * 2).rounded(.down)), 1), 5)
        return [0.05, 0.08, 0.12, 0.16, 0.22][step - 1]
    }

    // MARK: VoiceOver

    private func columnName(_ c: Matrix.Column) -> String {
        c.noQuota ? "\(Format.provider(c.provider)) with no quota" : "\(Format.provider(c.provider)) \(c.name)"
    }

    private func spoken(share v: Double?) -> String {
        guard let v, v > 0 else { return "none" }
        if v < 1 { return "under 1 percent" }
        if v > 99 && v < 100 { return "over 99 percent" }
        return "\(Int(v.rounded())) percent"
    }

    private func spoken(tokens: Int, share v: Double?) -> String {
        share ? spoken(share: v) : Format.spokenMillions(tokens)
    }

    /// A row as VoiceOver reads it: "build-01: Codex bots 141 million;
    /// total 167 million".
    private func spoken(_ r: Matrix.Row, _ m: Matrix) -> String {
        let name = [r.device, DeviceMark.spoken(device(r))].compactMap { $0 }.joined(separator: ", ")
        let cells = m.columns.indices.compactMap { i -> String? in
            guard i < r.cells.count, value(r.cells[i]) > 0 else { return nil }
            return "\(columnName(m.columns[i])) \(spoken(tokens: r.cells[i].usage[period], share: r.cells[i].share[period]))"
        }
        return "\(name): \(cells.isEmpty ? "none" : cells.joined(separator: ", ")); total \(spoken(tokens: r.usage[period], share: r.share[period]))"
    }

    private func spokenTotals(_ m: Matrix) -> String {
        let cells = m.columns.filter { $0.usage[period] > 0 }.map { "\(columnName($0)) \(Format.spokenMillions($0.usage[period]))" }
        let all = m.rows.reduce(0) { $0 + $1.usage[period] }
        return "Total: \(cells.isEmpty ? "none" : cells.joined(separator: ", ")); all \(Format.spokenMillions(all))"
    }
}

/// Each device, busiest in 7 days first as the matrix has them: its release,
/// when it reported, the tools it reads, its tokens, and what fails on it.
struct DeviceStatusView: View {
    let report: Report
    let now: Date

    private var devices: [TeamDevice] {
        let all = report.team.devices
        let ordered = report.team.matrix.rows.compactMap { r in all.first { $0.device == r.deviceId } }
        return ordered + all.filter { d in !ordered.contains { $0.device == d.device } }
    }

    var body: some View {
        let devices = self.devices
        VStack(alignment: .leading, spacing: 10) {
            Caption(text: summary(devices))
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 9) {
                GridRow {
                    Text("Device").padding(.leading, 20)
                    ForEach(Period.allCases) { p in
                        Text(p.title).gridColumnAlignment(.trailing)
                    }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
                ForEach(devices, id: \.device) { d in
                    GridRow {
                        VStack(alignment: .leading, spacing: 2) {
                            HStack(spacing: 4) {
                                DeviceMark(device: d)
                                Text(d.label).lineLimit(1).layoutPriority(1)
                                (Text(Image(systemName: "person")) + Text(" " + d.osUser))
                                    .font(.caption)
                                    .foregroundColor(.secondary)
                                    .lineLimit(1)
                                    .help("Collects as the user \(d.osUser)")
                            }
                            .truncationMode(.middle)
                            (version(d) + Text(" · ") + reported(d) + Text(" · ") + tools(d))
                                .font(.caption)
                                .foregroundColor(.secondary)
                                .lineLimit(2)
                                .padding(.leading, 20)
                                .help(help(d))
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        ForEach(Period.allCases) { p in
                            Millions(tokens: d.usage[p]).font(.callout.monospacedDigit())
                        }
                    }
                    if let note = note(d) {
                        GridRow {
                            // A short note, as the console's; the tooltip
                            // has all of it.
                            Text(note.text)
                                .font(.caption)
                                .foregroundColor(note.color)
                                .lineLimit(1)
                                .truncationMode(.tail)
                                .help(note.text)
                                .padding(.leading, 20)
                                .padding(.top, -5)
                                .gridCellColumns(5)
                        }
                    }
                }
                Divider().gridCellUnsizedAxes(.horizontal)
                GridRow {
                    Text("Total").foregroundStyle(.secondary).padding(.leading, 20)
                    ForEach(Period.allCases) { p in
                        Millions(tokens: devices.reduce(0) { $0 + $1.usage[p] }).fontWeight(.medium)
                    }
                }
                .font(.callout.monospacedDigit())
            }
            .accessibilityChildren {
                VStack {
                    ForEach(devices, id: \.device) { d in
                        Text(spoken(d))
                    }
                    Text("Total: " + DeviceStatusView.spokenTotal(devices))
                }
            }
        }
    }

    /// "12 devices · 1 error · 1 silent · 1 outdated", as the console counts
    /// them: a silent device's error is the one it last reported.
    private func summary(_ devices: [TeamDevice]) -> String {
        let errors = devices.filter { !$0.silent && $0.error != nil }.count
        let counts = [
            (errors, errors == 1 ? "error" : "errors"),
            (devices.filter(\.silent).count, "silent"),
            (devices.filter(\.old).count, "outdated"),
        ]
        return (["\(devices.count) devices"] + counts.filter { $0.0 > 0 }.map { "\($0.0) \($0.1)" } + ["M tokens in + out"])
            .joined(separator: " · ")
    }

    /// The release, marked when it is older than the team's newest. Dim, as
    /// the console has it, since such a device updates itself; in the
    /// tight color when it does not.
    private func version(_ d: TeamDevice) -> Text {
        guard d.old else { return Text(d.collectorVersion) }
        let outdated = Text("outdated")
        return Text(d.collectorVersion + " ") + (d.notUpdating ? outdated.foregroundColor(Palette.text("tight")) : outdated)
    }

    /// When the device last reported; in the silence's color when that was
    /// more than a day ago.
    private func reported(_ d: TeamDevice) -> Text {
        guard let at = d.collectedAt else { return Text("never reported") }
        return d.silent ? Text(Format.ago(at, now: now)).foregroundColor(Palette.text("tight")) : Text(Format.ago(at, now: now))
    }

    /// The release, the silence, and each tool's status in full.
    private func help(_ d: TeamDevice) -> String {
        var lines: [String] = []
        if d.old {
            let since = d.behindSince.map { ", at least since \(Format.clock($0, now: now))" } ?? ""
            lines.append("Runs an older release than \(report.team.latestVersion ?? "the team's newest")\(since); \(d.notUpdating ? "it has not updated itself" : "it updates itself")")
        }
        if d.silent, let at = d.collectedAt { lines.append("No report since \(Format.clock(at, now: now))") }
        for s in d.sources where s.status != "skipped" {
            lines.append("\(Format.provider(s.provider)): \(s.error ?? s.status)")
        }
        return lines.joined(separator: "\n")
    }

    /// The tools the device reads, as the console lists them: a mark only
    /// on the ones that fail, in part or in full.
    private func tools(_ d: TeamDevice) -> Text {
        let read = d.sources.filter { $0.status != "skipped" }
        guard !read.isEmpty else { return Text("no tools") }
        return read.enumerated().reduce(Text("")) { text, e in
            let s = e.element
            var item = Text(Format.provider(s.provider))
            switch s.status {
            case "ok": break
            case "partial": item = Text(Image(systemName: "circle.lefthalf.filled")).foregroundColor(Palette.text("over")) + Text(" ") + item.foregroundColor(.primary)
            default: item = Text(Image(systemName: "xmark.circle.fill")).foregroundColor(Palette.text("out")) + Text(" ") + item.foregroundColor(.primary)
            }
            return e.offset == 0 ? item : text + Text(", ") + item
        }
    }

    /// What is wrong with a device, as a short note: what fails on it, else
    /// why its release check fails, else how long it has not updated itself.
    private func note(_ d: TeamDevice) -> (text: String, color: Color)? {
        if d.silent, let e = d.error { return ("Last error: \(e)", .secondary) }
        if let e = d.error { return (e, Palette.text("out")) }
        if let e = d.updateError { return ("Update: \(e)", d.old ? Palette.text("out") : .secondary) }
        if d.notUpdating, let since = d.behindSince {
            let latest = report.team.latestVersion.map { " · latest \($0)" } ?? ""
            return ("Not updated for \(Format.duration(now.timeIntervalSince(since)))\(latest)", Palette.text("tight"))
        }
        return nil
    }

    /// A device as VoiceOver reads it: its name, state, release, when it
    /// reported, its tools, its tokens, and its note.
    private func spoken(_ d: TeamDevice) -> String {
        let tools = d.sources.filter { $0.status != "skipped" }.map { s in
            Format.provider(s.provider) + (s.status == "ok" ? "" : s.status == "partial" ? " failing in part" : " failing")
        }
        var parts = [[d.label, DeviceMark.spoken(d)].compactMap { $0 }.joined(separator: ", ")]
        parts.append((d.old ? "\(d.collectorVersion), outdated" : d.collectorVersion)
            + ", " + (d.collectedAt.map { "reported \(Format.ago($0, now: now))" } ?? "never reported")
            + ", " + (tools.isEmpty ? "no tools" : tools.joined(separator: ", ")))
        parts.append("tokens \(spokenPeriods(d.usage))")
        if let n = note(d) { parts.append(n.text) }
        return parts.joined(separator: "; ")
    }

    private static func spokenTotal(_ devices: [TeamDevice]) -> String {
        Period.allCases.map { p in "\(p.title.lowercased()) \(Format.spokenMillions(devices.reduce(0) { $0 + $1.usage[p] }))" }
            .joined(separator: ", ")
    }
}

/// A single device's tokens on each of its accounts.
struct SoloUsageView: View {
    let report: Report

    var body: some View {
        let providers = report.providers.filter { !$0.accounts.isEmpty }
        VStack(alignment: .leading, spacing: 10) {
            Caption(text: "M tokens in + out on \(report.collector.deviceLabel)")
            Grid(alignment: .trailing, horizontalSpacing: 14, verticalSpacing: 6) {
                GridRow {
                    Text("Account").gridColumnAlignment(.leading)
                    ForEach(Period.allCases) { Text($0.title) }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
                ForEach(providers, id: \.provider) { p in
                    GridRow {
                        GroupHeading(title: Format.provider(p.provider))
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.top, 6)
                            .gridCellColumns(5)
                    }
                    ForEach(p.accounts, id: \.label) { a in
                        GridRow {
                            Text(a.name)
                                .lineLimit(1)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .help(a.label)
                            ForEach(Period.allCases) { period in
                                Millions(tokens: a.usage[period])
                            }
                        }
                    }
                }
                if let d = report.thisDevice {
                    Divider().gridCellUnsizedAxes(.horizontal)
                    GridRow {
                        Text("Total").foregroundStyle(.secondary)
                        ForEach(Period.allCases) { p in
                            Millions(tokens: d.usage[p]).fontWeight(.medium)
                        }
                    }
                }
            }
            .font(.callout.monospacedDigit())
            .accessibilityChildren {
                VStack {
                    ForEach(providers, id: \.provider) { p in
                        ForEach(p.accounts, id: \.label) { a in
                            Text("\(Format.provider(p.provider)) \(a.name): \(spokenPeriods(a.usage))")
                        }
                    }
                    if let d = report.thisDevice {
                        Text("Total: \(spokenPeriods(d.usage))")
                    }
                }
            }
        }
    }
}
