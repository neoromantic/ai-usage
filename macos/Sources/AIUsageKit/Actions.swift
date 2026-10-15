import Foundation

/// What the app does from its menu and the popover's own buttons: each
/// action's words, symbol, and shortcut. The menu groups them in sections
/// under headers, after the collector's status.
public enum AppAction: Hashable, Sendable {
    case collect
    /// ai-usage update, to the release the collector found when it names one.
    case update(release: String?)
    case team
    /// The report, the device status view, and every project, in Terminal.
    case report, devices, projects
    case settings
    case quit

    public var title: String {
        switch self {
        case .collect: return "Collect Now"
        case .update(let release): return "Update ai-usage" + (release.map { " to \($0)" } ?? "") + "…"
        case .team: return "Team Window…"
        case .report: return "Report"
        case .devices: return "Device Status"
        case .projects: return "All Projects"
        case .settings: return "Settings…"
        case .quit: return "Quit AI Usage"
        }
    }

    /// The SF Symbol before its title.
    public var symbol: String {
        switch self {
        case .collect: return "arrow.clockwise"
        case .update: return "arrow.down.circle"
        case .team: return "person.2"
        case .report: return "terminal"
        case .devices: return "list.bullet.rectangle"
        case .projects: return "folder"
        case .settings: return "gearshape"
        case .quit: return "power"
        }
    }

    /// The key that runs it with ⌘, or none.
    public var key: String {
        switch self {
        case .collect: return "r"
        case .team: return "d"
        case .settings: return ","
        case .quit: return "q"
        default: return ""
        }
    }

    /// The command a Terminal action runs, and the name of its window.
    public var terminal: (args: [String], name: String)? {
        switch self {
        case .report: return (["report"], "AI Usage Report")
        case .devices: return (["report", "--devices"], "AI Usage Devices")
        case .projects: return (["report", "--projects"], "AI Usage Projects")
        default: return nil
        }
    }
}

/// The menu's sections of actions, and the actions the popover shows as
/// buttons of their own. The Team window is a team's alone: with one device,
/// Usage already shows it whole, and the device status view has one line.
public enum AppMenu {
    public struct Section: Equatable, Sendable {
        public let title: String
        public let actions: [AppAction]
    }

    /// With a report, its actions, Terminal's views, and the app's; before
    /// there is one, the app's alone. update is the collector's release to
    /// install, or its failed check, when there is one.
    public static func sections(_ r: Report?, update: Collector.Health? = nil) -> [Section] {
        let app = Section(title: "AI Usage", actions: [.settings, .quit])
        guard let r else { return [app] }
        let actions: [AppAction] = [.collect] + (update.map { [.update(release: $0.release)] } ?? []) + (r.solo ? [] : [.team])
        let terminal: [AppAction] = r.solo ? [.report, .projects] : [.report, .devices, .projects]
        return [Section(title: "Actions", actions: actions), Section(title: "Open in Terminal", actions: terminal), app]
    }

    /// The most lines of entries the menu shows, so that it stays shorter
    /// than the screen; the rest are a line of their count.
    public static let maxEntries = 8

    /// The entries the menu shows as lines, and how many more there are:
    /// all of them up to the most it shows, else one fewer and a line of
    /// the rest.
    public static func capped<T>(_ entries: [T]) -> (shown: [T], more: Int) {
        guard entries.count > maxEntries else { return (entries, 0) }
        return (Array(entries.prefix(maxEntries - 1)), entries.count - maxEntries + 1)
    }

    /// The buttons beside the popover's tabs: the Team window, for a team.
    public static func buttons(_ r: Report) -> [AppAction] {
        r.solo ? [] : [.team]
    }
}

/// The app's name and version in Settings' About, said once, and a line
/// about the command-line tool only when its release is not the app's:
/// ai-usage update, which Check for Updates runs, brings both to one.
public struct AboutVersion: Equatable, Sendable {
    /// The app's version, as "0.6.0", or "dev" for a build from source.
    public let version: String
    /// What differs about the command-line tool, or nothing.
    public let note: String?

    /// app is the bundle's version, as "0.6.0"; cli is what ai-usage version
    /// prints, as "v0.6.0", or nil before it is known; homebrew is whether
    /// the report says Homebrew installed it.
    public init(app: String, cli: String?, found: Bool, homebrew: Bool = false) {
        version = app
        if !found {
            note = "Command-line tool not found"
        } else if let cli, !cli.isEmpty, Self.bare(cli) != Self.bare(app) {
            note = homebrew
                ? "Command-line tool \(cli) (Homebrew): updates with Check for Updates or brew upgrade"
                : "Command-line tool \(cli): updates with Check for Updates"
        } else {
            note = nil
        }
    }

    /// A release without its leading "v", as a bundle's version has it.
    public static func bare(_ v: String) -> String {
        v.hasPrefix("v") ? String(v.dropFirst()) : v
    }
}
