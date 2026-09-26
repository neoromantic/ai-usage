import AIUsageKit
import SwiftUI

/// This Mac's projects, in the report's order. A project is a repository
/// with its subfolders and worktrees, as the report groups them. Projects
/// next to each other in the same folder show under its name, as a group;
/// a project on its own names its folder after its name, dim. Each has the
/// tokens of the period against the busiest, and how long ago it was last
/// active.
struct ProjectsView: View {
    @EnvironmentObject private var store: Store
    let report: Report
    let now: Date
    /// The folder paths show as "~".
    let home: String

    static let shown = 8

    var body: some View {
        let all = report.projects
        let projects = store.showAllProjects ? all : Array(all.prefix(Self.shown))
        let top = all.map { $0.usage[store.period] }.max() ?? 0
        VStack(alignment: .leading, spacing: 0) {
            if projects.isEmpty {
                Text("No projects on this Mac yet.")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity)
                    .padding(.top, 24)
            } else {
                ForEach(Array(ProjectSection.sections(projects, home: home).enumerated()), id: \.offset) { i, section in
                    if let title = section.title {
                        GroupHeading(title: title, first: i == 0, help: section.folder, symbol: section.grouped ? "folder" : nil)
                    }
                    ForEach(section.projects, id: \.path) { p in
                        ProjectRow(project: p, showFolder: !section.grouped, top: top, now: now, home: home)
                    }
                }
                if all.count > Self.shown {
                    MoreButton(title: store.showAllProjects ? "Show Less" : "Show All \(all.count)", expanded: store.showAllProjects) {
                        store.showAllProjects.toggle()
                    }
                    .padding(.leading, Metrics.slot)
                }
            }
        }
    }
}

/// The line over Projects: whose they are, with a team, and the period.
struct ProjectsTopLine: View {
    let report: Report

    var body: some View {
        TopLine {
            if !report.solo {
                TopTitle(text: "This Mac")
            }
        }
    }
}

private struct ProjectRow: View {
    @EnvironmentObject private var store: Store
    let project: Project
    let showFolder: Bool
    let top: Int
    let now: Date
    let home: String

    var body: some View {
        let p = project
        let parts = Format.pathParts(p.path, home: home)
        let period = store.period
        DisclosureRow(key: "proj:\(p.path)", spoken: spoken) {
            HStack(spacing: 0) {
                Color.clear.frame(width: Metrics.slot, height: 1)
                // The folder shows whole or not at all; the details have
                // the path in full.
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: 6) {
                        Text(parts.name)
                        if showFolder && !parts.folder.isEmpty && parts.folder != "~" {
                            Text(parts.folder)
                                .font(.callout)
                                .foregroundStyle(.tertiary)
                        }
                    }
                    .fixedSize()
                    Text(parts.name).lineLimit(1).truncationMode(.middle)
                }
                .frame(width: Metrics.name, alignment: .leading)
                TokenBar(value: p.usage[period], top: top)
                    .padding(.horizontal, Metrics.gap)
                TokensValue(tokens: p.usage[period])
                // An age, not a countdown as Limits has in this column.
                Text(p.lastActiveAt.map(activeAgo) ?? Format.none)
                    .font(.callout)
                    .monospacedDigit()
                    .foregroundStyle(.tertiary)
                    .lineLimit(1)
                    .frame(width: Metrics.trailing, alignment: .trailing)
                    .padding(.leading, Metrics.trailingGap)
                    .help(p.lastActiveAt.map { "Last active \(Format.clock($0, now: now))" } ?? "Never active")
            }
            .frame(height: Metrics.row)
        } details: {
            DetailGrid {
                DetailLine("Folder", Format.tilde(p.path, home: home) + (p.folders > 1 ? " · \(p.folders) folders" : ""),
                           help: p.folders > 1 ? "Counts \(p.folders) folders: the repository, its subfolders and worktrees" : Format.tilde(p.path, home: home))
                let via = (p.providers ?? []).map(Format.provider).joined(separator: ", ")
                DetailLine("Sessions", "\(p.sessions)" + (via.isEmpty ? "" : " · \(via)"))
                PeriodsLine(usage: p.usage)
            }
        }
    }

    /// "20m ago", or "now".
    private func activeAgo(_ t: Date) -> String {
        let age = Format.age(t, now: now)
        return age == "now" ? age : age + " ago"
    }

    /// A project as VoiceOver reads it.
    private var spoken: String {
        let p = project
        let parts = Format.pathParts(p.path, home: home)
        var s = parts.name + (parts.folder.isEmpty ? "" : " in \(parts.folder)")
        s += ", \(Format.spokenMillions(p.usage[store.period])) tokens in \(store.period.title.lowercased())"
        if let t = p.lastActiveAt { s += ", active \(Format.ago(t, now: now))" }
        return s
    }
}
