import AIUsageKit
import AppKit
import UserNotifications

/// Notifications when a window is out or runs out before its reset, as the
/// report's attention says and Settings asks for: once a period and state,
/// as `NoticeLedger` keeps track, which the app's settings keep over a
/// restart. Permission is asked when there is first something to say, or
/// when a notification is turned on; once it is denied, the app says
/// nothing and asks no more, and Settings says where to turn it on. A
/// click on one opens the popover on its account.
@MainActor
final class Notifier: NSObject, ObservableObject, UNUserNotificationCenterDelegate {
    static let shared = Notifier()
    private static let ledgerKey = "windowNotices"

    /// What the system lets the app do, for Settings; nil until asked.
    @Published private(set) var status: UNAuthorizationStatus?

    /// The system keeps notifications by the bundle's id, so a build run
    /// outside a bundle, such as the renderer, has none.
    static var available: Bool {
        Bundle.main.bundleURL.pathExtension == "app" && Bundle.main.bundleIdentifier != nil
    }

    /// Hears the clicks, and shows notifications while the app is in front
    /// too, as when Settings is open.
    func start() {
        guard Self.available else { return }
        UNUserNotificationCenter.current().delegate = self
    }

    /// Posts the notices a report calls for that were not said yet.
    func observe(_ r: Report) {
        let d = UserDefaults.standard
        let settings = NoticeSettings(d)
        guard Self.available, !settings.off else { return }
        var ledger = d.data(forKey: Self.ledgerKey).flatMap { try? JSONDecoder().decode(NoticeLedger.self, from: $0) } ?? NoticeLedger()
        let now = Date()
        let due = ledger.record(WindowNotice.notices(r, settings, now: now), now: now)
        if let data = try? JSONEncoder().encode(ledger) {
            d.set(data, forKey: Self.ledgerKey)
        }
        guard !due.isEmpty else { return }
        Task { await post(due) }
    }

    /// Asks for permission, when a notification is turned on and the
    /// system has not asked yet.
    func enable() {
        guard Self.available else { return }
        Task {
            _ = await authorized()
        }
    }

    /// Reads what the system lets the app do, for Settings.
    func refreshStatus() {
        guard Self.available else { return }
        Task {
            status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        }
    }

    /// The app's page in Notifications settings.
    func openSystemSettings() {
        let id = Bundle.main.bundleIdentifier ?? ""
        if let url = URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=\(id)") {
            NSWorkspace.shared.open(url)
        }
    }

    private func post(_ notices: [WindowNotice]) async {
        guard await authorized() else { return }
        let center = UNUserNotificationCenter.current()
        for n in notices {
            let content = UNMutableNotificationContent()
            content.title = n.title
            content.body = n.body
            content.threadIdentifier = n.thread
            content.sound = .default
            content.userInfo = ["provider": n.provider, "label": n.label]
            // One notification a window: out takes the place of runs out.
            try? await center.add(UNNotificationRequest(identifier: n.key, content: content, trigger: nil))
        }
    }

    /// Whether the app may post, asking the first time. The system asks
    /// once; after that, the answer stands until the person changes it in
    /// System Settings.
    private func authorized() async -> Bool {
        let center = UNUserNotificationCenter.current()
        var s = await center.notificationSettings().authorizationStatus
        if s == .notDetermined {
            _ = try? await center.requestAuthorization(options: [.alert, .sound])
            s = await center.notificationSettings().authorizationStatus
        }
        status = s
        return s == .authorized || s == .provisional
    }

    /// Opens the popover on an account's row in Limits, as the verdict's
    /// click does. A menu bar too full to show the item has nowhere to open
    /// it; the row is then open when the item's next click opens it.
    private func open(provider: String, label: String) {
        let store = Store.shared
        if store.popoverWindow?.isVisible != true {
            Self.statusButton()?.performClick(nil)
        }
        let key = "limits:\(provider)/\(label)"
        store.tab = .limits
        store.expanded = key
        // The list scrolls once the popover has laid it out.
        Task {
            try? await Task.sleep(for: .milliseconds(200))
            store.scrollTarget = key
        }
    }

    /// The menu bar item's button, which MenuBarExtra keeps to itself.
    private static func statusButton() -> NSStatusBarButton? {
        func find(_ v: NSView) -> NSStatusBarButton? {
            (v as? NSStatusBarButton) ?? v.subviews.lazy.compactMap(find).first
        }
        return NSApp.windows.lazy.compactMap { $0.contentView.flatMap(find) }.first
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list, .sound])
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        let info = response.notification.request.content.userInfo
        if response.actionIdentifier == UNNotificationDefaultActionIdentifier,
           let provider = info["provider"] as? String, let label = info["label"] as? String {
            Task { @MainActor in self.open(provider: provider, label: label) }
        }
        completionHandler()
    }
}
