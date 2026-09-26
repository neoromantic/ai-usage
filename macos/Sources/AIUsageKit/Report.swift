import Foundation

/// The report `ai-usage report --json` prints, schema version 4, as
/// docs/json-schema.md describes it. Only the fields the app shows are here;
/// the decoder ignores the rest.
public struct Report: Decodable, Sendable {
    public static let schemaVersion = 4

    public let schemaVersion: Int
    public let generatedAt: Date
    public let collector: Collector
    public let attention: [Attention]
    public let providers: [Provider]
    public let projects: [Project]
    public let team: Team

    /// Decodes a report, or says which schema version it has when that is
    /// not the one this app reads.
    public static func decode(_ data: Data) throws -> Report {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { d in
            let c = try d.singleValueContainer()
            let s = try c.decode(String.self)
            guard let date = parseTime(s) else {
                throw DecodingError.dataCorruptedError(in: c, debugDescription: "not an RFC 3339 time: \(s)")
            }
            return date
        }
        struct Version: Decodable { let schemaVersion: Int }
        let version = try decoder.decode(Version.self, from: data).schemaVersion
        guard version == schemaVersion else { throw ReportError.schema(version) }
        return try decoder.decode(Report.self, from: data)
    }
}

public enum ReportError: Error, LocalizedError, Equatable {
    case schema(Int)

    public var errorDescription: String? {
        switch self {
        case .schema(let v):
            return "This app reads report schema \(Report.schemaVersion); ai-usage printed \(v). Update the app and ai-usage to the same release."
        }
    }
}

/// Parses an RFC 3339 time with or without fractional seconds. The
/// fraction can have up to nine digits, which ISO8601DateFormatter does not
/// read, so it is split off and added back.
public func parseTime(_ s: String) -> Date? {
    var base = Substring(s)
    var fraction = 0.0
    if let dot = s.firstIndex(of: ".") {
        let digits = s[s.index(after: dot)...].prefix(while: \.isNumber)
        guard !digits.isEmpty else { return nil }
        fraction = Double("0." + digits) ?? 0
        base = s[..<dot] + s[digits.endIndex...]
    }
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: String(base))?.addingTimeInterval(fraction)
}

public struct Collector: Decodable, Sendable {
    public let version: String
    public let device: String
    public let deviceLabel: String
    public let osUser: String
    public let team: String
    public let lastRunAt: Date?
    public let lastSuccessAt: Date?
    public let lastError: String?
    public let lastErrorAt: Date?
    public let relay: Relay
    public let schedule: Schedule
    public let update: Update
    /// The collection, the relay, and the update, as the console's header
    /// shows them; empty from an ai-usage that does not say.
    @Absent public var health: [Health]

    public struct Relay: Decodable, Sendable {
        public let url: String?
        public let lastPushAt: Date?
        public let lastPullAt: Date?
        public let pending: Bool
        public let lastError: String?
    }

    public struct Schedule: Decodable, Sendable {
        public let registered: Bool
        public let foreground: Bool
        public let error: String?
    }

    public struct Update: Decodable, Sendable {
        public let checkedAt: Date?
        public let latest: String?
        public let staged: String?
        public let error: String?
    }

    /// How one part of the collector is doing.
    public struct Health: Decodable, Sendable {
        /// `collection`, `relay`, or `update`.
        public let item: String
        /// How the item is, such as `failed` or `pending`; `ok` when nothing
        /// is wrong with it.
        public let status: String
        /// `ok`, `warn`, `error`, `off`, or `info`: the color of its dot.
        public let state: String
        /// When it last collected, or failed to; when it last reached the
        /// relay; when it last checked for a release.
        public let at: Date?
        /// The staged or the available release.
        public let release: String?
    }
}

/// A field that ai-usage added within schema version 4: an earlier release
/// leaves it out, and it reads as empty, false, or 0.
@propertyWrapper
public struct Absent<Value: Decodable & Sendable & AbsentValue>: Decodable, Sendable {
    public var wrappedValue: Value

