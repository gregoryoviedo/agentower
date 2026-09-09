import Foundation
import Combine

final class BotController {
    private static let logFormatter: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        return f
    }()

    private var process: Process?
    private var stdoutPipe: Pipe?
    private var stderrPipe: Pipe?
    private var logFileHandle: FileHandle?
    private var terminateTimer: DispatchSourceTimer?

    private let queue = DispatchQueue(label: "dev.agentower.app.botcontroller", qos: .userInitiated)
    private(set) var state: BotStatus = .stopped
    private(set) var lastError: String?

    let stateSubject = PassthroughSubject<BotStatus, Never>()
    var envFileURL: URL = AppPaths.envFile
    var telegramAPIRoot: String = ""
    var telegramProxyURL: String = ""

    private var onStateChange: ((BotStatus) -> Void)?

    init(onStateChange: @escaping (BotStatus) -> Void) {
        self.onStateChange = onStateChange
    }

    var isRunning: Bool {
        state.isRunning
    }

    func start(telegramAPIRoot: String = "", telegramProxyURL: String = "") {
        guard !state.isRunning else { return }
        self.telegramAPIRoot = telegramAPIRoot
        self.telegramProxyURL = telegramProxyURL

        do {
            try AppPaths.ensureDirectories()
        } catch {
            update(state: .crashed(exitCode: -1), error: "No se pudo crear ~/Library/Application Support/Agentower: \(error.localizedDescription)")
            return
        }

        do {
            try openLogFile()
        } catch {
            update(state: .crashed(exitCode: -1), error: "No se pudo abrir el log: \(error.localizedDescription)")
            return
        }

        guard let binaryURL = locateBinary() else {
            log("ERROR: no se encontró el binario remote-bot embebido en el bundle.")
            update(state: .crashed(exitCode: -1), error: "No se encontró el binario remote-bot embebido en el bundle.")
            return
        }
        guard FileManager.default.fileExists(atPath: envFileURL.path) else {
            log("ERROR: falta el archivo .env en \(envFileURL.path).")
            update(state: .crashed(exitCode: -1), error: "Falta el archivo .env. Configura el bot primero.")
            return
        }
        guard FileManager.default.isExecutableFile(atPath: binaryURL.path) else {
            log("ERROR: el binario en \(binaryURL.path) no es ejecutable.")
            update(state: .crashed(exitCode: -1), error: "El binario remote-bot no tiene permisos de ejecución.")
            return
        }

        log("Arrancando remote-bot en \(binaryURL.path) con ENV_FILE=\(envFileURL.path)")

        let proc = Process()
        proc.executableURL = binaryURL
        var env = ProcessInfo.processInfo.environment
        env["ENV_FILE"] = envFileURL.path
        env["AGENTOWER_STATE_PATH"] = AppPaths.supportDirectory
            .appendingPathComponent("state.db").path
        env["GIN_MODE"] = "release"
        if !telegramAPIRoot.isEmpty {
            env["TELEGRAM_API_ROOT"] = telegramAPIRoot
        }
        if !telegramProxyURL.isEmpty {
            env["TELEGRAM_PROXY_URL"] = telegramProxyURL
        }
        proc.environment = env
        proc.currentDirectoryURL = URL(fileURLWithPath: NSHomeDirectory())
        proc.standardInput = FileHandle.nullDevice

        let stdout = Pipe()
        let stderr = Pipe()
        proc.standardOutput = stdout
        proc.standardError = stderr

        stdout.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty else { return }
            self?.writeToLog(data: data)
        }
        stderr.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty else { return }
            self?.writeToLog(data: data)
        }

        proc.terminationHandler = { [weak self] p in
            DispatchQueue.main.async {
                self?.handleTermination(process: p)
            }
        }

        do {
            try proc.run()
            process = proc
            stdoutPipe = stdout
            stderrPipe = stderr
            update(state: .running(pid: proc.processIdentifier))
        } catch {
            update(state: .crashed(exitCode: -1), error: "No se pudo iniciar: \(error.localizedDescription)")
        }
    }

    func stop() {
        guard let proc = process, proc.isRunning else {
            update(state: .stopped)
            return
        }
        update(state: .stopping)
        proc.terminate()

        terminateTimer?.cancel()
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + 5)
        timer.setEventHandler { [weak self] in
            guard let self = self, let p = self.process, p.isRunning else { return }
            kill(p.processIdentifier, SIGKILL)
        }
        timer.resume()
        terminateTimer = timer
    }

    func shutdown() {
        stop()
    }

    private func handleTermination(process p: Process) {
        stdoutPipe?.fileHandleForReading.readabilityHandler = nil
        stderrPipe?.fileHandleForReading.readabilityHandler = nil
        stdoutPipe = nil
        stderrPipe = nil
        process = nil
        terminateTimer?.cancel()
        terminateTimer = nil
        closeLogFile()

        let code = p.terminationStatus
        if case .stopping = state {
            update(state: .stopped)
        } else if code == 0 {
            update(state: .stopped)
        } else {
            update(state: .crashed(exitCode: code))
        }
    }

    private func locateBinary() -> URL? {
        if let bundled = Bundle.main.url(forResource: "remote-bot", withExtension: nil) {
            return bundled
        }
        if let bundled = Bundle.main.path(forResource: "remote-bot", ofType: "") {
            return URL(fileURLWithPath: bundled)
        }
        if let resourceURL = Bundle.main.resourceURL {
            let candidate = resourceURL.appendingPathComponent("remote-bot")
            if FileManager.default.isExecutableFile(atPath: candidate.path) {
                return candidate
            }
        }
        let fallback = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("bin/remote-bot")
        return FileManager.default.isExecutableFile(atPath: fallback.path) ? fallback : nil
    }

    private func openLogFile() throws {
        let url = AppPaths.botLogFile
        let fm = FileManager.default
        if !fm.fileExists(atPath: url.path) {
            guard fm.createFile(atPath: url.path, contents: nil, attributes: nil) else {
                throw NSError(domain: "BotController", code: 1, userInfo: [NSLocalizedDescriptionKey: "No se pudo crear el archivo de log en \(url.path)"])
            }
        }
        let handle = try FileHandle(forWritingTo: url)
        try handle.seekToEnd()
        logFileHandle = handle
        let banner = "\n--- remote-bot start at \(BotController.logFormatter.string(from: Date())) ---\n"
        if let data = banner.data(using: .utf8) {
            handle.write(data)
        }
    }

    private func closeLogFile() {
        try? logFileHandle?.close()
        logFileHandle = nil
    }

    private func writeToLog(data: Data) {
        try? logFileHandle?.write(contentsOf: data)
    }

    private func update(state newState: BotStatus, error: String? = nil) {
        state = newState
        lastError = error
        DispatchQueue.main.async { [weak self] in
            self?.stateSubject.send(newState)
            self?.onStateChange?(newState)
            if let error = error {
                NSLog("BotController error: %@", error)
                self?.log("ERROR: \(error)")
            }
        }
    }

    private func log(_ message: String) {
        let line = "[\(BotController.logFormatter.string(from: Date()))] \(message)\n"
        if let data = line.data(using: .utf8) {
            try? logFileHandle?.write(contentsOf: data)
        }
        NSLog("BotController: %@", message)
    }
}