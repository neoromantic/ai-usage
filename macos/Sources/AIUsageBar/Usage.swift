import AIUsageKit
import SwiftUI

/// Where the tokens go in a period: by the team's devices or by its
/// subscriptions, or with this Mac alone, by its accounts.
struct UsageView: View {
    @EnvironmentObject private var store: Store
    let report: Report
    let now: Date

    var body: some View {
        if report.solo {
            SoloUsage(report: report, now: now)
        } else {
            // Both lists at once, the hidden one clear, so that switching
            // between them keeps the popover's height.
            ZStack(alignment: .top) {
                shown(.devices, DeviceList(report: report, now: now))
                shown(.subscriptions, ColumnList(report: report))
            }
        }
    }

    private func shown(_ mode: UsageMode, _ view: some View) -> some View {
        // A chip's filter shows the devices whichever list was chosen.
        let on = store.deviceFilter != nil ? mode == .devices : store.usageMode == mode
        return view
            .opacity(on ? 1 : 0)
            .allowsHitTesting(on)
            .disabled(!on)
            .accessibilityHidden(!on)
    }
}

/// The line over Usage: what it lists, or the filter a chip set, and the
/// period.
struct UsageTopLine: View {
    @EnvironmentObject private var store: Store
    let report: Report

    var body: some View {
        TopLine {
            if report.solo {
                EmptyView()
            } else if let f = store.deviceFilter {
                Text(f.title)
                    .font(.callout.weight(.medium))
                    .lineLimit(1)
                Button("Show All") { store.deviceFilter = nil }
                    .buttonStyle(.link)
                    .font(.callout)
            } else {
                OptionMenu(title: "Show", options: UsageMode.allCases, selection: $store.usageMode, name: \.menuTitle) {
                    Text(store.usageMode.menuTitle)
                }
            }
        }
    }
}

/// The total under Usage, which stays put as the list scrolls.
struct UsageTotal: View {
    @EnvironmentObject private var store: Store
    let report: Report

    var body: some View {
        let p = store.period
        if report.solo {
            TotalRow(title: "Total", tokens: SoloUsage.accounts(report).reduce(0) { $0 + $1.account.usage[p] })
        } else if let f = store.deviceFilter {
            let all = report.team.matrix.rows
            TotalRow(title: "Showing \(all.filter { f.devices.contains($0.device) }.count) of \(all.count)", tokens: nil)
        } else if store.usageMode == .subscriptions {
            TotalRow(title: "Total", tokens: report.team.matrix.columns.reduce(0) { $0 + $1.usage[p] })
        } else {
            TotalRow(title: "Total", tokens: report.team.matrix.rows.reduce(0) { $0 + $1.usage[p] })
        }
    }
}

// MARK: - Devices

/// How a device is, as the mark before its name shows it, the worst first.
/// A device on an older release that updates itself is fine: that is lag
/// after a release, and its details name the release.
private enum DeviceStatus {
    case silent, error, notUpdating, thisMac, fine

    init(_ d: TeamDevice?) {
        guard let d else { self = .fine; return }
        // A silent device's error is the one it last reported, as the
        // report's attention has it, not what fails on it now.
        if d.silent { self = .silent } else if d.error != nil { self = .error } else if d.notUpdating { self = .notUpdating } else if d.thisDevice { self = .thisMac } else { self = .fine }
    }

    var spoken: String? {
        switch self {
        case .silent: return "not reporting"
        case .error: return "error"
        case .notUpdating: return "not updating"
        case .thisMac: return "this Mac"
        case .fine: return nil
        }
    }
}

private struct DeviceMark: View {
    let status: DeviceStatus

    var body: some View {
        ZStack {
            switch status {
            case .error: symbol("xmark.circle.fill", Palette.text("out"))
            case .silent: symbol("antenna.radiowaves.left.and.right.slash", Palette.text("tight"))
            case .notUpdating: symbol("arrow.up.circle.fill", Palette.text("tight"))
            case .thisMac: ThisMacMark()
            case .fine: EmptyView()
            }
        }
        .frame(width: Metrics.slot, height: 16, alignment: .leading)
        .accessibilityHidden(true)
    }

    private func symbol(_ name: String, _ color: Color) -> some View {
        Image(systemName: name)
            .symbolRenderingMode(.monochrome)
            .font(.system(size: 11, weight: .semibold))
            .foregroundStyle(color)
    }
}

