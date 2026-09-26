import AppKit
import ServiceManagement

/// Opening the app at login: a login item where the system takes one, else
/// a LaunchAgent that launchd loads at the next login.
@MainActor
enum LoginItem {
    enum Status {
        case on, off, needsApproval
    }

    private static let label = "io.github.neoromantic.ai-usage.bar"
    /// The bundle path opening at login was last set up for.
    private static let setUpKey = "loginItemSetUpFor"

    private static var agent: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/LaunchAgents/\(label).plist")
    }

    static var status: Status {
        switch SMAppService.mainApp.status {
        case .enabled: return .on
        case .requiresApproval: return .needsApproval
        default: return FileManager.default.fileExists(atPath: agent.path) ? .on : .off
        }
    }

    static func set(_ on: Bool) throws {
        guard on else {
            try? SMAppService.mainApp.unregister()
            try? FileManager.default.removeItem(at: agent)
            return
        }
        do {
            try SMAppService.mainApp.register()
        } catch {
            // An ad-hoc signed app may not register; launchd runs it anyway.
            try writeAgent()
        }
    }

    /// Turns opening at login on the first time the app runs from a bundle.
    /// After that, the person's choice stands. Every copy of the app shares
    /// its settings, so this is remembered for each place it runs from: a
    /// test build elsewhere does not keep the installed app from it.
    static func setUpOnce() {
        let bundle = Bundle.main.bundleURL.path
        guard bundle.hasSuffix(".app"), UserDefaults.standard.string(forKey: setUpKey) != bundle else { return }
        try? set(true)
        UserDefaults.standard.set(bundle, forKey: setUpKey)
    }

    static func openSystemSettings() {
        SMAppService.openSystemSettingsLoginItems()
    }

    private static func writeAgent() throws {
        guard let program = Bundle.main.executablePath else { return }
        let plist: [String: Any] = [
            "Label": label,
            "Program": program,
            "RunAtLoad": true,
            "LimitLoadToSessionType": "Aqua",
            "ProcessType": "Interactive",
        ]
        try FileManager.default.createDirectory(at: agent.deletingLastPathComponent(), withIntermediateDirectories: true)
        try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0).write(to: agent, options: .atomic)
    }
}
