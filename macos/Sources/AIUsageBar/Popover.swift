import AIUsageKit
import AppKit
import SwiftUI

/// The window under the menu bar item: a verdict on the subscriptions and
/// the collector's status on top, chips for what else asks for something,
/// then three tabs. The body is as tall as Limits, the tab it opens on and
/// the one people open it for; a longer list scrolls inside it. The alerts
/// list takes the body's place at the same height.
struct PopoverView: View {
    @EnvironmentObject private var store: Store
    @Environment(\.popoverMaxHeight) private var maxHeight
    @ViewState private var chrome: CGFloat = 0
    @ViewState private var heights: [Tab: CGFloat] = [:]

    var body: some View {
        VStack(spacing: 0) {
            Header().measure(ChromeHeight.self)
            if let report = store.report {
                let chips = Chip.chips(report, refreshFailed: store.refreshError != nil)
                if !chips.isEmpty {
                    ChipRow(chips: chips, report: report).measure(ChromeHeight.self)
                }
                Divider()
                TabPicker().measure(ChromeHeight.self)
                ZStack(alignment: .top) {
                    TabStack(report: report)
                        .opacity(store.showsAlerts ? 0 : 1)
                        .allowsHitTesting(!store.showsAlerts)
                        .disabled(store.showsAlerts)
                        .accessibilityHidden(store.showsAlerts)
                    if store.showsAlerts {
                        AlertsView(report: report)
                    }
                }
                .frame(height: bodyHeight)
            } else {
                Divider()
                EmptyStateView()
            }
        }
        .frame(width: Metrics.width)
        .background(AnchorView { view in store.popoverWindow = view.window })
        .onPreferenceChange(ChromeHeight.self) { chrome = $0 }
        .onPreferenceChange(TabHeights.self) { measured in
            // The popover keeps the height of its collapsed rows: what a
            // row, a filter, or Show All opens scrolls inside it, since a
            // window under the menu bar that changes its height blinks. The
            // renderer's pictures of a whole tab take what is open too.
            if store.collapsed || heights.isEmpty || maxHeight == .infinity {
                heights = measured
            }
        }
        .onAppear { store.popoverOpened() }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didBecomeKeyNotification)) { n in
            if isPopover(n) { store.popoverOpened() }
        }
        .onReceive(NotificationCenter.default.publisher(for: NSWindow.didResignKeyNotification)) { n in
            if isPopover(n) { store.popoverClosed() }
        }
    }

    /// Limits' height, within the popover's; the renderer's pictures of a
    /// whole tab, with no limit, take that tab's own. The divider takes a
    /// point.
    private var bodyHeight: CGFloat {
        if maxHeight == .infinity {
            return max(heights[store.tab] ?? 0, Metrics.minBody)
        }
        return max(min(max(heights[.limits] ?? 0, Metrics.minBody), maxHeight - chrome - 1), 0)
    }

    private func isPopover(_ n: Notification) -> Bool {
        guard let w = store.popoverWindow else { return true }
        return n.object as? NSWindow === w
    }
}

/// The heights of the parts that stay in place, added up.
private struct ChromeHeight: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value += nextValue()
    }
}

/// Each tab's height as it would be whole: its pinned lines and its list.
private struct TabHeights: PreferenceKey {
    static let defaultValue: [Tab: CGFloat] = [:]
    static func reduce(value: inout [Tab: CGFloat], nextValue: () -> [Tab: CGFloat]) {
        value.merge(nextValue()) { $0 + $1 }
    }
}

private extension View {
    func measure<K: PreferenceKey>(_ key: K.Type) -> some View where K.Value == CGFloat {
        background(GeometryReader { g in Color.clear.preference(key: key, value: g.size.height) })
    }

    func measure(_ tab: Tab) -> some View {
        background(GeometryReader { g in Color.clear.preference(key: TabHeights.self, value: [tab: g.size.height]) })
    }
}

/// An AppKit view in the popover, to reach its window and to open a menu
/// under a button.
private struct AnchorView: NSViewRepresentable {
    let found: @MainActor (NSView) -> Void

    func makeNSView(context: Context) -> NSView {
        let v = AnchorNSView()
        v.found = found
        return v
    }

