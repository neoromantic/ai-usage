import AIUsageKit
import SwiftUI

/// Each subscription of the team, by provider: how much is left of its main
/// window, when it resets, and how full it will be then, with a line for
/// each other window that limits it more. The subscriptions with no window
/// known now, which the report lists last, share a quiet line at the end of
/// their group: a row of empty bars would say nothing.
struct SubscriptionsView: View {
    let report: Report
    let now: Date

    var body: some View {
        let groups = report.team.providers.filter { $0.accounts.contains(where: \.subscription) }
        if groups.isEmpty {
            Text("No subscription has been used on this team's devices yet.")
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, minHeight: 60)
        } else {
            VStack(alignment: .leading, spacing: 12) {
                ForEach(groups, id: \.provider) { p in
                    let accounts = p.accounts.filter(\.subscription)
                    VStack(alignment: .leading, spacing: 8) {
                        GroupHeading(title: Format.provider(p.provider))
                        ForEach(accounts.filter(\.quotaKnown), id: \.label) { a in
                            AccountRow(provider: p.provider, account: a, solo: report.solo, now: now)
                        }
                        let unknown = accounts.filter { !$0.quotaKnown }
                        ForEach(Unread.Reason.allCases, id: \.self) { reason in
                            let some = unknown.filter { Unread.reason($0) == reason }
                            if !some.isEmpty {
                                Unread(provider: p.provider, reason: reason, accounts: some, solo: report.solo, now: now)
                            }
                        }
                    }
                }
            }
        }
    }
}

/// Subscriptions of a provider with no window known now, in a line, for one
/// reason: "No reading: ann, bo". The tooltip says of each where it is used
/// and what became of its reading.
private struct Unread: View {
    enum Reason: CaseIterable {
        case never, reset, uncovered

        var title: String {
            switch self {
            case .never: return "No reading"
            case .reset: return "Reset since the last reading"
            case .uncovered: return "Not in the last reading"
            }
        }
    }

    let provider: String
    let reason: Reason
    let accounts: [TeamAccount]
    let solo: Bool
    let now: Date

    static func reason(_ a: TeamAccount) -> Reason {
        guard let main = a.quota?.main else { return .never }
        return main.reset ? .reset : main.unread == true ? .uncovered : .never
    }

    var body: some View {
        (Text(reason.title + ": ") + Text(accounts.map(\.name).joined(separator: ", ")).foregroundColor(.primary))
            .font(.subheadline)
            .foregroundColor(.secondary)
            .lineLimit(2)
            .fixedSize(horizontal: false, vertical: true)
            .help(accounts.map(details).joined(separator: "\n"))
            .accessibilityLabel("\(Format.provider(provider)), \(reason.title.lowercased()): " + accounts.map(\.name).joined(separator: ", "))
    }

    /// "bo, Max plan: never read; used on bo-air, 15h ago".
    private func details(_ a: TeamAccount) -> String {
        let what: String
        switch reason {
        case .never:
            what = a.quota == nil ? "never read" : "no window of it read"
        case .reset, .uncovered:
            let q = a.quota!
            let w = q.main!
            let read = [q.device.map { "on \($0)" }, w.observedAt.map { Format.ago($0, now: now) }].compactMap { $0 }.joined(separator: " ")
            what = reason == .reset
                ? "its \(w.name) window reset" + (w.resetsAt.map { " \(Format.clock($0, now: now))" } ?? "") + ", after its last reading" + (read.isEmpty ? "" : " " + read)
                : "its \(w.name) window is not in the last reading" + (read.isEmpty ? "" : ", " + read)
        }
        let used = a.lastActiveAt.map { t in
            "used " + (solo ? "" : a.busiest.map { "on \(a.users > 1 ? "\($0) +\(a.users - 1)" : $0), " } ?? "") + Format.ago(t, now: now)
        }
        let here = a.current && !solo ? "logged in on this Mac" : nil
        return a.label + (a.plan.map { ", \(PlanTag.title($0)) plan" } ?? "") + ": " + [what, here, used].compactMap { $0 }.joined(separator: "; ")
    }
}

private struct AccountRow: View {
    let provider: String
    let account: TeamAccount
    let solo: Bool
    let now: Date