    public init(wrappedValue: Value) {
        self.wrappedValue = wrappedValue
    }

    public init(from decoder: Decoder) throws {
        wrappedValue = try decoder.singleValueContainer().decode(Value.self)
    }
}

/// What a field that is not there reads as.
public protocol AbsentValue {
    static var absent: Self { get }
}

extension Bool: AbsentValue {
    public static var absent: Bool { false }
}

extension Int: AbsentValue {
    public static var absent: Int { 0 }
}

extension Array: AbsentValue {
    public static var absent: [Element] { [] }
}

extension KeyedDecodingContainer {
    public func decode<Value>(_: Absent<Value>.Type, forKey key: Key) throws -> Absent<Value> {
        Absent(wrappedValue: try decodeIfPresent(Value.self, forKey: key) ?? .absent)
    }
}

public struct Attention: Decodable, Sendable {
    public enum Kind: String, Decodable, Sendable {
        case out, over, error, silent, old, under, other

        public init(from decoder: Decoder) throws {
            self = Kind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .other
        }
    }

    public let kind: Kind
    public let provider: String?
    public let account: String?
    public let name: String?
    public let window: String?
    public let devices: [String]?
    public let at: Date?
    public let resetsAt: Date?
    public let percent: Double?
    public let readingAgeSeconds: Int?
    public let message: String?
}

public struct Provider: Decodable, Sendable {
    public let provider: String
    public let status: String
    public let error: String?
    public let homes: [String]
    public let accounts: [Account]
}

/// One account on this device.
public struct Account: Decodable, Sendable {
    public let label: String
    public let name: String
    public let current: Bool
    public let plan: String?
    public let state: String
    public let quota: Quota?
    public let link: Link?
    public let sessions: Int
    public let usage: Usage
    public let lastActiveAt: Date?
}

public struct Link: Decodable, Sendable {
    public let provider: String
    public let label: String
}

public struct Quota: Decodable, Sendable {
    public let observedAt: Date
    public let ageSeconds: Int
    public let stale: Bool
    public let source: String?
    public let from: String?
    public let device: String?
    public let windows: [Window]

    public var main: Window? { windows.first(where: \.main) }

    /// A window's name beside the main one: "7d Fable" is "Fable" when the
    /// main window is "7d".
    public func shortName(_ window: String) -> String {
        guard let m = main?.name, window.hasPrefix(m + " ") else { return window }
        return String(window.dropFirst(m.count + 1))
    }

    public struct Window: Decodable, Sendable {
        public let name: String
        public let percent: Double
        public let resetsAt: Date?
        public let minutes: Int?
        public let main: Bool
        /// The window limits the account: it is the main window, or one
        /// that limits the account more. False from an ai-usage that does
        /// not say.
        @Absent public var limits: Bool
        public let observedAt: Date?
        public let stale: Bool
        public let reset: Bool
        public let unread: Bool?
        public let state: String
        public let forecast: Forecast?

        /// How full the window is now is known: it has not reset since it was
        /// read, and the reading covers it.
        public var known: Bool { !reset && unread != true }
    }
}

public struct Forecast: Decodable, Sendable {
    public let percent: Double
    public let elapsed: Double
    public let runsOutAt: Date?
}

/// Input plus output tokens in the report's four periods.
public struct Usage: Decodable, Sendable {
    public let today: Int
    public let week: Int
    public let month: Int
    public let quarter: Int

    enum CodingKeys: String, CodingKey {
        case today, week = "7d", month = "30d", quarter = "90d"
    }

    public subscript(p: Period) -> Int {
        switch p {
        case .today: return today
        case .week: return week
        case .month: return month
        case .quarter: return quarter
        }
    }
}

