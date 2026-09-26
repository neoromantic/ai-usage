import AIUsageKit
import AppKit
import SwiftUI

/// Draws the popover, in each of its tabs and empty states, and the
/// settings panes to PNGs at twice their size, light and dark, with "now" at
/// the report's time. Each tab is drawn as tall as the popover may be, as the
/// app shows it, and whole, as "-full".
@MainActor
enum Renderer {
    static func run(_ report: Report, into dir: URL) -> Int32 {
        _ = NSApplication.shared
        NSApp.setActivationPolicy(.prohibited)
        let store = Store(demo: report)
        Store.shared = store
        var popovers: [(String, (Store) -> Void)] = [
            ("subscriptions", { $0.tab = .subscriptions }),
            ("usage", { $0.tab = .usage; $0.share = false }),
        ]
        if !report.solo {
            popovers.append(("usage-share", { $0.tab = .usage; $0.share = true }))
            popovers.append(("devices", { $0.tab = .devices; $0.share = false }))
        }
        popovers.append(("projects", { $0.tab = .projects }))
        let empty: [(String, Store.Phase)] = [
            ("not-installed", .notInstalled),
            ("not-collected", .notCollected),
            ("mismatch", .mismatch(ReportError.schema(3).localizedDescription)),
            ("failed", .failed("ai-usage did not finish in 30 seconds")),
        ]
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            for (look, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
                for (name, setUp) in popovers {
                    setUp(store)
                    try write(popover(store), appearance, to: dir.appendingPathComponent("popover-\(name)-\(look).png"))
                    try write(popover(store, height: .infinity), appearance, to: dir.appendingPathComponent("popover-\(name)-full-\(look).png"))
                }
                for (name, phase) in empty {
                    try write(popover(Store(showing: phase)), appearance, to: dir.appendingPathComponent("popover-\(name)-\(look).png"))
                }
                store.tab = .subscriptions
                try write(showcase(store, dark: look == "dark"), appearance, to: dir.appendingPathComponent("menubar-\(look).png"))
                for pane in SettingsWindow.panes {
                    let view = pane.view.environmentObject(store).background(Color(nsColor: .windowBackgroundColor))
                    try write(view, appearance, to: dir.appendingPathComponent("settings-\(pane.title.lowercased())-\(look).png"))
                }
            }
        } catch {
            fputs("AIUsageBar: \(error.localizedDescription)\n", stderr)
            return 1
        }
        return 0
    }

    /// The popover in a window's rounded frame.
    private static func popover(_ store: Store, height: CGFloat = Metrics.maxHeight) -> some View {
        PopoverView()
            .environmentObject(store)
            .environment(\.popoverMaxHeight, height)
            .background(Color(nsColor: .windowBackgroundColor))
            .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(.separator))
    }

    /// The picture for the README: the menu bar item open on a desktop.
    private static func showcase(_ store: Store, dark: Bool) -> some View {
        let clock = DateFormatter()
        clock.locale = Locale(identifier: "en_US_POSIX")
        clock.dateFormat = "EEE d MMM HH:mm"
        let desktop = dark
            ? [Color(red: 0.11, green: 0.14, blue: 0.26), Color(red: 0.23, green: 0.14, blue: 0.32)]
            : [Color(red: 0.78, green: 0.84, blue: 0.95), Color(red: 0.92, green: 0.85, blue: 0.93)]
        return VStack(alignment: .trailing, spacing: 6) {
            HStack(spacing: 16) {
                MenuBarLabel(summary: store.summary, showPercent: true)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 2)
                    .background(RoundedRectangle(cornerRadius: 5).fill(.primary.opacity(0.12)))
                Text(clock.string(from: store.now))
            }
            .font(.system(size: 13, weight: .medium))
            .padding(.horizontal, 14)
            .frame(maxWidth: .infinity, minHeight: 26, alignment: .trailing)
            .background(dark ? Color.black.opacity(0.35) : Color.white.opacity(0.5))
            popover(store)
                .shadow(color: .black.opacity(dark ? 0.5 : 0.22), radius: 22, y: 10)
                .padding(.horizontal, 40)
                .padding(.bottom, 44)
        }
        .background(LinearGradient(colors: desktop, startPoint: .top, endPoint: .bottom))
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
    }

    /// Lays a view out in an offscreen window and draws it with AppKit, which,
    /// unlike ImageRenderer, draws controls such as pickers and text fields.
    private static func write<V: View>(_ view: V, _ appearance: NSAppearance.Name, to url: URL) throws {
        let host = NSHostingView(rootView: view)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 600), styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = NSAppearance(named: appearance)
        window.backgroundColor = .clear
        window.isOpaque = false
        window.contentView = host
        // Measured heights and state settle over a few turns of the run loop.
        for _ in 0..<4 {
            window.setContentSize(host.fittingSize)
            host.layoutSubtreeIfNeeded()
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        }
        let bounds = host.bounds
        guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(bounds.width) * 2, pixelsHigh: Int(bounds.height) * 2,
                                         bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                         colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0) else {
            throw CocoaError(.fileWriteUnknown)
        }
        rep.size = bounds.size
        host.cacheDisplay(in: bounds, to: rep)
        guard let png = rep.representation(using: .png, properties: [:]) else { throw CocoaError(.fileWriteUnknown) }
        try png.write(to: url)
    }
}
