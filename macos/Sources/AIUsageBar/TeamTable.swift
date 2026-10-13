import AppKit
import SwiftUI

/// A column of a table of the Team window.
struct TeamColumn {
    let id: String
    /// The heading over a run of columns, as a provider's over its
    /// subscriptions; it shows once, over the first of them.
    var group: String?
    let title: String
    /// What VoiceOver says of the heading, when not its title.
    var spoken: String?
    /// The title's color, as a subscription's state; nil for a quiet one.
    var color: NSColor?
    var help = ""
    let width: CGFloat
    var minWidth: CGFloat = 36
    var trailing = true
    /// Which way a first click on its heading sorts the rows; nil when the
    /// column does not sort them.
    var sortsAscending: Bool? = false
}

/// How a table's rows are sorted: by a column, which way. None keeps the
/// report's order.
struct TeamSort: Equatable {
    let column: String
    let ascending: Bool
}

/// The Team window's tables: AppKit's table, which SwiftUI's Table is made
/// of, for columns as many as the report has, under headings in two lines:
/// a provider's name over its subscriptions, and each one's in the color of
/// its state. A click on a heading sorts the rows, the TOTAL row staying
/// last. Each cell is a SwiftUI view.
struct TeamTable: NSViewRepresentable {
    let columns: [TeamColumn]
    /// How many rows there are, TOTAL last.
    let rows: Int
    let cell: (_ row: Int, _ column: String) -> AnyView
    @Binding var sort: TeamSort?
    /// The last column takes the width the others leave.
    var fills = false
    let name: String

    static let rowHeight: CGFloat = 24
    static let headerHeight: CGFloat = 36

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeNSView(context: Context) -> NSScrollView {
        let table = NSTableView()
        table.headerView = TallHeaderView(frame: NSRect(x: 0, y: 0, width: 0, height: Self.headerHeight))
        table.style = .fullWidth
        table.rowHeight = Self.rowHeight
        table.intercellSpacing = .zero
        table.allowsColumnReordering = false
        table.allowsColumnResizing = true
        table.allowsMultipleSelection = false
        table.columnAutoresizingStyle = fills ? .lastColumnOnlyAutoresizingStyle : .noColumnAutoresizing
        table.setAccessibilityLabel(name)
        table.dataSource = context.coordinator
        table.delegate = context.coordinator
        let scroll = TeamScrollView()
        scroll.fills = fills
        scroll.documentView = table
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = true
        scroll.autohidesScrollers = true
        scroll.drawsBackground = false
        context.coordinator.table = table
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        context.coordinator.parent = self
        context.coordinator.update()
    }

    @MainActor
    final class Coordinator: NSObject, NSTableViewDataSource, NSTableViewDelegate {
        var parent: TeamTable?
        weak var table: NSTableView?
        private var ids: [String] = []
        private var applying = false

        func update() {
            guard let table, let p = parent else { return }
            if ids != p.columns.map(\.id) {
                for c in table.tableColumns {
                    table.removeTableColumn(c)
                }
                for spec in p.columns {
                    let c = NSTableColumn(identifier: NSUserInterfaceItemIdentifier(spec.id))
                    c.headerCell = TeamHeaderCell()
                    c.width = spec.width
                    c.minWidth = spec.minWidth
                    c.resizingMask = .userResizingMask
                    c.sortDescriptorPrototype = spec.sortsAscending.map { NSSortDescriptor(key: spec.id, ascending: $0) }
                    table.addTableColumn(c)
                }
                ids = p.columns.map(\.id)
                if p.fills {
                    table.sizeLastColumnToFit()
                }
            }
            for (i, spec) in p.columns.enumerated() {
                let column = table.tableColumns[i]
                column.headerToolTip = spec.help.isEmpty ? nil : spec.help
                guard let h = column.headerCell as? TeamHeaderCell else { continue }
                h.stringValue = spec.spoken ?? spec.title
                h.name = spec.title
                h.color = spec.color
                h.trailing = spec.trailing
                h.group = spec.group
                h.groupStart = spec.group != nil && (i == 0 || p.columns[i - 1].group != spec.group)
                h.groupEnd = spec.group != nil && (i == p.columns.count - 1 || p.columns[i + 1].group != spec.group)
                h.sortedAscending = p.sort?.column == spec.id ? p.sort?.ascending : nil
            }
            applying = true
            table.sortDescriptors = p.sort.map { [NSSortDescriptor(key: $0.column, ascending: $0.ascending)] } ?? []
            applying = false
            table.reloadData()
            table.headerView?.needsDisplay = true
        }

        func numberOfRows(in tableView: NSTableView) -> Int {
            parent?.rows ?? 0
        }

        func tableView(_ tableView: NSTableView, viewFor tableColumn: NSTableColumn?, row: Int) -> NSView? {
            guard let id = tableColumn?.identifier, let p = parent else { return nil }
            let view = tableView.makeView(withIdentifier: id, owner: nil) as? HostingCell ?? HostingCell(id)
            view.host.rootView = p.cell(row, id.rawValue)
            return view
        }

