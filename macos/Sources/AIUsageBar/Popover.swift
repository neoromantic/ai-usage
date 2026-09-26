import AIUsageKit
import AppKit
import SwiftUI

/// The window under the menu bar item. The collector's health, what needs
/// attention, and the tabs stay in place; a tab's content scrolls when the
/// popover would be taller than it may be.
struct PopoverView: View {
    @EnvironmentObject private var store: Store
    @Environment(\.popoverMaxHeight) private var maxHeight
    @ViewState private var chrome: CGFloat = 0
    @ViewState private var content: CGFloat = 0

    var body: some View {
        VStack(spacing: 0) {
            Header().measure(ChromeHeight.self)
            Divider()
            if let report = store.report {
                VStack(alignment: .leading, spacing: 12) {
                    if !report.attention.isEmpty {
                        AttentionList(report: report, now: store.now)
                    }
                    TabPicker(solo: report.solo)
                }
                .padding(.horizontal, Metrics.inset)
                .padding(.top, 10)
                .padding(.bottom, 12)
                .measure(ChromeHeight.self)
                TabStack(report: report)
                    // The two dividers take a point each.
                    .frame(height: max(min(content, maxHeight - chrome - 2), 0))
            } else {
                EmptyStateView()
            }
            Divider()
            Footer().measure(ChromeHeight.self)
        }
        .frame(width: Metrics.width)
        .onPreferenceChange(ChromeHeight.self) { chrome = $0 }
        .onPreferenceChange(ContentHeight.self) { content = $0 }
        .onAppear { store.popoverOpened() }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didBecomeKeyNotification)) { _ in
            store.popoverOpened()
        }
    }
}

/// The heights of the parts that stay in place, added up.
private struct ChromeHeight: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value += nextValue()
    }
}

/// The height of the tallest tab.
private struct ContentHeight: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value = max(value, nextValue())
    }
}

private extension View {
    func measure<K: PreferenceKey>(_ key: K.Type) -> some View where K.Value == CGFloat {
        background(GeometryReader { g in Color.clear.preference(key: key, value: g.size.height) })
    }
}

/// The tabs, and ⌘1 to ⌘4 to pick one while the popover is open.
private struct TabPicker: View {
    @EnvironmentObject private var store: Store
    let solo: Bool

    var body: some View {
        let tabs = Tab.shown(solo: solo)
        Picker("View", selection: Binding(get: { store.shownTab(solo: solo) }, set: { store.tab = $0 })) {
            ForEach(tabs) { Text($0.title).tag($0) }
        }
        .pickerStyle(.segmented)
        .labelsHidden()
        .fixedSize()
        .frame(maxWidth: .infinity)
        .background {
            ForEach(Array(tabs.enumerated()), id: \.element) { i, tab in
                Button(tab.title) { store.tab = tab }
                    .keyboardShortcut(KeyEquivalent(Character(String(i + 1))))
            }
            .opacity(0)
            .allowsHitTesting(false)
            .accessibilityHidden(true)
        }
    }
}

/// Every tab at once, one over the other, with only the chosen one shown.
/// The menu bar item's window takes the size of its content, and a window
/// under the menu bar that changes its height moves and redraws all of
/// itself, which blinks. Tabs of their own heights would do that on every
/// switch; as tall as the tallest, the popover keeps its size, and each tab
/// keeps its scroll position and what was expanded in it.
private struct TabStack: View {
    @EnvironmentObject private var store: Store
    let report: Report

    var body: some View {
        let shown = store.shownTab(solo: report.solo)
        ZStack(alignment: .top) {
            ForEach(Tab.shown(solo: report.solo)) { tab in
                let on = tab == shown
                ScrollView {
                    TabContent(tab: tab, report: report)
                        .padding(.horizontal, Metrics.inset)
                        .padding(.bottom, 12)
                        .measure(ContentHeight.self)
                }
                .opacity(on ? 1 : 0)
                .allowsHitTesting(on)
                // The hidden tabs' controls stay out of the keyboard's way.
                .disabled(!on)
                .accessibilityHidden(!on)
            }
        }
    }
}

private struct TabContent: View {
    @EnvironmentObject private var store: Store
    let tab: Tab
    let report: Report

    var body: some View {
        switch tab {
        case .subscriptions:
            SubscriptionsView(report: report, now: store.now)
        case .usage:
            if report.solo {
                SoloUsageView(report: report)
            } else {
                UsageMatrixView(report: report)
            }
        case .devices:
            DeviceStatusView(report: report, now: store.now)
        case .projects:
            ProjectsView(report: report, now: store.now, home: store.home)
        }
    }
}

// MARK: - Header

