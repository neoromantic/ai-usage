import AIUsageKit
import AppKit
import SwiftUI

/// The popover's tabs, the same for a team and for one Mac: how much is
/// left of each subscription, where the tokens go by device or by
/// subscription, and this Mac's projects.
enum Tab: String, CaseIterable, Identifiable {
    case limits, usage, projects

    var id: Self { self }
    var title: String { rawValue.capitalized }
}

/// What the team's Usage tab lists.
enum UsageMode: String, CaseIterable, Identifiable {
    case devices, subscriptions

    var id: Self { self }
    var menuTitle: String {
        switch self {
        case .devices: return "By Device"
        case .subscriptions: return "By Subscription"
        }
    }
}

/// Usage narrowed to the devices a chip is about.
struct DeviceFilter: Equatable {
    let title: String
    let devices: Set<String>
    /// The chip that set it, which shows as chosen.
    var chip: Chip.Kind?
}

/// The report and everything the app does with `ai-usage`.
@MainActor
final class Store: ObservableObject {
    static var shared = Store()

    enum Phase: Equatable {
        case loading, ready, notInstalled, notCollected
        case failed(String)
        /// The command prints another schema than the app reads.
        case mismatch(String)
    }

    @Published private(set) var report: Report?
    @Published private(set) var phase = Phase.loading
    /// Why the last refresh failed while an older report is still shown.
    @Published private(set) var refreshError: String?
    @Published private(set) var collecting = false
    @Published private(set) var updating = false
    /// What the last update said, for Settings: "Up to date (v0.3.1)", or
    /// why it failed.
    @Published private(set) var updateResult: (text: String, failed: Bool)?
    /// The time the popover measures ages from: the report's own in a demo.
    @Published private(set) var now = Date()
    @Published private(set) var cliPath: String?

    // What the popover shows, kept while the app runs.
    /// Picking a tab, or anything that opens one, closes the alerts list.
    @Published var tab = Tab.limits {
        didSet { showsAlerts = false }
    }
    /// The period of Usage and Projects.
    @Published var period = Period.week
    @Published var usageMode = UsageMode.devices
    // What the popover opens up, cleared when it closes, so that it opens
    // at the height of its collapsed rows.
    /// The one row whose details show: "limits:claude/mira@studio.dev",
    /// "dev:<device id>", "col:<index>", "proj:<path>", "solo:<provider>/<label>".
    @Published var expanded: String?
    @Published var deviceFilter: DeviceFilter?
    @Published var showAllProjects = false
    /// Whether every alert shows in place of the tab's body.
    @Published var showsAlerts = false
    /// A row to scroll to in Limits, set with the verdict's click.
    @Published var scrollTarget: String?

    /// The popover's window, and the views the menu opens under: the
    /// status, and the menu's own button.
    weak var popoverWindow: NSWindow?
    weak var statusAnchor: NSView?
    weak var menuAnchor: NSView?

    let demo: Bool
    private var fetchedAt = Date.distantPast
    private var loading = false
    private var again = false
    private var timer: Timer?

    init() {
        demo = false
    }

    init(demo report: Report) {
        demo = true
        self.report = report
        phase = .ready
        now = report.generatedAt
        // The path the launch agent names under Homebrew, or where the
        // installer puts ai-usage in the report's user's home.
        cliPath = report.collector.update.homebrew ? "/opt/homebrew/opt/ai-usage/bin/ai-usage" : home + "/.local/bin/ai-usage"
    }

    /// A store without a report, for pictures of the popover's empty states.
    init(showing phase: Phase) {
        demo = true
        self.phase = phase
    }

    /// What the menu bar item shows, of this Mac's subscriptions or the
    /// team's.
    func summary(_ tracks: MenuBarTracks) -> MenuSummary? {
        report.flatMap { MenuSummary.pick($0, tracks: tracks) }
    }

    /// The collector's update, when there is one to install or the last
    /// check failed.
    var updateHealth: Collector.Health? {
        report?.collector.health.first { $0.item == "update" && ($0.status == "available" || $0.status == "failed") }
    }

    /// This app's version; in a demo, the report's release, as an app that
    /// ai-usage installed has.
    var appVersion: String {
        demo ? report.map { AboutVersion.bare($0.collector.version) } ?? "dev" : Bundle.main.shortVersion
    }

