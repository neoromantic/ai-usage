import Foundation

/// Whether usage stats go to ai-usage's author, as `ai-usage telemetry
/// status --json` says, with the notice while it is due. The app shows the
/// notice once and has the switch in Settings; ai-usage keeps the state and
/// sends the events.
public struct UsageStats: Decodable, Equatable, Sendable {
    public var enabled: Bool
    /// `AI_USAGE_TELEMETRY=debug`: events are printed, not sent.
    public var debug: Bool
    /// The rule that decided: `default`, `state` (the switch), `env`,
    /// `do-not-track`, `ci`, `test`, `dev-version`, or `no-endpoint`.
    public var reason: String
    /// The notice, while nobody has seen it and usage stats are on; until
    /// then nothing is sent.
    public var notice: String?

    public init(enabled: Bool, debug: Bool = false, reason: String, notice: String? = nil) {
        self.enabled = enabled
        self.debug = debug
        self.reason = reason
        self.notice = notice
    }

    public static func decode(_ data: Data) throws -> UsageStats {
        try JSONDecoder().decode(UsageStats.self, from: data)
    }

    /// Whether the switch decides. The environment or the build can decide
    /// instead, and the switch then only shows what they decided.
    public var switchable: Bool { reason == "default" || reason == "state" }

    /// Why the switch does not decide, in a few words; nil when it does.
    public var decidedBy: String? {
        switch reason {
        case "default", "state": return nil
        case "do-not-track": return "DO_NOT_TRACK is set"
        case "env": return "AI_USAGE_TELEMETRY is set"
        case "ci": return "ai-usage runs in CI"
        case "test": return "ai-usage runs in tests"
        case "dev-version": return "a development build of ai-usage sends none"
        case "no-endpoint": return "this build of ai-usage sends none"
        default: return reason
        }
    }

    /// The notice as Markdown, for a Text: its commands as code, and its
    /// address as a link.
    public static func markdown(_ notice: String) -> String {
        notice.replacingOccurrences(of: "https://usage.sergeybaus.com", with: "[usage.sergeybaus.com](https://usage.sergeybaus.com)")
    }

    /// The app's own events, by the names `ai-usage telemetry record` takes.
    public enum Event: String, Sendable {
        case started = "app_started"
        case popover
        case teamWindow = "team window"
        case settings
    }

    /// The command that records an event.
    public static func record(_ e: Event) -> [String] {
        e == .started ? ["telemetry", "record", e.rawValue] : ["telemetry", "record", "feature_used", e.rawValue]
    }

    /// The environment variable that marks the app's runs of ai-usage, such
    /// as its report every minute, which record nothing themselves.
    public static let callerKey = "AI_USAGE_CALLER"
}