    func updateNSView(_ view: NSView, context: Context) {}

    final class AnchorNSView: NSView {
        var found: (@MainActor (NSView) -> Void)?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if window != nil { found?(self) }
        }
    }
}

// MARK: - Tabs

/// The tabs, and ⌘1 to ⌘3 to pick one while the popover is open. None is
/// chosen while the alerts list shows; any of them closes it.
private struct TabPicker: View {
    @EnvironmentObject private var store: Store

    var body: some View {
        let selection = Binding<Tab?>(get: { store.showsAlerts ? nil : store.tab }, set: { $0.map { store.tab = $0 } })
        Picker("View", selection: selection) {
            ForEach(Tab.allCases) { Text($0.title).tag(Optional($0)) }
        }
        .pickerStyle(.segmented)
        .controlSize(.regular)
        .labelsHidden()
        .fixedSize()
        .frame(maxWidth: .infinity)
        .padding(.vertical, 10)
        .background {
            ForEach(Array(Tab.allCases.enumerated()), id: \.element) { i, tab in
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
/// switch; at one height, the popover keeps its size, and each tab keeps
/// its scroll position. A tab's top line and total stay put while its list
/// scrolls.
private struct TabStack: View {
    @EnvironmentObject private var store: Store
    let report: Report

    var body: some View {
        ZStack(alignment: .top) {
            ForEach(Tab.allCases) { tab in
                let on = tab == store.tab
                VStack(spacing: 0) {
                    TabTop(tab: tab, report: report).measure(tab)
                    ScrollViewReader { proxy in
                        ScrollView {
                            TabContent(tab: tab, report: report)
                                .padding(.horizontal, Metrics.inset)
                                .padding(.bottom, 12)
                                .measure(tab)
                        }
                        // A scroller that takes room of its own, as System
                        // Settings can ask for, would move every column of
                        // a list that grows past the popover; SwiftUI keeps
                        // that room even under an overlay style, so the
                        // list scrolls without one, and its cut last row
                        // over the total shows there is more.
                        .scrollIndicators(.never)
                        .onChange(of: store.scrollTarget) { _, key in
                            guard tab == .limits, let key else { return }
                            proxy.scrollTo(key, anchor: .top)
                            store.scrollTarget = nil
                        }
                    }
                    TabBottom(tab: tab, report: report).measure(tab)
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
        case .limits:
            LimitsView(report: report, now: store.now)
        case .usage:
            UsageView(report: report, now: store.now)
        case .projects:
            ProjectsView(report: report, now: store.now, home: store.home)
        }
    }
}

/// A tab's line over its list.
private struct TabTop: View {
    let tab: Tab
    let report: Report

    var body: some View {
        switch tab {
        case .limits: EmptyView()
        case .usage: UsageTopLine(report: report)
        case .projects: ProjectsTopLine(report: report)
        }
    }
}

/// A tab's line under its list.
private struct TabBottom: View {
    let tab: Tab
    let report: Report

    var body: some View {
        if tab == .usage {
            UsageTotal(report: report)
        }
    }
}

// MARK: - Header

/// The verdict, and the status and refresh buttons at the end of its title:
/// the line under the title runs under them, so that what it says keeps
/// its room.
private struct Header: View {
    @EnvironmentObject private var store: Store
    @ViewState private var trailing: CGFloat = 0

    var body: some View {
        ZStack(alignment: .topTrailing) {
            if let r = store.report {
                VerdictButton(report: r, trailing: trailing + 8)
            } else {
                let (symbol, title) = emptyTitle
                VerdictLabel(symbol: symbol.map { ($0, Color.secondary) }, title: title, subline: nil, trailing: trailing + 8)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack(alignment: .top, spacing: 8) {
                StatusButton()
                RefreshButton()
            }
            .background(GeometryReader { g in Color.clear.preference(key: TrailingWidth.self, value: g.size.width) })
        }
        .onPreferenceChange(TrailingWidth.self) { trailing = $0 }
        .padding(.top, 12)
        .padding(.bottom, 10)
        .padding(.horizontal, Metrics.inset)
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

    private var emptyTitle: (String?, String) {
        switch store.phase {
        case .loading, .ready: return (nil, "AI Usage")
        case .notInstalled: return ("terminal", "Install ai-usage")
        case .notCollected: return ("chart.bar.xaxis", "Nothing collected yet")
        case .mismatch: return ("arrow.triangle.2.circlepath", "Update needed")
        case .failed: return ("exclamationmark.triangle", "Couldn't read the report")
        }
    }
}

/// The width of the status and refresh buttons.
private struct TrailingWidth: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value = max(value, nextValue())
    }
}

/// A symbol, a title, and a line under it, which ends in room for the
/// alerts link. The title keeps clear of the buttons at its end.
private struct VerdictLabel: View {
    let symbol: (name: String, color: Color)?
    let title: String
    let subline: String?
    var alerts = 0
    let trailing: CGFloat
    /// Whether the title and line take their whole width.
    var fixed = false

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            if let symbol {
                Image(systemName: symbol.name)
                    .symbolRenderingMode(.hierarchical)
                    .font(.system(size: 17))
                    .foregroundStyle(symbol.color)
                    .frame(width: 20)
            }
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                    .font(.title3.weight(.semibold))
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .fixedSize(horizontal: fixed, vertical: false)
                    .padding(.trailing, trailing)
                if alerts > 0 || subline?.isEmpty == false {
                    HStack(spacing: 8) {
                        if let subline, !subline.isEmpty {
                            Text(subline)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .truncationMode(.tail)
                                .fixedSize(horizontal: fixed, vertical: false)
                        }
                        // The link shows over this place, out of the
                        // verdict's button.
                        if alerts > 0 {
                            AlertsLink.text(alerts)
                                .fixedSize()
                                .hidden()
                                .anchorPreference(key: LinkBounds.self, value: .bounds) { $0 }
                        }
                    }
                    .font(.callout)
                }
            }
        }
    }
}

/// Where the alerts link goes in the verdict.
private struct LinkBounds: PreferenceKey {
    static let defaultValue: Anchor<CGRect>? = nil
    static func reduce(value: inout Anchor<CGRect>?, nextValue: () -> Anchor<CGRect>?) {
        value = value ?? nextValue()
    }
}

/// "11 alerts ›" at the end of the verdict's line: a click shows every
/// alert in place of the tab's body, and another closes the list.
private struct AlertsLink: View {
    @EnvironmentObject private var store: Store
    let count: Int

    static func text(_ count: Int) -> Text {
        Text(count == 1 ? "1 alert ›" : "\(count) alerts ›")
    }

    var body: some View {
        Button {
            store.showsAlerts.toggle()
        } label: {
            Self.text(count)
                .font(.callout)
                .foregroundStyle(Color.accentColor)
                .fixedSize()
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(store.showsAlerts ? "Back to \(store.tab.title)" : "Every alert in the report")
        .accessibilityLabel(count == 1 ? "1 alert" : "\(count) alerts")
        .accessibilityHint(store.showsAlerts ? "Closes the list of alerts" : "Shows every alert in the report")
    }
}

/// The verdict on the subscriptions; a click opens its account in Limits.
private struct VerdictButton: View {
    @EnvironmentObject private var store: Store
    let report: Report
    let trailing: CGFloat

    var body: some View {
        let v = Verdict(report, now: store.now)
        let alerts = report.attention.count
        Button {
            store.tab = .limits
            if let p = v.provider, let l = v.label {
                let key = "limits:\(p)/\(l)"
                store.expanded = key
                store.scrollTarget = key
            }
        } label: {
            // Where the line does not fit, it drops how long before its
            // reset the window runs out, and then the time it runs out
            // leads it in place of the title's end; the reading's age
            // stays.
            ViewThatFits(in: .horizontal) {
                VerdictLabel(symbol: symbol(v.kind), title: v.title, subline: v.subline, alerts: alerts, trailing: trailing, fixed: true)
                VerdictLabel(symbol: symbol(v.kind), title: v.title, subline: v.shortSubline, alerts: alerts, trailing: trailing, fixed: true)
                VerdictLabel(symbol: symbol(v.kind), title: v.compactTitle, subline: v.compactSubline, alerts: alerts, trailing: trailing,
                             fixed: true)
                VerdictLabel(symbol: symbol(v.kind), title: v.compactTitle, subline: v.compactSubline, alerts: alerts, trailing: trailing)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(help)
        .accessibilityLabel(v.title + (v.subline.isEmpty ? "" : ". " + v.subline))
        .accessibilityHint("Shows the subscription in Limits")
        .overlayPreferenceValue(LinkBounds.self) { bounds in
            GeometryReader { g in
                if let bounds {
                    let r = g[bounds]
                    AlertsLink(count: alerts)
                        .frame(width: r.width, height: r.height)
                        .offset(x: r.minX, y: r.minY)
                }
            }
        }
    }

    private func symbol(_ kind: Verdict.Kind) -> (String, Color) {
        switch kind {
        case .out: return ("xmark.octagon.fill", .red)
        case .over: return ("exclamationmark.triangle.fill", .orange)
        case .calm: return ("checkmark.circle", .secondary)
        case .none: return ("circle.dashed", .secondary)
        }
    }

    /// Every entry about a subscription that asks for something, in a
    /// sentence of its own, under the word the report's ATTENTION gives it.
    private var help: String {
        let kinds: [Attention.Kind: String] = [.out: "out", .over: "over", .tight: "tight", .outside: "outside", .noReading: "no reading"]
        return report.attention.compactMap { a -> String? in
            guard let word = kinds[a.kind] else { return nil }
            let t = AttentionText(a, report: report)
            return "\(t.subject): \(word)" + (t.detail.isEmpty ? "" : " · \(t.detail)")
        }.joined(separator: "\n")
    }
}

/// The collector's status: a dot in the color of its worst part, and how
/// long ago it collected. A click opens the status menu.
private struct StatusButton: View {
    @EnvironmentObject private var store: Store

    var body: some View {
        Button {
            StatusMenu.show(store)
        } label: {
            Group {
                if let c = store.report?.collector {
                    HStack(spacing: 5) {
                        if store.collecting {
                            ProgressView().controlSize(.mini).frame(width: 7, height: 7)
                        } else {
                            Circle().fill(StatusMenu.dotColor(c)).frame(width: 7, height: 7)
                        }
                        Text(age(c))
                            .font(.callout)
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                    }
                    .padding(.horizontal, 4)
                } else {
                    Image(systemName: "ellipsis.circle")
                        .font(.system(size: 14))
                        .foregroundStyle(.secondary)
                }
            }
            .frame(height: 24)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(AnchorView { view in store.statusAnchor = view })
        .help(help)
        .accessibilityLabel(spoken)
    }

    private func age(_ c: Collector) -> String {
        c.lastRunAt.map { Format.age($0, now: store.now) } ?? Format.none
    }

    private var help: String {
        guard let c = store.report?.collector else { return "Settings and Quit" }
        guard let at = c.lastRunAt else { return "Never collected" }
        return "Updated \(Format.ago(at, now: store.now)) · \(Format.clock(at, now: store.now))"
    }

    private var spoken: String {
        guard let c = store.report?.collector else { return "Settings and Quit" }
        var s = "Status: " + (c.lastRunAt.map { t in
            store.now.timeIntervalSince(t) < 60 ? "collected just now" : "collected \(Format.spokenDuration(store.now.timeIntervalSince(t))) ago"
        } ?? "never collected")
        let problems = c.health.filter { $0.state != "ok" && $0.state != "off" }.map { HealthLine($0, c, now: store.now).text }
        if !problems.isEmpty { s += ", " + problems.joined(separator: ", ") }
        return s
    }
}

private struct RefreshButton: View {
    @EnvironmentObject private var store: Store

    var body: some View {
        Button {
            Task { await store.collect() }
        } label: {
            Group {
                if store.collecting {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: "arrow.clockwise")
                        .font(.system(size: 13, weight: .medium))
                }
            }
            .frame(width: 24, height: 24)
            .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.secondary)
        .disabled(store.collecting || store.phase == .notInstalled)
        .keyboardShortcut("r")
        .help("Collect Now (⌘R)")
        .accessibilityLabel("Collect Now")
    }
}

/// The menu under the status: each part of the collector, this Mac, and
/// what the app does. An AppKit menu, so each item keeps its colored dot.
@MainActor
enum StatusMenu {
    /// The menu, with the entries of the chip it opens from, or in solo,
    /// where no list shows them, with this Mac's errors.
    static func show(_ store: Store, entries: [Attention]? = nil) {
        guard let anchor = store.statusAnchor else { return }
        var shown: [Attention] = []
        if let entries {
            shown = entries
        } else if let r = store.report, r.solo {
            shown = r.attention.filter { $0.kind == .error || $0.kind == .silent }
        }
        let menu = build(store, entries: shown)
        let y = anchor.isFlipped ? anchor.bounds.height + 4 : -4
        menu.popUp(positioning: nil, at: NSPoint(x: 0, y: y), in: anchor)
    }

    /// The worst state among the collector's parts.
    static func worst(_ c: Collector) -> String {
        let states = Set(c.health.map(\.state))
        return ["error", "warn", "info"].first(where: states.contains) ?? "ok"
    }

    static func dotColor(_ c: Collector) -> Color {
        switch worst(c) {
        case "error": return Palette.text("out")
        case "warn": return Palette.fill("tight")!
        case "info": return .blue
        default: return Color.secondary.opacity(0.5)
        }
    }

    private static func nsColor(_ state: String) -> NSColor {
        switch state {
        case "error": return .systemRed
        case "warn": return .systemYellow
        case "info": return .systemBlue
        default: return NSColor.secondaryLabelColor.withAlphaComponent(0.5)
        }
    }

    private static func build(_ store: Store, entries: [Attention]) -> NSMenu {
        let menu = NSMenu()
        menu.autoenablesItems = false
        if let r = store.report {
            let c = r.collector
            for a in entries {
                let t = AttentionText(a, report: r)
                let text = r.solo ? t.detail.prefix(1).uppercased() + t.detail.dropFirst() : "\(t.subject): \(t.detail)"
                let item = NSMenuItem(title: text.count > 64 ? text.prefix(63) + "…" : text, action: nil, keyEquivalent: "")
                item.isEnabled = false
                item.image = dot(a.kind == .error ? .systemRed : .systemYellow)
                item.toolTip = text
                menu.addItem(item)
            }
            if !entries.isEmpty {
                menu.addItem(.separator())
            }
            for h in c.health {
                let line = HealthLine(h, c, now: store.now)
                let item = NSMenuItem(title: line.text, action: nil, keyEquivalent: "")
                item.isEnabled = false
                item.image = dot(nsColor(h.state))
                item.toolTip = line.help
                menu.addItem(item)
            }
            let this = NSMenuItem(title: "This Mac: \(c.deviceLabel)" + (r.solo ? "" : " · \(r.team.devices.count) devices"), action: nil, keyEquivalent: "")
            this.isEnabled = false
            this.toolTip = "Team \(c.team)"
            menu.addItem(this)
            menu.addItem(.separator())
            menu.addItem(action("Collect Now", key: "r", enabled: !store.collecting) { Task { await store.collect() } })
            if let h = store.updateHealth {
                let title = "Update ai-usage" + (h.release.map { " to \($0)" } ?? "") + "…"
                menu.addItem(action(title, enabled: !store.updating) { Task { await store.update() } })
            }
            menu.addItem(.separator())
            // The whole report, interactive, where the popover shows a
            // glance: alone, this Mac is the only device.
            let views: [(String, [String], String)] = [
                ("Open Report in Terminal", [], "AI Usage Report"),
                ("Device Status in Terminal", ["--devices"], "AI Usage Devices"),
                ("All Projects in Terminal", ["--projects"], "AI Usage Projects"),
            ].filter { !r.solo || $0.1 != ["--devices"] }
            for (title, flags, name) in views {
                let item = action(title, enabled: store.cliPath != nil) { store.openInTerminal(["report"] + flags, named: name) }
                item.toolTip = (["ai-usage report"] + flags).joined(separator: " ")
                menu.addItem(item)
            }
            menu.addItem(.separator())
        }
        menu.addItem(action("Settings…", key: ",") { SettingsWindow.show() })
        menu.addItem(action("Quit AI Usage", key: "q") { NSApp.terminate(nil) })
        return menu
    }

    private static func dot(_ color: NSColor) -> NSImage {
        let image = NSImage(size: NSSize(width: 8, height: 8), flipped: false) { rect in
            color.setFill()
            NSBezierPath(ovalIn: rect.insetBy(dx: 0.5, dy: 0.5)).fill()
            return true
        }
        image.isTemplate = false
        return image
    }

    private static func action(_ title: String, key: String = "", enabled: Bool = true, _ run: @escaping @MainActor () -> Void) -> NSMenuItem {
        let target = MenuAction(run)
        let item = NSMenuItem(title: title, action: #selector(MenuAction.fire), keyEquivalent: key)
        item.target = target
        // The item holds its action: a menu item does not keep its target.
        item.representedObject = target
        item.isEnabled = enabled
        return item
    }

    private final class MenuAction: NSObject {
        let run: @MainActor () -> Void

        init(_ run: @escaping @MainActor () -> Void) {
            self.run = run
        }

        @objc func fire() {
            MainActor.assumeIsolated { run() }
        }
    }
}

/// A health entry of the report in a few words, and the rest in its
/// tooltip.
struct HealthLine {
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
    }
}

// MARK: - Chips

/// What else asks for something, besides the subscriptions: a click shows
/// the devices it is about, or the status menu.
private struct ChipRow: View {
    @EnvironmentObject private var store: Store
    let chips: [Chip]
    let report: Report

    var body: some View {
        ViewThatFits(in: .horizontal) {
            row(short: false)
            row(short: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.bottom, 10)
        .padding(.horizontal, Metrics.inset)
    }

    private func row(short: Bool) -> some View {
        HStack(spacing: 6) {
            ForEach(Array(chips.enumerated()), id: \.offset) { _, chip in
                ChipButton(chip: chip, short: short, help: help(chip), chosen: store.deviceFilter?.chip == chip.kind) { tap(chip) }
            }
        }
        .fixedSize()
    }

    private func tap(_ chip: Chip) {
        let title: String
        switch chip.kind {
        case .refresh:
            Task { await store.refresh() }
            return
        case .errors: title = "Devices with errors"
        case .silent: title = "Devices not reporting"
        case .notUpdating: title = "Devices not updating"
        case .reinstall: title = "Devices to reinstall"
        case .duplicate: title = "Devices reinstalled"
        case .health, .collector:
            StatusMenu.show(store, entries: chip.entries)
            return
        }
        guard !report.solo else {
            StatusMenu.show(store, entries: chip.entries)
            return
        }
        // A second click on the chosen chip shows every device again.
        if store.deviceFilter?.chip == chip.kind {
            store.deviceFilter = nil
            return
        }
        store.tab = .usage
        store.deviceFilter = DeviceFilter(title: title, devices: Set(chip.devices), chip: chip.kind)
    }

    private func help(_ chip: Chip) -> String {
        let c = report.collector
        switch chip.kind {
        case .refresh:
            return "Could not read the report: \(store.refreshError ?? "")"
        case .health(let item, _):
            return c.health.first { $0.item == item }.map { HealthLine($0, c, now: store.now).help } ?? ""
        case .errors, .silent, .collector, .reinstall, .duplicate:
            return chip.entries.map { a in
                let t = AttentionText(a, report: report)
                return "\(t.subject): \(t.detail)"
            }.joined(separator: "\n")
        case .notUpdating:
            let latest = report.team.latestVersion.map { "Latest \($0)" }
            let more = chip.updating > 0 ? "\(chip.updating) more on older releases update on their own" : nil
            return ([chip.devices.joined(separator: ", ") + " did not update"] + [latest, more].compactMap { $0 }).joined(separator: "\n")
        }
    }
}

private struct ChipButton: View {
    let chip: Chip
    let short: Bool
    let help: String
    /// Whether the chip filters Usage now.
    let chosen: Bool
    let action: () -> Void

    var body: some View {
        let loud = chip.state != "unknown"
        let fg = loud ? Palette.text(chip.state) : Color.secondary
        Button(action: action) {
            HStack(spacing: 4) {
                // The chosen chip's mark is the one that clears it, so the
                // chip has one cross, and it is not the error's.
                Image(systemName: chosen ? "xmark.circle.fill" : chip.symbol)
                    .symbolRenderingMode(.monochrome)
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(chosen ? AnyShapeStyle(.secondary) : AnyShapeStyle(fg))
                if !short || !chip.short.isEmpty {
                    Text(short ? chip.short : chip.text)
                        .font(.subheadline.weight(.medium))
                        .monospacedDigit()
                        .lineLimit(1)
                }
            }
            .foregroundStyle(fg)
            .padding(.horizontal, 8)
            .frame(height: 22)
            .background {
                let shape = RoundedRectangle(cornerRadius: 11, style: .circular)
                ZStack {
                    shape.fill(loud ? fg.opacity(chosen ? 0.22 : 0.12) : Color.primary.opacity(chosen ? 0.12 : 0.06))
                    if chosen {
                        shape.inset(by: 0.5).stroke(fg.opacity(0.6), lineWidth: 1)
                    }
                }
            }
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .help(chosen ? "Show every device" : help)
        .accessibilityLabel(chip.text)
        .accessibilityValue(chosen ? "chosen" : "")
        .accessibilityHint(chosen ? "Shows every device" : help)
    }
}

// MARK: - Empty states

/// The popover before there is a report to show: one line at most and one
/// thing to do.
private struct EmptyStateView: View {
    @EnvironmentObject private var store: Store
    static let install = "curl -fsSL https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh | sh"
    @ViewState private var copied = false
    @ViewState private var showDetails = false

    var body: some View {
        VStack(spacing: 12) {
            switch store.phase {
            case .loading, .ready:
                ProgressView().controlSize(.small)
            case .notInstalled:
                Text("AI Usage shows what the ai-usage collector reads. Run this in Terminal to install it.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                Text(Self.install)
                    .font(.system(.callout, design: .monospaced))
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
                    .help(Self.install)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: 6).fill(Color.primary.opacity(0.06)))
                HStack(spacing: 8) {
                    Button {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(Self.install, forType: .string)
                        copied = true
                        Task {
                            try? await Task.sleep(for: .seconds(2))
                            copied = false
                        }
                    } label: {
                        if copied {
                            Label("Copied", systemImage: "checkmark")
                        } else {
                            Text("Copy Command")
                        }
                    }
                    .buttonStyle(.bordered)
                    Button("Open in Terminal") { Self.installInTerminal() }
                        .buttonStyle(.borderedProminent)
                        .help("Opens Terminal and runs the command there")
                }
            case .notCollected:
                busyButton(store.collecting ? "Collecting…" : "Collect Now", busy: store.collecting) {
                    await store.collect()
                }
                .help("After this, ai-usage collects every 15 minutes.")
            case .mismatch(let message):
                busyButton(store.updating ? "Updating…" : "Update Now", busy: store.updating) {
                    await store.update()
                }
                details(message)
            case .failed(let message):
                Button("Try Again") { Task { await store.refresh() } }
                    .buttonStyle(.bordered)
                details(message)
            }
        }
        .padding(20)
        .frame(maxWidth: .infinity, minHeight: 180)
    }

    /// Runs the install command in a Terminal window.
    private static func installInTerminal() {
        Terminal.open("#!/bin/sh\necho '$ \(install)'\n\(install)\necho\necho 'You can close this window.'\n", named: "Install ai-usage")
    }

    /// What went wrong, behind a quiet button under the one that fixes it.
    @ViewBuilder private func details(_ message: String) -> some View {
        Button {
            showDetails.toggle()
        } label: {
            HStack(spacing: 4) {
                Text("Details")
                Image(systemName: "chevron.right")
                    .font(.system(size: 9, weight: .semibold))
                    .rotationEffect(.degrees(showDetails ? 90 : 0))
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .font(.callout)
        .foregroundStyle(.secondary)
        .accessibilityValue(showDetails ? "expanded" : "collapsed")
        if showDetails {
            Text(message)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
        }
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