private struct Header: View {
    @EnvironmentObject private var store: Store

    var body: some View {
        HStack(alignment: .center, spacing: 12) {
            VStack(alignment: .leading, spacing: 1) {
                Text("AI Usage").font(.headline)
                if let r = store.report {
                    let c = r.collector
                    // The team's fingerprint says nothing at a glance; it is
                    // in the tooltip and in Settings.
                    Text(r.solo ? c.deviceLabel : "\(c.deviceLabel) · \(r.team.devices.count) devices")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .help("This Mac is \(c.deviceLabel) in team \(c.team)")
                }
            }
            Spacer(minLength: 8)
            if let c = store.report?.collector {
                let items = c.health.map { HealthLine($0, c, now: store.now) }
                let problems = items.filter { !$0.ok }
                HStack(spacing: 12) {
                    if !items.isEmpty && problems.isEmpty {
                        Label("Up to date", systemImage: "checkmark.circle")
                            .help(items.map(\.help).joined(separator: "\n"))
                    }
                    ForEach(problems, id: \.text) { h in
                        HStack(spacing: 5) {
                            StatusDot(color: h.color)
                            Text(h.text).lineLimit(1)
                        }
                        .help(h.help)
                        .accessibilityElement(children: .combine)
                    }
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
                // The health is what the header is for; the subtitle has a
                // tooltip, so it gives up its width first.
                .layoutPriority(1)
            }
        }
        .padding(.horizontal, Metrics.inset)
        .padding(.vertical, 10)
    }
}

/// A health entry of the report in a few words, and the rest in its
/// tooltip. The report says how each part of the collector is; the header
/// shows the parts that are not ok, else that all is.
struct HealthLine {
    let ok: Bool
    let color: Color
    let text: String
    let help: String

    init(_ h: Collector.Health, _ c: Collector, now: Date) {
        let ago = h.at.map { Format.ago($0, now: now) } ?? "never"
        let release = h.release ?? "A newer release"
        switch (h.item, h.status) {
        case ("collection", "never"):
            (text, help) = ("Never collected", "ai-usage has not collected on this Mac yet")
        case ("collection", "failed"):
            (text, help) = ("Last run failed", "\(ago): " + (c.lastError ?? "the last run failed"))
        case ("collection", "unscheduled"):
            (text, help) = ("Not scheduled", "The system scheduler does not run ai-usage" + (c.schedule.error.map { ": \($0)" } ?? ""))
        case ("collection", "ok"):
            (text, help) = ("Collected \(ago)", "Collected \(ago); ai-usage collects every 15 minutes")
        case ("relay", "none"):
            (text, help) = ("No relay", "No relay: this Mac does not share its usage")
        case ("relay", "failing"):
            (text, help) = ("Relay failing", c.relay.lastError ?? "The relay fails")
        case ("relay", "pending"):
            (text, help) = ("Relay pending", "The newest snapshot has not reached \(c.relay.url ?? "the relay") yet")
        case ("relay", "ok"):
            (text, help) = ("Shared \(ago)", "Shared with \(c.relay.url ?? "the relay") \(ago)")
        case ("update", "dev"):
            (text, help) = ("Dev build", "ai-usage \(c.version) does not update itself")
        case ("update", "staged"):
            (text, help) = ("\(release) next run", "\(release) is installed; the next collection runs it")
        case ("update", "failed"):
            (text, help) = ("Update failed", c.update.error ?? "The update check failed")
        case ("update", "unchecked"):
            (text, help) = ("Not checked", "ai-usage has not checked for a release yet")
        case ("update", "available"):
            (text, help) = ("\(release) available", "ai-usage \(c.version) installs \(release) on its next check")
        case ("update", "ok"):
            (text, help) = ("Up to date", "ai-usage \(c.version) is the latest release, checked \(ago)")
        default:
            (text, help) = (h.item.capitalized + " " + h.status, "")
        }
        ok = h.state == "ok"
        switch h.state {
        case "error": color = Palette.text("out")
        case "warn": color = Palette.fill("tight")!
        case "info": color = .blue
        case "ok": color = .green
        default: color = .secondary
        }
    }
}

// MARK: - Attention

private struct AttentionList: View {
    let report: Report
    let now: Date
    @ViewState private var expanded = false

