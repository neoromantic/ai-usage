import AIUsageKit
import SwiftUI

/// Each subscription of the team, by provider: how much is left of its main
/// window, a bar of what is left and what will be by its reset, and how
/// long until that reset. The account's state, from the report, marks
/// the row; another window that limits it more has a line under it, and
/// everything else opens under the row. The subscriptions with no window
/// known that limits them share a quiet line at the end of their group: a
/// row of empty bars would say nothing.
struct LimitsView: View {
    @EnvironmentObject private var store: Store
    let report: Report
    let now: Date

    private struct Group {
        let provider: String
        let rows: [(account: TeamAccount, line: LimitLine)]
        let unread: [TeamAccount]
    }

    private var groups: [Group] {
        report.team.providers.compactMap { p in
            let subs = p.accounts.filter(\.subscription)
            let g = Group(provider: p.provider, rows: subs.compactMap { a in LimitLine(a).map { (a, $0) } },
                          unread: subs.filter { LimitLine($0) == nil })
            return g.rows.isEmpty && g.unread.isEmpty ? nil : g
        }
    }

    var body: some View {
        let groups = self.groups
        if groups.isEmpty {
            VStack(spacing: 4) {
                Text("No subscriptions yet").foregroundStyle(.secondary)
                Text("Claude Code, Codex and Grok appear after their first use.")
                    .font(.subheadline)
                    .foregroundStyle(.tertiary)
            }
            .multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.top, 24)
        } else {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(groups.enumerated()), id: \.element.provider) { i, g in
                    GroupHeading(title: Format.provider(g.provider), first: i == 0)
                    ForEach(g.rows, id: \.account.label) { r in
                        LimitRow(provider: g.provider, account: r.account, line: r.line, report: report, now: now)
                    }
                    if !g.unread.isEmpty {
                        NotRead(provider: g.provider, accounts: g.unread, solo: report.solo, now: now)
                    }
                }
            }
        }
    }
}

/// The subscriptions of a provider with no window known now that limits
/// them, in a line: "Not read: ann, bo". The tooltip says why of each.
private struct NotRead: View {
    let provider: String
    let accounts: [TeamAccount]
    let solo: Bool
    let now: Date

    var body: some View {
        (Text("Not read: ").foregroundColor(Color(nsColor: .tertiaryLabelColor))
            + Text(accounts.map(\.name).joined(separator: ", ")).foregroundColor(.secondary))
            .font(.subheadline)
            .lineLimit(1)
            .truncationMode(.tail)
            .frame(height: Metrics.row - 6)
            .padding(.leading, Metrics.slot)
            .help(accounts.map(details).joined(separator: "\n"))
            .accessibilityLabel("\(Format.provider(provider)), not read: " + accounts.map(\.name).joined(separator: ", "))
    }

    /// "bo, Max: No reading; used on bo-air, 15h ago".
    private func details(_ a: TeamAccount) -> String {
        let why: String
        if let q = a.quota, let w = q.main {
            let read = [q.device.map { "on \($0)" }, w.observedAt.map { Format.ago($0, now: now) }].compactMap { $0 }.joined(separator: " ")
            why = (w.reset ? "Reset since the last reading" : w.unread == true ? "Not in the last reading" : "No reading")
                + (read.isEmpty ? "" : ", read \(read)")
        } else {
            why = "No reading"
        }
        let used = a.lastActiveAt.map { t in
            "used " + (solo ? "" : a.busiest.map { "on \(a.users > 1 ? "\($0) +\(a.users - 1)" : $0), " } ?? "") + Format.ago(t, now: now)
        }
        return a.label + (a.plan.map { ", \(Format.plan($0))" } ?? "") + ": " + [why, used].compactMap { $0 }.joined(separator: "; ")
    }
}

private struct LimitRow: View {
    @EnvironmentObject private var store: Store
    let provider: String
    let account: TeamAccount
    let line: LimitLine
    let report: Report
    let now: Date

