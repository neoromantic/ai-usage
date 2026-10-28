import AIUsageKit
import AppKit
import SwiftUI
import SystemConfiguration

/// The app's windows, Settings and Team. While one is open the app is a
/// regular one, in the Dock and in ⌘-Tab, so the window does not get lost
/// behind others; once the last one closes, the app is in the menu bar alone.
@MainActor
enum AppWindows {
    private static var open: Set<ObjectIdentifier> = []

    /// Follows a new window's closing.
    static func register(_ w: NSWindow) {
        NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: w, queue: .main) { n in
            MainActor.assumeIsolated {
                guard let w = n.object as? NSWindow else { return }
                open.remove(ObjectIdentifier(w))
                if open.isEmpty {
                    _ = NSApp.setActivationPolicy(.accessory)
                }
            }
        }
    }

    static func show(_ w: NSWindow) {
        open.insert(ObjectIdentifier(w))
        NSApp.setActivationPolicy(.regular)
        NSApp.activate()
        w.makeKeyAndOrderFront(nil)
    }
}

/// The settings window: a toolbar of panes, as a Mac app's settings have.
@MainActor
enum SettingsWindow {
    private static var window: NSWindow?

    /// The panes, in the order of `panes`.
    enum Pane: Int {
        case general, accounts, team, cost
    }

    static let panes: [(title: String, symbol: String, view: AnyView)] = [
        ("General", "gearshape", AnyView(GeneralSettings())),
        ("Accounts", "person.crop.circle", AnyView(AccountSettings())),
        ("Team", "person.2", AnyView(TeamSettings())),
        ("Cost", "dollarsign.circle", AnyView(CostSettings())),
    ]

    /// Opens the window, on a pane when one is named.
    static func show(pane: Pane? = nil) {
        Store.shared.record(.settings)
        if window == nil {
            let tabs = NSTabViewController()
            tabs.tabStyle = .toolbar
            for pane in panes {
                let host = NSHostingController(rootView: pane.view.environmentObject(Store.shared))
                host.sizingOptions = .preferredContentSize
                // The window takes its title from the chosen pane's
                // controller, and the toolbar item its label; without one,
                // the window says "Untitled".
                host.title = pane.title
                let item = NSTabViewItem(viewController: host)
                item.image = NSImage(systemSymbolName: pane.symbol, accessibilityDescription: pane.title)
                tabs.addTabViewItem(item)
            }
            let w = NSWindow(contentViewController: tabs)
            w.styleMask = [.titled, .closable]
            w.toolbarStyle = .preference
            w.isReleasedWhenClosed = false
            w.center()
            AppWindows.register(w)
            window = w
        }
        if let pane, let tabs = window?.contentViewController as? NSTabViewController {
            tabs.selectedTabViewItemIndex = pane.rawValue
        }
        window.map(AppWindows.show)
    }
}

/// The size of every pane, so the window keeps its width between them.
private extension View {
    func pane(height: CGFloat) -> some View {
        formStyle(.grouped).frame(width: 460, height: height)
    }
}

/// A line in a section's footer.
private struct Footnote: View {
    let text: String

    init(_ text: String) {
        self.text = text
    }

    var body: some View {
        Text(text)
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }
}

/// A small borderless button with a symbol, beside or inside a field.
private struct IconButton: View {
    let symbol: String
    let help: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.system(size: 11, weight: .medium))
                .frame(width: 16, height: 16)
                .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.secondary)
        .help(help)
        .accessibilityLabel(help)
    }
}

/// A text field that takes effect when you press Return or leave it, as the
/// fields of a Mac's settings do. It runs the command that sets the value,
/// shows a check when that worked, and ai-usage's error in red under it when
/// not. An empty field, or its reset button, clears the setting.
private struct CommitField: View {
    let title: String
    let prompt: String
    /// The value set now.
    let value: String
    var width: CGFloat = 160
    /// The reset button inside the field's end, when it shows.
    var reset: String?
    let commit: (String) async throws -> Void