/// The team's devices, as the report orders them, with their tokens in the
/// period against the busiest.
private struct DeviceList: View {
    @EnvironmentObject private var store: Store
    let report: Report
    let now: Date

    var body: some View {
        let p = store.period
        let all = report.team.matrix.rows
        let rows = store.deviceFilter.map { f in all.filter { f.devices.contains($0.device) } } ?? all
        let top = all.map { $0.usage[p] }.max() ?? 0
        VStack(alignment: .leading, spacing: 0) {
            ForEach(rows, id: \.deviceId) { r in
                DeviceRow(row: r, device: report.team.devices.first { $0.device == r.deviceId }, top: top, report: report, now: now)
            }
        }
    }
}

private struct DeviceRow: View {
    @EnvironmentObject private var store: Store
    let row: Matrix.Row
    let device: TeamDevice?
    let top: Int
    let report: Report
    let now: Date

    private var status: DeviceStatus { DeviceStatus(device) }

    var body: some View {
        let p = store.period
        DisclosureRow(key: "dev:\(row.deviceId)", spoken: spoken) {
            VStack(alignment: .leading, spacing: 0) {
                HStack(spacing: 0) {
                    DeviceMark(status: status)
                    Text(row.device)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(width: Metrics.name, alignment: .leading)
                    TokenBar(value: row.usage[p], top: top)
                        .padding(.horizontal, Metrics.gap)
                    TokensValue(tokens: row.usage[p])
                }
                .frame(height: Metrics.row)
                if let note {
                    Text(note.text)
                        .font(.subheadline)
                        .foregroundStyle(note.color)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .help(note.help)
                        .frame(height: Metrics.subLine, alignment: .top)
                        .padding(.leading, Metrics.slot)
                        .padding(.top, -5)
                        .padding(.bottom, 4)
                }
            }
        } details: {
            if let d = device {
                details(d)
            }
        }
    }

    /// What is wrong with the device, in a line under it.
    private var note: (text: String, color: Color, help: String)? {
        guard let d = device else { return nil }
        let all = [d.error, d.lastError.map { "Last error: \($0)" }, d.updateError.map { "Update: \($0)" }].compactMap { $0 }
        switch status {
        case .silent:
            let ago = d.collectedAt.map { Format.duration(now.timeIntervalSince($0)) }
            return (ago.map { "No report for \($0)" } ?? "Never reported", Palette.text("tight"), all.joined(separator: "\n"))
        case .error:
            return (d.error!, Palette.text("out"), all.joined(separator: "\n"))
        case .notUpdating:
            let since = d.behindSince.map { " for \(Format.duration(now.timeIntervalSince($0)))" } ?? ""
            let latest = report.team.latestVersion.map { " · latest \($0)" } ?? ""
            return ("Not updating\(since)\(latest)", Palette.text("tight"), all.joined(separator: "\n"))
        default:
            guard let e = d.updateError else { return nil }
            return ("Update: \(e)", .secondary, e)
        }
    }

    /// When it last reported, on which release, its tools, which
    /// subscriptions it used in the period, and its tokens in each period.
    private func details(_ d: TeamDevice) -> some View {
        let tools = d.sources.filter { $0.status != "skipped" }
        return DetailGrid {
            DetailLine("Reported", reported(d), help: "\(d.label) as \(d.osUser)" + (d.thisDevice ? ", this Mac" : ""))
            if let ok = d.lastSuccessAt, d.collectedAt.map({ $0.timeIntervalSince(ok) >= 60 }) ?? true {
                DetailLine("Last success", Format.ago(ok, now: now))
            }
            if !tools.isEmpty {
                DetailLine("Tools", toolsText(tools))
            }
            // The error the line under the row says already is not said again.
            ForEach(Array(tools.filter { $0.error != nil && !said($0, d) }.enumerated()), id: \.offset) { _, s in
                DetailLine(Format.provider(s.provider), Text(s.error!).foregroundColor(Palette.text("out")), help: s.error!)
            }
            if let used = accounts {
                DetailLine("Accounts", used.text, help: used.help)
            }
            PeriodsLine(usage: d.usage)
        }
    }

