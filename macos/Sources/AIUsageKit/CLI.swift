import Foundation

/// Where the `ai-usage` command is, and what its collector's LaunchAgent
/// tells about how to run it.
public struct CLILocation: Equatable, Sendable {
    public var executable: URL
    /// The collector's folder, the LaunchAgent's `--home`.
    public var stateHome: String?
    /// The LaunchAgent's PATH, which the tools it reads need.
    public var path: String?

    public init(executable: URL, stateHome: String? = nil, path: String? = nil) {
        self.executable = executable
        self.stateHome = stateHome
        self.path = path
    }

    public static let agentLabel = "io.github.neoromantic.ai-usage"

    /// The folders the installer and package managers put the command in.
    public static func standardDirs(home: URL) -> [String] {
        [home.appendingPathComponent(".local/bin").path, home.appendingPathComponent("bin").path, "/opt/homebrew/bin", "/usr/local/bin"]
    }

    /// Finds the command: `AI_USAGE_BIN`, else the program the collector's
    /// LaunchAgent runs, else the first of dirs that has it, by default the
    /// standard folders.
    public static func locate(environment: [String: String] = ProcessInfo.processInfo.environment,
                              home: URL = FileManager.default.homeDirectoryForCurrentUser,
                              dirs: [String]? = nil) -> CLILocation? {
        let fm = FileManager.default
        let executable = { (path: String) in fm.isExecutableFile(atPath: path) ? URL(fileURLWithPath: path) : nil }
        var agentProgram: String?, stateHome: String?, path: String?
        let plist = home.appendingPathComponent("Library/LaunchAgents/\(agentLabel).plist")
        if let data = try? Data(contentsOf: plist),
           let dict = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any] {
            let args = dict["ProgramArguments"] as? [String] ?? []
            agentProgram = args.first
            if let i = args.firstIndex(of: "--home"), i + 1 < args.count {
                stateHome = args[i + 1]
            }
            path = (dict["EnvironmentVariables"] as? [String: String])?["PATH"]
        }
        let candidates = [environment["AI_USAGE_BIN"], agentProgram] + (dirs ?? standardDirs(home: home)).map { $0 + "/ai-usage" }
        for case let c? in candidates where !c.isEmpty {
            if let url = executable(c) {
                return CLILocation(executable: url, stateHome: stateHome, path: path)
            }
        }
        return nil
    }
}

public enum CLIError: Error, LocalizedError, Equatable {
    /// The command exited with a status other than 0; the message is its
    /// standard error.
    case failed(status: Int32, message: String)
    case timedOut(seconds: TimeInterval)
    case launch(String)

    public var errorDescription: String? {
        switch self {
        case .failed(_, let message): return message
        case .timedOut(let s): return "ai-usage did not finish in \(Int(s)) seconds"
        case .launch(let why): return "could not start ai-usage: \(why)"
        }
    }
}

/// Runs `ai-usage` commands.
public struct CLI: Sendable {
    public let location: CLILocation

    public init(location: CLILocation) {
        self.location = location
    }

    /// The child's environment: this one, with the LaunchAgent's PATH, else
    /// this PATH and the standard folders, and the collector's folder.
    public func environment(_ base: [String: String] = ProcessInfo.processInfo.environment) -> [String: String] {
        var env = base
        let home = FileManager.default.homeDirectoryForCurrentUser
        env["PATH"] = location.path ?? ([base["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin"] + CLILocation.standardDirs(home: home)).joined(separator: ":")
        if let h = location.stateHome {
            env["AI_USAGE_HOME"] = h
        }
        return env
    }

    public func run(_ args: [String], input: Data? = nil, timeout: TimeInterval = 30) async throws -> Data {
        try await withCheckedThrowingContinuation { cont in
            DispatchQueue.global(qos: .userInitiated).async {
                cont.resume(with: Result { try runSync(args, input: input, timeout: timeout) })
            }
        }
    }

    /// Runs a command and returns its standard output. Both pipes are read
    /// while it runs, since a report is larger than a pipe holds.
    public func runSync(_ args: [String], input: Data? = nil, timeout: TimeInterval = 30) throws -> Data {
        let p = Process()
        p.executableURL = location.executable
        p.arguments = args
        p.environment = environment()
        let stdout = Pipe(), stderr = Pipe(), stdin = Pipe()
        p.standardOutput = stdout
        p.standardError = stderr
        p.standardInput = input == nil ? FileHandle.nullDevice : stdin
        do {
            try p.run()
        } catch {
            throw CLIError.launch(error.localizedDescription)
        }
        let deadline = DispatchTime.now() + timeout
        let out = Collected(stdout.fileHandleForReading), err = Collected(stderr.fileHandleForReading)
        if let input {
            // A child that exits without reading closes the pipe, and the
            // write fails with EPIPE instead of killing the app by a signal.
            signal(SIGPIPE, SIG_IGN)
            try? stdin.fileHandleForWriting.write(contentsOf: input)
            try? stdin.fileHandleForWriting.close()
        }
        if !out.wait(until: deadline) || !err.wait(until: deadline) {
            p.terminate()
            if !out.wait(until: .now() + 2) {
                kill(p.processIdentifier, SIGKILL)
            }
            throw CLIError.timedOut(seconds: timeout)
        }
        p.waitUntilExit()
        guard p.terminationStatus == 0 else {
            var message = Self.message(stderr: String(decoding: err.data, as: UTF8.self))
            if message.isEmpty {
                message = "ai-usage exited with status \(p.terminationStatus)"
            }
            throw CLIError.failed(status: p.terminationStatus, message: message)
        }
        return out.data
    }

    /// The error in what a failed command wrote to standard error: its first
    /// paragraph, since the whole help follows a usage error, without the
    /// lines that say it waited for another run, and without "ai-usage: ".
    public static func message(stderr: String) -> String {
        let text = stderr.trimmingCharacters(in: .whitespacesAndNewlines)
        let paragraph = text.components(separatedBy: "\n\n").first ?? ""
        let waiting = ["ai-usage: waiting for another run", "ai-usage: another run"]
        return paragraph.split(separator: "\n", omittingEmptySubsequences: false)
            .filter { line in !waiting.contains { line.hasPrefix($0) } }
            .map { $0.hasPrefix("ai-usage: ") ? String($0.dropFirst("ai-usage: ".count)) : String($0) }
            .joined(separator: "\n")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

/// Everything read from a pipe until its end, on a thread of its own.
private final class Collected: @unchecked Sendable {
    private let done = DispatchSemaphore(value: 0)
    private(set) var data = Data()

    init(_ handle: FileHandle) {
        DispatchQueue.global(qos: .userInitiated).async {
            self.data = handle.readDataToEndOfFile()
            self.done.signal()
        }
    }

    /// Waits for the end of the pipe; true once it came.
    func wait(until deadline: DispatchTime) -> Bool {
        guard done.wait(timeout: deadline) == .success else { return false }
        done.signal()
        return true
    }
}
