// AIUsageBar is the menu bar app. Two hidden modes help to build it:
//
//   AIUsageBar --render REPORT.json OUTDIR   draw the popover, menu bar, and settings to PNGs
//   AIUsageBar --demo REPORT.json            run on a saved report, without ai-usage
import AIUsageKit
import AppKit

let args = CommandLine.arguments

func report(at path: String) -> Report {
    do {
        return try Report.decode(Data(contentsOf: URL(fileURLWithPath: path)))
    } catch {
        fputs("AIUsageBar: \(path): \(error.localizedDescription)\n", stderr)
        exit(1)
    }
}

MainActor.assumeIsolated {
    if let i = args.firstIndex(of: "--render") {
        guard i + 2 < args.count else {
            fputs("usage: AIUsageBar --render REPORT.json OUTDIR\n", stderr)
            exit(2)
        }
        exit(Renderer.run(URL(fileURLWithPath: args[i + 1]), into: URL(fileURLWithPath: args[i + 2])))
    }
    if let i = args.firstIndex(of: "--demo"), i + 1 < args.count {
        Store.shared = Store(demo: report(at: args[i + 1]))
    } else if let id = Bundle.main.bundleIdentifier,
              NSRunningApplication.runningApplications(withBundleIdentifier: id).contains(where: { $0 != .current }) {
        // One app at a time: a second launch, such as at login, leaves.
        exit(0)
    }
}
AIUsageApp.main()
