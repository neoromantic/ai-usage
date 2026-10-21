import AIUsageKit
import AppKit
import SwiftUI

/// What the Team window shows, kept while the app runs.
@MainActor
final class TeamOptions: ObservableObject {
    enum Segment: String, CaseIterable, Identifiable {
        case usage, status

        var id: Self { self }
        var title: String { rawValue.capitalized }
    }

    @Published var segment = Segment.usage
    @Published var period = Period.week
    @Published var mode = TeamMatrix.Mode.tokens
    /// How each table is sorted; none keeps the report's order.
    @Published var usageSort: TeamSort?
    @Published var statusSort: TeamSort?

    /// The console's keys: 1, 7, 3, and 9 pick the period, % switches the
    /// matrix between tokens and shares, and s between usage and status.
    /// Says whether it took the key.
    func press(_ key: String) -> Bool {
        switch key {
        case "1": period = .today
        case "7": period = .week
        case "3": period = .month
        case "9": period = .quarter
        case "%" where segment == .usage: mode = mode == .tokens ? .share : .tokens
        case "s": segment = segment == .usage ? .status : .usage
        default: return false
        }
        return true
    }
}

/// The Team window: the team's devices against its subscriptions, or each
/// device's status, in a period, as the console's DEVICES has them, in a
/// window of its own that opens where it was last, at its size.
@MainActor
enum TeamWindow {
    private static var window: NSWindow?
    private static var keys: Any?
    static let options = TeamOptions()
    private static let frameName = "TeamWindow"

    static func show() {
        if window == nil {
            let host = NSHostingController(rootView: TeamView().environmentObject(Store.shared).environmentObject(options))
            // The window is the size the person makes it.
            host.sizingOptions = []
            let w = NSWindow(contentViewController: host)
            w.title = "Team"
            w.styleMask = [.titled, .closable, .miniaturizable, .resizable]
            w.isReleasedWhenClosed = false
            w.contentMinSize = NSSize(width: 640, height: 300)
            w.setContentSize(NSSize(width: 980, height: 500))
            if !w.setFrameUsingName(frameName) {
                w.center()
            }
            w.setFrameAutosaveName(frameName)
            AppWindows.register(w)
            keys = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { e in
                MainActor.assumeIsolated { key(e) } ? nil : e
            }
            window = w
        }
        window.map(AppWindows.show)
    }

    /// Whether the window took a key: one of the console's, pressed alone,
    /// while the window is the key one.
    private static func key(_ e: NSEvent) -> Bool {
        guard let w = window, e.window === w, w.isKeyWindow,
              e.modifierFlags.intersection([.command, .control, .option]).isEmpty,
              let chars = e.characters, chars.count == 1 else { return false }
        return options.press(chars)
    }
}

/// The Team window's content: what it shows and in which period over the
/// table, the team's counts and the unit under them, and the table.
struct TeamView: View {
    @EnvironmentObject private var store: Store
    @EnvironmentObject private var options: TeamOptions

