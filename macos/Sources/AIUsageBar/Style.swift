import AIUsageKit
import AppKit
import SwiftUI

/// SwiftUI's State property wrapper. The macOS 27 SDK also declares a State
/// macro whose plugin comes only with Xcode, so `@State` does not build with
/// the Command Line Tools; this name reaches the wrapper on every SDK.
typealias ViewState = SwiftUI.State

/// The sizes the popover keeps to.
enum Metrics {
    static let width: CGFloat = 400
    /// The popover's side margin.
    static let inset: CGFloat = 14
    static let maxHeight: CGFloat = 560
    /// The least a tab's body is, so a short tab does not make a stub.
    static let minBody: CGFloat = 260
    static let limitRow: CGFloat = 32
    static let row: CGFloat = 30
    static let subLine: CGFloat = 16
    /// How far a row's highlight reaches past the margin: to 5 points from
    /// the popover's edge, as a menu's highlight does.
    static let bleed: CGFloat = 9

    // The grid every tab's rows share, so that switching tabs moves no
    // column: a slot for a mark, the name, a bar that takes the rest, the
    // value, and a trailing column for a countdown or an age.
    static let slot: CGFloat = 16
    static let name: CGFloat = 120
    static let gap: CGFloat = 8
    static let value: CGFloat = 54
    static let trailing: CGFloat = 54
    /// The space between the value and the trailing column.
    static let trailingGap: CGFloat = 6
}

/// How tall the popover may grow; the renderer lifts the limit for its
/// pictures of whole tabs.
private struct MaxHeightKey: EnvironmentKey {
    static let defaultValue = Metrics.maxHeight
}

extension EnvironmentValues {
    var popoverMaxHeight: CGFloat {
        get { self[MaxHeightKey.self] }
        set { self[MaxHeightKey.self] = newValue }
    }
}

/// The report's states in words and colors. Only the states that ask for
/// something have a color, so they stand out: out red, over orange, and
/// tight yellow. Under has a mark of its own, in gray: room to spare asks
/// for nothing. On track and unknown stay quiet; nothing is green.
enum Palette {
    /// The fill of a bar or a dot; nil in a calm state.
    static func fill(_ state: String) -> Color? {
        switch state {
        case "out": return .red
        case "over": return .orange
        case "tight": return .yellow
        default: return nil
        }
    }

    /// The fill of a calm state's bar.
    static let calm = Color.primary.opacity(0.35)
    /// The fill of a bar of tokens.
    static let tokens = Color.primary.opacity(0.28)

    /// The empty part of a bar.
    static func track(_ contrast: ColorSchemeContrast) -> Color {
        Color.primary.opacity(contrast == .increased ? 0.15 : 0.08)
    }

    /// Where a window is headed by its reset, past what it has used.
    static func projection(_ state: String, _ contrast: ColorSchemeContrast) -> Color {
        let strong = contrast == .increased
        if let fill = fill(state) { return fill.opacity(strong ? 0.5 : 0.35) }
        return Color.primary.opacity(strong ? 0.5 : 0.16)
    }

    /// Text in a state's color. In light mode each is a shade darker than
    /// the system color, so small text keeps a contrast of 4.5:1 on white.
    static func text(_ state: String) -> Color {
        switch state {
        case "out": return adaptive(.systemRed, light: (0.80, 0.10, 0.08))
        case "over": return adaptive(.systemOrange, light: (0.70, 0.33, 0))
        case "tight": return adaptive(.systemYellow, light: (0.55, 0.40, 0))
        default: return .secondary
        }
    }

    /// Whether a state asks for something, and so takes its color.
    static func loud(_ state: String) -> Bool {
        ["out", "over", "tight"].contains(state)
    }

    /// Whether a state has a mark and a word: the loud ones, and under.
    static func marked(_ state: String) -> Bool {
        loud(state) || state == "under"
    }

    /// A state as a word.
    static func label(_ state: String) -> String {
        switch state {
        case "out": return "Out"
        case "over": return "Over"
        case "tight": return "Tight"
        case "ok": return "On Track"
        case "under": return "Underused"
        default: return "Unknown"
        }
    }

    /// The mark before a percent in a state that asks for something.
    static func glyph(_ state: String) -> String? {
        switch state {
        case "out": return "xmark.octagon.fill"
        case "over": return "exclamationmark.triangle.fill"
        case "tight": return "exclamationmark.circle.fill"
        case "under": return "arrow.down.circle.fill"
        default: return nil
        }
    }