    var body: some View {
        let all = report.attention
        let shown = expanded || all.count <= 4 ? all : Array(all.prefix(3))
        VStack(alignment: .leading, spacing: 6) {
            GroupHeading(title: "Needs Attention")
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 8, verticalSpacing: 5) {
                ForEach(shown.indices, id: \.self) { i in
                    let a = shown[i]
                    let text = AttentionText(a, report: report, now: now)
                    GridRow {
                        Image(systemName: a.kind.symbol)
                            .foregroundStyle(Palette.text(a.kind.state))
                            .accessibilityLabel(a.kind.title)
                        Text(text.subject)
                            .fontWeight(.medium)
                            .lineLimit(1)
                        (Text(a.kind.title).state(a.kind.state) + text.line)
                            .font(.callout)
                            // Two lines keep what an error says, and the
                            // tooltip has all of it.
                            .lineLimit(2)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .help(text.help)
                    }
                    .accessibilityElement(children: .combine)
                }
            }
            if all.count > shown.count || expanded {
                MoreButton(title: expanded ? "Show Less" : "Show \(all.count - shown.count) More", expanded: expanded) {
                    expanded.toggle()
                }
                .padding(.leading, 26)
            }
        }
    }
}

/// What an attention entry is about and what to know, in a sentence.
struct AttentionText {
    var subject = ""
    /// What asks for something, first and in the tight color: how long
    /// the devices on an older release have not updated.
    var alert: String?
    var detail = ""
    var help = ""

    /// The words after the entry's kind.
    var line: Text {
        let rest = Text(" · " + detail).foregroundColor(.secondary)
        guard let alert else { return rest }
        return Text(" · ").foregroundColor(.secondary) + Text(alert).foregroundColor(Palette.text("tight")) + rest
    }