    var body: some View {
        VStack(spacing: 0) {
            controls
            if let r = store.report, !r.solo {
                let matrix = TeamMatrix(r, period: options.period, mode: options.mode)
                let unit = options.segment == .usage ? matrix.unit : "M " + r.tokenLabel
                HeaderLine(header: TeamHeader(r, period: options.period, unit: unit))
                Divider()
                switch options.segment {
                case .usage:
                    MatrixTable(report: r, matrix: matrix, sort: $options.usageSort)
                case .status:
                    StatusTable(report: r, period: options.period, now: store.now, sort: $options.statusSort)
                }
                if r.team.estimated {
                    let reason = r.team.estimatedReason ?? "Some devices' weighted tokens are estimated from their input and output"
                    Divider()
                    Text("~ " + reason)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .help(reason)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 6)
                }
            } else {
                Divider()
                VStack(spacing: 6) {
                    Text(store.report == nil ? "No report yet" : "This Mac is the team's only device")
                        .font(.title3)
                    Text(store.report == nil ? "The team shows here once ai-usage has collected." : "Usage in the menu bar shows its subscriptions.")
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .frame(minWidth: 640, minHeight: 300)
    }

    private var controls: some View {
        HStack(spacing: 12) {
            Picker("View", selection: $options.segment) {
                ForEach(TeamOptions.Segment.allCases) { Text($0.title).tag($0) }
            }
            .fixedSize()
            .help("Usage or Status (S)")
            Spacer(minLength: 12)
            Picker("Period", selection: $options.period) {
                ForEach(Period.allCases) { Text($0.title).tag($0) }
            }
            .fixedSize()
            .help("The period; the header says which days it counts (1, 7, 3, 9)")
            let usage = options.segment == .usage
            Picker("Show", selection: $options.mode) {
                ForEach(TeamMatrix.Mode.allCases) { Text($0.title).tag($0) }
            }
            .fixedSize()
            .help("Tokens, or each device's share of each subscription (%)")
            .opacity(usage ? 1 : 0)
            .disabled(!usage)
            .accessibilityHidden(!usage)
        }
        .pickerStyle(.segmented)
        .labelsHidden()
        .padding(.horizontal, 16)
        .padding(.top, 12)
        .padding(.bottom, 8)
    }
}

/// "12 devices · 2 errors · 1 not reporting · 7 Days UTC · M weighted
/// tokens", the errors and the silence in their colors.
private struct HeaderLine: View {
    let header: TeamHeader

    var body: some View {
        let dot = Text(" · ")
        var line = Text(header.devicesText)
        if let e = header.errorsText {
            line = line + dot + Text(e).foregroundColor(Palette.text("out"))
        }
        if let s = header.silentText {
            line = line + dot + Text(s).foregroundColor(Palette.text("tight"))
        }
        return (line + dot + Text(header.period) + dot + Text(header.unit))
            .font(.callout)
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 16)
            .padding(.bottom, 8)
            .accessibilityLabel(header.text)
    }
}

// MARK: - Usage

/// DEVICES × SUBSCRIPTIONS: a row per device, a column per subscription
/// under its provider, the tokens with no quota, and TOTAL, in the report's
/// order until a heading sorts them.
private struct MatrixTable: View {
    let report: Report
    let matrix: TeamMatrix
    @Binding var sort: TeamSort?

    var body: some View {
        let lines = matrix.lines(sortedBy: key, ascending: sort?.ascending ?? true)
        TeamTable(columns: columns, rows: lines.count + 1, cell: { cell($0, $1, lines) }, sort: $sort, name: "Devices and subscriptions")
    }

    private var columns: [TeamColumn] {
        let subscriptions = matrix.columns.enumerated().map { i, c -> TeamColumn in
            let loud = c.state.map(Palette.loud) ?? false
            let state = c.state.map { " · " + Palette.label($0) } ?? ""
            return TeamColumn(id: "c:" + c.id, group: matrix.groups.first { $0.columns.contains(i) }?.title, title: c.title,
                              spoken: c.name + (loud ? ", " + Palette.label(c.state!).lowercased() : ""),
                              color: loud ? Palette.nsText(c.state!) : nil, help: c.help + state, width: 64)
        }
        return [TeamColumn(id: "device", title: "Device", width: 150, minWidth: 90, trailing: false, sortsAscending: true)]
            + subscriptions
            + [TeamColumn(id: "total", title: "Total", help: "The device's tokens over every subscription", width: 72)]
    }

    private var key: TeamMatrix.SortKey? {
        guard let s = sort else { return nil }
        switch s.column {
        case "device": return .device
        case "total": return .total
        default: return matrix.columns.firstIndex { "c:" + $0.id == s.column }.map { .column($0) }
        }
    }

    private func cell(_ row: Int, _ id: String, _ lines: [TeamMatrix.Line]) -> AnyView {
        let total = row >= lines.count
        if id == "device" {
            guard !total else { return AnyView(TotalLabel()) }
            let l = lines[row]
            return AnyView(DeviceCell(name: l.device, device: report.team.devices.first { $0.device == l.deviceId }))
        }
        let c: TeamMatrix.Cell
        if id == "total" {
            c = total ? matrix.total : lines[row].total
        } else if let i = matrix.columns.firstIndex(where: { "c:" + $0.id == id }) {
            c = total ? matrix.totals[i] : lines[row].cells[i]
        } else {
            return AnyView(EmptyView())
        }
        return AnyView(HeatCell(cell: c, strong: total || id == "total", rule: total))
    }
}

/// A number of the matrix on its step of the heat map, the top step's in
/// bold, as the console's.
private struct HeatCell: View {
    let cell: TeamMatrix.Cell
    /// A total, in bold.
    var strong = false
    /// The first line of TOTAL, under a rule.
    var rule = false
    @Environment(\.colorSchemeContrast) private var contrast