    private static func adaptive(_ dark: NSColor, light: (CGFloat, CGFloat, CGFloat)) -> Color {
        let light = NSColor(srgbRed: light.0, green: light.1, blue: light.2, alpha: 1)
        return Color(nsColor: NSColor(name: nil) { appearance in
            let match = appearance.bestMatch(from: [.aqua, .darkAqua, .accessibilityHighContrastAqua, .accessibilityHighContrastDarkAqua])
            return match == .darkAqua || match == .accessibilityHighContrastDarkAqua ? dark : light
        })
    }
}

/// A state's mark, small, before a percent or a name.
struct StateGlyph: View {
    let state: String

    var body: some View {
        if let symbol = Palette.glyph(state) {
            Image(systemName: symbol)
                .symbolRenderingMode(.monochrome)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(Palette.text(state))
                .accessibilityHidden(true)
        }
    }
}

/// What marks this Mac's row: a small laptop, dim, in the slot before the
/// name. Color is for problems.
struct ThisMacMark: View {
    var body: some View {
        Image(systemName: "laptopcomputer")
            .font(.system(size: 8, weight: .semibold))
            .foregroundStyle(.tertiary)
            .help("This Mac")
            .accessibilityHidden(true)
    }
}

/// A window as a capsule that drains as it is used, as the percent beside
/// it and the menu bar's ring do: solid from the leading edge, what is left
/// now. The forecast is cut from the end of it, fainter: what will be gone
/// by the reset, so the solid part that stays is what will be left then.
/// When the forecast is past all of it, the whole fill is faint, with a
/// notch at the leading edge, as the window runs out before it resets. The
/// fill takes the window's color only when it is tight or worse; an out
/// window is an empty track tinted red. A stale reading is faded, and so is
/// a muted bar, of a window that another one stops. The text beside it says
/// all of this to VoiceOver.
struct QuotaBar: View {
    let window: Quota.Window
    var height: CGFloat = 6
    /// Drawn without the window's color, dim, for an account that another
    /// window stops: its own state is not what counts now.
    var muted = false
    @Environment(\.colorSchemeContrast) private var contrast

    var body: some View {
        let w = window
        let state = muted ? "ok" : w.state
        let fill = Palette.fill(state)
        let left = Double(Format.percentLeft(w)) / 100
        let atReset = w.forecast.map { $0.percent > w.percent ? max(100 - $0.percent, 0) / 100 : left } ?? left
        let notch = state != "out" && (w.forecast?.percent ?? 0) > 100
        let track = state == "out" ? Palette.fill("out")!.opacity(0.25) : Palette.track(contrast)
        let projection = Palette.projection(state, contrast)
        Canvas { ctx, size in
            let h = height, y = (size.height - h) / 2
            let bar = { (fraction: Double) in
                Path(roundedRect: CGRect(x: 0, y: y, width: max(h, size.width * fraction), height: h), cornerRadius: h / 2)
            }
            ctx.fill(bar(1), with: .color(track))
            if left > atReset {
                ctx.fill(bar(left), with: .color(projection))
            }
            if atReset > 0 {
                ctx.fill(bar(atReset), with: .color(fill ?? Palette.calm))
            }
            if notch {
                let r = CGRect(x: 0, y: 0, width: 2, height: size.height)
                ctx.fill(Path(roundedRect: r, cornerRadius: 1), with: .color(fill ?? Color.primary.opacity(0.5)))
            }
        }
        .frame(height: height + 4)
        .opacity(w.stale || muted ? 0.45 : 1)
        .accessibilityHidden(true)
    }
}

/// Tokens as a bar against the largest in its list.
struct TokenBar: View {
    let value: Int
    let top: Int
    @Environment(\.colorSchemeContrast) private var contrast

    var body: some View {
        let fraction = top > 0 ? min(Double(max(value, 0)) / Double(top), 1) : 0
        Canvas { ctx, size in
            let h: CGFloat = 4, y = (size.height - h) / 2
            ctx.fill(Path(roundedRect: CGRect(x: 0, y: y, width: size.width, height: h), cornerRadius: h / 2), with: .color(Palette.track(contrast)))
            if fraction > 0 {
                let r = CGRect(x: 0, y: y, width: max(h, size.width * fraction), height: h)
                ctx.fill(Path(roundedRect: r, cornerRadius: h / 2), with: .color(Palette.tokens))
            }
        }
        .frame(height: 8)
        .accessibilityHidden(true)
    }
}

/// Two weeks of tokens as a line, oldest on the left, against the peak of
/// every line in its list, so a quiet account draws a flat line.
struct Sparkline: View {
    let points: [Int]
    let top: Int