    private var solo: Bool { report.solo }
    private var quota: Quota? { account.quota }
    /// The window the bar and percent show.
    private var shown: Quota.Window { line.window }

    private func short(_ w: Quota.Window) -> String { quota?.shortName(w.name) ?? w.name }
    private func clock(_ t: Date) -> String { Format.clock(t, now: now) }

    var body: some View {
        DisclosureRow(key: "limits:\(provider)/\(account.label)", spoken: spoken) {
            VStack(alignment: .leading, spacing: 0) {
                row.frame(height: Metrics.limitRow)
                if let sub = subLine {
                    sub
                        .font(.subheadline)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .frame(height: Metrics.subLine, alignment: .top)
                        .padding(.leading, Metrics.slot)
                        .padding(.top, -5)
                        .padding(.bottom, 4)
                }
            }
        } details: {
            details
        }
    }

    private var row: some View {
        let a = account, w = shown
        // The mark is the account's, as the report has it; the percent is
        // the shown window's, in its color when that window is what marks
        // the account, and dim when another one stops the account. So is
        // the bar then: the mark and the countdown say why.
        let own = w.state == line.state
        return HStack(spacing: 0) {
            ZStack {
                if a.current && !solo {
                    ThisMacMark()
                }
            }
            .frame(width: Metrics.slot, alignment: .leading)
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 6) {
                    name
                    if let plan = a.plan {
                        Text(Format.plan(plan))
                            .font(.subheadline)
                            .foregroundStyle(.tertiary)
                            .lineLimit(1)
                    }
                }
                .fixedSize()
                name.truncationMode(.middle)
            }
            .frame(width: Metrics.name, alignment: .leading)
            .help(a.label)
            QuotaBar(window: w, muted: line.blocking != nil)
                .padding(.horizontal, Metrics.gap)
            HStack(spacing: 3) {
                if Palette.marked(line.state) {
                    StateGlyph(state: line.state)
                }
                Text(Format.left(w))
                    .font(.system(size: 17, weight: .semibold, design: .rounded))
                    .foregroundStyle(line.blocking != nil ? AnyShapeStyle(.secondary)
                        : own && Palette.loud(w.state) ? AnyShapeStyle(Palette.text(w.state)) : AnyShapeStyle(.primary))
            }
            .monospacedDigit()
            .lineLimit(1)
            .fixedSize()
            .frame(width: Metrics.value, alignment: .trailing)
            countdown
                .frame(width: Metrics.trailing, alignment: .trailing)
                .padding(.leading, Metrics.trailingGap)
        }
    }

    private var name: some View {
        Text(account.name)
            .font(account.current && !solo ? .body.weight(.medium) : .body)
            .lineLimit(1)
    }

    /// How long until the account can be used again when a window stops
    /// it, else until the shown window resets.
    private var countdown: some View {
        let w = line.blocking ?? shown
        let out = w.state == "out"
        return Group {
            if let at = w.resetsAt {
                Text(Format.duration(at.timeIntervalSince(now)))
                    .foregroundStyle(out ? Palette.text("out") : .secondary)
                    .help(out ? "\(short(w)) back \(clock(at))" : "Resets \(clock(at))")
            } else {
                Text(Format.none).foregroundStyle(.tertiary)
            }
        }
        .font(.callout)
        .monospacedDigit()
        .lineLimit(1)
        .fixedSize()
    }

    /// The line under the row: the window that stops the account, with the
    /// shown window's reset that the countdown gives up for it, "5h out ·
    /// 7d resets Sun 01:00"; or the window that limits it more, "Fable ·
    /// 7% left · resets Sun 01:00 · Over"; or, with the main window unread,
    /// "7d not in the last reading".
    private var subLine: Text? {
        let secondary = { (s: String) in Text(s).foregroundColor(.secondary) }
        if let b = line.blocking {
            return secondary(short(b) + " ") + Text("out").foregroundColor(Palette.text("out"))
                + secondary(shown.resetsAt.map { " · \(short(shown)) resets \(clock($0))" } ?? "")
        }
        if let m = line.unreadMain {
            return secondary(short(m) + (m.reset ? " reset since the last reading" : " not in the last reading"))
        }
        guard let w = line.others.first else { return nil }
        var text: Text
        if w.state == "out" {
            text = secondary(short(w) + " ") + Text("out").foregroundColor(Palette.text("out"))
                + secondary(w.resetsAt.map { " · back \(clock($0))" } ?? "")
        } else {
            text = secondary("\(short(w)) · \(Format.left(w)) left" + (w.resetsAt.map { " · resets \(clock($0))" } ?? ""))
            if Palette.marked(w.state) {
                text = text + secondary(" · ") + Text(Palette.label(w.state)).foregroundColor(Palette.text(w.state))
            }
        }
        if line.others.count > 1 {
            text = text + secondary(" · +\(line.others.count - 1)")
        }
        return text
    }

    // MARK: Details

    /// A line for each thing the row does not show: the account's login,
    /// each window, and the devices that used it. Who read it goes in the
    /// windows' tooltips.
    private var details: some View {
        let a = account
        return DetailGrid {
            DetailLine("Account", accountValue)
            ForEach(orderedWindows, id: \.name) { w in
                DetailLine(short(w), windowValue(w), help: windowHelp(w))
            }
            if solo {
                DetailLine("Active", a.lastActiveAt.map { Format.ago($0, now: now) })
            } else if let per = perDevice {
                DetailLine("Devices", per.text, help: per.help)
            }
            ForEach(Array(a.linkedUsage.enumerated()), id: \.offset) { _, l in
                DetailLine(Format.provider(l.provider), linked(l))
            }
        }
    }

    /// The login when it is more than the row's name, and this Mac when it
    /// is logged in here.
    private var accountValue: String? {
        let a = account
        let parts = [a.label == a.name ? nil : a.label, a.current && !solo ? "this Mac" : nil].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// The main window first, then the others in the report's order.
    private var orderedWindows: [Quota.Window] {
        let ws = quota?.windows ?? []
        return ws.filter(\.main) + ws.filter { !$0.main }
    }

    /// A window in a few words, all in what is left: "42% left · ~12% at
    /// reset Sun 01:00", "17% left · runs out ~Thu 23:55", "Out · back Thu
    /// 17:05". Where it is headed takes the window's color when that asks
    /// for something.
    private func windowValue(_ w: Quota.Window) -> Text {
        if w.reset { return Text("Reset since the last reading") }
        if w.unread == true { return Text("Not in the last reading") }
        if w.state == "out" {
            return Text("Out").foregroundColor(Palette.text("out")) + Text(w.resetsAt.map { " · back \(clock($0))" } ?? "")
        }
        let left = Text("\(Format.left(w)) left · ")
        let tint = { (s: String) in Palette.loud(w.state) ? Text(s).foregroundColor(Palette.text(w.state)) : Text(s) }
        if w.state == "over", let at = w.forecast?.runsOutAt {
            return left + tint((at > now ? "runs out ~" : "ran out ~") + clock(at))
        }
        let resets = w.resetsAt.map { " \(clock($0))" } ?? ""
        if let f = w.forecast, f.percent > w.percent {
            return left + tint("~\(max(100 - Int(f.percent), 0))% at reset") + Text(resets)
        }
        return left + Text(w.resetsAt == nil ? "no reset known" : "resets" + resets)
    }

    /// A window in a sentence, with who read it: "7d: 42% left, ~12% left
    /// at its reset Sun 01:00, in 2d 9h · Tight · read 3m ago on mira-mbp
    /// (mira)".
    private func windowHelp(_ w: Quota.Window) -> String {
        var parts: [String] = []
        if w.known {
            var s = w.state == "out" ? "Out" : "\(Format.left(w)) left"
            if w.state != "out", let f = w.forecast, f.percent > w.percent {
                s += ", ~\(max(100 - Int(f.percent), 0))% left at its reset"
            }
            if let at = w.resetsAt {
                s += (w.state == "out" ? ", back " : ", resets ") + "\(clock(at)), in \(Format.duration(at.timeIntervalSince(now)))"
            }
            parts.append(s)
            if Palette.marked(w.state) { parts.append(Palette.label(w.state)) }
            if w.stale, let age = readingAge(w) { parts.append("reading \(age) old") }
        }
        if let read = readLine { parts.append(read) }
        return "\(short(w)): " + parts.joined(separator: " · ")
    }

    private func readingAge(_ w: Quota.Window) -> String? {
        (w.observedAt ?? quota?.observedAt).map { Format.duration(now.timeIntervalSince($0)) }
    }

    /// "read 20m ago on mira-mbp (mira) via the Codex login".
    private var readLine: String? {
        guard let q = quota else { return nil }
        var s = "read " + Format.ago(q.observedAt, now: now)
        if let d = q.device {
            s += " on \(d)" + (report.teamDevice(named: d).map { " (\($0.osUser))" } ?? "")
        }
        if let from = q.from {
            s += " via the \(Format.provider(from)) login"
        }
        return s
    }

    /// The devices that used the account in the period, busiest first:
    /// "build-01 141M, scout 49M +8"; the tooltip has all of them, and when
    /// it was last used.
    private var perDevice: (text: String, help: String)? {
        let p = store.period
        let used = account.perDevice.filter { $0.usage[p] > 0 }
        guard !used.isEmpty else { return nil }
        let items = used.map { "\($0.device) \(Format.tokensM($0.usage[p]))" }
        let text = items.prefix(2).joined(separator: ", ") + (items.count > 2 ? " +\(items.count - 2)" : "")
        let last = account.lastActiveAt.map { "Last active \(Format.ago($0, now: now))" }
        return (text, (["\(p.title):"] + items + [last].compactMap { $0 }).joined(separator: "\n"))
    }

    /// "312 sessions through it on 9 devices".
    private func linked(_ l: TeamAccount.LinkedUsage) -> String {
        "\(l.sessions) sessions through it"
            + (l.devices.map { " on \($0.count) \($0.count == 1 ? "device" : "devices")" } ?? "")
    }

    // MARK: VoiceOver

    /// "Claude mira, Max, logged in here: out; 7d window 42% left, tight,
    /// about 12% left at reset, resets in 2 days 9 hours; 5h window out,
    /// back in 1 hour 25 minutes".
    private var spoken: String {
        let a = account, w = shown
        var head = [Format.provider(provider) + " " + a.name]
        if let plan = a.plan { head.append(Format.plan(plan)) }
        if a.current && !solo { head.append("logged in here") }
        var parts = [short(w) + " window " + Format.left(w) + " left"]
        if w.stale { parts.append("from an old reading") }
        if w.state != "unknown" && w.state != "ok" { parts.append(Palette.label(w.state).lowercased()) }
        if let f = w.forecast, w.state != "out", f.percent > w.percent {
            parts.append(f.percent >= 100 ? "runs out before its reset" : "about \(max(100 - Int(f.percent), 0))% left at reset")
        }
        if let at = w.resetsAt {
            let d = Format.spokenDuration(at.timeIntervalSince(now))
            parts.append(w.state == "out" ? "back in \(d)" : "resets in \(d)")
        }
        var s = head.joined(separator: ", ") + ": "
        if line.state != w.state && line.state != "unknown" && line.state != "ok" {
            s += Palette.label(line.state).lowercased() + "; "
        }
        s += parts.joined(separator: ", ")
        if let m = line.unreadMain {
            s += "; \(short(m)) window not read"
        }
        for o in line.others {
            s += "; \(short(o)) window " + (o.state == "out"
                ? "out" + (o.resetsAt.map { ", back in \(Format.spokenDuration($0.timeIntervalSince(now)))" } ?? "")
                : "\(Format.left(o)) left" + (Palette.marked(o.state) ? ", \(Palette.label(o.state).lowercased())" : ""))
        }
        return s
    }
}
