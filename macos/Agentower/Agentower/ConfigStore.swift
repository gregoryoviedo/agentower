import Foundation

struct BotConfiguration: Equatable {
    var workspaceRoot: String = ""
    var telegramBotToken: String = ""
    var allowedChatID: String = ""

    var agentowerStatePath: String = ""
    var telegramAPIRoot: String = ""
    var telegramProxyURL: String = ""

    // Per-agent settings, keyed by AgentKind (lowercase). Opencode is
    // always present so the existing single-agent Settings keep working.
    // Each row's available flag is true once the binary is detected
    // (PATH for the standard CLIs; VS Code extension bundle for Copilot).
    var agents: [String: AgentSettings] = [:]

    var isValid: Bool {
        !workspaceRoot.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !telegramBotToken.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && Int64(allowedChatID.trimmingCharacters(in: .whitespacesAndNewlines)) ?? 0 != 0
    }

    var validationMessage: String? {
        if workspaceRoot.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "WORKSPACE_ROOT es obligatorio."
        }
        if telegramBotToken.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return "TELEGRAM_BOT_TOKEN es obligatorio."
        }
        if (Int64(allowedChatID.trimmingCharacters(in: .whitespacesAndNewlines)) ?? 0) == 0 {
            return "ALLOWED_CHAT_ID debe ser un entero distinto de cero."
        }
        return nil
    }
}

// AgentSettings is one row in the multi-agent Settings section.
struct AgentSettings: Equatable {
    var enabled: Bool = false
    var bin: String = ""
    var port: Int = 4096
    var args: String = ""
    // available mirrors the bot's Available flag for this kind. The
    // wrapper flips it to true once the matching adapter ships; until
    // then the row stays disabled with a "Próximamente" hint.
    var available: Bool = false
}

final class ConfigStore {
    private enum Key {
        static let workspaceRoot = "workspaceRoot"
        static let telegramBotToken = "telegramBotToken"
        static let allowedChatID = "allowedChatID"
        static let agentowerStatePath = "agentowerStatePath"
        static let telegramAPIRoot = "telegramAPIRoot"
        static let telegramProxyURL = "telegramProxyURL"
        // Multi-agent keys. Each kind has its own sub-namespace.
        static let agentEnabledPrefix = "agentEnabled."
        static let agentBinPrefix = "agentBin."
        static let agentPortPrefix = "agentPort."
        static let agentArgsPrefix = "agentArgs."
    }

    /// The set of agent kinds the wrapper knows about, in display order.
    /// Matches internal/adapter/agents.AllAgentKinds.
    static let agentKinds: [String] = ["opencode", "claude", "kiro", "copilot"]

    /// Default port per agent. Matches the detector defaults.
    static func defaultPort(for kind: String) -> Int {
        switch kind {
        case "opencode": return 4096
        case "claude":   return 4097
        case "kiro":     return 4099
        case "copilot":  return 4100
        default:         return 4096
        }
    }

    /// Display name per agent. Matches the detector DisplayName strings.
    static func displayName(for kind: String) -> String {
        switch kind {
        case "opencode": return "opencode"
        case "claude":   return "Claude"
        case "kiro":     return "Kiro"
        case "copilot":  return "GitHub Copilot"
        default:         return kind
        }
    }

    private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    func load() -> BotConfiguration {
        var cfg = BotConfiguration(
            workspaceRoot: defaults.string(forKey: Key.workspaceRoot) ?? "",
            telegramBotToken: defaults.string(forKey: Key.telegramBotToken) ?? "",
            allowedChatID: defaults.string(forKey: Key.allowedChatID) ?? "",
            agentowerStatePath: defaults.string(forKey: Key.agentowerStatePath) ?? "",
            telegramAPIRoot: defaults.string(forKey: Key.telegramAPIRoot) ?? "",
            telegramProxyURL: defaults.string(forKey: Key.telegramProxyURL) ?? ""
        )
        cfg.agents = loadAgents(cfg: cfg)
        return cfg
    }

    func save(_ config: BotConfiguration) throws {
        defaults.set(config.workspaceRoot, forKey: Key.workspaceRoot)
        defaults.set(config.telegramBotToken, forKey: Key.telegramBotToken)
        defaults.set(config.allowedChatID, forKey: Key.allowedChatID)
        defaults.set(config.agentowerStatePath, forKey: Key.agentowerStatePath)
        defaults.set(config.telegramAPIRoot, forKey: Key.telegramAPIRoot)
        defaults.set(config.telegramProxyURL, forKey: Key.telegramProxyURL)

        for (kind, settings) in config.agents {
            defaults.set(settings.enabled, forKey: Key.agentEnabledPrefix + kind)
            defaults.set(settings.bin, forKey: Key.agentBinPrefix + kind)
            defaults.set(settings.port, forKey: Key.agentPortPrefix + kind)
            defaults.set(settings.args, forKey: Key.agentArgsPrefix + kind)
        }

        try AppPaths.ensureDirectories()
        try writeEnvFile(config)
    }

    /// Detected returns the list of agent kinds whose binary the
    /// wrapper can find. PATH is the first source; for copilot we also
    /// fall back to `copilot-language-server` and the VS Code / VS Code
    /// Server extension bundles, mirroring the Go detector
    /// (internal/adapter/agents/detector.go:scanCopilot).
    static func detectAgentsInPath() -> [String: String] {
        var found: [String: String] = [:]
        let pathCandidates: [(String, [String])] = [
            ("opencode", ["opencode"]),
            ("claude",   ["claude"]),
            ("kiro",     ["kiro"]),
            ("copilot",  ["copilot", "copilot-language-server"]),
        ]
        for (kind, binaries) in pathCandidates {
            for binary in binaries {
                if let path = Self.which(binary) {
                    found[kind] = path
                    break
                }
            }
        }
        if found["copilot"] == nil, let bundle = findVSCodeCopilotBundle() {
            found["copilot"] = bundle
        }
        return found
    }