    /// Whether the popover shows its rows as they first open: none
    /// expanded, none filtered, none added. Only then is it measured.
    var collapsed: Bool { expanded == nil && deviceFilter == nil && !showAllProjects }

    /// Closes what the popover opened, as it closes.
    func popoverClosed() {
        expanded = nil
        deviceFilter = nil
        showAllProjects = false
        showsAlerts = false
        scrollTarget = nil
    }

    /// Opens a row's details, or closes them.
    func toggle(_ key: String) {
        expanded = expanded == key ? nil : key
    }

    /// The home folder that paths show as "~": this user's, or in a demo
    /// the report's user's.
    var home: String {
        demo ? report.map { "/Users/" + $0.collector.osUser } ?? "" : FileManager.default.homeDirectoryForCurrentUser.path
    }

    func start() {
        guard !demo else { return }
        Task { await refresh() }
        timer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { _ in
            Task { @MainActor in await Store.shared.refresh() }
        }
    }

    /// Reads the report again when the popover opens and the one shown is
    /// more than a few seconds old.
    func popoverOpened() {
        guard !demo else { return }
        now = Date()
        if Date().timeIntervalSince(fetchedAt) > 10 {
            Task { await refresh() }
        }
    }

    /// Reads the last collected report; collecting takes the scheduler's
    /// run or Collect Now.
    func refresh() async {
        guard !demo else { return }
        guard !loading, !collecting else {
            again = true
            return
        }
        loading = true
        await load(["report", "--json"], timeout: 30)
        loading = false
        if again {
            again = false
            await refresh()
        }
    }

    /// Collects now. The JSON form prints the fresh report and leaves the
    /// first-run guide for the terminal.
    func collect() async {
        guard !demo, !collecting else { return }
        collecting = true
        await load(["collect", "--json"], timeout: 300)
        collecting = false
    }

    /// Brings ai-usage, and with it this app, to the latest release, then
    /// reads the report again. Under Homebrew, ai-usage update gives brew up
    /// to 8 minutes and the binary brew installed 4 more to finish, so the
    /// app waits 15.
    func update() async {
        guard !demo, !updating, let cli = locate() else { return }
        updating = true
        updateResult = nil
        defer { updating = false }
        do {
            let out = String(decoding: try await cli.run(["update"], timeout: 900), as: UTF8.self)
                .trimmingCharacters(in: .whitespacesAndNewlines)
            updateResult = (out.prefix(1).uppercased() + out.dropFirst(), false)
            await refresh()
        } catch {
            updateResult = (error.localizedDescription, true)
            if report == nil {
                phase = .failed(error.localizedDescription)
            }
        }
    }

    private func load(_ args: [String], timeout: TimeInterval) async {
        guard let cli = locate() else {
            report = nil
            phase = .notInstalled
            return
        }
        do {
            let r = try Report.decode(try await cli.run(args, timeout: timeout))
            report = r
            Notifier.shared.observe(r)
            phase = .ready
            refreshError = nil
            fetchedAt = Date()
        } catch let CLIError.failed(_, message) where message.contains("nothing collected yet") {
            report = nil
            phase = .notCollected
        } catch let error as ReportError {
            report = nil
            phase = .mismatch(error.localizedDescription)
        } catch {
            if report == nil {
                phase = .failed(error.localizedDescription)
            } else {
                refreshError = error.localizedDescription
            }
        }
        now = Date()
    }

    /// Opens ai-usage with args in Terminal, run as the collector runs it.
    func openInTerminal(_ args: [String], named name: String) {
        guard !demo, let cli = locate() else { return }
        Terminal.open(cli.terminalScript(args), named: name)
    }

    private func locate() -> CLI? {
        let location = CLILocation.locate()
        cliPath = location?.executable.path
        return location.map(CLI.init)
    }

    /// Runs a command, such as one that changes a setting, and returns what
    /// it printed. A change is read back with the next report. The error is
    /// ai-usage's own words, for the control that ran the command.
    @discardableResult
    func command(_ args: [String], input: String? = nil, changes: Bool = true, timeout: TimeInterval = 30) async throws -> String {
        guard !demo else { return "" }
        guard let cli = locate() else { throw CLIError.launch("ai-usage is not installed") }
        let out = try await cli.run(args, input: input.map { Data($0.utf8) }, timeout: timeout)
        if changes {
            Task { await refresh() }
        }
        return String(decoding: out, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
