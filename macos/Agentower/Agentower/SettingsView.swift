import SwiftUI
import AppKit
import UniformTypeIdentifiers

struct SettingsView: View {
    @ObservedObject var appState: AppState
    var onClose: () -> Void

    @State private var workspaceRoot: String = ""
    @State private var telegramBotToken: String = ""
    @State private var allowedChatID: String = ""

    @State private var openCodePort: Int = 4096
    @State private var openCodeBin: String = "opencode"
    @State private var openCodeAutostart: Bool = false

    @State private var agentowerStatePath: String = ""
    @State private var telegramAPIRoot: String = ""
    @State private var telegramProxyURL: String = ""

    @State private var agentRows: [AgentRow] = []

    @State private var loginItemEnabled: Bool = true
    @State private var savedAt: Date?
    @State private var errorMessage: String?
    @State private var selectedTab: SettingsTab = .telegram

    @FocusState private var focusedField: Field?
    private enum Field: Hashable {
        case workspaceRoot, token, chatID, port, bin, statePath, apiRoot, proxyURL
        case agentBin(Int), agentArgs(Int)
    }

    private enum SettingsTab: String, CaseIterable, Identifiable {
        case telegram, agents, advanced, login
        var id: Self { self }
        var title: String {
            switch self {
            case .telegram: return "Telegram"
            case .agents:   return "Agentes"
            case .advanced: return "Avanzado"
            case .login:    return "Inicio"
            }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Picker("", selection: $selectedTab) {
                ForEach(SettingsTab.allCases) { tab in
                    Text(tab.title).tag(tab)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(.horizontal, 16)
            .padding(.top, 12)
            .padding(.bottom, 8)

            Form {
                switch selectedTab {
                case .telegram: telegramSection
                case .agents:   agentsFormContent
                case .advanced: advancedSection
                case .login:    loginSection
                }
            }
            .formStyle(.grouped)
            .frame(minWidth: 560, minHeight: 460)

            Divider()
            HStack {
                if let savedAt = savedAt {
                    Text("Guardado \(savedAt.formatted(date: .omitted, time: .standard))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                if let errorMessage = errorMessage {
                    Text(errorMessage)
                        .font(.caption)
                        .foregroundStyle(.red)
                }
                Spacer()
                if appState.status.isRunning {
                    Text("Los cambios aplican al próximo inicio.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Button("Cancelar") { onClose() }
                    .keyboardShortcut(.cancelAction)
                Button("Guardar") { save() }
                    .keyboardShortcut(.defaultAction)
                    .buttonStyle(.borderedProminent)
            }
            .padding(12)
        }
        .onAppear { load() }
    }

    // MARK: - Sections (per tab)

    @ViewBuilder
    private var telegramSection: some View {
        Section("Bot de Telegram") {
            LabeledContent("WORKSPACE_ROOT") {
                HStack {
                    TextField("/Users/you/dev", text: $workspaceRoot)
                        .textFieldStyle(.roundedBorder)
                        .focused($focusedField, equals: .workspaceRoot)
                        .onPasteCommand(of: [UTType.text]) { handlePaste(into: .workspaceRoot, providers: $0) }
                    Button("Elegir…") { pickWorkspace() }
                }
            }
            LabeledContent("TELEGRAM_BOT_TOKEN") {
                SecureField("token de @BotFather", text: $telegramBotToken)
                    .textFieldStyle(.roundedBorder)
                    .focused($focusedField, equals: .token)
                    .onPasteCommand(of: [UTType.text]) { handlePaste(into: .token, providers: $0) }
            }
            LabeledContent("ALLOWED_CHAT_ID") {
                TextField("id numérico", text: $allowedChatID)
                    .textFieldStyle(.roundedBorder)
                    .focused($focusedField, equals: .chatID)
                    .onPasteCommand(of: [UTType.text]) { handlePaste(into: .chatID, providers: $0) }
            }
        }
    }

    @ViewBuilder
    private var loginSection: some View {
        Section("Inicio de sesión") {
            Toggle("Iniciar Agentower al arrancar macOS", isOn: $loginItemEnabled)
                .disabled(!LoginItemManager.isInstalledInApplications)
            if !LoginItemManager.isInstalledInApplications {
                Text("Para usar auto-inicio, copia la app a /Applications.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    // MARK: - Sections

    @ViewBuilder
    private var agentsFormContent: some View {
        agentsSection
        opencodeBackCompatSection
    }

    private var agentsSection: some View {
        Section("Agentes de IA") {
            ForEach($agentRows) { $row in
                agentRow(row: $row)
            }
            HStack {
                Text("Detecta los binarios en PATH (y los bundles de VS Code para Copilot) y permite habilitar cada agente desde Telegram con /agents.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Reintentar detección") { runDetection() }
            }
        }
    }

    @ViewBuilder
    private func agentRow(row: Binding<AgentRow>) -> some View {
        let kind = row.wrappedValue.kind
        let label = ConfigStore.displayName(for: kind)
        let disabled = !row.wrappedValue.available
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Toggle("Habilitado", isOn: row.enabled)
                    .disabled(disabled)
                Spacer()
                if disabled {
                    Text(disableReason(for: row.wrappedValue))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            LabeledContent("\(label.uppercased())_BIN") {
                TextField(kind, text: row.bin)
                    .textFieldStyle(.roundedBorder)
                    .disabled(disabled)
                    .focused($focusedField, equals: .agentBin(row.wrappedValue.id))
                    .onPasteCommand(of: [UTType.text]) { handleAgentPaste(row: row.wrappedValue.id, providers: $0) }
            }
            HStack {
                Stepper(value: row.port, in: 1...65535) {
                    HStack {
                        Text("\(label.uppercased())_PORT")
                        Spacer()
                        Text("\(row.wrappedValue.port)")
                            .foregroundStyle(.secondary)
                            .monospacedDigit()
                    }
                }
                .disabled(disabled)
            }
            LabeledContent("\(label.uppercased())_ARGS") {
                TextField("argumentos extra", text: row.args)
                    .textFieldStyle(.roundedBorder)
                    .disabled(disabled)
                    .focused($focusedField, equals: .agentArgs(row.wrappedValue.id))
                    .onPasteCommand(of: [UTType.text]) { handleAgentPaste(row: row.wrappedValue.id, providers: $0) }
            }
        }
        .padding(.vertical, 4)
        .opacity(disabled ? 0.6 : 1.0)
    }

    private var opencodeBackCompatSection: some View {
        Section("Servidor OpenCode (legacy)") {
            Stepper(value: $openCodePort, in: 1...65535) {
                HStack {
                    Text("OPENCODE_PORT")
                    Spacer()
                    Text("\(openCodePort)")
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                }
            }
            LabeledContent("OPENCODE_BIN") {
                TextField("opencode", text: $openCodeBin)
                    .textFieldStyle(.roundedBorder)
                    .focused($focusedField, equals: .bin)
                    .onPasteCommand(of: [UTType.text]) { handlePaste(into: .bin, providers: $0) }
            }
            Toggle("OPENCODE_AUTOSTART", isOn: $openCodeAutostart)
            Text("Estos campos se mantienen por compatibilidad con .env antiguos; los nuevos valores viven en la sección Agentes de IA.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private var advancedSection: some View {
        Section("Avanzado (opcional)") {
            LabeledContent("AGENTOWER_STATE_PATH") {
                TextField("(por defecto junto a WORKSPACE_ROOT)", text: $agentowerStatePath)
                    .textFieldStyle(.roundedBorder)
                    .focused($focusedField, equals: .statePath)
                    .onPasteCommand(of: [UTType.text]) { handlePaste(into: .statePath, providers: $0) }
            }
            LabeledContent("TELEGRAM_API_ROOT") {
                TextField("(por defecto de telebot)", text: $telegramAPIRoot)
                    .textFieldStyle(.roundedBorder)
                    .focused($focusedField, equals: .apiRoot)
                    .onPasteCommand(of: [UTType.text]) { handlePaste(into: .apiRoot, providers: $0) }
            }
            LabeledContent("TELEGRAM_PROXY_URL") {
                TextField("http://host:port", text: $telegramProxyURL)
                    .textFieldStyle(.roundedBorder)
                    .focused($focusedField, equals: .proxyURL)
                    .onPasteCommand(of: [UTType.text]) { handlePaste(into: .proxyURL, providers: $0) }
            }
        }
    }

    // MARK: - Helpers

    private func disableReason(for row: AgentRow) -> String {
        if let path = row.detectedPath {
            return "Detectado en \(path)"
        }
        let hint: String
        switch row.kind {
        case "copilot": hint = "instala la extensión GitHub Copilot en VS Code o añade `copilot` a PATH"
        case "opencode": hint = "instala opencode CLI y asegúrate de que esté en PATH"
        default:        hint = "instala \(ConfigStore.displayName(for: row.kind)) y asegúrate de que esté en PATH"
        }
        return "No se encontró el binario — \(hint)"
    }

    private func runDetection() {
        let detected = ConfigStore.detectAgentsInPath()
        for index in agentRows.indices {
            let kind = agentRows[index].kind
            if let path = detected[kind] {
                agentRows[index].detectedPath = path
                if agentRows[index].bin == kind {
                    agentRows[index].bin = path
                }
            }
        }
    }

    private func handlePaste(into field: Field, providers: [NSItemProvider]) {
        if let provider = providers.first {
            provider.loadObject(ofClass: NSString.self) { obj, _ in
                let resolved = (obj as? NSString) as String?
                    ?? NSPasteboard.general.string(forType: .string)
                    ?? ""
                guard !resolved.isEmpty else { return }
                DispatchQueue.main.async { [field] in
                    self.applyPaste(resolved, to: field)
                }
            }
            return
        }
        if let text = NSPasteboard.general.string(forType: .string), !text.isEmpty {
            applyPaste(text, to: field)
        }
    }

    private func pickWorkspace() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.prompt = "Elegir"
        panel.message = "Elige la carpeta raíz del workspace"
        if panel.runModal() == .OK, let url = panel.url {
            workspaceRoot = url.path
        }
    }

    private func handleAgentPaste(row id: Int, providers: [NSItemProvider]) {
        guard let index = agentRows.firstIndex(where: { $0.id == id }) else { return }
        let closure: (String) -> Void = { text in
            let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmed.isEmpty {
                agentRows[index].bin = trimmed
            }
        }
        if let provider = providers.first {
            provider.loadObject(ofClass: NSString.self) { obj, _ in
                let text = (obj as? NSString) as String?
                    ?? NSPasteboard.general.string(forType: .string)
                    ?? ""
                guard !text.isEmpty else { return }
                DispatchQueue.main.async { closure(text) }
            }
            return
        }
        if let text = NSPasteboard.general.string(forType: .string), !text.isEmpty {
            closure(text)
        }
    }

    private func applyPaste(_ text: String, to field: Field) {
        switch field {
        case .workspaceRoot:
            workspaceRoot = text.trimmingCharacters(in: .whitespacesAndNewlines)
        case .token:
            telegramBotToken = text.trimmingCharacters(in: .whitespacesAndNewlines)
        case .chatID:
            let digits = text.filter { $0.isNumber || $0 == "-" }
            allowedChatID = String(digits.prefix(20))
        case .port:
            let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
            if let p = Int(trimmed), p >= 1, p <= 65535 {
                openCodePort = p
            } else if !trimmed.isEmpty {
                errorMessage = "OPENCODE_PORT debe ser un entero entre 1 y 65535."
            }
        case .bin:
            openCodeBin = text.trimmingCharacters(in: .whitespacesAndNewlines)
        case .statePath:
            agentowerStatePath = text.trimmingCharacters(in: .whitespacesAndNewlines)
        case .apiRoot:
            telegramAPIRoot = text.trimmingCharacters(in: .whitespacesAndNewlines)
        case .proxyURL:
            telegramProxyURL = text.trimmingCharacters(in: .whitespacesAndNewlines)
        case .agentBin, .agentArgs:
            break
        }
    }

    private func load() {
        let cfg = appState.configuration
        workspaceRoot = cfg.workspaceRoot
        telegramBotToken = cfg.telegramBotToken
        allowedChatID = cfg.allowedChatID
        openCodePort = cfg.openCodePort
        openCodeBin = cfg.openCodeBin
        openCodeAutostart = cfg.openCodeAutostart
        agentowerStatePath = cfg.agentowerStatePath
        telegramAPIRoot = cfg.telegramAPIRoot
        telegramProxyURL = cfg.telegramProxyURL
        loginItemEnabled = true
        rebuildAgentRows(from: cfg)
        runDetection()
    }

    private func rebuildAgentRows(from cfg: BotConfiguration) {
        let detected = ConfigStore.detectAgentsInPath()
        agentRows = ConfigStore.agentKinds.enumerated().map { index, kind in
            let stored = cfg.agents[kind] ?? AgentSettings()
            let detectedPath = detected[kind]
            let available = kind == "opencode" || detectedPath != nil
            return AgentRow(
                id: index,
                kind: kind,
                enabled: stored.enabled,
                bin: stored.bin.isEmpty ? (detectedPath ?? kind) : stored.bin,
                port: stored.port == 0 ? ConfigStore.defaultPort(for: kind) : stored.port,
                args: stored.args,
                available: available,
                detectedPath: detectedPath
            )
        }
    }

    private func save() {
        var cfg = BotConfiguration(
            workspaceRoot: workspaceRoot.trimmingCharacters(in: .whitespacesAndNewlines),
            telegramBotToken: telegramBotToken.trimmingCharacters(in: .whitespacesAndNewlines),
            allowedChatID: allowedChatID.trimmingCharacters(in: .whitespacesAndNewlines),
            openCodePort: openCodePort,
            openCodeBin: openCodeBin.trimmingCharacters(in: .whitespacesAndNewlines),
            openCodeAutostart: openCodeAutostart,
            agentowerStatePath: agentowerStatePath.trimmingCharacters(in: .whitespacesAndNewlines),
            telegramAPIRoot: telegramAPIRoot.trimmingCharacters(in: .whitespacesAndNewlines),
            telegramProxyURL: telegramProxyURL.trimmingCharacters(in: .whitespacesAndNewlines)
        )
        if cfg.openCodeBin.isEmpty {
            cfg.openCodeBin = "opencode"
        }
        var agentsMap: [String: AgentSettings] = [:]
        for row in agentRows {
            agentsMap[row.kind] = AgentSettings(
                enabled: row.enabled,
                bin: row.bin.trimmingCharacters(in: .whitespacesAndNewlines),
                port: row.port,
                args: row.args.trimmingCharacters(in: .whitespacesAndNewlines),
                available: row.available
            )
        }
        cfg.agents = agentsMap
        if let msg = cfg.validationMessage {
            errorMessage = msg
            savedAt = nil
            return
        }
        do {
            try appState.configStore.save(cfg)
            appState.configuration = cfg
            savedAt = Date()
            errorMessage = nil
            if loginItemEnabled != LoginItemManager.isEnabled {
                if loginItemEnabled {
                    LoginItemManager.register()
                } else {
                    LoginItemManager.unregister()
                }
            }
            onClose()
        } catch {
            errorMessage = error.localizedDescription
            savedAt = nil
        }
    }
}

/// AgentRow is the SwiftUI-side mirror of AgentSettings, indexed so
/// ForEach can identify each row without relying on the String key.
struct AgentRow: Identifiable, Equatable {
    let id: Int
    var kind: String
    var enabled: Bool
    var bin: String
    var port: Int
    var args: String
    var available: Bool
    var detectedPath: String?
}