    @ViewState private var text = ""
    @ViewState private var sent: String?
    @ViewState private var saved = false
    @ViewState private var error: String?
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .trailing, spacing: 4) {
            HStack(spacing: 6) {
                Image(systemName: "checkmark")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(.secondary)
                    .opacity(saved ? 1 : 0)
                    .accessibilityLabel("Saved")
                    .accessibilityHidden(!saved)
                TextField(title, text: $text, prompt: Text(prompt))
                    .labelsHidden()
                    .textFieldStyle(.roundedBorder)
                    .multilineTextAlignment(.leading)
                    .frame(width: width)
                    .focused($focused)
                    .onSubmit(save)
                    .overlay(alignment: .trailing) {
                        if let reset {
                            IconButton(symbol: "arrow.uturn.backward", help: reset) {
                                text = ""
                                save()
                            }
                            .padding(.trailing, 3)
                        }
                    }
            }
            if let error {
                Text(error)
                    .font(.subheadline)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .onAppear { text = value }
        .onChange(of: value) { _, v in
            sent = nil
            if !focused { text = v }
        }
        .onChange(of: focused) { _, f in
            if f { saved = false } else { save() }
        }
    }

    private func save() {
        let t = text.trimmingCharacters(in: .whitespaces)
        guard t != value, t != sent else { return }
        sent = t
        saved = false
        Task {
            do {
                try await commit(t)
                error = nil
                saved = true
            } catch {
                self.error = error.localizedDescription
                sent = nil
            }
        }
    }
}

// MARK: - General

struct GeneralSettings: View {
    @EnvironmentObject private var store: Store
    @AppStorage(MenuBarShows.key) private var shows = MenuBarShows.percent.rawValue
    @AppStorage(MenuBarTracks.key) private var tracks = MenuBarTracks.mac.rawValue
    @AppStorage(NoticeSettings.outKey) private var notifyOut = true
    @AppStorage(NoticeSettings.runsOutKey) private var runsOut = NoticeSettings.RunsOut.day.rawValue
    @AppStorage(NoticeSettings.scopeKey) private var scope = NoticeSettings.Scope.mac.rawValue
    @ObservedObject private var notifier = Notifier.shared
    @ViewState private var login = LoginItem.status
    @ViewState private var loginError: String?
    @ViewState private var cliVersion: String?

    /// The name ai-usage gives this Mac by default, as it reads it.
    private var hostName: String {
        if store.demo { return store.report?.collector.deviceLabel ?? "" }
        if let h = SCDynamicStoreCopyLocalHostName(nil) as String?, !h.isEmpty { return h }
        let h = ProcessInfo.processInfo.hostName
        return h.hasSuffix(".local") ? String(h.dropLast(6)) : h
    }

