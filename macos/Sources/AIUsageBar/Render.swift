import AIUsageKit
import AppKit
import SwiftUI

/// Draws the popover, in each of its tabs, states, and empty states, the
/// menu bar item, and the settings panes to PNGs at twice their size, light
/// and dark, with "now" at the report's time. Each tab is drawn as tall as
/// the popover may be, as the app shows it, and whole, as "-full".
@MainActor
enum Renderer {
    static func run(_ file: URL, into dir: URL) -> Int32 {
        // The pictures take the system's default blue accent, whatever
        // this Mac's is.
        UserDefaults.standard.setVolatileDomain(["AppleAccentColor": 4], forName: UserDefaults.argumentDomain)
        _ = NSApplication.shared
        NSApp.setActivationPolicy(.prohibited)
        do {
            let data = try Data(contentsOf: file)
            let report = try Report.decode(data)
            let store = Store(demo: report)
            Store.shared = store
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            let tabs: [(String, (Store) -> Void)] = [
                ("limits", { $0.tab = .limits }),
                ("usage", { $0.tab = .usage }),
                ("usage-subscriptions", { $0.tab = .usage; $0.usageMode = .subscriptions }),
                ("projects", { $0.tab = .projects }),
            ].filter { !report.solo || $0.0 != "usage-subscriptions" }
            // States reached from the collapsed popover, as a click reaches them.
            var states: [(String, (Store) -> Void, (Store) -> Void)] = []
            if let key = firstLimitRow(report) {
                states.append(("limits-expanded", { $0.tab = .limits }, { $0.expanded = key }))
            }
            if report.solo {
                if let p = report.providers.first(where: { $0.accounts.contains { $0.usage.quarter > 0 } }),
                   let a = p.accounts.first(where: { $0.usage.quarter > 0 }) {
                    states.append(("usage-expanded", { $0.tab = .usage }, { $0.expanded = "solo:\(p.provider)/\(a.label)" }))
                }
            } else {
                if let d = report.team.devices.first(where: { $0.error != nil && !$0.silent }) ?? report.team.devices.first {
                    states.append(("usage-expanded", { $0.tab = .usage }, { $0.expanded = "dev:\(d.device)" }))
                }
                // Usage after a click on each chip that filters it; the
                // errors chip's is plain usage-filtered.
                let filters: [(Chip.Kind, String, String)] = [
                    (.errors, "usage-filtered", "Devices with errors"),
                    (.silent, "usage-filtered-silent", "Devices not reporting"),
                    (.notUpdating, "usage-filtered-not-updating", "Devices not updating"),
                ]
                for (kind, name, title) in filters {
                    guard let chip = Chip.chips(report).first(where: { $0.kind == kind }) else { continue }
                    // From Usage by subscription, so a filter that left that
                    // list showing under the devices would show here.
                    states.append((name, { $0.tab = .usage; $0.usageMode = .subscriptions }, {
                        $0.tab = .usage
                        $0.deviceFilter = DeviceFilter(title: title, devices: Set(chip.devices), chip: chip.kind)
                    }))
                }
            }
            if let p = report.projects.first {
                states.append(("projects-expanded", { $0.tab = .projects }, { $0.expanded = "proj:\(p.path)" }))
            }
            let calm = try calmStore(data)
            let empty: [(String, Store.Phase)] = [
                ("loading", .loading),
                ("not-installed", .notInstalled),
                ("not-collected", .notCollected),
                ("mismatch", .mismatch(ReportError.schema(3).localizedDescription)),
                ("failed", .failed("ai-usage did not finish in 30 seconds")),
            ]
            for (look, appearance) in [("light", NSAppearance.Name.aqua), ("dark", .darkAqua)] {
                let png = { (name: String) in dir.appendingPathComponent("\(name)-\(look).png") }
                for (name, setUp) in tabs {
                    reset(store)
                    setUp(store)
                    try write(popover(store), appearance, to: png("popover-\(name)"))
                    try write(popover(store, height: .infinity), appearance, to: png("popover-\(name)-full"))
                }
                for (name, setUp, change) in states {
                    reset(store)
                    setUp(store)
                    try write(popover(store), appearance, to: png("popover-\(name)")) { change(store) }
                }
                if let calm {
                    try write(popover(calm), appearance, to: png("popover-calm"))
                }
                for (name, phase) in empty {
                    try write(popover(Store(showing: phase)), appearance, to: png("popover-\(name)"))
                }
                reset(store)
                try write(showcase(store, dark: look == "dark"), appearance, to: png("menubar"))
                try write(ringStates(store.now, dark: look == "dark"), appearance, to: png("menubar-states"))
                for pane in SettingsWindow.panes {
                    let view = pane.view.environmentObject(store).background(Color(nsColor: .windowBackgroundColor))
                    try write(view, appearance, to: png("settings-\(pane.title.lowercased())"))
                }
            }
        } catch {
            fputs("AIUsageBar: \(error.localizedDescription)\n", stderr)
            return 1
        }
        return 0
    }

    private static func reset(_ store: Store) {
        store.popoverClosed()
        store.tab = .limits
        store.usageMode = .devices
        store.period = .week
    }