/// A part of a whole in percent, in each period; nil where the whole is none.
public struct Share: Decodable, Sendable {
    public let today: Double?
    public let week: Double?
    public let month: Double?
    public let quarter: Double?

    enum CodingKeys: String, CodingKey {
        case today, week = "7d", month = "30d", quarter = "90d"
    }

    public subscript(p: Period) -> Double? {
        switch p {
        case .today: return today
        case .week: return week
        case .month: return month
        case .quarter: return quarter
        }
    }
}

public enum Period: String, CaseIterable, Identifiable, Sendable {
    case today, week, month, quarter

    public var id: Self { self }

    public var title: String {
        switch self {
        case .today: return "Today"
        case .week: return "7 Days"
        case .month: return "30 Days"
        case .quarter: return "90 Days"
        }
    }
}

public struct Tokens: Decodable, Sendable {
    public let input: Int
    public let output: Int
    public let cacheRead: Int
    public let cacheWrite: Int
}

/// A git repository, or a folder outside any, with the sessions in its
/// subfolders and worktrees.
public struct Project: Decodable, Sendable {
    public let path: String
    /// How many working folders count under the project, 1 or more; 0 from
    /// an ai-usage that does not say.
    @Absent public var folders: Int
    public let sessions: Int
    public let usage: Usage
    public let providers: [String]?
    public let lastActiveAt: Date?
}

public struct Team: Decodable, Sendable {
    public let pulledAt: Date?
    public let latestVersion: String?
    public let devices: [TeamDevice]
    public let providers: [TeamProvider]
    public let matrix: Matrix
}

public struct TeamDevice: Decodable, Sendable {
    public let device: String
    public let label: String
    public let osUser: String
    public let thisDevice: Bool
    public let collectorVersion: String
    public let collectedAt: Date?
    public let ageSeconds: Int
    public let lastSuccessAt: Date?
    public let lastError: String?
    public let updateError: String?
    public let sources: [Source]
    public let error: String?
    public let silent: Bool
    public let old: Bool
    public let behindSince: Date?
    /// An old device that does not update itself. False from an ai-usage
    /// that does not say.
    @Absent public var notUpdating: Bool
    public let usage: Usage

    public struct Source: Decodable, Sendable {
        public let provider: String
        public let status: String
        public let error: String?
    }
}

public struct TeamProvider: Decodable, Sendable {
    public let provider: String
    public let accounts: [TeamAccount]
}

/// An account summed across the team's devices.
public struct TeamAccount: Decodable, Sendable {
    public let label: String
    public let name: String
    public let alias: String?
    public let subscription: Bool
    public let current: Bool
    public let devices: [String]
    public let plan: String?
    public let state: String
    public let quota: Quota?
    public let link: Link?
    public let sessions: Int
    public let usage: Usage
    public let users: Int
    public let busiest: String?
    public let lastActiveAt: Date?
    public let perDevice: [PerDevice]
    public let linkedUsage: [LinkedUsage]

    public struct PerDevice: Decodable, Sendable {
        public let device: String
        public let current: Bool
        public let sessions: Int
        public let usage: Usage
        public let lastActiveAt: Date?
    }

    public struct LinkedUsage: Decodable, Sendable {
        public let provider: String
        public let label: String?
        public let devices: [String]?
        public let sessions: Int
        public let tokens: Tokens
    }
}

public struct Matrix: Decodable, Sendable {
    public let columns: [Column]
    public let rows: [Row]

    public struct Column: Decodable, Sendable {
        public let provider: String
        public let label: String?
        public let name: String
        public let noQuota: Bool
        public let state: String
        public let percent: Double?
        public let usage: Usage
        public let windowTokens: Int
    }

    public struct Row: Decodable, Sendable {
        public let device: String
        public let deviceId: String
        public let cells: [Cell]
        public let usage: Usage
        public let share: Share
    }

    public struct Cell: Decodable, Sendable {
        public let usage: Usage
        public let windowTokens: Int
        public let share: Share
    }
}
