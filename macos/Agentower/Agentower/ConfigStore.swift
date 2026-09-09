import Foundation

struct BotConfiguration: Equatable {
    var workspaceRoot: String = ""
    var telegramBotToken: String = ""
    var allowedChatID: String = ""

    var openCodePort: Int = 4096
    var openCodeBin: String = "opencode"
    var openCodeAutostart: Bool = false

    var agentowerStatePath: String = ""
    var telegramAPIRoot: String = ""
    var telegramProxyURL: String = ""

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

final class ConfigStore {
    private enum Key {
        static let workspaceRoot = "workspaceRoot"
        static let telegramBotToken = "telegramBotToken"
        static let allowedChatID = "allowedChatID"
        static let openCodePort = "openCodePort"
        static let openCodeBin = "openCodeBin"
        static let openCodeAutostart = "openCodeAutostart"
        static let agentowerStatePath = "agentowerStatePath"
        static let telegramAPIRoot = "telegramAPIRoot"
        static let telegramProxyURL = "telegramProxyURL"
    }

    private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    func load() -> BotConfiguration {
        BotConfiguration(
            workspaceRoot: defaults.string(forKey: Key.workspaceRoot) ?? "",
            telegramBotToken: defaults.string(forKey: Key.telegramBotToken) ?? "",
            allowedChatID: defaults.string(forKey: Key.allowedChatID) ?? "",
            openCodePort: defaults.object(forKey: Key.openCodePort) as? Int ?? 4096,
            openCodeBin: defaults.string(forKey: Key.openCodeBin) ?? "opencode",
            openCodeAutostart: defaults.bool(forKey: Key.openCodeAutostart),
            agentowerStatePath: defaults.string(forKey: Key.agentowerStatePath) ?? "",
            telegramAPIRoot: defaults.string(forKey: Key.telegramAPIRoot) ?? "",
            telegramProxyURL: defaults.string(forKey: Key.telegramProxyURL) ?? ""
        )
    }

    func save(_ config: BotConfiguration) throws {
        defaults.set(config.workspaceRoot, forKey: Key.workspaceRoot)
        defaults.set(config.telegramBotToken, forKey: Key.telegramBotToken)
        defaults.set(config.allowedChatID, forKey: Key.allowedChatID)
        defaults.set(config.openCodePort, forKey: Key.openCodePort)
        defaults.set(config.openCodeBin, forKey: Key.openCodeBin)
        defaults.set(config.openCodeAutostart, forKey: Key.openCodeAutostart)
        defaults.set(config.agentowerStatePath, forKey: Key.agentowerStatePath)
        defaults.set(config.telegramAPIRoot, forKey: Key.telegramAPIRoot)
        defaults.set(config.telegramProxyURL, forKey: Key.telegramProxyURL)

        try AppPaths.ensureDirectories()
        try writeEnvFile(config)
    }

    private func writeEnvFile(_ config: BotConfiguration) throws {
        var lines: [String] = []
        lines.append("WORKSPACE_ROOT=\(shellQuote(config.workspaceRoot))")
        lines.append("TELEGRAM_BOT_TOKEN=\(shellQuote(config.telegramBotToken))")
        lines.append("ALLOWED_CHAT_ID=\(shellQuote(config.allowedChatID))")

        if !config.openCodePort.description.isEmpty {
            lines.append("OPENCODE_PORT=\(config.openCodePort)")
        }
        if !config.openCodeBin.isEmpty {
            lines.append("OPENCODE_BIN=\(shellQuote(config.openCodeBin))")
        }
        if config.openCodeAutostart {
            lines.append("OPENCODE_AUTOSTART=true")
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