    var body: some View {
        let label = store.report?.collector.deviceLabel ?? ""
        Form {
            Section("Menu Bar") {
                Picker("Show", selection: $shows) {
                    Text("Percent Left").tag(MenuBarShows.percent.rawValue)
                    Text("Time Until Reset").tag(MenuBarShows.time.rawValue)
                    Text("Icon Only").tag(MenuBarShows.icon.rawValue)
                }
                .pickerStyle(.menu)
                Picker("Track", selection: $tracks) {
                    Text("This Mac's Subscriptions").tag(MenuBarTracks.mac.rawValue)
                    Text("The Team's").tag(MenuBarTracks.team.rawValue)
                }
                .pickerStyle(.menu)
                LabeledContent("Preview") {
                    MenuBarLabel(summary: store.summary(MenuBarTracks(rawValue: tracks) ?? .mac), shows: MenuBarShows(rawValue: shows) ?? .percent,
                                 now: store.now)
                        .font(.system(size: 13, weight: .medium))
                        .padding(.vertical, 4)
                        .padding(.horizontal, 8)
                        .background(RoundedRectangle(cornerRadius: 5).fill(Color.primary.opacity(0.06)))
                }
            }
            Section {
                Toggle("When a Window Is Out", isOn: $notifyOut)
                Picker("When a Window Will Run Out", selection: $runsOut) {
                    Text("Within a Day").tag(NoticeSettings.RunsOut.day.rawValue)
                    Text("Any Time").tag(NoticeSettings.RunsOut.always.rawValue)
                    Text("Never").tag(NoticeSettings.RunsOut.never.rawValue)
                }
                .pickerStyle(.menu)
                Picker("Subscriptions", selection: $scope) {
                    Text("This Mac's").tag(NoticeSettings.Scope.mac.rawValue)
                    Text("The Team's").tag(NoticeSettings.Scope.team.rawValue)
                }
                .pickerStyle(.menu)
            } header: {
                Text("Notifications")
            } footer: {
                if notifier.status == .denied && !NoticeSettings(UserDefaults.standard).off {
                    HStack(spacing: 6) {
                        Footnote("Notifications for AI Usage are off in System Settings.")
                        Button("Open Notifications…") { notifier.openSystemSettings() }
                            .buttonStyle(.borderless)
                            .font(.subheadline)
                    }
                } else {
                    Footnote("Once for each window until it resets. A forecast early in a long window can change by the next reading; Within a Day waits until it is near.")
                }
            }
            .onChange(of: notifyOut) { _, on in
                if on { notifier.enable() }
            }
            .onChange(of: runsOut) { _, v in
                if v != NoticeSettings.RunsOut.never.rawValue { notifier.enable() }
            }
            Section {
                LabeledContent("Name") {
                    CommitField(title: "Name", prompt: "Host name", value: label,
                                reset: !label.isEmpty && label != hostName ? "Use Host Name" : nil) { name in
                        try await store.command(name.isEmpty ? ["name", "clear"] : ["name", "set", name])
                    }
                    .help("The team sees the new name after the next collection.")
                }
                Toggle("Open at Login", isOn: Binding(get: { login != .off }, set: { on in
                    do {
                        try LoginItem.set(on)
                        loginError = nil
                    } catch {
                        loginError = error.localizedDescription
                    }
                    login = LoginItem.status
                }))
                if let stats = store.usageStats {
                    // ai-usage's switch: what it shares with its author, by
                    // the notice's words.
                    Toggle("Share Usage Stats", isOn: Binding(get: { stats.enabled }, set: { on in
                        Task { await store.shareUsageStats(on) }
                    }))
                    .disabled(!stats.switchable)
                    .help("How often AI Usage runs, which features are used, errors, version and OS, with a random install ID; never your files, accounts, or projects. See usage.sergeybaus.com")
                }
            } header: {
                Text("This Mac")
            } footer: {
                if let loginError {
                    Text(loginError).font(.subheadline).foregroundStyle(.red)
                } else if login == .needsApproval {
                    HStack(spacing: 6) {
                        Footnote("Approve AI Usage in Login Items.")
                        Button("Open Login Items…") { LoginItem.openSystemSettings() }
                            .buttonStyle(.borderless)
                            .font(.subheadline)
                    }
                } else if let stats = store.usageStats, let why = stats.decidedBy {
                    Footnote("Usage stats are \(stats.enabled ? "on" : "off"): \(why).")
                }
            }
            Section("About") {
                // The app's version once; the command-line tool's only when
                // it is another release, or missing.
                let update = store.report?.collector.update
                let about = AboutVersion(app: store.appVersion, cli: cliVersion ?? store.report?.collector.version,
                                         found: store.cliPath != nil, homebrew: update?.homebrew == true)
                LabeledContent("AI Usage") {
                    VStack(alignment: .trailing, spacing: 4) {
                        HStack(spacing: 8) {
                            Text(about.version)
                                .textSelection(.enabled)
                            if store.updating {
                                ProgressView().controlSize(.small)
                            } else {
                                // ai-usage update checks for a release and
                                // installs it when there is one.
                                Button(store.updateHealth?.release.map { "Update to \($0)" } ?? "Check for Updates") {
                                    Task { await store.update() }
                                }
                                .disabled(store.cliPath == nil && !store.demo)
                                .help(update.flatMap { $0.homebrew ? $0.command : nil }
                                    .map { "Runs ai-usage update, which runs \($0) and brings this app to its release" }
                                    ?? "Runs ai-usage update, which brings ai-usage and this app to the latest release")
                            }
                        }
                        if let note = about.note {
                            Text(note)
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.trailing)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        if let r = store.updateResult, !store.updating {
                            Label(r.text, systemImage: r.failed ? "xmark.circle.fill" : "checkmark")
                                .font(.subheadline)
                                .foregroundStyle(r.failed ? .red : .secondary)
                                .lineLimit(2)
                                .multilineTextAlignment(.trailing)
                                .textSelection(.enabled)
                        }
                    }
                }
                if let path = store.cliPath {
                    LabeledContent {
                        HStack(spacing: 4) {
                            Text(path)
                                .font(.system(.subheadline, design: .monospaced))
                                .lineLimit(1)
                                .truncationMode(.middle)
                                .textSelection(.enabled)
                                .help("ai-usage, which collects what the app shows: \(path)")
                            IconButton(symbol: "folder", help: "Show in Finder") {
                                NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
                            }
                        }
                    } label: {
                        // Homebrew installed it when the report says so; the
                        // path gives way to it.
                        if update?.homebrew == true {
                            (Text("Command-Line Tool ") + Text("(Homebrew)").foregroundColor(.secondary))
                                .lineLimit(1)
                                .fixedSize()
                        } else {
                            Text("Command-Line Tool")
                        }
                    }
                }
            }
        }
        .pane(height: 685)
        .onAppear {
            login = LoginItem.status
            notifier.refreshStatus()
            Task { await store.loadUsageStats() }
            Task {
                if let v = try? await store.command(["version"], changes: false), !v.isEmpty {
                    cliVersion = v
                }
            }
        }
        // Coming back from System Settings, which may have changed it.
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            notifier.refreshStatus()
        }
    }
}

