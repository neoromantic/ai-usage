import AIUsageKit
import AppKit
import SwiftUI

/// The popover's tabs. Usage is the team's matrix of devices against
/// subscriptions, or with this device alone its accounts; Devices, the
/// team's devices' status, is not there alone.
enum Tab: String, CaseIterable, Identifiable {
    case subscriptions, usage, devices, projects

    var id: Self { self }
    var title: String { rawValue.capitalized }

    static func shown(solo: Bool) -> [Tab] {
        solo ? [.subscriptions, .usage, .projects] : allCases
    }
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
    /// The time the popover measures ages from: the report's own in a demo.
    @Published private(set) var now = Date()
    @Published private(set) var cliPath: String?

    // What the popover shows, kept while the app runs.
    @Published var tab = Tab.subscriptions
    @Published var period = Period.week
    @Published var share = false

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
    }

    /// A store without a report, for pictures of the popover's empty states.
    init(showing phase: Phase) {
        demo = true
        self.phase = phase
    }

    var summary: MenuSummary? { report.flatMap(MenuSummary.pick) }

    /// The tab to show: with this device alone, Devices is not there.
    func shownTab(solo: Bool) -> Tab {
        solo && tab == .devices ? .usage : tab
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
    /// run or the Refresh button.
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
    /// reads the report again.
    func update() async {
        guard !demo, !updating, let cli = locate() else { return }
        updating = true
        defer { updating = false }
        do {
            _ = try await cli.run(["update"], timeout: 300)
            await refresh()
        } catch {
            phase = .failed(error.localizedDescription)
        }
    }

    private func load(_ args: [String], timeout: TimeInterval) async {
        guard let cli = locate() else {
            report = nil
            phase = .notInstalled
            return
        }
        do {
            report = try Report.decode(try await cli.run(args, timeout: timeout))
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
