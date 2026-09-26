import AIUsageKit
import SwiftUI

/// This Mac's projects, most tokens in the last 7 days first. A project is
/// a repository with its subfolders and worktrees, as the report groups
/// them: its name, then the folder it is in, dim.
struct ProjectsView: View {
    let report: Report
    let now: Date
    /// The folder paths show as "~".
    let home: String
    @ViewState private var all = false

    var body: some View {
        let projects = all ? report.projects : Array(report.projects.prefix(10))
        if projects.isEmpty {
            Text("No project on this Mac yet.")
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, minHeight: 60)
        } else {
            VStack(alignment: .leading, spacing: 10) {
                Caption(text: "On \(report.collector.deviceLabel) · M tokens in + out · most in 7 days first")
                Grid(alignment: .trailing, horizontalSpacing: 14, verticalSpacing: 7) {
                    GridRow {
                        Text("Project").gridColumnAlignment(.leading)
                        Text(Period.week.title)
                        Text(Period.quarter.title)
                        Text("Sessions")
                        Text("Via").gridColumnAlignment(.leading)
                        Text("Active")
                    }
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    ForEach(projects, id: \.path) { p in
                        let parts = Format.pathParts(p.path, home: home)
                        GridRow {
                            // The folder shows whole or not at all; the
                            // help has the path in full.
                            ViewThatFits(in: .horizontal) {
                                HStack(spacing: 6) {
                                    Text(parts.name)
                                    Text(parts.folder).foregroundStyle(.secondary)
                                }
                                .fixedSize()
                                Text(parts.name).lineLimit(1)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .help(help(p))
                            Millions(tokens: p.usage.week)
                            Millions(tokens: p.usage.quarter)
                            Text("\(p.sessions)").foregroundStyle(.secondary)
                            let via = (p.providers ?? []).map(Format.provider).joined(separator: ", ")
                            Text(via)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .frame(maxWidth: 130, alignment: .leading)
                                .fixedSize()
                                .help(via)
                            Text(p.lastActiveAt.map { Format.age($0, now: now) } ?? Format.none)
                                .foregroundStyle(.secondary)
                                .help(p.lastActiveAt.map { "Last active \(Format.ago($0, now: now))" } ?? "")
                        }
                    }
                }
                .font(.callout.monospacedDigit())
                .accessibilityChildren {
                    VStack {
                        ForEach(projects, id: \.path) { p in
                            Text(spoken(p))
                        }
                    }
                }
                if report.projects.count > 10 {
                    MoreButton(title: all ? "Show Top 10" : "Show All \(report.projects.count)", expanded: all) { all.toggle() }
                }
            }
        }
    }

    /// The project's path in full, and how many folders count under it.
    private func help(_ p: Project) -> String {
        guard p.folders > 1 else { return p.path }
        return "\(p.path)\nSessions in \(p.folders) folders count here: the repository's own, its subfolders, and its worktrees"
    }

    /// A project as VoiceOver reads it.
    private func spoken(_ p: Project) -> String {
        let parts = Format.pathParts(p.path, home: home)
        var s = parts.name + (parts.folder.isEmpty ? "" : " in \(parts.folder)")
        if p.folders > 1 { s += ", \(p.folders) folders" }
        s += ": 7 days \(Format.spokenMillions(p.usage.week)), 90 days \(Format.spokenMillions(p.usage.quarter)), \(p.sessions) sessions"
        if let via = p.providers, !via.isEmpty { s += ", via " + via.map(Format.provider).joined(separator: ", ") }
        if let t = p.lastActiveAt { s += ", active \(Format.ago(t, now: now))" }
        return s
    }
}
