import AIUsageKit
import AppKit
import UserNotifications

/// Notifications when a window is out or runs out before its reset, as the
/// report's attention says and Settings asks for: once a period and state,
/// as `NoticeLedger` keeps track, which the app's settings keep over a
/// restart. The cost's events and questions get one each too, once, in the
/// same ledger; a question's actions answer it with `ai-usage cost`.
/// Permission is asked when there is first something to say, or when a
/// notification is turned on; once it is denied, the app says nothing and
/// asks no more, and Settings says where to turn it on. A click on a
/// window's opens the popover on its account, and on a cost's, the cost in
/// Settings.
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

    /// The cost's notifications' categories, by the question's kind, and
    /// their actions.
    private enum Answer {
        static let stillPaid = "costStillPaid", renewal = "costRenewal", suggested = "costRenewalSuggested", price = "costPrice"
        static let confirm = "costConfirm", cancel = "costCancel", skip = "costSkip", typed = "costTyped", suggestion = "costSuggestion"
    }

    /// Hears the clicks, and shows notifications while the app is in front
    /// too, as when Settings is open. A question's notification has its
    /// answers as actions.
    func start() {
        guard Self.available else { return }
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        let skip = UNNotificationAction(identifier: Answer.skip, title: "Skip")
        let day = UNTextInputNotificationAction(identifier: Answer.typed, title: "Next Charge…", textInputButtonTitle: "Save",
                                                textInputPlaceholder: "YYYY-MM-DD or day of the month")
        center.setNotificationCategories([
            UNNotificationCategory(identifier: Answer.stillPaid, actions: [
                UNNotificationAction(identifier: Answer.confirm, title: "Still Paying"),
                UNNotificationAction(identifier: Answer.cancel, title: "Cancelled", options: .destructive),
                skip,
            ], intentIdentifiers: []),
            UNNotificationCategory(identifier: Answer.renewal, actions: [day, skip], intentIdentifiers: []),
            UNNotificationCategory(identifier: Answer.suggested, actions: [
                UNNotificationAction(identifier: Answer.suggestion, title: "Use the Suggested Day"), day, skip,
            ], intentIdentifiers: []),
            UNNotificationCategory(identifier: Answer.price, actions: [
                UNTextInputNotificationAction(identifier: Answer.typed, title: "Type Price…", textInputButtonTitle: "Save",
                                              textInputPlaceholder: "Dollars each charge, as 20"),
                skip,
            ], intentIdentifiers: []),
        ])
    }

    /// Posts the notices a report calls for that were not said yet.
    func observe(_ r: Report) {
        let d = UserDefaults.standard
        let settings = NoticeSettings(d)
        guard Self.available, !settings.off || !settings.costOff else { return }
        var ledger = d.data(forKey: Self.ledgerKey).flatMap { try? JSONDecoder().decode(NoticeLedger.self, from: $0) } ?? NoticeLedger()
        let now = Date()
        let due = settings.off ? [] : ledger.record(WindowNotice.notices(r, settings, now: now), now: now)
        // A report from an ai-usage without the cost leaves what was said
        // of it.
        let costs = r.cost == nil ? [] : ledger.record(settings.costOff ? [] : CostNotice.notices(r, settings, now: now),
                                                       among: CostNotice.keys(r), now: now)
        if let data = try? JSONEncoder().encode(ledger) {
            d.set(data, forKey: Self.ledgerKey)
        }
        guard !due.isEmpty || !costs.isEmpty else { return }
        Task {
            await post(due)
            await post(costs)
        }
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
        guard !notices.isEmpty, await authorized() else { return }
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

    private func post(_ notices: [CostNotice]) async {
        guard !notices.isEmpty, await authorized() else { return }
        let center = UNUserNotificationCenter.current()
        for n in notices {
            let content = UNMutableNotificationContent()
            content.title = n.title
            content.body = n.body
            content.threadIdentifier = "\(n.provider)/\(n.label)"
            content.sound = .default
            var info: [String: String] = ["provider": n.provider, "label": n.label, "cost": n.key]
            info["question"] = n.questionKind
            info["suggestion"] = n.suggestion
            content.userInfo = info
            switch n.kind {
            case .stillPaid: content.categoryIdentifier = Answer.stillPaid
            case .renewal: content.categoryIdentifier = n.suggestion == nil ? Answer.renewal : Answer.suggested
            case .price: content.categoryIdentifier = Answer.price
            case .change, .chargesSoon: break
            }
            try? await center.add(UNNotificationRequest(identifier: "cost/" + n.key, content: content, trigger: nil))
        }
    }

    /// Runs the answer a question's notification took, and says so when
    /// ai-usage refuses it.
    private func answer(_ args: [String]?, title: String) {
        guard let args else {
            say("Couldn't save the answer", "Type a day, as 2026-10-04, or a day of the month; or a price, as 20. Settings, Cost, asks it too.")
            return
        }
        Task {
            do {
                try await Store.shared.command(args)
            } catch {
                say("Couldn't save the answer to " + title, error.localizedDescription)
            }
        }
    }

    private func say(_ title: String, _ body: String) {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil))
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
        let title = response.notification.request.content.title
        let action = response.actionIdentifier
        let typed = (response as? UNTextInputNotificationResponse)?.userText ?? ""
        guard let provider = info["provider"] as? String, let label = info["label"] as? String else {
            completionHandler()
            return
        }
        if info["cost"] != nil {
            let target = provider + ":" + label, kind = info["question"] as? String ?? "", suggestion = info["suggestion"] as? String
            Task { @MainActor in
                switch action {
                case UNNotificationDefaultActionIdentifier: SettingsWindow.show(pane: .cost)
                case Answer.confirm: self.answer(CostCommand.confirm(target), title: title)
                case Answer.cancel: self.answer(CostCommand.cancel(target), title: title)
                case Answer.skip: self.answer(CostCommand.skip(target, kind), title: title)
                case Answer.suggestion: self.answer(CostCommand.set(target, renews: suggestion), title: title)
                case Answer.typed: self.answer(CostCommand.answer(kind: kind, target: target, text: typed), title: title)
                default: break
                }
            }
        } else if action == UNNotificationDefaultActionIdentifier {
            Task { @MainActor in self.open(provider: provider, label: label) }
        }
        completionHandler()
    }
}