    var body: some View {
        Canvas { ctx, size in
            let top = Double(max(top, points.max() ?? 0))
            guard points.count > 1, top > 0 else { return }
            let step = size.width / CGFloat(points.count - 1)
            var path = Path()
            for (i, v) in points.enumerated() {
                let p = CGPoint(x: CGFloat(i) * step, y: size.height - 1 - (size.height - 2) * CGFloat(Double(v) / top))
                if i == 0 { path.move(to: p) } else { path.addLine(to: p) }
            }
            ctx.stroke(path, with: .color(Color.primary.opacity(0.45)), style: StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round))
        }
        .frame(height: 14)
        .accessibilityHidden(true)
    }
}

/// A heading over a group of rows.
struct GroupHeading: View {
    let title: String
    var first = false
    var help = ""
    /// A symbol before the title, as a folder's.
    var symbol: String?

    var body: some View {
        HStack(spacing: 4) {
            if let symbol {
                Image(systemName: symbol)
                    .font(.system(size: 10, weight: .semibold))
                    .accessibilityHidden(true)
            }
            Text(title)
        }
        .font(.subheadline.weight(.semibold))
        .foregroundStyle(.secondary)
        .lineLimit(1)
        .truncationMode(.middle)
        .padding(.top, first ? 4 : 12)
        .padding(.bottom, 4)
        .help(help)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

/// A row that opens its details under it on a click: one open at a time in
/// the whole popover. It lights up under the pointer, as a source list's
/// row does, and an open row keeps a faint fill behind it and its details.
struct DisclosureRow<Row: View, Details: View>: View {
    @EnvironmentObject private var store: Store
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    let key: String
    /// The row as one sentence, for VoiceOver.
    let spoken: String
    /// Where the details start, under the row's name.
    var indent: CGFloat = Metrics.slot
    @ViewBuilder let row: () -> Row
    @ViewBuilder let details: () -> Details
    @ViewState private var hover = false

    var body: some View {
        let open = store.expanded == key
        VStack(alignment: .leading, spacing: 0) {
            Button(action: toggle) {
                row()
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .background(highlight(hover && !open ? 0.06 : 0))
            .onHover { hover = $0 }
            .accessibilityElement(children: .combine)
            .accessibilityLabel(spoken)
            .accessibilityValue(open ? "expanded" : "collapsed")
            .accessibilityAddTraits(.isButton)
            .accessibilityAction(named: "Show Details", toggle)
            if open {
                VStack(alignment: .leading, spacing: 3) {
                    details()
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.leading, indent)
                .padding(.top, 2)
                .padding(.bottom, 8)
                .transition(.opacity)
            }
        }
        .background(highlight(open ? 0.05 : 0))
        .id(key)
    }

    private func highlight(_ opacity: Double) -> some View {
        RoundedRectangle(cornerRadius: 6, style: .continuous)
            .fill(Color.primary.opacity(opacity))
            .padding(.horizontal, -Metrics.bleed)
    }

    private func toggle() {
        withAnimation(reduceMotion ? nil : .easeInOut(duration: 0.15)) {
            store.toggle(key)
        }
    }
}

/// The line over Usage and Projects, which stays put as the list under it
/// scrolls: what they show on the left, the period on the right.
struct TopLine<Leading: View>: View {
    @ViewBuilder let leading: () -> Leading

    var body: some View {
        HStack(spacing: 8) {
            leading()
            Spacer(minLength: 8)
            PeriodMenu()
        }
        .frame(height: 26)
        .padding(.horizontal, Metrics.inset)
        .padding(.bottom, 2)
    }
}

/// A quiet menu of view options, as Finder and Activity Monitor have them.
struct OptionMenu<Value: Hashable & Identifiable, Label: View>: View {
    let title: String
    let options: [Value]
    @Binding var selection: Value
    let name: (Value) -> String
    @ViewBuilder let label: () -> Label

    var body: some View {
        Menu {
            Picker(title, selection: $selection) {
                ForEach(options) { Text(name($0)).tag($0) }
            }
            .pickerStyle(.inline)
            .labelsHidden()
        } label: {
            label()
        }
        .menuStyle(.borderlessButton)
        .font(.callout)
        .foregroundStyle(.secondary)
        .tint(.secondary)
        .fixedSize()
        .accessibilityLabel("\(title), \(name(selection))")
    }
}

/// The period of Usage and Projects, one for both.
struct PeriodMenu: View {
    @EnvironmentObject private var store: Store

    var body: some View {
        OptionMenu(title: "Period", options: Period.allCases, selection: $store.period, name: \.title) {
            Text(store.period.title)
        }
        .help("Input + output tokens, cache excluded")
    }
}

/// A title on the left of a top line.
struct TopTitle: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(.secondary)
    }
}

/// The total under a list of tokens, after a rule, pinned under the list.
struct TotalRow: View {
    let title: String
    /// Nil for no total, as when the list is filtered.
    let tokens: Int?

