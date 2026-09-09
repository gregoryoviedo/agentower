import AppKit
import SwiftUI

@main
struct AgentowerApp {
    static func main() {
        let delegate = AppDelegate()
        NSApplication.shared.delegate = delegate
        NSApp.setActivationPolicy(.accessory)
        NSApplication.shared.run()
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var appState: AppState!
    private var botController: BotController!
    private var statusBarController: StatusBarController!
    private var idleNotifier: IdleNotifier!

    func applicationDidFinishLaunching(_ notification: Notification) {
        appState = AppState()
        botController = BotController { [weak self] status in
            guard let self = self else { return }
            DispatchQueue.main.async {
                self.handleStatusUpdate(status)
            }
        }
        statusBarController = StatusBarController(appState: appState, botController: botController)
        idleNotifier = IdleNotifier()
        idleNotifier.start()
    }

    func applicationWillTerminate(_ notification: Notification) {
        idleNotifier?.stop()
        botController?.shutdown()
    }

    private func handleStatusUpdate(_ status: BotStatus) {
        appState.status = status
        appState.lastError = botController.lastError
        NotificationCenter.default.post(name: .botStateChanged, object: status)
        switch status {
        case .running:
            appState.startedAt = Date()
            appState.lastError = nil
        case .stopped, .crashed:
            appState.startedAt = nil
        case .stopping, .starting:
            break
        }
    }
}
