import Foundation
import Combine

enum BotStatus: Equatable {
    case stopped
    case starting
    case running(pid: Int32)
    case stopping
    case crashed(exitCode: Int32)

    var isRunning: Bool {
        switch self {
        case .running, .starting:
            return true
        default:
            return false
        }
    }

    var label: String {
        switch self {
        case .stopped:
            return "Detenido"
        case .starting:
            return "Iniciando…"
        case .running(let pid):
            return "En ejecución (PID \(pid))"
        case .stopping:
            return "Deteniendo…"
        case .crashed(let code):
            return "Salida con código \(code)"
        }
    }
}

final class AppState: ObservableObject {
    @Published var status: BotStatus = .stopped
    @Published var configuration: BotConfiguration
    @Published var startedAt: Date?
    @Published var lastError: String?

    let configStore: ConfigStore

    init(configStore: ConfigStore = ConfigStore()) {
        self.configStore = configStore
        self.configuration = configStore.load()
    }

    func reloadConfiguration() {
        configuration = configStore.load()
    }
}