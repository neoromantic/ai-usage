import AIUsageKit
import AppKit
import SwiftUI

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
                let item = NSTabViewItem(viewController: host)
                item.label = pane.title
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
        formStyle(.grouped).frame(width: 500, height: height)
    }
}

/// A caption in a section's footer.
private struct Footnote: View {
    let text: String

    init(_ text: String) {
        self.text = text
    }

    var body: some View {
        Text(text)
            .font(.caption)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
    }
}

/// A text field that takes effect when you press Return or leave it, as the
/// fields of a Mac's settings do. It runs the command that sets the value,
/// shows a check when that worked, and ai-usage's error in red under it when
/// not. An empty field, or the clear button, clears the setting.
private struct CommitField: View {
    let title: String
    let prompt: String
    /// The value set now.
    let value: String
    var width: CGFloat = 160
    var clearTitle: String?
    let commit: (String) async throws -> Void

    @ViewState private var text = ""
    @ViewState private var sent: String?
    @ViewState private var saved = false
    @ViewState private var error: String?
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .trailing, spacing: 4) {
            HStack(spacing: 6) {
                Image(systemName: "checkmark.circle.fill")
                    .foregroundStyle(.green)
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
                if let clearTitle {
                    Button(clearTitle) {
                        text = ""
                        save()
                    }
                }
            }
            if let error {
                Text(error)
                    .font(.caption)
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
    @AppStorage("showPercent") private var showPercent = true
    @ViewState private var login = LoginItem.status
    @ViewState private var loginError: String?
    @ViewState private var cliVersion: String?

    var body: some View {
        Form {
            Section {
                LabeledContent("This Mac's name") {
                    CommitField(title: "This Mac's name", prompt: "Host name", value: store.report?.collector.deviceLabel ?? "",
                                clearTitle: "Use Host Name") { name in
                        try await store.command(name.isEmpty ? ["name", "clear"] : ["name", "set", name])
                    }
                }
            } footer: {
                Footnote("The team sees the new name after the next collection.")
            }
            Section {
                Toggle("Open at login", isOn: Binding(get: { login != .off }, set: { on in
                    do {
                        try LoginItem.set(on)
                        loginError = nil
                    } catch {
                        loginError = error.localizedDescription
                    }
                    login = LoginItem.status
                }))
                if login == .needsApproval {
                    LabeledContent {
                        Button("Open Login Items…") { LoginItem.openSystemSettings() }
                    } label: {
                        Text("Allow AI Usage in Login Items to open it at login.")
                    }
                }
                if let loginError {
                    Text(loginError).font(.caption).foregroundStyle(.red)
                }
                Toggle("Show percent in menu bar", isOn: $showPercent)
            }
            Section("Versions") {
                LabeledContent("Menu Bar App", value: Bundle.main.shortVersion)
                LabeledContent("Command-Line Tool", value: cliVersion ?? store.report?.collector.version ?? "–")
                LabeledContent("Tool Location") {
                    Text(store.cliPath ?? "–")
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .textSelection(.enabled)
                }
            }
        }
        .pane(height: 400)
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
    static let rule = "A name is what the whole team sees for an account: at most 12 characters, no spaces. Empty goes back to the default. The team sees a change after the next collection."

    var body: some View {
        let providers = store.report?.team.providers.filter { !$0.accounts.isEmpty } ?? []
        Form {
            Section {
            } footer: {
                Footnote(Self.rule)
            }
            if providers.isEmpty {
                Text("No account has been seen on this team's devices yet.").foregroundStyle(.secondary)
            }
            ForEach(providers, id: \.provider) { p in
                Section(Format.provider(p.provider)) {
                    ForEach(p.accounts, id: \.label) { a in
                        AliasRow(provider: p.provider, account: a)
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
        let target = "\(provider):\(account.label)"
        LabeledContent {
            // Usage the tool did not tie to an account is no account to name.
            if account.label == "unknown" {
                Text("Cannot be named")
                    .foregroundStyle(.secondary)
                    .help("Usage the tool did not tie to an account")
            } else {
                CommitField(title: "Name for \(account.label)", prompt: account.alias == nil ? account.name : "Default name",
                            value: account.alias ?? "", width: 130) { name in
                    try await store.command(name.isEmpty ? ["alias", target, "--clear"] : ["alias", target, name])
                }
                .help(AccountSettings.rule)
            }
        } label: {
            Text(account.label).lineLimit(1).truncationMode(.middle)
            Text([account.plan.map(PlanTag.title), account.subscription ? nil : "no quota of its own",
                  "\(account.devices.count) \(account.devices.count == 1 ? "device" : "devices")"]
                .compactMap { $0 }.joined(separator: " · "))
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

    var body: some View {
        Form {
            Section("This Team") {
                LabeledContent("Fingerprint") {
                    Text(store.report?.collector.team ?? "–")
                        .font(.system(.body, design: .monospaced))
                        .textSelection(.enabled)
                }
                LabeledContent("Devices", value: store.report.map { "\($0.team.devices.count)" } ?? "–")
            }
            Section {
                LabeledContent("Relay URL") {
                    CommitField(title: "Relay URL", prompt: "https://relay.example.com", value: relay, width: 210,
                                clearTitle: "Use Default") { url in
                        try await store.command(url.isEmpty ? ["relay", "clear"] : ["relay", "set", url])
                        await showRelay()
                    }
                }
            } header: {
                Text("Relay")
            } footer: {
                Footnote("Every device of the team publishes to the same relay.")
            }
            Section("Team Key") {
                LabeledContent {
                    Button("Copy Team Key", action: copyKey)
                } label: {
                    Text("Anyone with the team key can read the team's snapshots. Share it only with your own devices.")
                        .foregroundStyle(.secondary)
                }
                if let r = keyResult {
                    Label(r.text, systemImage: r.failed ? "xmark.circle.fill" : "checkmark.circle.fill")
                        .font(.caption)
                        .foregroundStyle(r.failed ? .red : .secondary)
                }
                LabeledContent {
                    Button("Join Another Team…") { joining = true }
                } label: {
                    Text("Joining another team leaves this one.")
                        .foregroundStyle(.secondary)
                }
            }
        }
        .pane(height: 450)
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
                keyResult = ("Copied. On the other device, choose Join Another Team… or run ai-usage team join.", false)
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
            Text("Paste the team key from a device of the other team. This Mac leaves its current team; its old key is kept beside the new one.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            SecureField("Team key", text: $key)
                .onSubmit(join)
            if let error {
                Text(error).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
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