extension Bundle {
    var shortVersion: String {
        infoDictionary?["CFBundleShortVersionString"] as? String ?? "dev"
    }
}

// MARK: - Accounts

struct AccountSettings: View {
    @EnvironmentObject private var store: Store

    var body: some View {
        let providers = store.report?.team.providers.filter { !$0.accounts.isEmpty } ?? []
        Form {
            if providers.isEmpty {
                Text("No accounts yet.").foregroundStyle(.secondary)
            }
            ForEach(Array(providers.enumerated()), id: \.element.provider) { i, p in
                Section {
                    ForEach(p.accounts, id: \.label) { a in
                        AliasRow(provider: p.provider, account: a)
                    }
                } header: {
                    Text(Format.provider(p.provider))
                } footer: {
                    if i == providers.count - 1 {
                        Footnote("Names are shared with the team: up to 12 characters, no spaces.")
                    }
                }
            }
        }
        .pane(height: 480)
    }
}

private struct AliasRow: View {
    @EnvironmentObject private var store: Store
    let provider: String
    let account: TeamAccount

    var body: some View {
        let a = account
        let target = "\(provider):\(a.label)"
        let devices = "\(a.devices.count) \(a.devices.count == 1 ? "device" : "devices")"
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 1) {
                Text(a.label)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Text([a.subscription ? a.plan.map(Format.plan) : "No quota", devices].compactMap { $0 }.joined(separator: " · "))
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 8)
            // Usage the tool did not tie to an account is no account to name.
            if a.label == "unknown" {
                Text("Can't be named")
                    .foregroundStyle(.secondary)
                    .help("Usage the tool did not tie to an account")
            } else {
                CommitField(title: "Name for \(a.label)", prompt: a.alias == nil ? a.name : "Default name",
                            value: a.alias ?? "", width: 120) { name in
                    try await store.command(name.isEmpty ? ["alias", target, "--clear"] : ["alias", target, name])
                }
            }
        }
    }
}