    init(_ a: Attention, report r: Report, now: Date) {
        let clock = { (t: Date) in Format.clock(t, now: now) }
        let from = { (t: Date) in Format.duration(abs(t.timeIntervalSince(now))) }
        let stale = a.readingAgeSeconds.map { " · reading \(Format.duration(TimeInterval($0))) old" } ?? ""
        let devices = a.devices ?? []
        switch a.kind {
        case .out, .over, .under:
            let account = a.provider.flatMap { r.teamAccount(provider: $0, label: a.account ?? "") }
            subject = Format.provider(a.provider ?? "") + " " + (a.name ?? a.account ?? "")
            if let w = a.window {
                subject += " · " + (account?.quota?.shortName(w) ?? w)
            }
            help = [a.account, a.window.map { "window \($0)" }].compactMap { $0 }.joined(separator: ", ")
        case .old:
            subject = devices.count <= 2 ? devices.joined(separator: ", ") : "\(devices[0]), \(devices[1]) +\(devices.count - 2)"
        default:
            subject = devices.joined(separator: ", ")
        }
        let pct = a.percent.map { "\(Int($0))%" } ?? "?"
        switch a.kind {
        case .out:
            detail = (a.at.map { "back \(clock($0)) (in \(from($0)))" } ?? "until its reset") + stale
        case .over:
            if let at = a.at, let reset = a.resetsAt, reset.timeIntervalSince(at) >= 60 {
                detail = (at > now ? "runs out" : "ran out") + " ~\(clock(at)), \(Format.duration(reset.timeIntervalSince(at))) before reset" + stale
            } else {
                detail = "runs out at its reset" + stale
            }
            help += "\nAt its pace so far it reaches \(pct) by its reset" + (a.resetsAt.map { ", \(clock($0))" } ?? "")
        case .under:
            let unused = a.percent.map { "~\(max(100 - Int($0), 0))% would go unused" } ?? "some would go unused"
            detail = unused + (a.resetsAt.map { " · resets \(clock($0))" } ?? "") + stale
            help += "\nAt its pace so far it reaches \(pct) of the subscription by its reset" + (a.resetsAt.map { ", in \(from($0))" } ?? "")
        case .error:
            detail = a.message ?? "failing"
            help = detail
        case .silent:
            detail = a.at.map { "no report since \(clock($0)) (\(from($0)) ago)" } ?? "no report for a day"
            if let m = a.message {
                detail += " · last error: \(m)"
            }
            help = detail
        case .old:
            let versions = Set(devices.compactMap { r.teamDevice(named: $0)?.collectorVersion }).sorted()
            let latest = a.message.map { "latest \($0)" } ?? "a newer release is out"
            detail = devices.count == 1 && versions.count == 1
                ? "on \(versions[0]) · \(latest)"
                : "\(devices.count) devices on " + (versions.count == 1 ? versions[0] : "older releases") + " · \(latest)"
            // The report sets the time once a device has had the runs to
            // update and has not.
            if let at = a.at {
                alert = "not updated for " + (devices.count > 1 ? "up to " : "") + from(at)
            }
            help = devices.joined(separator: ", ")
        case .other:
            detail = a.message ?? ""
        }
        help = help.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

// MARK: - Footer

private struct Footer: View {
    @EnvironmentObject private var store: Store

    var body: some View {
        HStack(spacing: 6) {
            if store.collecting {
                Text("Collecting…")
            } else if let at = store.report?.collector.lastRunAt {
                Text("Updated \(Format.ago(at, now: store.now))")
                    .help("The last collection on this Mac, \(Format.clock(at, now: store.now))")
            }
            if let e = store.refreshError {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(Palette.text("over"))
                    .help("Could not read the report: \(e)")
                    .accessibilityLabel("Could not read the report: \(e)")
            }
            Spacer()
            Button {
                Task { await store.collect() }
            } label: {
                Group {
                    if store.collecting {
                        ProgressView().controlSize(.small)
                    } else {
                        Image(systemName: "arrow.clockwise")
                    }
                }
                .frame(width: 24, height: 24)
                .contentShape(Rectangle())
            }
            .buttonStyle(.borderless)
            .disabled(store.collecting || store.phase == .notInstalled)
            .keyboardShortcut("r")
            .help("Collect now (⌘R)")
            .accessibilityLabel("Collect Now")
            Menu {
                Button("Settings…") { SettingsWindow.show() }
                    .keyboardShortcut(",")
                Divider()
                Button("Quit AI Usage") { NSApp.terminate(nil) }
                    .keyboardShortcut("q")
            } label: {
                Image(systemName: "gearshape")
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .frame(width: 24, height: 24)
            .help("Settings and Quit")
            .accessibilityLabel("Settings and Quit")
        }
        .font(.subheadline)
        .foregroundStyle(.secondary)
        .padding(.leading, Metrics.inset)
        .padding(.trailing, Metrics.inset - 4)
        .padding(.vertical, 6)
        .background {
            // The shortcuts work while the popover is open, without the menu.
            Group {
                Button("Settings") { SettingsWindow.show() }.keyboardShortcut(",")
                Button("Quit") { NSApp.terminate(nil) }.keyboardShortcut("q")
            }
            .opacity(0)
            .allowsHitTesting(false)
            .accessibilityHidden(true)
        }
    }
}

// MARK: - Empty states

private struct EmptyStateView: View {
    @EnvironmentObject private var store: Store
    static let install = "curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | sh"
    @ViewState private var copied = false

    var body: some View {
        VStack(spacing: 10) {
            switch store.phase {
            case .loading, .ready:
                ProgressView().controlSize(.small)
            case .notInstalled:
                symbol("terminal")
                Text("Install ai-usage").font(.headline)
                explain("AI Usage shows what the ai-usage command collects. Install it in Terminal:")
                Text(Self.install)
                    .font(.system(.caption, design: .monospaced))
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
                    .help(Self.install)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(.quaternary.opacity(0.6), in: RoundedRectangle(cornerRadius: 6))
                Button(copied ? "Copied" : "Copy Command") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(Self.install, forType: .string)
                    copied = true
                }
                .buttonStyle(.borderedProminent)
            case .notCollected:
                symbol("chart.bar.xaxis")
                Text("Nothing Collected Yet").font(.headline)
                explain("Collect this Mac's usage now. After that, ai-usage collects every 15 minutes.")
                busyButton(store.collecting ? "Collecting…" : "Collect Now", busy: store.collecting) {
                    await store.collect()
                }
            case .mismatch(let message):
                symbol("arrow.triangle.2.circlepath")
                Text("Update Needed").font(.headline)
                explain("AI Usage and ai-usage are different versions. Updating brings both to the latest release.")
                busyButton(store.updating ? "Updating…" : "Update Now", busy: store.updating) {
                    await store.update()
                }
                Text(message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .textSelection(.enabled)
            case .failed(let message):
                symbol("exclamationmark.triangle")
                Text("Can't Read the Report").font(.headline)
                explain(message)
                    .textSelection(.enabled)
                Button("Try Again") { Task { await store.refresh() } }
            }
        }
        .font(.callout)
        .padding(24)
        .frame(maxWidth: .infinity, minHeight: 160)
    }

    private func symbol(_ name: String) -> some View {
        Image(systemName: name)
            .font(.system(size: 28, weight: .light))
            .foregroundStyle(.secondary)
            .padding(.bottom, 2)
    }

    private func explain(_ text: String) -> some View {
        Text(text)
            .multilineTextAlignment(.center)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }

    private func busyButton(_ title: String, busy: Bool, action: @escaping () async -> Void) -> some View {
        Button {
            Task { await action() }
        } label: {
            HStack(spacing: 6) {
                if busy {
                    ProgressView().controlSize(.small)
                }
                Text(title)
            }
        }
        .buttonStyle(.borderedProminent)
        .disabled(busy)
    }
}
