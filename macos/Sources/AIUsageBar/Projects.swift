import AIUsageKit
import SwiftUI

/// This Mac's projects, most tokens in the last 7 days first.
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
                Caption(text: "On \(report.collector.deviceLabel) · tokens in + out · most in 7 days first")
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
                        GridRow {
                            Text(Format.tilde(p.path, home: home))
                                .lineLimit(1)
                                .truncationMode(.middle)
                                .frame(maxWidth: 200, alignment: .leading)
                                .fixedSize()
                                .help(p.path)
                            tokens(p.usage.week)
                            tokens(p.usage.quarter)
                            Text("\(p.sessions)").foregroundStyle(.secondary)
                            let via = (p.providers ?? []).map(Format.provider).joined(separator: ", ")
                            Text(via)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .help(via)
                            Text(p.lastActiveAt.map { Format.age($0, now: now) } ?? "–")
                                .foregroundStyle(.secondary)
                                .help(p.lastActiveAt.map { "Last active \(Format.ago($0, now: now))" } ?? "")
                        }
                    }
                }
                .font(.callout.monospacedDigit())
                if report.projects.count > 10 {
                    MoreButton(title: all ? "Show Top 10" : "Show All \(report.projects.count)", expanded: all) { all.toggle() }
                }
            }
        }
    }

    private func tokens(_ n: Int) -> some View {
        Text(n > 0 ? Format.tokens(n) : "–").foregroundStyle(n > 0 ? .primary : .tertiary)
    }
}