    private var main: Quota.Window? { account.quota?.main }

    /// The other windows that limit the account, each on a line of its own
    /// as the console has them; the rest are in the tooltip.
    private var others: [Quota.Window] {
        account.quota?.windows.filter { !$0.main && $0.limits } ?? []
    }

    var body: some View {
        let a = account
        HStack(alignment: .top, spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 4) {
                    Text(a.name)
                        .fontWeight(.medium)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .layoutPriority(1)
                    if a.current && !solo {
                        ThisMacMark(help: "Logged in on this Mac")
                    }
                    if let plan = a.plan {
                        PlanTag(plan: plan)
                    }
                }
                Text(who)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            .frame(width: 146, alignment: .leading)
            .help(details)

            VStack(alignment: .leading, spacing: 4) {
                QuotaBar(window: main)
                    .padding(.top, 6)
                    .padding(.bottom, 2)
                // Without room for both, the countdown goes to the tooltip.
                ViewThatFits(in: .horizontal) {
                    resetLine(countdown: true)
                    resetLine(countdown: false)
                }
                ForEach(others, id: \.name) { w in
                    other(w)
                        .lineLimit(1)
                }
            }
            .font(.subheadline)
            .help(windowHelp)

            left
                .frame(width: 64, alignment: .trailing)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel(spoken)
    }

    /// "42% left", or "?" when how full the window is now is not known.
    private var left: Text {
        guard let w = main, w.known else {
            return Text("?").font(.system(size: 15, weight: .medium)).foregroundColor(.secondary)
        }
        return (Text(Format.left(w)).font(.system(size: 15, weight: .medium))
            + Text(" left").font(.subheadline).foregroundColor(.secondary))
            .monospacedDigit()
    }

    private func resetLine(countdown: Bool) -> some View {
        HStack(spacing: 8) {
            Text(resets(countdown: countdown))
                .foregroundStyle(.secondary)
            Spacer(minLength: 0)
            forecast
        }
        .lineLimit(1)
    }

    /// "Resets in 2d 9h · Sun 01:00", or "Resets Sun 01:00".
    private func resets(countdown: Bool) -> String {
        guard let w = main else { return "No reading" }
        if w.reset { return "Reset since its last reading" }
        if w.unread == true { return "Not covered by the last reading" }
        guard let at = w.resetsAt else { return "Reset time not known" }
        let clock = Format.clock(at, now: now)
        return countdown ? "Resets in \(Format.duration(at.timeIntervalSince(now))) · \(clock)" : "Resets \(clock)"
    }

    /// How full the window will be at its reset, and its state unless that
    /// is on track: "88% at reset · Tight".
    private var forecast: Text {
        guard let w = main, w.known else { return Text("") }
        if w.state == "out" {
            return Text(Palette.label("out")).state("out")
        }
        guard let f = w.forecast, w.state != "unknown" else { return Text("") }
        let at = Text("\(w.stale ? "~" : "")\(Int(f.percent))% at reset").foregroundColor(.secondary)
        return w.state == "ok" ? at : at + Text(" · ").foregroundColor(.secondary) + Text(Palette.label(w.state)).state(w.state)
    }

    /// Another window in a line: "5h · Out · back Thu 17:05", or
    /// "5h · 82% left · resets Thu 19:30 · Tight".
    private func other(_ w: Quota.Window) -> Text {
        let name = Text((account.quota?.shortName(w.name) ?? w.name) + " · ").foregroundColor(.secondary)
        let at = w.resetsAt.map { Format.clock($0, now: now) }
        if w.state == "out" {
            return name + Text(Palette.label("out")).state("out") + Text(at.map { " · back \($0)" } ?? "").foregroundColor(.secondary)
        }
        let line = name + Text(Format.left(w) + " left" + (at.map { " · resets \($0)" } ?? "")).foregroundColor(.secondary)
        return ["tight", "over"].contains(w.state) ? line + Text(" · ").foregroundColor(.secondary) + Text(Palette.label(w.state)).state(w.state) : line
    }

