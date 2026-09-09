import AppKit
import SwiftUI

final class SettingsWindowController: NSWindowController {
    static let shared = SettingsWindowController()

    private init() {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 560, height: 540),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Agentower — Configuración"
        window.isReleasedWhenClosed = false
        window.center()
        super.init(window: window)
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) no implementado")
    }

    func show(appState: AppState) {
        guard let window = window else { return }
        let content = SettingsView(appState: appState) { [weak window] in
            window?.close()
        }
        let host = NSHostingController(rootView: content)
        host.view.frame = window.contentView?.bounds ?? .zero
        window.contentViewController = host
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }
}