    var body: some View {
        Text(cell.text)
            .font(.callout.weight(strong || cell.heat == 5 ? .semibold : .regular))
            .monospacedDigit()
            .foregroundStyle(cell.text == Format.none ? .tertiary : .primary)
            .lineLimit(1)
            .padding(.horizontal, 6)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .trailing)
            .background {
                RoundedRectangle(cornerRadius: 3).fill(Palette.heat(cell.heat, contrast)).padding(1)
            }
            .overlay(alignment: .top) { if rule { Divider() } }
            .help(cell.help)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(cell.spoken)
    }
}

/// A device's name, with its mark before it.
private struct DeviceCell: View {
    let name: String
    let device: TeamDevice?

    var body: some View {
        let status = DeviceStatus(device)
        HStack(spacing: 2) {
            DeviceMark(status: status)
            Text(name)
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .padding(.leading, 8)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
        .help([device.map { "\($0.label) as \($0.osUser)" }, device?.note].compactMap { $0 }.joined(separator: "\n"))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([name, status.spoken].compactMap { $0 }.joined(separator: ", "))
    }
}

/// TOTAL's name, under the rule over its line.
private struct TotalLabel: View {
    var body: some View {
        Text("Total")
            .fontWeight(.semibold)
            .foregroundStyle(.secondary)
            .padding(.leading, 8 + Metrics.slot + 2)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
            .overlay(alignment: .top) { Divider() }
    }
}

// MARK: - Status

/// Each device's status, a line each: who runs it, its release, its last
/// report, its tools, its tokens in each period, and the report's NOTE.
private struct StatusTable: View {
    let report: Report
    let period: Period
    let now: Date
    @Binding var sort: TeamSort?

    private static let periods: [(Period, String)] = [(.today, "Today"), (.week, "7D"), (.month, "30D"), (.quarter, "90D")]

    var body: some View {
        let table = DeviceStatusTable(report)
        let lines = table.lines(sortedBy: key, ascending: sort?.ascending ?? true)
        TeamTable(columns: columns, rows: lines.count + 1, cell: { cell($0, $1, lines, table) }, sort: $sort, fills: true,
                  name: "Device status")
    }

    private var columns: [TeamColumn] {
        let tokens = "M " + report.tokenLabel
        return [
            TeamColumn(id: "device", title: "Device", width: 150, minWidth: 90, trailing: false, sortsAscending: true),
            TeamColumn(id: "version", group: "Collector", title: "Version", help: "Its release; ↓ is behind the latest", width: 80,
                       trailing: false, sortsAscending: true),
            TeamColumn(id: "seen", group: "Collector", title: "Seen", help: "How long ago it last reported", width: 64),
            TeamColumn(id: "via", group: "Collector", title: "Via", help: "The tools it reads; × fails", width: 180, trailing: false,
                       sortsAscending: nil),
        ] + Self.periods.map { p, title in
            // The window's period stands out.
            TeamColumn(id: p.rawValue, group: "Tokens", title: title, color: p == period ? .labelColor : nil, help: p.title + ", " + tokens,
                       width: 56)
        } + [
            TeamColumn(id: "note", title: "Note", width: 220, minWidth: 80, trailing: false, sortsAscending: nil),
        ]
    }

    private var key: DeviceStatusTable.SortKey? {
        switch sort?.column {
        case "device": return .device
        case "version": return .version
        case "seen": return .seen
        case let id?: return Period(rawValue: id).map { .tokens($0) }
        case nil: return nil
        }
    }

