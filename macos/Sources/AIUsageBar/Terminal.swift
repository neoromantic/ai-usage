import AppKit

/// Runs a script in a Terminal window, from a `.command` file the app
/// writes, so the person sees what runs and how it ends.
@MainActor
enum Terminal {
    static func open(_ script: String, named name: String) {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(name + ".command")
        guard (try? script.write(to: url, atomically: true, encoding: .utf8)) != nil,
              (try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)) != nil,
              let terminal = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.apple.Terminal") else { return }
        NSWorkspace.shared.open([url], withApplicationAt: terminal, configuration: NSWorkspace.OpenConfiguration())
    }
}