    var body: some View {
        VStack(spacing: 0) {
            Divider()
            HStack(spacing: 0) {
                Text(title).foregroundStyle(.secondary)
                Spacer()
                if let tokens {
                    Text(Format.tokensM(tokens))
                        .font(.callout.weight(.semibold))
                        .monospacedDigit()
                        .frame(width: Metrics.value, alignment: .trailing)
                    Color.clear.frame(width: Metrics.trailingGap + Metrics.trailing, height: 1)
                }
            }
            .frame(height: Metrics.row)
            .padding(.horizontal, Metrics.inset)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(tokens.map { "\(title): \(Format.spokenMillions($0)) tokens" } ?? title)
    }
}

/// Tokens in whole millions as a row ends with them; the none mark is dim.
struct TokensValue: View {
    let tokens: Int

    var body: some View {
        Text(Format.tokensM(tokens))
            .font(.callout)
            .monospacedDigit()
            .foregroundStyle(tokens > 0 ? .primary : .tertiary)
            .lineLimit(1)
            .frame(width: Metrics.value, alignment: .trailing)
    }
}

/// A row's details: a label, dim, and its value, one line each, in two
/// columns. What the row shows already is not said again, and a sentence
/// that explains goes in a tooltip.
struct DetailGrid<Content: View>: View {
    @ViewBuilder let content: () -> Content

    var body: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 10, verticalSpacing: 4) {
            content()
        }
    }
}

/// A line of a detail grid; nothing for no value.
struct DetailLine: View {
    let label: String
    let value: Text?
    var help = ""

    init(_ label: String, _ value: Text?, help: String = "") {
        (self.label, self.value, self.help) = (label, value, help)
    }

    init(_ label: String, _ value: String?, help: String = "") {
        self.init(label, value.map { Text($0) }, help: help)
    }

    var body: some View {
        if let value {
            GridRow {
                Text(label)
                    .foregroundStyle(.tertiary)
                    .lineLimit(1)
                value
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .help(help)
            }
        }
    }
}

/// Each period's tokens as a line of a detail grid: the numbers, with the
/// periods under them, small, and the period Usage shows in full color.
struct PeriodsLine: View {
    @EnvironmentObject private var store: Store
    let usage: Usage

    var body: some View {
        GridRow(alignment: .firstTextBaseline) {
            Text("Tokens")
                .foregroundStyle(.tertiary)
            HStack(alignment: .top, spacing: 0) {
                ForEach(Period.allCases) { p in
                    VStack(alignment: .leading, spacing: 0) {
                        Text(Format.tokensM(usage[p]))
                            .font(.callout)
                            .monospacedDigit()
                            .foregroundStyle(p == store.period ? .primary : .secondary)
                        Text(p.title)
                            .foregroundStyle(.tertiary)
                    }
                    .frame(width: 64, alignment: .leading)
                }
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(Period.allCases.map { "\($0.title) \(Format.spokenMillions(usage[$0]))" }.joined(separator: ", "))
        }
    }
}

/// Shows more rows in place, or fewer: a quiet button with a chevron.
struct MoreButton: View {
    let title: String
    let expanded: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                Text(title)
                Image(systemName: expanded ? "chevron.up" : "chevron.down")
                    .font(.system(size: 9, weight: .semibold))
            }
            .frame(height: Metrics.row)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .font(.callout)
        .foregroundStyle(.secondary)
    }
}

/// Makes the scroll view it is in draw overlay scrollers, as the menu bar's
/// own windows do, whatever System Settings says: a scroller that takes
/// room of its own would move every column of a list that grows past the
/// popover, as when a row opens.
struct OverlayScrollers: NSViewRepresentable {
    func makeNSView(context: Context) -> NSView { Probe() }
    func updateNSView(_ view: NSView, context: Context) {
        (view as? Probe)?.apply()
    }

    final class Probe: NSView {
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            guard window != nil else { return }
            apply()
            NotificationCenter.default.removeObserver(self)
            NotificationCenter.default.addObserver(self, selector: #selector(styleChanged),
                                                   name: NSScroller.preferredScrollerStyleDidChangeNotification, object: nil)
        }

        @objc private func styleChanged() {
            // After the scroll view has taken the new style itself.
            DispatchQueue.main.async { [weak self] in
                MainActor.assumeIsolated { self?.apply() }
            }
        }

        func apply() {
            guard let scroll = enclosingScrollView else { return }
            if scroll.scrollerStyle != .overlay {
                scroll.scrollerStyle = .overlay
            }
            scroll.autohidesScrollers = true
        }
    }
}