    /// The key of the first row in Limits.
    private static func firstLimitRow(_ r: Report) -> String? {
        for p in r.team.providers {
            if let a = p.accounts.first(where: { $0.subscription && LimitLine($0) != nil }) {
                return "limits:\(p.provider)/\(a.label)"
            }
        }
        return nil
    }

    /// The report with nothing out or running out, for the calm verdict.
    private static func calmStore(_ data: Data) throws -> Store? {
        guard var json = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let attention = json["attention"] as? [[String: Any]] else { return nil }
        json["attention"] = attention.filter { !["out", "over"].contains($0["kind"] as? String) }
        return Store(demo: try Report.decode(JSONSerialization.data(withJSONObject: json)))
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

    private static func desktop(dark: Bool) -> LinearGradient {
        let colors = dark
            ? [Color(red: 0.11, green: 0.14, blue: 0.26), Color(red: 0.23, green: 0.14, blue: 0.32)]
            : [Color(red: 0.78, green: 0.84, blue: 0.95), Color(red: 0.92, green: 0.85, blue: 0.93)]
        return LinearGradient(colors: colors, startPoint: .top, endPoint: .bottom)
    }

    /// A strip of menu bar the way macOS draws it over a desktop.
    private static func menuBar<Content: View>(dark: Bool, @ViewBuilder _ content: () -> Content) -> some View {
        content()
            .font(.system(size: 13, weight: .medium))
            .padding(.horizontal, 14)
            .frame(maxWidth: .infinity, minHeight: 26, alignment: .trailing)
            .background(dark ? Color.black.opacity(0.35) : Color.white.opacity(0.5))
    }

    /// The picture for the README: the menu bar item open on a desktop.
    private static func showcase(_ store: Store, dark: Bool) -> some View {
        let clock = DateFormatter()
        clock.locale = Locale(identifier: "en_US_POSIX")
        clock.dateFormat = "EEE d MMM HH:mm"
        return VStack(alignment: .trailing, spacing: 6) {
            menuBar(dark: dark) {
                HStack(spacing: 16) {
                    MenuBarLabel(summary: store.summary, shows: .percent, now: store.now)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                        .background(RoundedRectangle(cornerRadius: 5).fill(.primary.opacity(0.12)))
                    Text(clock.string(from: store.now))
                }
            }
            popover(store)
                .shadow(color: .black.opacity(dark ? 0.5 : 0.22), radius: 22, y: 10)
                .padding(.horizontal, 40)
                .padding(.bottom, 44)
        }
        .background(desktop(dark: dark))
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
    }

    /// The menu bar item in each of its looks, side by side.
    private static func ringStates(_ now: Date, dark: Bool) -> some View {
        let s = { (used: Double, state: String, stale: Bool) in
            MenuSummary(provider: "claude", label: "mira@studio.dev", name: "mira", window: "7d", percentLeft: max(100 - Int(used), 0),
                        used: used, stale: stale, state: state, resetsAt: now.addingTimeInterval(state == "out" ? 85 * 60 : 2 * 86400 + 9 * 3600))
        }
        let items: [(String, MenuSummary?, MenuBarShows)] = [
            ("unused", s(0, "ok", false), .percent),
            ("on track", s(58, "ok", false), .percent),
            ("tight", s(83, "tight", false), .percent),
            ("used up", s(100, "tight", false), .percent),
            ("stale", s(58, "ok", true), .percent),
            ("over", s(83, "over", false), .percent),
            ("out", s(100, "out", false), .percent),
            ("time", s(58, "ok", false), .time),
            ("no reading", nil, .percent),
            ("icon only", s(58, "ok", false), .icon),
        ]
        return HStack(alignment: .top, spacing: 20) {
            ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                VStack(spacing: 10) {
                    MenuBarLabel(summary: item.1, shows: item.2, now: now)
                        .font(.system(size: 13, weight: .medium))
                        .frame(height: 24)
                    Text(item.0).font(.subheadline).foregroundStyle(.secondary)
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .background(alignment: .top) {
            (dark ? Color.black.opacity(0.35) : Color.white.opacity(0.5)).frame(height: 40)
        }
        .background(desktop(dark: dark))
    }

    /// Lays a view out in an offscreen window and draws it with AppKit, which,
    /// unlike ImageRenderer, draws controls such as pickers and text fields.
    /// A change, when given, comes after the first layout, as a click does.
    private static func write<V: View>(_ view: V, _ appearance: NSAppearance.Name, to url: URL, change: (() -> Void)? = nil) throws {
        let host = NSHostingView(rootView: view)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 600), styleMask: [.borderless], backing: .buffered, defer: false)
        window.appearance = NSAppearance(named: appearance)
        window.backgroundColor = .clear
        window.isOpaque = false
        window.contentView = host
        // Measured heights and state settle over a few turns of the run loop.
        let settle = {
            for _ in 0..<4 {
                window.setContentSize(host.fittingSize)
                host.layoutSubtreeIfNeeded()
                RunLoop.current.run(until: Date().addingTimeInterval(0.05))
            }
        }
        settle()
        if let change {
            change()
            settle()
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
