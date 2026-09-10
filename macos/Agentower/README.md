# Agentower — macOS menu bar app

Swift wrapper alrededor del binario Go `remote-bot`. Vive en la barra de
menús (junto a Wi-Fi/Bluetooth/AirDrop), reproduce el patrón click → popover
con toggle estilo Bluetooth, y arranca/detiene el bot según el estado del
switch. La sección **Agentes de IA** del Settings detecta los CLIs
(opencode, Claude, Kiro, GitHub Copilot) instalados en PATH o en
las extensiones de VS Code para que el usuario habilite los que quiera
usar antes de arrancar el bot.

## Arquitectura

```
Agentower.app/
├── Contents/
│   ├── MacOS/Agentower    ← Swift app (status bar + NSPopover)
│   └── Resources/
│       ├── Assets.car          ← iconos (AppIcon + MenuBarIcon)
│       └── remote-bot          ← binario Go embebido (arm64)
└── Info.plist                  ← LSUIElement = YES
```

El código Swift no toca el backend Go: lo lanza como `Process` y le pasa
`ENV_FILE` apuntando a un `.env` que la app regenera con los valores del
formulario de Settings. El binario Go es exactamente el mismo del repo,
compilado con `go build`.

## Build

```bash
make app
```

Esto:

1. Compila el binario Go (`arm64`) y lo coloca en `Agentower/Resources/`.
2. Genera el `MenuBarIcon` monocromo (template) y el `AppIcon.icns` desde
   `macos/Agentower/assets/app-icon.jpeg` con `sips` + Python/Pillow.
3. Instala `xcodegen` si no está.
4. Regenera el `.xcodeproj`.
5. Compila con `xcodebuild` (`arm64`, sin firma de Apple Developer).
6. Empaqueta en `dist/Agentower.app`.

Salida:

```
dist/Agentower.app
```

## Instalación y primer uso

```bash
# Mover a Aplicaciones
cp -R dist/Agentower.app /Applications/

# Quitar la marca de Gatekeeper (porque no tiene Developer ID)
xattr -dr com.apple.quarantine /Applications/Agentower.app

# Abrir
open /Applications/Agentower.app
```

Aparece un icono en la barra de menús. **Click izquierdo** abre el popover
estilo Bluetooth con un toggle. Click derecho abre menú contextual.

**Primera vez**: el toggle está deshabilitado. Click en **Settings…**, llena
`WORKSPACE_ROOT`, `TELEGRAM_BOT_TOKEN`, `ALLOWED_CHAT_ID`. La sección
**Agentes de IA** muestra qué CLIs detectó en PATH (botón "Reintentar
detección" para refrescar) y te permite habilitar opencode, Claude,
Kiro o Copilot individualmente. Al guardar, la app escribe
`~/Library/Application Support/Agentower/.env` con permisos `0600` y a
partir de ahí el toggle funciona.

**Auto-inicio al login**: en Settings marcá la casilla. La app llama
`SMAppService.mainApp.register()` y aparece en *Ajustes del sistema →
General → Ítems de inicio*. Solo funciona si la app vive en `/Applications/`.

## Ubicaciones

| Recurso | Ruta |
|---|---|
| Settings | `~/Library/Application Support/Agentower/.env` (`chmod 600`) |
| State DB (SQLite) | `~/Library/Application Support/Agentower/state.db` |
| Logs | `~/Library/Logs/Agentower/bot.log` |

## Cómo funciona el toggle

- **Apagado → click**: arranca `Resources/remote-bot` con env vars
  (`ENV_FILE=…/.env`, `AGENTOWER_STATE_PATH=…/state.db`). El binario lee el
  `.env` y entra en long-polling de Telegram.
- **Encendido → click**: `terminate()` (SIGTERM), espera 5 s, `SIGKILL` si
  no salió.
- **Cambios en Settings**: no rearrancan el bot en caliente; aparece un
  banner "Los cambios aplican al próximo inicio". Restart manual desde el
  toggle.

## Estructura del módulo

```
macos/Agentower/
├── project.yml                         # XcodeGen spec
├── Agentower/
│   ├── AgentowerApp.swift         # @main, NSApplicationDelegate
│   ├── StatusBarController.swift       # NSStatusItem + NSPopover
│   ├── PopoverContentView.swift        # SwiftUI: toggle + estado
│   ├── SettingsView.swift              # SwiftUI form
│   ├── SettingsWindowController.swift  # NSWindow propia
│   ├── BotController.swift             # Process start/stop
│   ├── ConfigStore.swift               # UserDefaults + writer .env
│   ├── AppState.swift                  # ObservableObject
│   ├── AppPaths.swift                  # paths de support/logs
│   ├── LoginItemManager.swift          # SMAppService wrapper
│   ├── Agentower.entitlements
│   └── Assets.xcassets/
│       ├── AppIcon.appiconset/
│       └── MenuBarIcon.imageset/       # template PNG @1x/@2x/@3x
└── scripts/
    ├── build.sh                        # orquesta todo
    └── make-icon.sh                    # genera iconos desde el JPEG
```

## Personalizar el icono

El script `make-icon.sh` convierte el JPEG del repo a silueta monocroma con
umbralización. Si querés reemplazarlo con una versión más cuidada:

1. Creá una PNG monocromo (negro sólido sobre transparente) en 1024×1024.
2. Marcá *Template Image* en *Attributes Inspector* dentro de Xcode (o en el
   `Contents.json` del imageset con `"template-rendering-intent": "template"`).
3. Exportá `MenuBarIcon@1x.png` (18pt), `@2x.png` (36pt), `@3x.png` (54pt).
4. Colocá los tres en `Assets.xcassets/MenuBarIcon.imageset/`.

## Troubleshooting

**Gatekeeper "developer cannot be verified"** (primera vez):
Click-derecho sobre el `.app` → Abrir → Abrir.

**Toggle deshabilitado**: falta configurar Settings.

**Bot no arranca**: abrí *Open Log* desde el menú; los logs del Go están en
`~/Library/Logs/Agentower/bot.log`.

**Auto-inicio no aparece**: la app debe estar en `/Applications/`.

**Cambios en `WORKSPACE_ROOT`/token no aplican**: detener el toggle y volver
a encenderlo.