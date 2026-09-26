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
}

private func tokens(_ n: Int) -> Text {
    Text(n > 0 ? Format.tokens(n) : "–").foregroundColor(n > 0 ? .primary : Color(nsColor: .tertiaryLabelColor))
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
                .help("Tokens in and out, or each device's part of each subscription's tokens")
                Picker("Period", selection: $store.period) {
                    ForEach(Period.allCases) { Text($0.title).tag($0) }
                }
                .labelsHidden()
                .fixedSize()
                Spacer(minLength: 8)
                Caption(text: store.share ? "Each device's share of a subscription" : "Tokens in + out")
            }
            .controlSize(.small)
            MatrixView(report: report, period: store.period, share: store.share)
        }
    }
}

private struct MatrixView: View {
    let report: Report
    let period: Period
    let share: Bool

    /// A wide table scrolls sideways.
    var body: some View {
        ScrollView(.horizontal) {
            table.padding(.bottom, 2)
        }
    }

    /// The narrowest a column is, wide enough for any cell: the heads and
    /// the totals size the columns, and each cell fills its column.
    private static let cellWidth: CGFloat = 52

    private var table: some View {
        let m = report.team.matrix
        let groups = columnGroups(m.columns)
        let top = m.rows.flatMap { $0.cells.map(value) }.max() ?? 0
        return Grid(alignment: .trailing, horizontalSpacing: 6, verticalSpacing: 3) {
            GridRow {
                Color.clear.gridCellUnsizedAxes([.horizontal, .vertical])
                ForEach(groups, id: \.start) { g in
                    VStack(spacing: 3) {
                        Text(g.title).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                        Rectangle().fill(.separator).frame(height: 1)
                    }
                    .gridCellUnsizedAxes(.horizontal)
                    .gridCellColumns(g.count)
                }
                Color.clear.gridCellUnsizedAxes([.horizontal, .vertical])
            }
            GridRow {
                Text("Device")
                    .foregroundStyle(.secondary)
                    .padding(.leading, 20)
                    .gridColumnAlignment(.leading)
                ForEach(m.columns.indices, id: \.self) { i in
                    columnHead(m.columns[i])
                }
                Text("Total").foregroundStyle(.secondary)
            }
            .font(.caption)
            ForEach(m.rows, id: \.deviceId) { r in
                GridRow {
                    HStack(spacing: 4) {
                        DeviceMark(device: report.team.devices.first { $0.device == r.deviceId })
                        Text(r.device).lineLimit(1).frame(maxWidth: 110, alignment: .leading)
                    }
                    .fixedSize()
                    ForEach(m.columns.indices, id: \.self) { i in
                        let v = i < r.cells.count ? value(r.cells[i]) : 0
                        Text(v > 0 ? text(r.cells[i]) : "–")
                            .foregroundStyle(v > 0 ? .primary : .tertiary)
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .frame(maxWidth: .infinity, alignment: .trailing)
                            .background(RoundedRectangle(cornerRadius: 4).fill(Color.primary.opacity(heat(v, top: top))))
                            .gridCellUnsizedAxes(.horizontal)
                    }
                    Text(total(r))
                        .fontWeight(.medium)
                        .frame(minWidth: 44, alignment: .trailing)
                        .fixedSize()
                }
            }
            Divider().gridCellUnsizedAxes(.horizontal)
            GridRow {
                Text("Total").foregroundStyle(.secondary).padding(.leading, 20)
                ForEach(m.columns.indices, id: \.self) { i in
                    Text(columnTotal(m.columns[i].usage[period]))
                        .padding(.horizontal, 5)
                        .frame(minWidth: Self.cellWidth, alignment: .trailing)
                        .fixedSize()
                }
                Text(columnTotal(m.rows.reduce(0) { $0 + $1.usage[period] }))
                    .fontWeight(.medium)
                    .fixedSize()
            }
        }
        .font(.callout.monospacedDigit())
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
        share ? Format.share(cell.share[period] ?? 0) : Format.tokens(cell.usage[period])
    }

    private func total(_ r: Matrix.Row) -> String {
        if share {
            return (r.share[period] ?? 0) > 0 ? Format.share(r.share[period]!) : "–"
        }
        return r.usage[period] > 0 ? Format.tokens(r.usage[period]) : "–"
    }

    /// A column's total; in shares, all of it when it has any tokens.
    private func columnTotal(_ tokens: Int) -> String {
        guard tokens > 0 else { return "–" }
        return share ? "100%" : Format.tokens(tokens)
    }

    /// The tint of a cell, a neutral gray, a step darker for every half of
    /// a tenfold nearer to the largest cell.
    private func heat(_ v: Double, top: Double) -> Double {
        guard v > 0, top > 0 else { return 0 }
        let step = min(max(5 - Int((log10(top / v) * 2).rounded(.down)), 1), 5)
        return [0.05, 0.08, 0.12, 0.16, 0.22][step - 1]
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
                            tokens(d.usage[p]).font(.callout.monospacedDigit())
                        }
                    }
                    if let note = note(d) {
                        GridRow {
                            Text(note.text)
                                .font(.caption)
                                .foregroundColor(note.color)
                                .lineLimit(2)
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
                        tokens(devices.reduce(0) { $0 + $1.usage[p] }).fontWeight(.medium)
                    }
                }
                .font(.callout.monospacedDigit())
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
        return (["\(devices.count) devices"] + counts.filter { $0.0 > 0 }.map { "\($0.0) \($0.1)" } + ["tokens in + out"])
            .joined(separator: " · ")
    }

    /// The release, marked when it is older than the team's newest.
    private func version(_ d: TeamDevice) -> Text {
        d.old ? Text(d.collectorVersion + " outdated").foregroundColor(Palette.text("over")) : Text(d.collectorVersion)
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
        if d.old { lines.append("Runs an older release than \(report.team.latestVersion ?? "the team's newest"); it updates itself") }
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
    /// why its release check fails.
    private func note(_ d: TeamDevice) -> (text: String, color: Color)? {
        if d.silent, let e = d.error { return ("Last error: \(e)", .secondary) }
        if let e = d.error { return (e, Palette.text("out")) }
        if let e = d.updateError { return ("Update: \(e)", d.old ? Palette.text("out") : .secondary) }
        return nil
    }
}

/// A single device's tokens on each of its accounts.
struct SoloUsageView: View {
    let report: Report

    var body: some View {
        let providers = report.providers.filter { !$0.accounts.isEmpty }
        VStack(alignment: .leading, spacing: 10) {
            Caption(text: "Tokens in + out on \(report.collector.deviceLabel)")
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
                                tokens(a.usage[period])
                            }
                        }
                    }
                }
                if let d = report.thisDevice {
                    Divider().gridCellUnsizedAxes(.horizontal)
                    GridRow {
                        Text("Total").foregroundStyle(.secondary)
                        ForEach(Period.allCases) { p in
                            tokens(d.usage[p]).fontWeight(.medium)
                        }
                    }
                }
            }
            .font(.callout.monospacedDigit())
        }
    }
}