    /// The device that spent the most through the account this window, how
    /// many more did, and when it was last active: "mira-mbp +1 · 7m ago".
    private var who: String {
        let a = account
        let last = a.lastActiveAt.map { t in
            let age = Format.age(t, now: now)
            return age == "now" ? "just now" : age + " ago"
        }
        guard !solo, let busiest = a.busiest else {
            return last.map { "Active \($0)" } ?? "Not used yet"
        }
        let devices = a.users > 1 ? "\(busiest) +\(a.users - 1)" : busiest
        return [devices, last].compactMap { $0 }.joined(separator: " · ")
    }

    /// Who uses the account, where it was read, and what spent through it.
    private var details: String {
        let a = account
        var lines = [a.label + (a.plan.map { ", \($0) plan" } ?? "")]
        if a.current { lines.append("Logged in on this Mac") }
        if a.users > 0 {
            lines.append("\(a.users) \(a.users == 1 ? "device" : "devices") used it this window" + (a.busiest.map { ", most of all \($0)" } ?? ""))
        }
        if let t = a.lastActiveAt { lines.append("Last active \(Format.ago(t, now: now))") }
        for d in a.perDevice where d.usage.week > 0 {
            lines.append("\(d.device): \(Format.tokens(d.usage.week)) tokens in + out in 7 days")
        }
        for l in a.linkedUsage {
            lines.append("\(Format.provider(l.provider)) \(l.label ?? "") ran \(l.sessions) sessions through it" + (l.devices.map { " on \($0.count) devices" } ?? ""))
        }
        return lines.joined(separator: "\n")
    }

    /// Each window in full, where the reading came from, and the windows
    /// whose fill is not known.
    private var windowHelp: String {
        guard let q = account.quota, let w = main else { return "No reading of this subscription" }
        var lines = ["\(Int(w.percent))% of the \(w.name) window used"]
        if let at = w.resetsAt, w.known { lines.append("Resets in \(Format.duration(at.timeIntervalSince(now))), \(Format.clock(at, now: now))") }
        if let e = w.forecast?.elapsed {
            lines[0] += ", \(Int((e * 100).rounded()))% of it gone; the tick marks even use"
        }
        if let t = w.observedAt { lines.append("Read \(Format.ago(t, now: now))" + (q.device.map { " on \($0)" } ?? "")) }
        if let from = q.from { lines.append("The reading of the \(Format.provider(from)) login it bills through") }
        if w.stale { lines.append("The reading is more than 6 hours old") }
        for o in q.windows where !o.main {
            let name = q.shortName(o.name)
            if o.reset {
                lines.append("\(name): reset since the last reading")
            } else if o.unread == true {
                lines.append("\(name): not covered by the last reading")
            } else if let f = o.forecast {
                lines.append("\(name): \(Int(o.percent))% used, \(Int(f.percent))% at its reset")
            }
        }
        return lines.joined(separator: "\n")
    }

    /// The row for VoiceOver: "Claude mira, 42% left, resets Sun 01:00, Tight".
    private var spoken: String {
        let a = account
        var parts = ["\(Format.provider(provider)) \(a.name)"]
        if let plan = a.plan { parts.append(PlanTag.title(plan) + " plan") }
        if a.current && !solo { parts.append("on this Mac") }
        guard let w = main, w.known else { return (parts + ["no reading"]).joined(separator: ", ") }
        parts.append("\(Format.left(w)) left")
        if let at = w.resetsAt { parts.append("resets \(Format.clock(at, now: now))") }
        if w.state != "unknown" { parts.append(Palette.label(w.state)) }
        for o in others {
            parts.append("\(account.quota?.shortName(o.name) ?? o.name) window \(Format.left(o)) left" + (["tight", "over", "out"].contains(o.state) ? ", \(Palette.label(o.state))" : ""))
        }
        return parts.joined(separator: ", ")
    }
}

/// The plan of a subscription as a small tag after its name. A long plan
/// name gives way before the account's name does.
struct PlanTag: View {
    let plan: String

    static func title(_ plan: String) -> String {
        plan.prefix(1).uppercased() + plan.dropFirst()
    }

    var body: some View {
        Text(Self.title(plan))
            .font(.caption2.weight(.medium))
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .truncationMode(.tail)
            .padding(.horizontal, 4)
            .padding(.vertical, 1)
            .background(Capsule().fill(.quaternary))
    }
}