// MARK: - Team

struct TeamSettings: View {
    @EnvironmentObject private var store: Store
    /// The relay in use, as `relay show` prints it; empty when there is none.
    @ViewState private var relay = ""
    @ViewState private var keyResult: (text: String, failed: Bool)?
    @ViewState private var joining = false
    @ViewState private var copied = false

    var body: some View {
        let team = store.report?.collector.team
        Form {
            Section("Team") {
                LabeledContent("Devices", value: store.report.map { "\($0.team.devices.count)" } ?? Format.none)
                LabeledContent("Fingerprint") {
                    HStack(spacing: 4) {
                        Text(team ?? Format.none)
                            .font(.system(.body, design: .monospaced))
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .textSelection(.enabled)
                        if let team {
                            IconButton(symbol: copied ? "checkmark" : "doc.on.doc", help: "Copy Fingerprint") {
                                NSPasteboard.general.clearContents()
                                NSPasteboard.general.setString(team, forType: .string)
                                copied = true
                                Task {
                                    try? await Task.sleep(for: .seconds(2))
                                    copied = false
                                }
                            }
                        }
                    }
                }
            }
            Section {
                LabeledContent("URL") {
                    // The app cannot tell the release's default relay from
                    // one set by hand, so the reset shows with any relay.
                    CommitField(title: "Relay URL", prompt: "https://relay.example.com", value: relay, width: 230,
                                reset: relay.isEmpty ? nil : "Use Default") { url in
                        try await store.command(url.isEmpty ? ["relay", "clear"] : ["relay", "set", url])
                        await showRelay()
                    }
                }
            } header: {
                Text("Relay")
            } footer: {
                Footnote("Every device of the team uses the same relay.")
            }
            Section {
                HStack {
                    Button("Copy Team Key", action: copyKey)
                    Button("Join Another Team…") { joining = true }
                    Spacer()
                }
                if let r = keyResult {
                    Label(r.text, systemImage: r.failed ? "xmark.circle.fill" : "checkmark")
                        .font(.subheadline)
                        .foregroundStyle(r.failed ? .red : .secondary)
                }
            } header: {
                Text("Team Key")
            } footer: {
                Footnote("Share the key only with your own devices. Joining another team leaves this one.")
            }
        }
        .pane(height: 440)
        .sheet(isPresented: $joining) { JoinSheet() }
        .onAppear {
            relay = store.report?.collector.relay.url ?? ""
            Task { await showRelay() }
        }
    }

    private func showRelay() async {
        guard let shown = try? await store.command(["relay", "show"], changes: false), !shown.isEmpty else { return }
        relay = shown.hasPrefix("http") ? shown : ""
    }

    private func copyKey() {
        Task {
            do {
                let key = try await store.command(["team", "key"], changes: false)
                // Clipboard managers that follow nspasteboard.org keep a
                // concealed item out of their history.
                let concealed = NSPasteboard.PasteboardType("org.nspasteboard.ConcealedType")
                let pb = NSPasteboard.general
                pb.declareTypes([.string, concealed], owner: nil)
                pb.setString(key, forType: .string)
                pb.setString("", forType: concealed)
                keyResult = ("Copied. On the other device, choose Join Another Team….", false)
            } catch {
                keyResult = (error.localizedDescription, true)
            }
        }
    }
}

private struct JoinSheet: View {
    @EnvironmentObject private var store: Store
    @Environment(\.dismiss) private var dismiss
    @ViewState private var key = ""
    @ViewState private var error: String?
    @ViewState private var busy = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Join Another Team").font(.headline)
            Text("Paste the key from a device of the other team; this Mac leaves its current team.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            SecureField("Team key", text: $key)
                .onSubmit(join)
            if let error {
                Text(error).font(.subheadline).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                if busy {
                    ProgressView().controlSize(.small)
                }
                Spacer()
                Button("Cancel", role: .cancel) { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("Join", action: join)
                    .keyboardShortcut(.defaultAction)
                    .disabled(key.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || busy)
            }
        }
        .padding(20)
        .frame(width: 400)
    }

