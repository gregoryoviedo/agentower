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

    @State private var loginItemEnabled: Bool = true
    @State private var savedAt: Date?
    @State private var errorMessage: String?

    @FocusState private var focusedField: Field?
    private enum Field: Hashable { case workspaceRoot, token, chatID, port, bin, statePath, apiRoot, proxyURL }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Form {
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

                Section("Servidor OpenCode") {
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
                    }
                    Toggle("OPENCODE_AUTOSTART", isOn: $openCodeAutostart)
                }

                Section("Avanzado (opcional)") {
                    LabeledContent("AGENTOWER_STATE_PATH") {
                        TextField("(por defecto junto a WORKSPACE_ROOT)", text: $agentowerStatePath)
                            .textFieldStyle(.roundedBorder)
                            .focused($focusedField, equals: .statePath)
                    }
                    LabeledContent("TELEGRAM_API_ROOT") {
                        TextField("(por defecto de telebot)", text: $telegramAPIRoot)
                            .textFieldStyle(.roundedBorder)
                            .focused($focusedField, equals: .apiRoot)
                    }
                    LabeledContent("TELEGRAM_PROXY_URL") {
                        TextField("http://host:port", text: $telegramProxyURL)
                            .textFieldStyle(.roundedBorder)
                            .focused($focusedField, equals: .proxyURL)
                    }
                }

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
            .formStyle(.grouped)
            .frame(minWidth: 520, minHeight: 480)

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
    }

    private func pickWorkspace() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.prompt = "Elegir"
        panel.message = "Elige el WORKSPACE_ROOT"
        if panel.runModal() == .OK, let url = panel.url {
            workspaceRoot = url.path
        }
    }

    private func handlePaste(into field: Field, providers: [NSItemProvider]) {
        guard let provider = providers.first else { return }
        provider.loadObject(ofClass: NSString.self) { obj, _ in
            guard let str = obj as? NSString else { return }
            DispatchQueue.main.async {
                applyPaste(str as String, to: field)
            }
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