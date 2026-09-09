import AppKit
import SwiftUI

final class StatusBarController: NSObject {
    private let statusItem: NSStatusItem
    private let popover: NSPopover
    private let appState: AppState
    private let botController: BotController
    private var stateSink: Any?

    init(appState: AppState, botController: BotController) {
        self.appState = appState
        self.botController = botController
        self.statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        self.popover = NSPopover()

        super.init()

        configureStatusItem()
        configurePopover()
        observeState()
        updateIcon(for: appState.status, running: appState.status.isRunning)
    }

    deinit {
        if let token = stateSink {
            NotificationCenter.default.removeObserver(token)
        }
    }

    private func configureStatusItem() {
        guard let button = statusItem.button else { return }
        button.image = NSImage(named: "MenuBarIcon")
        button.image?.isTemplate = true
        button.imageScaling = .scaleProportionallyDown
        button.target = self
        button.action = #selector(handleButtonClick(_:))
        button.sendAction(on: [.leftMouseUp, .rightMouseUp])
    }

    private func configurePopover() {
        popover.behavior = .transient
        popover.animates = true
    }

    private func observeState() {
        stateSink = NotificationCenter.default.addObserver(
            forName: .botStateChanged, object: nil, queue: .main
        ) { [weak self] note in
            guard let self = self,
                  let status = note.object as? BotStatus else { return }
            self.updateIcon(for: status, running: status.isRunning)
        }
    }

    private func updateIcon(for status: BotStatus, running: Bool) {
        guard let button = statusItem.button else { return }
        button.image = NSImage(named: "MenuBarIcon")
        button.image?.isTemplate = true
        button.alphaValue = appState.configuration.isValid ? 1.0 : 0.4
    }

    @objc private func handleButtonClick(_ sender: NSStatusBarButton) {
        let event = NSApp.currentEvent
        if event?.type == .rightMouseUp {
            showContextMenu()
        } else {
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                if self.popover.isShown,
                   self.popover.contentViewController?.view.window?.isVisible == true {
                    self.popover.performClose(nil)
                } else {
                    self.showPopover()
                }
            }
        }
    }

    private func togglePopover() {
        if popover.isShown {
            popover.performClose(nil)
        } else {
            showPopover()
        }
    }

    private func showPopover() {
        guard let button = statusItem.button else { return }
        let content = PopoverContentView(
            appState: appState,
            onToggle: { [weak self] in self?.handleToggle() },
            onSettings: { [weak self] in
                self?.popover.performClose(nil)
                self?.showSettings()
            },
            onOpenLog: { [weak self] in
                self?.popover.performClose(nil)
                self?.openLog()
            },
            onQuit: { [weak self] in
                self?.popover.performClose(nil)
                self?.quitApp()
            }
        )
        popover.contentViewController = NSHostingController(rootView: content)
        popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)

        guard let popoverWindow = popover.contentViewController?.view.window else { return }
        let buttonRect = button.window?.convertToScreen(button.convert(button.bounds, to: nil)) ?? .zero
        let x = buttonRect.midX - popoverWindow.frame.width / 2
        let y = buttonRect.minY - popoverWindow.frame.height
        popoverWindow.setFrameOrigin(NSPoint(x: x, y: y))
    }

    private func showContextMenu() {
        let menu = NSMenu()
        menu.addItem(withTitle: appState.status.label, action: nil, keyEquivalent: "")
            .isEnabled = false
        menu.addItem(NSMenuItem.separator())

        let toggle = NSMenuItem(
            title: appState.status.isRunning ? "Detener" : "Iniciar",
            action: #selector(menuToggle(_:)),
            keyEquivalent: ""
        )
        toggle.target = self
        toggle.isEnabled = appState.configuration.isValid
        menu.addItem(toggle)

        menu.addItem(withTitle: "Configuración…", action: #selector(menuSettings(_:)), keyEquivalent: ",")
            .target = self
        menu.addItem(withTitle: "Abrir registro", action: #selector(menuOpenLog(_:)), keyEquivalent: "")
            .target = self
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: "Salir de Agentower", action: #selector(menuQuit(_:)), keyEquivalent: "q")
            .target = self

        statusItem.menu = menu
        statusItem.button?.performClick(nil)
        statusItem.menu = nil
    }

    @objc private func menuToggle(_ sender: Any?) { handleToggle() }
    @objc private func menuSettings(_ sender: Any?) { showSettings() }
    @objc private func menuOpenLog(_ sender: Any?) { openLog() }
    @objc private func menuQuit(_ sender: Any?) { quitApp() }

    private func handleToggle() {
        if botController.isRunning {
            botController.stop()
        } else {
            let cfg = appState.configuration
            botController.start(
                telegramAPIRoot: cfg.telegramAPIRoot,
                telegramProxyURL: cfg.telegramProxyURL
            )
        }
    }

    private func showSettings() {
        SettingsWindowController.shared.show(appState: appState)
    }

    private func openLog() {
        NSWorkspace.shared.open(AppPaths.botLogFile)
    }

    private func quitApp() {
        botController.shutdown()
        NSApp.terminate(nil)
    }
}

extension Notification.Name {
    static let botStateChanged = Notification.Name("BotStateChanged")
}
