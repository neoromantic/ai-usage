import AIUsageKit
import AppKit
import IOKit.ps
import SwiftUI

/// SwiftUI's State property wrapper. The macOS 27 SDK also declares a State
/// macro whose plugin comes only with Xcode, so `@State` does not build with
/// the Command Line Tools; this name reaches the wrapper on every SDK.
typealias ViewState = SwiftUI.State

/// The report's states in words and colors. Only the states that ask for
/// something have a color, so they stand out: out red, over orange, tight
/// yellow, and under blue in words alone. On track and unknown stay neutral.
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

    /// Text in a state's color. In light mode each is a shade darker than
    /// the system color, so small text keeps a contrast of 4.5:1 on white.
    static func text(_ state: String) -> Color {
        switch state {
        case "out": return adaptive(.systemRed, light: (0.80, 0.10, 0.08))
        case "over": return adaptive(.systemOrange, light: (0.70, 0.33, 0))
        case "tight": return adaptive(.systemYellow, light: (0.55, 0.40, 0))
        case "under": return adaptive(.systemBlue, light: (0, 0.40, 0.85))
        default: return .secondary
        }
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

    private static func adaptive(_ dark: NSColor, light: (CGFloat, CGFloat, CGFloat)) -> Color {
        let light = NSColor(srgbRed: light.0, green: light.1, blue: light.2, alpha: 1)
        return Color(nsColor: NSColor(name: nil) { appearance in
            let match = appearance.bestMatch(from: [.aqua, .darkAqua, .accessibilityHighContrastAqua, .accessibilityHighContrastDarkAqua])
            return match == .darkAqua || match == .accessibilityHighContrastDarkAqua ? dark : light
        })
    }
}

extension Text {
    /// A state's word, in its color.
    func state(_ state: String) -> Text {
        foregroundColor(Palette.text(state)).fontWeight(.semibold)
    }
}

extension Attention.Kind {
    var symbol: String {
        switch self {
        case .out: return "exclamationmark.octagon.fill"
        case .over: return "exclamationmark.triangle.fill"
        case .error: return "xmark.circle.fill"
        case .silent: return "moon.zzz.fill"
        case .old: return "arrow.up.circle.fill"
        case .under: return "arrow.down.circle.fill"
        case .other: return "info.circle.fill"
        }
    }

    /// The state whose color the kind takes.
    var state: String {
        switch self {
        case .out, .error: return "out"
        case .over: return "over"
        case .silent: return "tight"
        case .under: return "under"
        case .old, .other: return "unknown"
        }
    }

    var title: String {
        switch self {
        case .out, .over, .under: return Palette.label(state)
        case .error: return "Error"
        case .silent: return "Silent"
        case .old: return "Outdated"
        case .other: return "Note"
        }
    }
}

/// A window as a capsule: the used part, in color only when the window is
/// tight or worse, and a tick where even use of the window would be by its
/// reading. A stale reading is faded; a window whose fill is not known is
/// an empty outline. The text beside it says all of this to VoiceOver.
struct QuotaBar: View {
    let window: Quota.Window?
    var height: CGFloat = 6

    var body: some View {
        GeometryReader { g in
            if let w = window, w.known {
                let used = min(max(w.percent, 0), 100) / 100
                ZStack(alignment: .leading) {
                    Capsule().fill(.quaternary)
                    if used > 0 {
                        Capsule().fill(Palette.fill(w.state) ?? Palette.calm).frame(width: max(height, g.size.width * used))
                    }
                    if let e = w.forecast?.elapsed {
                        Capsule()
                            .fill(.primary)
                            .frame(width: 2, height: height + 6)
                            .offset(x: min(max(g.size.width * e - 1, 0), g.size.width - 2))
                    }
                }
                .frame(height: g.size.height)
                .opacity(w.stale ? 0.45 : 1)
            } else {
                Capsule()
                    .strokeBorder(style: StrokeStyle(lineWidth: 1, dash: [3, 2]))
                    .foregroundStyle(.tertiary)
            }
        }
        .frame(height: height)
        .accessibilityHidden(true)
    }
}

/// Marks what is on this Mac: its device, and the accounts logged in on it,
/// with the symbol of a laptop or of a desktop Mac.
struct ThisMacMark: View {
    var help = "This Mac"

    /// A Mac with a battery of its own is a laptop.
    static let symbol: String = {
        let info = IOPSCopyPowerSourcesInfo().takeRetainedValue()
        let sources = IOPSCopyPowerSourcesList(info).takeRetainedValue() as [CFTypeRef]
        let battery = sources.contains { source in
            let d = IOPSGetPowerSourceDescription(info, source)?.takeUnretainedValue() as? [String: Any]
            return d?[kIOPSTypeKey] as? String == kIOPSInternalBatteryType
        }
        return battery ? "laptopcomputer" : "desktopcomputer"
    }()

    var body: some View {
        Image(systemName: Self.symbol)
            .font(.caption)
            .foregroundStyle(.secondary)
            .help(help)
            .accessibilityLabel(help)
    }
}

/// A small colored dot before a few words, as the header shows health.
struct StatusDot: View {
    let color: Color

    var body: some View {
        Circle().fill(color).frame(width: 7, height: 7)
    }
}

/// A heading over a group of rows.
struct GroupHeading: View {
    let title: String

    var body: some View {
        Text(title)
            .font(.subheadline.weight(.semibold))
            .foregroundStyle(.secondary)
            .accessibilityAddTraits(.isHeader)
    }
}

/// What a table counts, in a line over it.
struct Caption: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.caption)
            .foregroundStyle(.secondary)
            .lineLimit(1)
    }
}

/// Shows more rows in place, or fewer: a quiet button with a chevron.
struct MoreButton: View {
    let title: String
    let expanded: Bool
    let action: () -> Void

    var body: some View {
        Button {
            withAnimation(.easeInOut(duration: 0.15)) { action() }
        } label: {
            HStack(spacing: 4) {
                Text(title)
                Image(systemName: expanded ? "chevron.up" : "chevron.down")
                    .font(.caption2.weight(.semibold))
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .font(.callout)
        .foregroundStyle(.secondary)
    }
}

/// The sizes the popover keeps to.
enum Metrics {
    static let width: CGFloat = 540
    static let maxHeight: CGFloat = 640
    static let inset: CGFloat = 16
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
