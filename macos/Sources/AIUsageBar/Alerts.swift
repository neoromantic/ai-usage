import AIUsageKit
import AppKit
import SwiftUI

/// Every alert of the report, in its order, in place of the tab's body:
/// one line each, with the mark and color of its kind, what it is about,
/// and what to know in the report's words. A click opens the subscription
/// in Limits or the devices in Usage. The last line names the subscription
/// with the most room, as the verdict does when all is on track.
struct AlertsView: View {
    @EnvironmentObject private var store: Store
    let report: Report

    var body: some View {
        let lines = AlertLine.lines(report)
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Button {
                    store.showsAlerts = false
                } label: {
                    HStack(spacing: 3) {
                        Image(systemName: "chevron.left")
                            .font(.system(size: 11, weight: .semibold))
                        Text("Back")
                    }
                    .font(.callout)
                    .foregroundStyle(Color.accentColor)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .help("Back to \(store.tab.title)")
                .accessibilityLabel("Back to \(store.tab.title)")
                Spacer(minLength: 8)
                TopTitle(text: lines.count == 1 ? "1 alert" : "\(lines.count) alerts")
            }
            .frame(height: 26)
            .padding(.horizontal, Metrics.inset)
            .padding(.bottom, 2)
            ScrollView {
                let width = subjectWidth(lines)
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                        AlertRow(line: line, solo: report.solo, subjectWidth: width)
                    }
                }
                .padding(.horizontal, Metrics.inset)
                .padding(.bottom, 12)
            }
            .scrollIndicators(.never)
            if let room = Room.most(report) {
                RoomRow(room: room)
            }
        }
    }

    /// The subject column: as wide as the widest subject, up to a limit
    /// that leaves the text its room.
    private func subjectWidth(_ lines: [AlertLine]) -> CGFloat {
        let font = NSFont.systemFont(ofSize: NSFont.systemFontSize)
        let widest = lines.map { ($0.text.subject as NSString).size(withAttributes: [.font: font]).width }.max() ?? 0
        return min(ceil(widest) + 2, 150)
    }
}

/// An alert's line: its kind's mark, the subject, and the report's text,
/// whole in the tooltip.
private struct AlertRow: View {
    @EnvironmentObject private var store: Store
    let line: AlertLine
    let solo: Bool
    let subjectWidth: CGFloat
    @ViewState private var hover = false