        func tableView(_ tableView: NSTableView, sortDescriptorsDidChange oldDescriptors: [NSSortDescriptor]) {
            guard !applying, let d = tableView.sortDescriptors.first, let key = d.key else { return }
            parent?.sort = TeamSort(column: key, ascending: d.ascending)
        }

        /// TOTAL is not a device to pick.
        func tableView(_ tableView: NSTableView, shouldSelectRow row: Int) -> Bool {
            row < (parent?.rows ?? 0) - 1
        }
    }
}

/// A team table's scroll view, which fits the table's last column to its
/// width, when that column fills the room the others leave, as it resizes.
private final class TeamScrollView: NSScrollView {
    var fills = false

    override func tile() {
        super.tile()
        if fills, let table = documentView as? NSTableView, table.numberOfColumns > 0 {
            table.sizeLastColumnToFit()
        }
    }
}

/// A cell of a team table: a SwiftUI view that fills it.
private final class HostingCell: NSTableCellView {
    let host = NSHostingView(rootView: AnyView(EmptyView()))

    init(_ id: NSUserInterfaceItemIdentifier) {
        super.init(frame: .zero)
        identifier = id
        // The table sets the cell's size; the view takes it.
        host.sizingOptions = []
        host.translatesAutoresizingMaskIntoConstraints = false
        addSubview(host)
        NSLayoutConstraint.activate([
            host.leadingAnchor.constraint(equalTo: leadingAnchor),
            host.trailingAnchor.constraint(equalTo: trailingAnchor),
            host.topAnchor.constraint(equalTo: topAnchor),
            host.bottomAnchor.constraint(equalTo: bottomAnchor),
        ])
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError() }
}

/// A table's headings, two lines tall, which the table would make one.
private final class TallHeaderView: NSTableHeaderView {
    override func setFrameSize(_ size: NSSize) {
        super.setFrameSize(NSSize(width: size.width, height: TeamTable.headerHeight))
    }
}

/// A heading of a team table in two lines: its group's name over the first
/// column of the group, with a thin rule over the rest of it, and under it
/// the column's own title, in its color.
private final class TeamHeaderCell: NSTableHeaderCell {
    var group: String?
    var groupStart = false
    var groupEnd = false
    var name = ""
    var color: NSColor?
    var trailing = true
    var sortedAscending: Bool?

    /// The whole cell, which the header hands its interior inset to a line.
    private var whole = NSRect.zero

    override func draw(withFrame cellFrame: NSRect, in controlView: NSView) {
        whole = cellFrame
        super.draw(withFrame: cellFrame, in: controlView)
    }

    override func drawInterior(withFrame _: NSRect, in view: NSView) {
        let frame = whole
        // The header draws the room right of the last column, and the
        // corner over the scroller, with copies of a column's cell, which
        // have no name there.
        guard let table = (view as? NSTableHeaderView)?.tableView,
              table.tableColumns.contains(where: { $0.headerCell === self }) else { return }
        let pad: CGFloat = 6
        let half = frame.height / 2
        // The header view is flipped: its top half is the group's.
        let top = NSRect(x: frame.minX, y: frame.minY + 2, width: frame.width, height: half - 2)
        if let group {
            var x = frame.minX + (groupStart ? pad : 0)
            if groupStart {
                let g = NSAttributedString(string: group, attributes: [
                    .font: NSFont.systemFont(ofSize: 11, weight: .semibold),
                    .foregroundColor: NSColor.secondaryLabelColor,
                ])
                let w = min(g.size().width, frame.maxX - x - pad)
                g.draw(with: NSRect(x: x, y: top.midY - g.size().height / 2, width: w, height: g.size().height),
                       options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine])
                x += w + 6
            }
            let end = frame.maxX - (groupEnd ? pad : 0)
            if end > x {
                NSColor.separatorColor.setFill()
                NSRect(x: x, y: top.midY.rounded(), width: end - x, height: 1).fill()
            }
        }
        let indicator: CGFloat = sortedAscending == nil ? 0 : 14
        let text = NSAttributedString(string: name, attributes: [
            .font: NSFont.systemFont(ofSize: 11, weight: color == nil ? .regular : .semibold),
            .foregroundColor: color ?? NSColor.secondaryLabelColor,
            .paragraphStyle: {
                let p = NSMutableParagraphStyle()
                p.alignment = trailing ? .right : .left
                p.lineBreakMode = .byTruncatingTail
                return p
            }(),
        ])
        let h = text.size().height
        let bottom = NSRect(x: frame.minX + pad, y: frame.minY + half + (half - h) / 2 - 1, width: max(frame.width - 2 * pad - indicator, 0), height: h)
        text.draw(with: bottom, options: [.usesLineFragmentOrigin, .truncatesLastVisibleLine])
        if let ascending = sortedAscending {
            let r = NSRect(x: frame.minX, y: frame.minY + half, width: frame.width, height: half)
            drawSortIndicator(withFrame: r, in: view, ascending: ascending, priority: 0)
        }
    }
}
