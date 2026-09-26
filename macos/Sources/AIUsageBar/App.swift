import AIUsageKit
import AppKit
import SwiftUI

struct AIUsageApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @ObservedObject private var store = Store.shared
    @AppStorage(MenuBarShows.key) private var shows = MenuBarShows.percent.rawValue

    init() {
        let d = UserDefaults.standard
        if let m = MenuBarShows.migrated(showPercent: d.object(forKey: "showPercent") as? Bool, shows: d.string(forKey: MenuBarShows.key)) {
            d.set(m.rawValue, forKey: MenuBarShows.key)
        }
    }

    var body: some Scene {
        MenuBarExtra {
            PopoverView().environmentObject(store)
        } label: {
            MenuBarLabel(summary: store.summary, shows: MenuBarShows(rawValue: shows) ?? .percent, now: store.now)
        }
        .menuBarExtraStyle(.window)
    }
}

/// The menu bar item: a ring of what is left of the window with the least
/// left, among those that limit this Mac's subscriptions, as a battery shows
/// its charge, and what is left of it or how long until it resets.
struct MenuBarLabel: View {
    let summary: MenuSummary?
    let shows: MenuBarShows
    let now: Date

    var body: some View {
        HStack(spacing: 3) {
            Image(nsImage: MenuRing.image(left: summary.map { Double($0.percentLeft) }, state: summary?.state ?? "unknown", empty: summary == nil))
            if let s = summary, let text = s.text(shows, now: now) {
                Text(text).monospacedDigit()
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(summary.map { "AI Usage: \($0.spoken(now: now))" } ?? "AI Usage")
    }
}

/// The ring the menu bar item draws: its arc is the percent left, from 12
/// o'clock clockwise, over a faint track, so it drains as the window does
/// and agrees with the number beside it. It takes the menu bar's color, as
/// a template, unless its window is over or out. Over is a tint of orange
/// under a solid orange arc. Out, the most urgent, is the strongest mark:
/// a solid red disc with a white cross, as nothing is left to drain. Tight
/// stays in the menu bar's color, since yellow does not read on a light
/// menu bar. With no reading, the ring is dashed and empty.
enum MenuRing {
    static func image(left: Double?, state: String, empty: Bool) -> NSImage {
        let out = !empty && state == "out"
        let tint: NSColor? = empty ? nil : out ? .systemRed : state == "over" ? .systemOrange : nil
        let fraction = min(max(left ?? 0, 0), 100) / 100
        let image = NSImage(size: NSSize(width: 16, height: 16), flipped: false) { rect in
            // The colors resolve as the image draws, in the menu bar's
            // appearance.
            let disc = NSRect(x: rect.midX - 7, y: rect.midY - 7, width: 14, height: 14)
            if out {
                NSColor.systemRed.setFill()
                NSBezierPath(ovalIn: disc).fill()
                let cross = NSBezierPath()
                let r: CGFloat = 3
                cross.move(to: NSPoint(x: rect.midX - r, y: rect.midY - r))
                cross.line(to: NSPoint(x: rect.midX + r, y: rect.midY + r))
                cross.move(to: NSPoint(x: rect.midX - r, y: rect.midY + r))
                cross.line(to: NSPoint(x: rect.midX + r, y: rect.midY - r))
                cross.lineWidth = 1.75
                cross.lineCapStyle = .round
                NSColor.white.setStroke()
                cross.stroke()
                return true
            }
            let circle = NSBezierPath(ovalIn: disc.insetBy(dx: 1, dy: 1))
            circle.lineWidth = 2
            if empty {
                circle.setLineDash([2, 2], count: 2, phase: 0)
            }
            (tint ?? .black).withAlphaComponent(0.3).setStroke()
            circle.stroke()
            guard !empty, fraction > 0 else { return true }
            let arc = NSBezierPath()
            arc.appendArc(withCenter: NSPoint(x: rect.midX, y: rect.midY), radius: 6, startAngle: 90, endAngle: 90 - 360 * fraction, clockwise: true)
            arc.lineWidth = 2
            arc.lineCapStyle = fraction < 1 ? .round : .butt
            (tint ?? .black).setStroke()
            arc.stroke()
            return true
        }
        image.isTemplate = tint == nil
        image.accessibilityDescription = "AI Usage"
        return image
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private let version = Bundle.main.shortVersion
    private var timer: Timer?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        guard !Store.shared.demo else { return }
        LoginItem.setUpOnce()
        Store.shared.start()
        timer = Timer.scheduledTimer(withTimeInterval: 30, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.relaunchIfUpdated() }
        }
    }

    /// Updaters only swap the bundle on disk. When its version is no longer
    /// this one's, the new app starts in place of this one.
    private func relaunchIfUpdated() {
        let bundle = Bundle.main.bundleURL
        guard bundle.pathExtension == "app",
              let info = NSDictionary(contentsOf: bundle.appendingPathComponent("Contents/Info.plist")),
              let onDisk = info["CFBundleShortVersionString"] as? String, onDisk != version else { return }
        let relaunch = Process()
        relaunch.executableURL = URL(fileURLWithPath: "/bin/sh")
        relaunch.arguments = ["-c", "sleep 1; /usr/bin/open \"$0\"", bundle.path]
        guard (try? relaunch.run()) != nil else { return }
        NSApp.terminate(nil)
    }
}
