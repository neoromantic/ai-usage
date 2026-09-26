import AIUsageKit
import AppKit
import SwiftUI
import SystemConfiguration

/// The settings window: a toolbar of panes, as a Mac app's settings have.
/// While it is open the app is a regular one, in the Dock and in ⌘-Tab, so
/// the window does not get lost behind others.
@MainActor
enum SettingsWindow {
    private static var window: NSWindow?

    static let panes: [(title: String, symbol: String, view: AnyView)] = [
        ("General", "gearshape", AnyView(GeneralSettings())),
        ("Accounts", "person.crop.circle", AnyView(AccountSettings())),
        ("Team", "person.2", AnyView(TeamSettings())),
    ]

    static func show() {
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
            NotificationCenter.default.addObserver(forName: NSWindow.willCloseNotification, object: w, queue: .main) { _ in
                MainActor.assumeIsolated { _ = NSApp.setActivationPolicy(.accessory) }
            }
            window = w
        }
        NSApp.setActivationPolicy(.regular)
        NSApp.activate()
        window?.makeKeyAndOrderFront(nil)
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
                LabeledContent("Preview") {
                    MenuBarLabel(summary: store.summary, shows: MenuBarShows(rawValue: shows) ?? .percent, now: store.now)
                        .font(.system(size: 13, weight: .medium))
                        .padding(.vertical, 4)
                        .padding(.horizontal, 8)
                        .background(RoundedRectangle(cornerRadius: 5).fill(Color.primary.opacity(0.06)))
                }
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
                }
            }
            Section("About") {
                LabeledContent("AI Usage", value: Bundle.main.shortVersion)
                LabeledContent("ai-usage") {
                    VStack(alignment: .trailing, spacing: 4) {
                        HStack(spacing: 8) {
                            Text(cliVersion ?? store.report?.collector.version ?? Format.none)
                            if store.updating {
                                ProgressView().controlSize(.small)
                            } else {
                                // ai-usage update checks for a release and
                                // installs it when there is one.
                                Button(store.updateHealth?.release.map { "Update to \($0)" } ?? "Check for Updates") {
                                    Task { await store.update() }
                                }
                                .disabled(store.cliPath == nil && !store.demo)
                            }
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
                LabeledContent("Location") {
                    if let path = store.cliPath {
                        HStack(spacing: 4) {
                            Text(path)
                                .font(.system(.subheadline, design: .monospaced))
                                .lineLimit(1)
                                .truncationMode(.middle)
                                .textSelection(.enabled)
                                .help(path)
                            IconButton(symbol: "folder", help: "Show in Finder") {
                                NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
                            }
                        }
                    } else {
                        Text("Not found").foregroundStyle(.secondary)
                    }
                }
            }
        }
        .pane(height: 440)
        .onAppear {
            login = LoginItem.status
            Task {
                if let v = try? await store.command(["version"], changes: false), !v.isEmpty {
                    cliVersion = v
                }
            }
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