    private func join() {
        busy = true
        Task {
            do {
                // ai-usage waits up to 3 minutes for a collection that runs.
                try await store.command(["team", "join"], input: key.trimmingCharacters(in: .whitespacesAndNewlines) + "\n", timeout: 240)
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}

// MARK: - Cost

/// Each subscription's cost as the report has it, under the questions this
/// Mac asks, and which notifications the cost gets. Every answer and edit
/// runs `ai-usage cost`, and the next report shows it: the app keeps and
/// works out nothing of its own.
struct CostSettings: View {
    @EnvironmentObject private var store: Store
    @AppStorage(NoticeSettings.costChangesKey) private var costChanges = true
    @AppStorage(NoticeSettings.chargesSoonKey) private var chargesSoon = true
    @AppStorage(NoticeSettings.costQuestionsKey) private var costQuestions = true
    @ObservedObject private var notifier = Notifier.shared

    var body: some View {
        let cost = store.report?.cost
        // Each provider's subscriptions, in the report's order.
        let groups = (cost?.subscriptions ?? []).reduce(into: [(provider: String, subs: [CostSubscription])]()) { g, s in
            if g.last?.provider == s.provider {
                g[g.count - 1].subs.append(s)
            } else {
                g.append((s.provider, [s]))
            }
        }
        Form {
            if let cost {
                if !cost.questions.isEmpty {
                    Section {
                        ForEach(cost.questions, id: \.id) { q in
                            QuestionRow(question: q)
                        }
                    } header: {
                        Text("Questions")
                    } footer: {
                        Footnote("Answers are shared with the team. Skip asks that question no more on this Mac.")
                    }
                }
                ForEach(Array(groups.enumerated()), id: \.element.provider) { i, g in
                    Section {
                        ForEach(g.subs, id: \.account) { s in
                            CostRow(subscription: s)
                        }
                    } header: {
                        Text(Format.provider(g.provider))
                    } footer: {
                        if i == groups.count - 1 {
                            Footnote(summary(cost))
                        }
                    }
                }
                if groups.isEmpty {
                    Text("No paid subscriptions yet.").foregroundStyle(.secondary)
                }
            } else {
                Text(store.report == nil ? "No report yet." : "This ai-usage reports no cost; v0.10.0 and later do.")
                    .foregroundStyle(.secondary)
            }
            Section {
                Toggle("When a Subscription Starts, Changes, or Ends", isOn: $costChanges)
                Toggle("3 Days Before a Charge", isOn: $chargesSoon)
                Toggle("Questions About the Cost", isOn: $costQuestions)
            } header: {
                Text("Notifications")
            } footer: {
                Footnote("Of this Mac's subscriptions or the team's, as General sets. A question's notification takes its answer.")
            }
            .onChange(of: costChanges) { _, on in
                if on { notifier.enable() }
            }
            .onChange(of: chargesSoon) { _, on in
                if on { notifier.enable() }
            }
            .onChange(of: costQuestions) { _, on in
                if on { notifier.enable() }
            }
        }
        .pane(height: 600)
    }

    /// "$1,000/mo in all, +1 without a price. Prices of 20 Sep, from the
    /// relay."
    private func summary(_ c: Cost) -> String {
        let total = Format.usd(c.totalUsd) + "/mo in all" + (c.withoutPrice > 0 ? ", +\(c.withoutPrice) without a price" : "")
        let from = c.prices.source == "relay" ? "from the relay" : "built in"
        return "\(total). The price list of \(c.prices.updated), \(from); a typed price counts instead, for the whole team."
    }
}

/// A subscription's cost in a line, and under it, open, what a person can
/// say of it: its price, how it is billed, its next charge, and that it is
/// still paid or cancelled.
private struct CostRow: View {
    @EnvironmentObject private var store: Store
    let subscription: CostSubscription
    @ViewState private var open = false
    @ViewState private var result: (text: String, failed: Bool)?

    var body: some View {
        let s = subscription
        DisclosureGroup(isExpanded: $open) {
            LabeledContent(s.cadence == "yearly" ? "Price a Year" : "Price a Month") {
                CommitField(title: "Price", prompt: s.priceUsd.map { Format.usd($0) + "/mo, " + s.priceWords } ?? "Dollars",
                            value: typed, width: 150) { v in
                    guard !v.isEmpty else { throw CostEditError.empty("price") }
                    guard let usd = CostCommand.dollars(v) else { throw CostEditError.notPrice }
                    try await run(CostCommand.set(s.target, price: usd))
                }
            }
            LabeledContent("Billed") {
                // A typed price is turned into the new cadence's, so the
                // cost a month stays; ai-usage says so in the result.
                Picker("Billed", selection: Binding(get: { s.cadence }, set: { c in
                    if let args = CostCommand.set(s.target, cadence: c) { act(args) }
                })) {
                    Text("Monthly").tag("monthly")
                    Text("Yearly").tag("yearly")
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .fixedSize()
            }
            LabeledContent("Next Charge") {
                CommitField(title: "Next charge", prompt: "YYYY-MM-DD or day",
                            value: s.renewsAt.map(Self.isoDay) ?? "", width: 150) { v in
                    guard !v.isEmpty else { throw CostEditError.empty("next charge") }
                    try await run(CostCommand.set(s.target, renews: v))
                }
            }
            HStack(spacing: 8) {
                Button("Still Paying") { act(CostCommand.confirm(s.target)) }
                    .help("Runs ai-usage cost confirm: nobody is asked again for 35 days")
                Button("Cancelled") { act(CostCommand.cancel(s.target)) }
                    .help("Runs ai-usage cost cancel: it counts in no total, and shows as ended for 30 days")
                Spacer()
                Button("Reset") { act(CostCommand.reset(s.target)) }
                    .help("Runs ai-usage cost reset: clears the typed price, cadence, and charge day")
            }
            if let r = result {
                Label(r.text, systemImage: r.failed ? "xmark.circle.fill" : "checkmark")
                    .font(.subheadline)
                    .foregroundStyle(r.failed ? .red : .secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
        } label: {
            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 1) {
                    Text(s.alias ?? s.account)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Text(details)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                }
                Spacer(minLength: 8)
                Text(s.priceText + (s.priceSource == "typed" ? "*" : ""))
                    .monospacedDigit()
                    .foregroundStyle(s.ended ? .secondary : .primary)
            }
            .help(s.account + "\n" + s.help(now: store.now))
        }
        .opacity(s.ended ? 0.6 : 1)
    }

    /// "Max 20x · via App Store · used 91% · next Fri 25 Sep · idle 23d".
    private var details: String {
        let s = subscription
        let next = s.renewsAt.map { "next " + Format.day($0) }
        return ([s.planTitle, s.priceSource == "typed" ? "typed" : nil, s.billedThrough.map { "via " + $0 }, s.usedText, next,
                 s.stateText(now: store.now)] as [String?])
            .compactMap { $0 }.joined(separator: " · ")
    }

    /// The price a person typed, for its field: a month's, as a yearly
    /// one's year is not in the report.
    private var typed: String {
        let s = subscription
        guard s.priceSource == "typed", s.cadence == "monthly", let p = s.priceUsd else { return "" }
        return p == p.rounded() ? String(Int(p)) : String(format: "%.2f", p)
    }

    private static func isoDay(_ d: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "yyyy-MM-dd"
        return f.string(from: d)
    }

    private func run(_ args: [String]?) async throws {
        guard let args else { return }
        let said = try await store.command(args)
        result = (said, false)
    }

    private func act(_ args: [String]) {
        Task {
            do {
                try await run(args)
            } catch {
                result = (error.localizedDescription, true)
            }
        }
    }
}

/// Why an edit of the cost ran nothing.
private enum CostEditError: LocalizedError {
    case empty(String)
    case notPrice

    var errorDescription: String? {
        switch self {
        case .empty(let what): return "Type a \(what); Reset clears what was typed"
        case .notPrice: return "Type the price in dollars, as 20 or 1,200"
        }
    }
}

/// A question this Mac asks, in the report's words, with its answers: still
/// paying or cancelled, or a day or a price typed, and Skip.
private struct QuestionRow: View {
    @EnvironmentObject private var store: Store
    let question: Cost.Question
    @ViewState private var text = ""
    @ViewState private var cadence = "monthly"
    @ViewState private var error: String?

    var body: some View {
        let q = question
        let target = q.provider + ":" + q.account
        VStack(alignment: .leading, spacing: 6) {
            Text(q.text)
                .fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 8) {
                switch q.kind {
                case "still_paid":
                    Button("Still Paying") { answer(CostCommand.confirm(target)) }
                    Button("Cancelled") { answer(CostCommand.cancel(target)) }
                case "price" where !q.choices.isEmpty:
                    // A plan read without its tier: its tiers' prices, or
                    // one typed.
                    ForEach(q.choices, id: \.self) { c in
                        Button(c) { if let args = CostCommand.answer(kind: q.kind, target: target, text: c) { answer(args) } }
                            .fixedSize()
                    }
                    TextField(q.kind, text: $text, prompt: Text("Dollars"))
                        .labelsHidden()
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 80)
                        .onSubmit(save)
                default:
                    // The cadence first: a yearly subscription charges on a
                    // date of its own, and is offered its own.
                    if !q.choices.isEmpty {
                        Picker("Billed", selection: $cadence) {
                            ForEach(q.choices, id: \.self) { Text(Format.plan($0)).tag($0) }
                        }
                        .pickerStyle(.segmented)
                        .labelsHidden()
                        .fixedSize()
                    }
                    TextField(q.kind, text: $text, prompt: Text(q.kind == "price" ? "Dollars each charge"
                        : suggestion ?? (cadence == "yearly" ? "YYYY-MM-DD" : "YYYY-MM-DD or day")))
                        .labelsHidden()
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 110)
                        .onSubmit(save)
                    // Return takes the suggestion, as the console's prompt
                    // does.
                    Button("Save", action: save)
                        .fixedSize()
                        .disabled(text.trimmingCharacters(in: .whitespaces).isEmpty && suggestion == nil)
                }
                Spacer(minLength: 0)
                Button("Skip") { answer(CostCommand.skip(target, q.kind)) }
                    .fixedSize()
                    .help("Asks this question of the subscription no more on this Mac")
            }
            if let error {
                Text(error)
                    .font(.subheadline)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.vertical, 2)
    }

    /// The day offered at the cadence chosen.
    private var suggestion: String? {
        cadence == "yearly" ? question.suggestionYearly : question.suggestion
    }

    private func save() {
        let q = question
        let typed = text.trimmingCharacters(in: .whitespaces)
        let given = typed.isEmpty ? suggestion ?? "" : typed
        // A price question's choices are prices, not cadences.
        let billed = q.kind == "renewal" && !q.choices.isEmpty ? " " + cadence : ""
        guard let args = CostCommand.answer(kind: q.kind, target: q.provider + ":" + q.account, text: given + billed) else {
            error = q.kind == "price" ? "Type the price in dollars, as 20 or 1,200" : "Type a day, as 2026-10-04, or a day of the month"
            return
        }
        answer(args)
    }

    private func answer(_ args: [String]) {
        Task {
            do {
                try await store.command(args)
                error = nil
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
