import Foundation
import AppKit
import UserNotifications
import os.log

/// IdleNotifier is the macOS wrapper side of the "task done" feature. It
/// watches CGEventSourceSecondsSinceLastEventType to learn how long the
/// user has been away from the keyboard, polls the bot's local control
/// server to learn whether a session just completed, and — when both
/// conditions hold — fires a local notification AND asks the bot to send
/// a Telegram message to the configured chat.
final class IdleNotifier {
    struct State: Decodable {
        let chatId: Int64
        let activeProject: String?
        let activeSession: String?
        let pendingNotifChat: Int64
        let lastCompleted: CompletedSession?

        enum CodingKeys: String, CodingKey {
            case chatId = "ChatID"
            case activeProject = "ActiveProject"
            case activeSession = "ActiveSession"
            case pendingNotifChat = "PendingNotifChat"
            case lastCompleted = "LastCompleted"
        }
    }

    struct CompletedSession: Decodable {
        let chatId: Int64
        let sessionId: String
        let projectName: String?
        let directory: String?
        let title: String?
        let preview: String?
        let completedAt: Date

        enum CodingKeys: String, CodingKey {
            case chatId = "ChatID"
            case sessionId = "SessionID"
            case projectName = "ProjectName"
            case directory = "Directory"
            case title = "Title"
            case preview = "Preview"
            case completedAt = "CompletedAt"
        }
    }

    struct NotifyResponse: Decodable {
        let chatId: Int64
        let sessionId: String

        enum CodingKeys: String, CodingKey {
            case chatId = "chat_id"
            case sessionId = "session_id"
        }
    }

    // The thresholds are intentionally generous so the wrapper doesn't
    // become annoying; users can always tune them later in Settings.
    private static let pollInterval: TimeInterval = 5
    private static let idleThreshold: TimeInterval = 5 * 60
    private static let completionFreshness: TimeInterval = 60 * 60
    private static let cooldownAfterNotify: TimeInterval = 60

    private let logger = OSLog(subsystem: "dev.agentower.app", category: "IdleNotifier")
    private let controlFileURL: URL
    private let session: URLSession
    private var pollTimer: DispatchSourceTimer?
    private let queue = DispatchQueue(label: "dev.agentower.app.idlenotifier", qos: .utility)
    private var lastNotifySentAt: Date?

    init(supportDirectory: URL = AppPaths.supportDirectory) {
        self.controlFileURL = supportDirectory.appendingPathComponent("control.json")
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 4
        cfg.timeoutIntervalForResource = 8
        self.session = URLSession(configuration: cfg)
    }

    /// Start the background polling loop. Safe to call repeatedly; the
    /// previous timer is cancelled on re-entry.
    func start() {
        stop()
        requestNotificationPermissionIfNeeded()
        let timer = DispatchSource.makeTimerSource(queue: queue)
        timer.schedule(deadline: .now() + 1, repeating: Self.pollInterval)
        timer.setEventHandler { [weak self] in
            self?.tick()
        }
        timer.resume()
        pollTimer = timer
        os_log("idle notifier started", log: logger, type: .info)
    }

    /// Stop polling. Safe to call when already stopped.
    func stop() {
        pollTimer?.cancel()
        pollTimer = nil
    }

    private func tick() {
        guard let addr = readControlAddress() else {
            os_log("control endpoint not yet available", log: logger, type: .debug)
            return
        }
        guard let state = fetchState(addr: addr) else {
            return
        }
        let idle = idleSeconds()
        guard idle >= Self.idleThreshold else {
            // User is back; nothing to do.
            return
        }
        guard let last = state.lastCompleted else {
            return
        }
        let age = Date().timeIntervalSince(last.completedAt)
        guard age >= 0, age <= Self.completionFreshness else {
            return
        }
        guard state.pendingNotifChat != 0 else {
            return
        }
        if let last = lastNotifySentAt,
           Date().timeIntervalSince(last) < Self.cooldownAfterNotify {
            return
        }
        os_log("triggering notification: idle=%{public}.0fs project=%{public}@",
               log: logger, type: .info,
               idle, last.projectName ?? "<unknown>")
        triggerNotification(addr: addr, chatId: state.pendingNotifChat, completion: last)
    }

    // MARK: - Networking

    private func readControlAddress() -> String? {
        guard let data = try? Data(contentsOf: controlFileURL),
              let dict = try? JSONDecoder().decode([String: String].self, from: data) else {
            return nil
        }
        return dict["addr"]
    }

    private func fetchState(addr: String) -> State? {
        let url = URL(string: "http://\(addr)/state")!
        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        let semaphore = DispatchSemaphore(value: 0)
        var result: State?
        let task = session.dataTask(with: request) { data, _, _ in
            defer { semaphore.signal() }
            guard let data = data else { return }
            let decoder = JSONDecoder()
            decoder.dateDecodingStrategy = .iso8601
            result = try? decoder.decode(State.self, from: data)
        }
        task.resume()
        _ = semaphore.wait(timeout: .now() + 5)
        return result
    }

    private func triggerNotification(addr: String, chatId: Int64, completion: CompletedSession) {
        let url = URL(string: "http://\(addr)/notify")!
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONSerialization.data(withJSONObject: ["chat_id": chatId])
        session.dataTask(with: request) { [weak self] data, response, error in
            if let error = error {
                os_log("notify failed: %{public}@", log: self?.logger ?? .default, type: .error,
                       error.localizedDescription)
                return
            }
            guard let http = response as? HTTPURLResponse,
                  (200...299).contains(http.statusCode) else {
                os_log("notify non-2xx: %{public}d", log: self?.logger ?? .default, type: .error,
                       (response as? HTTPURLResponse)?.statusCode ?? -1)
                return
            }
            if data != nil {
                self?.lastNotifySentAt = Date()
                self?.showLocalNotification(completion: completion)
            }
        }.resume()
    }

    // MARK: - macOS user notification

    private func requestNotificationPermissionIfNeeded() {
        let center = UNUserNotificationCenter.current()
        center.getNotificationSettings { settings in
            if settings.authorizationStatus == .notDetermined {
                center.requestAuthorization(options: [.alert, .sound]) { _, _ in }
            }
        }
    }

    private func showLocalNotification(completion: CompletedSession) {
        let content = UNMutableNotificationContent()
        content.title = "Tarea de Agentower terminada"
        let project = completion.projectName ?? completion.directory ?? "tu proyecto"
        content.body = "\(project): Agentower terminó mientras no estabas. Revisa Telegram o vuelve a la laptop."
        content.sound = .default
        let request = UNNotificationRequest(
            identifier: "agentower.completion.\(completion.sessionId)",
            content: content,
            trigger: nil
        )
        UNUserNotificationCenter.current().add(request) { error in
            if let error = error {
                os_log("local notification failed: %{public}@", log: self.logger, type: .error,
                       error.localizedDescription)
            }
        }
    }

    // MARK: - Idle

    /// Returns the seconds since the user last interacted with the machine
    /// (mouse move, keyboard press, etc). CGEventSourceSecondsSinceLastEventType
    /// requires no special permission; it is the same primitive that screen
    /// lockers use.
    private func idleSeconds() -> TimeInterval {
        let raw = CGEventSource.secondsSinceLastEventType(
            .hidSystemState,
            eventType: CGEventType(rawValue: UInt32.max)!
        )
        return raw < 0 ? 0 : raw
    }
}