    private func cell(_ row: Int, _ id: String, _ lines: [DeviceStatusTable.Line], _ table: DeviceStatusTable) -> AnyView {
        if row >= lines.count {
            if id == "device" { return AnyView(TotalLabel()) }
            guard let p = Period(rawValue: id) else { return AnyView(Color.clear.overlay(alignment: .top) { Divider() }) }
            return AnyView(PeriodCell(tokens: table.total[p], estimated: table.estimated, chosen: p == period, label: report.tokenLabel,
                                      period: p, rule: true))
        }
        let l = lines[row]
        let status = DeviceStatus(l.team)
        switch id {
        case "device":
            return AnyView(DeviceCell(name: l.device, device: l.team))
        case "version":
            let latest = report.team.latestVersion.map { "; the latest is \($0)" } ?? ""
            return AnyView(Plain(text: l.version + (l.behind ? " ↓" : ""), style: l.behind ? .secondary : .primary,
                                 help: l.behind ? "Behind" + latest : ""))
        case "seen":
            let seen = l.seen.map { Format.duration(now.timeIntervalSince($0)) } ?? Format.none
            return AnyView(Plain(text: seen, color: l.team?.silent == true ? Palette.text("tight") : .secondary, trailing: true,
                                 help: l.seen.map { "Reported " + Format.clock($0, now: now) } ?? "Never reported"))
        case "via":
            return AnyView(ToolsCell(tools: l.tools))
        case "note":
            let color: Color
            switch status {
            case .error: color = Palette.text("out")
            case .silent, .notUpdating: color = Palette.text("tight")
            default: color = .secondary
            }
            return AnyView(Plain(text: l.note ?? "", color: color, help: l.note ?? ""))
        default:
            guard let p = Period(rawValue: id) else { return AnyView(EmptyView()) }
            return AnyView(PeriodCell(tokens: l.tokens.value[p], estimated: l.estimated, chosen: p == period, label: l.tokens.label, period: p))
        }
    }
}

/// A line of text in a cell.
private struct Plain: View {
    let text: String
    var color: Color?
    var style: HierarchicalShapeStyle = .primary
    var trailing = false
    var help = ""

    var body: some View {
        Group {
            if let color {
                Text(text).foregroundStyle(color)
            } else {
                Text(text).foregroundStyle(style)
            }
        }
        .lineLimit(1)
        .truncationMode(.tail)
        .padding(.horizontal, 6)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: trailing ? .trailing : .leading)
        .help(help)
    }
}

/// A device's tokens in a period, the window's period in full color.
private struct PeriodCell: View {
    let tokens: Int
    let estimated: Bool
    let chosen: Bool
    let label: String
    let period: Period
    var rule = false

    var body: some View {
        Text((estimated && tokens > 0 ? "~" : "") + Format.millions(tokens))
            .font(.callout.weight(rule ? .semibold : .regular))
            .monospacedDigit()
            .foregroundStyle(tokens == 0 ? .tertiary : chosen ? .primary : .secondary)
            .lineLimit(1)
            .padding(.horizontal, 6)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .trailing)
            .overlay(alignment: .top) { if rule { Divider() } }
            .help(tokens > 0 ? "\(Format.tokensM(tokens)) \(label) in \(period.title)" : "")
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("\(period.title): \(Format.spokenMillions(tokens))" + (estimated && tokens > 0 ? ", estimated" : ""))
    }
}

/// The tools a device reads, dim, with one that fails in the error's color
/// and marked: "Claude ×, Codex".
private struct ToolsCell: View {
    let tools: [DeviceStatusTable.Tool]

    var body: some View {
        let text = tools.enumerated().reduce(Text("")) { t, e in
            let name = Format.provider(e.element.provider)
            let part = e.element.failing ? Text(name + " ×").foregroundColor(Palette.text("out")) : Text(name).foregroundColor(.secondary)
            return t + Text(e.offset == 0 ? "" : ", ").foregroundColor(.secondary) + part
        }
        (tools.isEmpty ? Text(Format.none).foregroundColor(.secondary) : text)
            .lineLimit(1)
            .truncationMode(.tail)
            .padding(.horizontal, 6)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
            .help(tools.compactMap { t in t.error.map { "\(Format.provider(t.provider)): \($0)" } }.joined(separator: "\n"))
            .accessibilityLabel(tools.map { Format.provider($0.provider) + ($0.failing ? ", failing" : "") }.joined(separator: "; "))
    }
}
