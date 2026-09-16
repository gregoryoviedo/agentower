# Agentower para Windows

Wrapper nativo de Windows (C# / .NET 8 WinForms) que vive en el **área de
notificación** de la barra de tareas —junto a Steam, Epic, Discord,
etc.— y supervisa el bot Go `remote-bot.exe`, igual que `Agentower.app`
lo hace en macOS.

## Características

- **Icono en el área de notificación** con estado del bot.
- **Click izquierdo** → popover con toggle, estado, tiempo activo y las
  acciones rápidas (Configuración, Abrir registro, Salir).
- **Click derecho** → menú contextual con lo mismo.
- **Ventana de Configuración** con las mismas secciones que macOS:
  - **Telegram** — `WORKSPACE_ROOT` (con selector de carpeta),
    `TELEGRAM_BOT_TOKEN`, `ALLOWED_CHAT_ID`.
  - **Agentes** — una fila por agente (`ENABLED` / `BIN` / `PORT` /
    `ARGS`) con detección automática y botón *Reintentar detección*.
  - **Avanzado** — `AGENTOWER_STATE_PATH`, `TELEGRAM_API_ROOT`,
    `TELEGRAM_PROXY_URL`.
  - **Inicio** — iniciar Agentower al arrancar Windows.
- **Notificación al terminar una tarea** — si el usuario no ha tocado el
  teclado/ratón en 2 minutos y el agente terminó, el bot envía el
  mensaje a Telegram y aparece una notificación de Windows.
- **Preguntas respondibles desde Telegram** — si el agente se detiene a
  pedir una decisión con opciones, el wrapper reenvía la pregunta al
  minuto de inactividad; se responde con botones o texto desde
  Telegram.
- **Arranque automático** — al iniciar Windows, `Agentower.exe` se lanza
  solo (clave `Run` del usuario) y **arranca el bot inmediatamente** si la
  configuración es válida, de modo que queda en segundo plano apenas
  empiezas a trabajar en tu IDE/CLI. Se activa por defecto en la primera
  configuración y se controla desde la pestaña **Inicio**.
- **Un único `.exe`** self-contained (no requiere instalar .NET).

## Requisitos de build

- **Go 1.22+** (`winget install GoLang.Go`).
- **.NET 8 SDK** (`winget install Microsoft.DotNet.SDK.8`).
- Windows 10/11 x64.

## Build

Desde la raíz del repo:

```powershell
.\windows\build.ps1
```

El script:

1. Compila `cmd\remote-bot` para `windows/amd64` en
   `windows\Agentower\Resources\remote-bot.exe`.
2. Publica el wrapper como único fichero en `dist\Agentower.exe`
   (self-contained, ~150 MB).

Salida: `dist\Agentower.exe`.

> Para un binario mucho más pequeño (≈1 MB) que dependa del
> **.NET 8 Desktop Runtime** instalado, publica en modo
> framework-dependent:
> `dotnet publish windows\Agentower\Agentower.csproj -c Release -r win-x64 --self-contained false -o dist`.

## Instalación

`Agentower.exe` es portable: al ejecutarlo **te ofrece instalarse** para
no depender de la carpeta donde lo descargaste.

1. Doble clic en `Agentower.exe` → aparece el diálogo **Instalar Agentower**.
2. Deja marcadas las casillas (acceso directo en el escritorio y arranque
   con Windows) y pulsa **Instalar**.
3. El exe se copia a `%LOCALAPPDATA%\Programs\Agentower\`, se crean los
   accesos directos (menú Inicio y, opcionalmente, escritorio) y se
   registra el auto-inicio. La copia instalada se abre sola y la portable
   se cierra.

A partir de ahí, Agentower arranca al encender Windows (clave `Run` del
usuario) y arranca el bot si la configuración es válida. Si eliges
**Ahora no**, se ejecuta en modo portable y no vuelve a preguntar.

La instalación también registra Agentower en **Aplicaciones instaladas**
(Configuración → Aplicaciones → Aplicaciones instaladas), así puedes
desinstalarlo como cualquier otro programa.

También puedes controlarlo por línea de comandos:

```powershell
.\Agentower.exe --install              # instala + accesos directos + auto-inicio
.\Agentower.exe --install --no-desktop # sin acceso directo en el escritorio
.\Agentower.exe --install --no-autostart
.\Agentower.exe --portable             # ejecuta sin ofrecer instalar
.\Agentower.exe --uninstall            # desinstala (pregunta si borrar datos)
.\Agentower.exe --uninstall --quiet    # desinstala y borra todo, sin preguntar
```

## Desinstalación

Hay tres formas equivalentes:

1. **Aplicaciones instaladas** → busca *Agentower* → **Desinstalar**.
2. Menú del tray (click derecho) → **Desinstalar Agentower…**.
3. CLI: `.\Agentower.exe --uninstall` (interactivo) o
   `--uninstall --quiet` (borra también configuración y token).

Quita el auto-inicio, los accesos directos, la entrada de *Aplicaciones
instaladas* y, si eliges borrar datos, `%APPDATA%\Agentower` (token,
workspace, estado) y `%LOCALAPPDATA%\Agentower` (logs y binario extraído).
Deja el `.exe` portable que hayas descargado; puedes reinstalar desde él
en cualquier momento.

## Uso

```powershell
.\dist\Agentower.exe
```

La primera vez:

1. El icono aparece en el área de notificación (y, tras instalar, el
   programa queda registrado para el próximo arranque de Windows).
2. Click izquierdo → **Configuración…**.
3. Rellena `WORKSPACE_ROOT`, `TELEGRAM_BOT_TOKEN`, `ALLOWED_CHAT_ID`.
4. En **Agentes**, pulsa *Reintentar detección* y habilita los que uses.
5. Guarda y activa el toggle del popover.

Los cambios de configuración aplican al **próximo inicio** del bot;
deténlo y arráncalo de nuevo con el toggle.

## Rutas en Windows

| Recurso | Ruta |
|---|---|
| App instalada | `%LOCALAPPDATA%\Programs\Agentower\Agentower.exe` |
| Acceso directo | `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Agentower.lnk` |
| Configuración (`settings.json`) | `%APPDATA%\Agentower\settings.json` |
| `.env` para el bot | `%APPDATA%\Agentower\.env` |
| State DB (SQLite) | `%APPDATA%\Agentower\state.db` |
| Endpoint de control | `%APPDATA%\Agentower\control.json` |
| Binario extraído | `%LOCALAPPDATA%\Agentower\remote-bot.exe` |
| Logs | `%LOCALAPPDATA%\Agentower\logs\bot.log` y `agentower.log` |

## Detección de agentes

El wrapper busca cada CLI en `PATH` (respetando `PATHEXT`, así que
resuelve `.exe`, `.cmd`, …) y además en las ubicaciones típicas de
Windows:

| Agente | Ubicaciones sondeadas |
|---|---|
| **opencode** | `%APPDATA%\npm\opencode.cmd`, `%USERPROFILE%\.bun\bin\opencode.exe`, `%USERPROFILE%\.opencode\bin\opencode.exe` |
| **Claude** | `%USERPROFILE%\.local\bin\claude.exe`, `%APPDATA%\npm\claude.cmd` |
| **Kiro** | `%LOCALAPPDATA%\Programs\Kiro\kiro.exe`, `%USERPROFILE%\.kiro\bin\kiro.exe` |
| **GitHub Copilot** | `%APPDATA%\npm\copilot.cmd`, extensión de VS Code en `%USERPROFILE%\.vscode\extensions\github.copilot*` |
| **Codex** | `%APPDATA%\npm\codex.cmd`, `%USERPROFILE%\.codex\bin\codex.exe` |
| **Antigravity** | `%APPDATA%\npm\agy.cmd`, `%USERPROFILE%\.local\bin\agy.exe` |

Estado de sesiones que lee el bot:

- Claude: `%USERPROFILE%\.claude\projects\<cwd sanitizado>`
- Kiro: `%USERPROFILE%\.kiro`
- Copilot: `%APPDATA%\Code\User\globalStorage\github.copilot-chat`
- Codex: `%USERPROFILE%\.codex\sessions`
- Antigravity: `%USERPROFILE%\.gemini\antigravity-cli` y `.gemini\antigravity`

## Verificación (CI / sin interfaz)

```powershell
.\dist\Agentower.exe --selftest
```

Construye todas las ventanas y ejecuta la detección de agentes sin abrir
interfaz; deja el resultado en
`%LOCALAPPDATA%\Agentower\logs\selftest.log` y devuelve código 0 si todo
va bien.

## Diferencias con la versión macOS

- Área de notificación en lugar de barra de menús.
- Auto-inicio por registro HKCU en lugar de `SMAppService`.
- Notificaciones vía *balloon tip* del tray en lugar de
  `UNUserNotificationCenter`.
- El binario Go se extrae a `%LOCALAPPDATA%` en lugar de viajar dentro
  del bundle `.app`.
