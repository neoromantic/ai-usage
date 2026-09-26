import AIUsageKit
import AppKit
import SwiftUI

struct AIUsageApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @ObservedObject private var store = Store.shared
    @AppStorage("showPercent") private var showPercent = true

    var body: some Scene {
        MenuBarExtra {
            PopoverView().environmentObject(store)
        } label: {
            MenuBarLabel(summary: store.summary, showPercent: showPercent)
        }
        .menuBarExtraStyle(.window)
    }
}

/// The menu bar item: a gauge whose needle follows the window with the
/// least left among those that limit this Mac's subscriptions, and what is
/// left of it.
struct MenuBarLabel: View {
    let summary: MenuSummary?
    let showPercent: Bool

    var body: some View {
        let gauge = Image(systemName: MenuSummary.symbol(used: summary?.used))
        Group {
            if showPercent, let s = summary {
                HStack(spacing: 3) {
                    gauge
                    Text(s.text).monospacedDigit()
                }
            } else {
                gauge
            }
        }
        .accessibilityLabel(summary.map { "AI Usage: \($0.spoken)" } ?? "AI Usage")
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
