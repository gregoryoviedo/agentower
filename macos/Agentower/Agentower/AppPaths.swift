import Foundation

enum AppPaths {
    static var supportDirectory: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("Library/Application Support")
        return base.appendingPathComponent("Agentower", isDirectory: true)
    }

    static var envFile: URL {
        supportDirectory.appendingPathComponent(".env")
    }

    static var logsDirectory: URL {
        let base = FileManager.default.urls(for: .libraryDirectory, in: .userDomainMask).first
            ?? URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("Library")
        return base.appendingPathComponent("Logs/Agentower", isDirectory: true)
    }

    static var botLogFile: URL {
        logsDirectory.appendingPathComponent("bot.log")
    }

    static func ensureDirectories() throws {
        let fm = FileManager.default
        try fm.createDirectory(at: supportDirectory, withIntermediateDirectories: true, attributes: nil)
        try fm.createDirectory(at: logsDirectory, withIntermediateDirectories: true, attributes: nil)
    }
}