    var body: some View {
        let a = line.attention
        let mark = Self.mark(a)
        // An idle subscription is a quiet line, dim throughout, as the
        // report's is; so is what its cost says.
        let quiet = a.kind == .idle || a.kind == .check || a.kind == .cost
        let row = HStack(alignment: .firstTextBaseline, spacing: 0) {
            Image(systemName: mark.symbol)
                .symbolRenderingMode(.monochrome)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(mark.color)
                .frame(width: Metrics.slot, alignment: .leading)
            Text(line.text.subject)
                .foregroundStyle(quiet ? .tertiary : .primary)
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(width: subjectWidth, alignment: .leading)
            Text(line.text.detail)
                .font(.callout)
                .foregroundStyle(quiet ? .tertiary : .secondary)
                .lineLimit(1)
                .truncationMode(.tail)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.leading, Metrics.gap)
        }
        .frame(height: Metrics.row)
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
        let spoken = "\(Self.word(a.kind)): \(line.text.subject)" + (line.text.detail.isEmpty ? "" : ", \(line.text.detail)")
        if let hint = hint {
            Button(action: open) { row }
                .buttonStyle(.plain)
                .background(
                    RoundedRectangle(cornerRadius: 6, style: .continuous)
                        .fill(Color.primary.opacity(hover ? 0.06 : 0))
                        .padding(.horizontal, -Metrics.bleed)
                )
                .onHover { hover = $0 }
                .help(line.text.detail)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(spoken)
                .accessibilityHint(hint)
                .accessibilityAddTraits(.isButton)
        } else {
            row
                .help(line.text.detail)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(spoken)
        }
    }

    /// What a click does, for VoiceOver; nil for a line that opens nothing.
    private var hint: String? {
        switch line.target {
        case .limits, .notRead: return "Shows the subscription in Limits"
        case .devices: return solo ? "Shows this Mac's status" : "Shows the devices in Usage"
        case .cost: return "Shows the subscription's cost in Settings"
        case .none: return nil
        }
    }

    private func open() {
        switch line.target {
        case .limits(let provider, let label):
            let key = "limits:\(provider)/\(label)"
            store.tab = .limits
            store.expanded = key
            store.scrollTarget = key
        case .notRead(let provider):
            let key = "unread:\(provider)"
            store.tab = .limits
            store.expanded = key
            store.scrollTarget = key
        case .devices(let devices):
            // Alone, this Mac is the only device, and Usage lists none.
            guard !solo else {
                StatusMenu.show(store, entries: [line.attention])
                return
            }
            store.tab = .usage
            store.deviceFilter = DeviceFilter(title: line.text.subject, devices: Set(devices), chip: nil)
        case .cost:
            SettingsWindow.show(pane: .cost)
        case .none:
            break
        }
    }

    /// The mark of an entry's kind, in the color the report's ATTENTION
    /// gives its word, and the symbol its chip or row has.
    private static func mark(_ a: Attention) -> (symbol: String, color: Color) {
        switch a.kind {
        case .out, .over, .tight, .under:
            return (Palette.glyph(a.kind.rawValue) ?? "circle", Palette.text(a.kind.rawValue))
        case .outside: return ("person.fill.questionmark", Palette.text("tight"))
        case .error: return ("xmark.circle.fill", Palette.text("out"))
        case .noReading: return ("questionmark.circle", .secondary)
        case .silent: return ("antenna.radiowaves.left.and.right.slash", Palette.text("tight"))
        case .duplicate: return ("square.on.square", .secondary)
        case .old:
            // A release that lacks something needs a reinstall; one the
            // report gives a time to has not updated itself.
            if !a.limitations.isEmpty { return ("arrow.down.circle.fill", Palette.text("tight")) }
            return ("arrow.up.circle.fill", a.at != nil ? Palette.text("tight") : .secondary)
        case .idle: return ("moon.zzz", Color(nsColor: .tertiaryLabelColor))
        case .poolRise: return ("person.2", .secondary)
        case .check: return ("questionmark.circle", Color(nsColor: .tertiaryLabelColor))
        case .cost: return ("dollarsign.circle", Color(nsColor: .tertiaryLabelColor))
        case .other: return ("circle", Color(nsColor: .tertiaryLabelColor))
        }
    }

    /// An entry's kind as a word, for VoiceOver.
    private static func word(_ kind: Attention.Kind) -> String {
        switch kind {
        case .out: return "Out"
        case .over: return "Runs out"
        case .tight: return "Tight"
        case .outside: return "Used outside the team"
        case .error: return "Error"
        case .noReading: return "No reading"
        case .silent: return "Not reporting"
        case .duplicate: return "Reinstalled"
        case .old: return "Older release"
        case .under: return "Underused"
        case .idle: return "Idle"
        case .poolRise: return "Maybe the Account Pooler"
        case .check: return "Still paid?"
        case .cost: return "Cost"
        case .other(let name): return name
        }
    }
}

/// The alerts list's last line, pinned under it: the subscription with the
/// most room, which a click opens in Limits.
private struct RoomRow: View {
    @EnvironmentObject private var store: Store
    let room: Room

    var body: some View {
        VStack(spacing: 0) {
            Divider()
            Button(action: open) {
                HStack(spacing: 8) {
                    Text("Most room").foregroundStyle(.secondary)
                    Spacer(minLength: 8)
                    Text("\(room.name) · \(Format.left(room.window))")
                        .font(.callout.weight(.semibold))
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                .frame(height: Metrics.row)
                .padding(.horizontal, Metrics.inset)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help("Shows it in Limits")
            .accessibilityLabel("Most room: \(room.name), \(Format.left(room.window))")
            .accessibilityHint("Shows the subscription in Limits")
        }
    }

    private func open() {
        let key = "limits:\(room.provider)/\(room.account.label)"
        store.tab = .limits
        store.expanded = key
        store.scrollTarget = key
    }
}