    /// "11m ago · v0.2.2 · latest v0.3.1", with "not updating" in the
    /// tight color when it does not update itself.
    private func reported(_ d: TeamDevice) -> Text {
        var s = (d.collectedAt.map { Format.ago($0, now: now) } ?? "Never") + " · \(d.collectorVersion)"
        if d.old, let latest = report.team.latestVersion {
            s += " · latest \(latest)"
        }
        return d.notUpdating ? Text(s + " · ") + Text("not updating").foregroundColor(Palette.text("tight")) : Text(s)
    }

    /// Each tool with its state: "✓ Claude   ✕ Codex".
    private func toolsText(_ tools: [TeamDevice.Source]) -> Text {
        tools.enumerated().reduce(Text("")) { text, e in
            let s = e.element
            let mark: Text
            switch s.status {
            case "ok": mark = Text(Image(systemName: "checkmark.circle")).foregroundColor(.secondary)
            case "partial": mark = Text(Image(systemName: "circle.lefthalf.filled")).foregroundColor(Palette.text("over"))
            default: mark = Text(Image(systemName: "xmark.circle.fill")).foregroundColor(Palette.text("out"))
            }
            return text + Text(e.offset == 0 ? "" : "   ") + mark + Text(" " + Format.provider(s.provider))
        }
    }

    /// Whether the line under the row says a tool's error.
    private func said(_ s: TeamDevice.Source, _ d: TeamDevice) -> Bool {
        guard note != nil, let e = s.error, let shown = d.error else { return false }
        return shown == e || shown.lowercased() == "\(s.provider): \(e)".lowercased()
    }

    /// The subscriptions the device used in the period, busiest first:
    /// "Claude leo 85M, Codex leo 37M"; the tooltip has its part of the team.
    private var accounts: (text: String, help: String)? {
        let p = store.period
        let cols = report.team.matrix.columns
        let items = row.cells.enumerated().compactMap { i, c -> (String, Int)? in
            guard i < cols.count, c.usage[p] > 0 else { return nil }
            return (columnName(cols[i]), c.usage[p])
        }.sorted { $0.1 > $1.1 }.map { "\($0.0) \(Format.tokensM($0.1))" }
        guard !items.isEmpty else { return nil }
        let share = row.share[p].map { "\(p.title): \(Format.share($0))% of the team" } ?? "\(p.title):"
        return (items.prefix(2).joined(separator: ", ") + (items.count > 2 ? " +\(items.count - 2)" : ""),
                ([share] + items).joined(separator: "\n"))
    }

    private var spoken: String {
        let p = store.period
        let share = row.share[p].map { ", \(Format.share($0))% of the team" } ?? ""
        return [row.device, status.spoken].compactMap { $0 }.joined(separator: ", ")
            + ", \(Format.spokenMillions(row.usage[p])) tokens in \(p.title.lowercased())" + share
            + (note.map { ", \($0.text)" } ?? "")
    }
}

/// A subscription's name, as the matrix has it: "Claude mira", "Hermes (no quota)".
private func columnName(_ c: Matrix.Column) -> String {
    c.noQuota ? "\(Format.provider(c.provider)) (no quota)" : "\(Format.provider(c.provider)) \(c.name)"
}

// MARK: - Subscriptions

/// The team's subscriptions, as the report orders its matrix's columns,
/// with their tokens in the period against the busiest.
private struct ColumnList: View {
    @EnvironmentObject private var store: Store
    let report: Report

    var body: some View {
        let p = store.period
        let m = report.team.matrix
        let top = m.columns.map { $0.usage[p] }.max() ?? 0
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(m.columns.enumerated()), id: \.offset) { i, c in
                ColumnRow(index: i, column: c, matrix: m, top: top)
            }
        }
    }
}

private struct ColumnRow: View {
    @EnvironmentObject private var store: Store
    let index: Int
    let column: Matrix.Column
    let matrix: Matrix
    let top: Int

