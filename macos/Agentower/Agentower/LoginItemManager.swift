import Foundation
import ServiceManagement

enum LoginItemManager {
    static let service = SMAppService.mainApp

    static var isEnabled: Bool {
        service.status == .enabled
    }

    static func register() {
        do {
            try service.register()
        } catch {
            NSLog("LoginItemManager.register failed: %@", error.localizedDescription)
        }
    }

    static func unregister() {
        do {
            try service.unregister()
        } catch {
            NSLog("LoginItemManager.unregister failed: %@", error.localizedDescription)
        }
    }

    static var isInstalledInApplications: Bool {
        let bundlePath = Bundle.main.bundlePath
        let home = NSHomeDirectory()
        if bundlePath.hasPrefix("/Applications/") {
            return true
        }
        if bundlePath.hasPrefix("\(home)/Applications/") {
            return true
        }
        return false
    }
}