    private static func which(_ binary: String) -> String? {
        let task = Process()
        task.launchPath = "/usr/bin/which"
        task.arguments = [binary]
        let pipe = Pipe()
        task.standardOutput = pipe
        task.standardError = Pipe()
        do {
            try task.run()
            task.waitUntilExit()
            guard task.terminationStatus == 0 else { return nil }
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let raw = String(data: data, encoding: .utf8) ?? ""
            return raw.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                ? nil
                : raw.trimmingCharacters(in: .whitespacesAndNewlines)
        } catch {
            return nil
        }
    }

    /// Mirrors Go's findVSCodeCopilotBundle: scans the user's VS Code
    /// install for the github.copilot* extension folder and returns the
    /// newest version's dist/extension.js (the LSP entry point).
    private static func findVSCodeCopilotBundle() -> String? {
        let home = FileManager.default.homeDirectoryForCurrentUser
        let roots = [
            ".vscode/extensions",
            ".vscode-server/extensions",
        ]
        let prefixes = ["github.copilot-", "github.copilot-chat-"]
        for root in roots {
            let dir = home.appendingPathComponent(root)
            guard let entries = try? FileManager.default.contentsOfDirectory(
                at: dir, includingPropertiesForKeys: nil, options: [.skipsHiddenFiles]
            ) else { continue }
            for prefix in prefixes {
                let matches = entries
                    .filter { $0.lastPathComponent.hasPrefix(prefix) }
                    .map { $0.lastPathComponent }
                    .sorted()
                if let newest = matches.last {
                    let bundle = dir
                        .appendingPathComponent(newest)
                        .appendingPathComponent("dist/extension.js")
                    if FileManager.default.fileExists(atPath: bundle.path) {
                        return bundle.path
                    }
                }
            }
        }
        return nil
    }

    private func loadAgents(cfg: BotConfiguration) -> [String: AgentSettings] {
        let detected = Self.detectAgentsInPath()
        var out: [String: AgentSettings] = [:]
        for kind in Self.agentKinds {
            let storedBin = defaults.string(forKey: Key.agentBinPrefix + kind)
            let storedPort = defaults.object(forKey: Key.agentPortPrefix + kind) as? Int
            let storedEnabled = defaults.object(forKey: Key.agentEnabledPrefix + kind) as? Bool
            let resolvedBin: String = {
                if let stored = storedBin, !stored.isEmpty { return stored }
                if let detectedBin = detected[kind] { return detectedBin }
                return kind
            }()
            let resolvedPort = storedPort ?? Self.defaultPort(for: kind)
            let available = kind == "opencode" || detected[kind] != nil
            // Default the enabled flag to "true when available" so the
            // bot picks the agent up out of the box on first run.
            let resolvedEnabled = storedEnabled ?? available
            out[kind] = AgentSettings(
                enabled: resolvedEnabled,
                bin: resolvedBin,
                port: resolvedPort,
                args: defaults.string(forKey: Key.agentArgsPrefix + kind) ?? "",
                available: available
            )
        }
        return out
    }

    private func writeEnvFile(_ config: BotConfiguration) throws {
        var lines: [String] = []
        lines.append("WORKSPACE_ROOT=\(shellQuote(config.workspaceRoot))")
        lines.append("TELEGRAM_BOT_TOKEN=\(shellQuote(config.telegramBotToken))")
        lines.append("ALLOWED_CHAT_ID=\(shellQuote(config.allowedChatID))")

        for (kind, settings) in config.agents {
            let prefix = "AGENT_" + kind.uppercased()
            lines.append("\(prefix)_ENABLED=\(settings.enabled ? "true" : "false")")
            if !settings.bin.isEmpty {
                lines.append("\(prefix)_BIN=\(shellQuote(settings.bin))")
            }
            if settings.port > 0 {
                lines.append("\(prefix)_PORT=\(settings.port)")
            }
            let trimmedArgs = settings.args.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmedArgs.isEmpty {
                lines.append("\(prefix)_ARGS=\(shellQuote(trimmedArgs))")
            }
        }
        if !config.agentowerStatePath.isEmpty {
            lines.append("AGENTOWER_STATE_PATH=\(shellQuote(config.agentowerStatePath))")
        }
        if !config.telegramAPIRoot.isEmpty {
            lines.append("TELEGRAM_API_ROOT=\(shellQuote(config.telegramAPIRoot))")
        }
        if !config.telegramProxyURL.isEmpty {
            lines.append("TELEGRAM_PROXY_URL=\(shellQuote(config.telegramProxyURL))")
        }

        let content = lines.joined(separator: "\n") + "\n"
        try content.write(to: AppPaths.envFile, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: AppPaths.envFile.path)
    }

    private func shellQuote(_ value: String) -> String {
        if value.allSatisfy({ $0.isLetter || $0.isNumber || "/._-:@?=+&%".contains($0) }) {
            return value
        }
        let escaped = value.replacingOccurrences(of: "\"", with: "\\\"")
        return "\"\(escaped)\""
    }
}