    var body: some View {
        let p = store.period
        let c = column
        DisclosureRow(key: "col:\(index)", spoken: spoken) {
            HStack(spacing: 0) {
                ZStack {
                    if !c.noQuota {
                        StateGlyph(state: c.state)
                    }
                }
                .frame(width: Metrics.slot, alignment: .leading)
                (Text(Format.provider(c.provider) + " ").foregroundColor(.secondary)
                    + Text(c.noQuota ? "" : c.name).foregroundColor(.primary)
                    + Text(c.noQuota ? "no quota" : "").foregroundColor(Color(nsColor: .tertiaryLabelColor)))
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .frame(width: Metrics.name, alignment: .leading)
                    .help(c.label ?? c.name)
                TokenBar(value: c.usage[p], top: top)
                    .padding(.horizontal, Metrics.gap)
                TokensValue(tokens: c.usage[p])
            }
            .frame(height: Metrics.row)
        } details: {
            let items = split
            DetailGrid {
                ForEach(Array(items.prefix(4).enumerated()), id: \.offset) { _, s in
                    DetailLine(s.device, s.value)
                }
                if items.count > 4 {
                    DetailLine("", "+\(items.count - 4) more", help: items.dropFirst(4).map { "\($0.device) \($0.value)" }.joined(separator: "\n"))
                }
                if items.isEmpty {
                    DetailLine("Devices", "none in \(p.title.lowercased())")
                }
            }
        }
    }

    /// Each device's part of the column: "build-01", "141M · 44%".
    private var split: [(device: String, value: String)] {
        let p = store.period
        return matrix.rows.compactMap { r in
            guard index < r.cells.count, r.cells[index].usage[p] > 0 else { return nil }
            let cell = r.cells[index]
            return (r.device, "\(Format.tokensM(cell.usage[p])) · \(Format.share(cell.share[p]))%")
        }
    }

    private var spoken: String {
        let p = store.period
        let c = column
        let state = !c.noQuota && Palette.loud(c.state) ? ", \(Palette.label(c.state).lowercased())" : ""
        return columnName(c) + state + ", \(Format.spokenMillions(c.usage[p])) tokens in \(p.title.lowercased())"
    }
}

// MARK: - Solo

/// This Mac's accounts in one list, named as the team's subscriptions are,
/// each with two weeks of tokens as a line against the busiest of them.
struct SoloUsage: View {
    @EnvironmentObject private var store: Store
    let report: Report
    let now: Date

    /// The accounts with tokens in 90 days, by provider.
    static func accounts(_ r: Report) -> [(id: String, provider: String, account: Account)] {
        r.providers.flatMap { p in p.accounts.filter { $0.usage.quarter > 0 }.map { ("\(p.provider)/\($0.label)", p.provider, $0) } }
    }

    var body: some View {
        let rows = Self.accounts(report)
        let top = rows.map { Format.sparkline($0.account.days).max() ?? 0 }.max() ?? 0
        VStack(alignment: .leading, spacing: 0) {
            if rows.isEmpty {
                Text("No tokens on this Mac yet.")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity)
                    .padding(.top, 24)
            } else {
                ForEach(rows, id: \.id) { r in
                    SoloRow(provider: r.provider, account: r.account, top: top, report: report, now: now)
                }
            }
        }
    }
}

private struct SoloRow: View {
    @EnvironmentObject private var store: Store
    let provider: String
    let account: Account
    let top: Int
    let report: Report
    let now: Date

    var body: some View {
        let a = account
        let p = store.period
        DisclosureRow(key: "solo:\(provider)/\(a.label)", spoken: spoken) {
            HStack(spacing: 0) {
                Color.clear.frame(width: Metrics.slot, height: 1)
                (Text(Format.provider(provider) + " ").foregroundColor(.secondary) + Text(a.name).foregroundColor(.primary))
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .frame(width: Metrics.name, alignment: .leading)
                    .help(a.label)
                ZStack {
                    if !a.days.isEmpty {
                        Sparkline(points: Format.sparkline(a.days), top: top)
                            .help("Tokens a day, the last 14 days")
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(.horizontal, Metrics.gap)
                TokensValue(tokens: a.usage[p])
            }
            .frame(height: Metrics.row)
        } details: {
            DetailGrid {
                DetailLine("Sessions", "\(a.sessions) in 90 days" + (a.lastActiveAt.map { " · last \(Format.ago($0, now: now))" } ?? ""))
                DetailLine("Plan", a.plan.map(Format.plan))
                ForEach(Array((report.teamAccount(provider: provider, label: a.label)?.linkedUsage ?? []).enumerated()), id: \.offset) { _, l in
                    DetailLine(Format.provider(l.provider), "\(l.sessions) sessions through it")
                }
                PeriodsLine(usage: a.usage)
            }
        }
    }

    private var spoken: String {
        let p = store.period
        return "\(Format.provider(provider)) \(account.name), \(Format.spokenMillions(account.usage[p])) tokens in \(p.title.lowercased())"
    }
}
