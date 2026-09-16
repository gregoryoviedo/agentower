# Agentower

[![CI](https://github.com/gregoryoviedo/agentower/actions/workflows/ci.yml/badge.svg)](https://github.com/gregoryoviedo/agentower/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/gregoryoviedo/agentower)](https://goreportcard.com/report/github.com/gregoryoviedo/agentower)
[![Latest Release](https://img.shields.io/github/v/release/gregoryoviedo/agentower)](https://github.com/gregoryoviedo/agentower/releases/latest)
[![License: MIT](https://img.shields.io/github/license/gregoryoviedo/agentower)](https://github.com/gregoryoviedo/agentower/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gregoryoviedo/agentower)](https://github.com/gregoryoviedo/agentower/blob/main/go.mod)
[![Dependabot](https://img.shields.io/badge/dependabot-enabled-025e8c?logo=dependabot)](https://github.com/gregoryoviedo/agentower/network/dependencies)

Control remoto desde Telegram para una o varias instancias locales de
agentes de IA — **opencode, Claude Code, Kiro, GitHub Copilot, Codex y
Antigravity**. Dos artefactos: un binario Go único (`remote-bot`) que hace
el trabajo, y opcionalmente un wrapper nativo — para macOS
(`Agentower.app`, barra de menús) o para Windows (`Agentower.exe`, área de
notificación de la barra de tareas) — que guarda la configuración,
supervisa el binario y arranca/para el bot por ti.

La idea es permitirte ejecutar, monitorear y guiar sesiones de tu agente
desde el teléfono mientras tu máquina hace el trabajo real, con cero
superficie de ataque pública más allá de la API de bots de Telegram.

## Características

- **Multi-agente** — un solo bot maneja opencode (HTTP + SQLite), Claude Code
  (stdio JSON), Kiro (ACP sobre `kiro-cli`), GitHub Copilot (ACP/LSP),
  Codex (`codex exec --json`) y Antigravity (`agy --output-format
  stream-json`). Cada chat puede cambiar de agente sobre la marcha desde
  Telegram.
- **Long polling a Telegram** — sin puertos expuestos, sin túneles, sin
  webhooks.
- **Whitelist estricta por usuario** — solo el `ALLOWED_CHAT_ID` configurado
  puede usar el bot; cualquier otro intento se descarta silenciosamente.
- **Navegación limitada al workspace** — selector recursivo de carpetas que
  rechaza salir del workspace raíz, incluso con symlinks o `..`.
- **Estado persistente en SQLite** — workspace, proyecto activo, sesión
  activa, agente activo por chat y estados efímeros de navegación
  sobreviven a reinicios.
- **Aviso de tarea completada** — cuando el agente termina y no hubo
  actividad local, el wrapper (macOS o Windows) te manda un mensaje a
  Telegram (a los 2 minutos de inactividad) con la previsualización y
  botones para continuar o ver cambios.
- **Preguntas respondibles desde Telegram** — si el agente se detiene a
  pedir una decisión con opciones (hoy: opencode), el wrapper te reenvía
  la pregunta con botones al minuto de inactividad; tu respuesta
  (botón o texto) vuelve al agente que está corriendo en tu máquina.
- **App nativa para macOS (opcional)** — menú-barra con toggle, settings
  con formulario, auto-start al login, sección multi-agente con detección
  en PATH y en los bundles de las apps (Kiro CLI, VS Code Copilot), y
  logs accesibles desde Finder. Construye un `.app` ad-hoc firmado con
  `make app` que embebe el binario Go.
- **App nativa para Windows (opcional)** — icono en el área de
  notificación con popover y menú contextual, la misma ventana de
  Settings que macOS, detección de agentes con rutas de Windows,
  auto-inicio por registro (que además arranca el bot al iniciar sesión)
  y notificaciones de tarea/pregunta. Construye un `Agentower.exe`
  self-contained con `.\windows\build.ps1` (embebe `remote-bot.exe`).
- **Binario único en Go** — sin daemon, sin GUI obligatoria, sin firma.
  Tú lo compilas, lo ejecutas y lo paras con `Ctrl+C`.

## Requisitos

- macOS, Linux o Windows para el binario Go.
- macOS 13.0+ para la app nativa de menú-barra.
- Windows 10/11 x64 para la app de bandeja (`Agentower.exe`).
- Go 1.22 o superior.
- El **.NET 8 SDK** si vas a compilar el wrapper de Windows.
- Al menos uno de los siguientes agentes instalado localmente:
  - `opencode` (CLI; instala vía `brew install anomalyco/tap/opencode`)
  - `claude` (Claude Code CLI)
  - `kiro` (Kiro IDE/CLI; sesiones en `~/.kiro/`)
  - `copilot` o `copilot-language-server` (GitHub Copilot ACP/LSP)
  - `codex` (OpenAI Codex CLI; sesiones en `~/.codex/`)
  - `agy` (Antigravity CLI; sesiones en `~/.gemini/antigravity-cli/`)
- Un token de bot de Telegram desde `@BotFather`.
- Tu ID personal de chat desde `@userinfobot`.

## Inicio rápido

Agentower se distribuye como un wrapper nativo que **embebe** el binario
Go. La forma recomendada de usarlo es construir el wrapper de tu sistema y
configurar las credenciales desde la UI.

### macOS

```bash
make app
cp -R dist/Agentower.app /Applications/
xattr -dr com.apple.quarantine /Applications/Agentower.app
open /Applications/Agentower.app
```

Al primer arranque, abrí **Settings…** desde el menú de la barra, completá
`WORKSPACE_ROOT`, `TELEGRAM_BOT_TOKEN` y `ALLOWED_CHAT_ID`. La sección
**Agentes de IA** muestra qué CLIs detectó en PATH (botón "Reintentar
detección" para refrescar) y te permite habilitar opencode, Claude,
Kiro, Copilot, Codex o Antigravity individualmente. Al guardar, la app
escribe un `.env` con permisos `0600` en
`~/Library/Application Support/Agentower/`. A partir de ahí el toggle del
popover arranca y detiene el bot.

### Windows

```powershell
.\windows\build.ps1
.\dist\Agentower.exe
```

Al primer arranque aparece un icono en el área de notificación.
**Click izquierdo** abre el popover (toggle + acciones), **click derecho**
el menú contextual. Abrí **Configuración…**, completá `WORKSPACE_ROOT`,
`TELEGRAM_BOT_TOKEN` y `ALLOWED_CHAT_ID`, y en **Agentes** habilitá los
CLIs detectados. Al guardar, el wrapper escribe `settings.json` y `.env`
en `%APPDATA%\Agentower\`. La casilla de **Inicio** activa el arranque al
encender Windows; al lanzarse, la app **arranca el bot automáticamente**
si la configuración es válida. Detalles en
[`windows/README.md`](windows/README.md).

### Ejecución del binario sin la app (avanzado)

Si preferís correr el binario Go directamente, podés exportando las
variables en tu shell o apuntando `ENV_FILE` a un archivo propio:

```bash
go build -o remote-bot ./cmd/remote-bot
ENV_FILE=/ruta/a/tu/.env ./remote-bot
```

El bot resuelve la configuración desde el `.env` (con búsqueda en
directorios padre) o directamente desde las variables de entorno si
preferís no usar archivo.

### Variables obligatorias

| Variable             | Descripción                                            |
|----------------------|--------------------------------------------------------|
| `WORKSPACE_ROOT`     | Ruta absoluta que delimita toda la navegación.         |
| `TELEGRAM_BOT_TOKEN` | Token del bot desde `@BotFather`.                      |
| `ALLOWED_CHAT_ID`    | Tu ID numérico de usuario desde `@userinfobot`.        |

### Variables opcionales (multi-agente)

Cada agente tiene su propio slot, incluido opencode. El prefijo es
`AGENT_<KIND>_` en mayúsculas (`AGENT_OPENCODE_`, `AGENT_CLAUDE_`,
`AGENT_KIRO_`, `AGENT_COPILOT_`, `AGENT_CODEX_`, `AGENT_ANTIGRAVITY_`).
Las claves disponibles son:

| Clave                      | Por defecto | Descripción                                                                 |
|----------------------------|-------------|-----------------------------------------------------------------------------|
| `AGENT_<KIND>_ENABLED`      | `true`      | Habilita este agente para el bot. `false` lo apaga.                          |
| `AGENT_<KIND>_BIN`          | derivado    | Ruta al binario. Si está vacío se autodetecta vía `PATH` o los bundles/instaladores (VS Code Copilot, Kiro, Antigravity). |
| `AGENT_<KIND>_PORT`         | derivado    | Puerto loopback. Solo lo usan los agentes con servidor (opencode); el resto lo reserva sin usarlo. |
| `AGENT_<KIND>_ARGS`         | _(vacío)_   | Argumentos extra a pasarle al binario (avanzado).                            |

### Otras variables opcionales

| Variable              | Por defecto                                            | Descripción                                                                                              |
|-----------------------|--------------------------------------------------------|----------------------------------------------------------------------------------------------------------|
| `AGENTOWER_STATE_PATH`   | `<WORKSPACE_ROOT>/.agentower/state.db`           | Ubicación de la base SQLite.                                                                             |
| `AGENTOWER_STALE_AFTER`   | `30m`                                           | Sesiones más viejas que esto se ignoran en `/resume`.                                                      |
| `AGENTOWER_OPENCODE_STATE_DIR` | `~/.local/share/opencode`                  | Override del path de la base SQLite de opencode (historial de sesiones).                                 |
| `AGENTOWER_COPILOT_STATE_DIR` | derivado del SO                            | Override del path de VS Code globalStorage para Copilot Chat.                                            |
| `AGENTOWER_CLAUDE_STATE_DIR`  | `~/.claude`                                  | Override del path de Claude Code.                                                                        |
| `AGENTOWER_KIRO_STATE_DIR`    | `~/.kiro`                                    | Override del path de Kiro (CLI e IDE).                                                                   |
| `AGENTOWER_CODEX_STATE_DIR`   | `~/.codex`                                   | Override del path de sesiones de Codex.                                                                  |
| `AGENTOWER_ANTIGRAVITY_STATE_DIR` | `~/.gemini`                              | Override de la raíz de Antigravity (el adapter deriva `antigravity-cli`/`antigravity`).                  |
| `TELEGRAM_API_ROOT`   | (predeterminado de `telebot.v3`)                        | Override del endpoint de la API de Telegram (útil para mirrors o tests). Se lee del `.env` o del shell. |
| `TELEGRAM_PROXY_URL`  | _(vacío)_                                               | URL de proxy HTTP para las llamadas a la API de Telegram (formato `http://host:port`).                  |
| `ENV_FILE`            | `.env` subiendo desde el directorio actual              | Forzar un archivo `.env` específico.                                                                     |

> **Codex y Antigravity** son CLIs headless sin TTY para responder prompts
> de permisos, así que sus managers pasan flags de auto-aprobación por
> defecto (`--dangerously-bypass-approvals-and-sandbox` y
> `--dangerously-skip-permissions`). Puedes sobreescribir el conjunto de
> flags con `AGENT_CODEX_ARGS` / `AGENT_ANTIGRAVITY_ARGS`.

## Multi-agente

Agentower es una extensión de Telegram para **seguir** a tus agentes, no
para controlarlos por completo. Detecta los agentes instalados y observa
sus sesiones sin que tengas que elegir nada: cada agente con un
`SessionLocator` tiene un observador que detecta cuándo termina una tarea
o hace una pregunta, y te avisa por Telegram. Desde el chat podés
continuar la sesión y responder, pero el agente lo seguís manejando en tu
IDE/TUI.

Los agentes disponibles son los que el detector encuentra en `PATH` o, si
no están ahí, en las ubicaciones de instalación de cada uno: bundles de
apps en macOS (Kiro CLI, GitHub Copilot de VS Code, Antigravity) y
directorios por usuario en Windows (`%APPDATA%\npm`,
`%LOCALAPPDATA%\Programs`, `~/.opencode`, `~/.bun`, `~/.codex`, …). Los
lanzamientos desde la GUI heredan un `PATH` mínimo (Finder/launchd en
macOS, el entorno de Explorer en Windows), así que el bot lo aumenta
antes de detectar con los directorios habituales del usuario
(`~/.local/bin`, `~/.nvm/...`, Homebrew, `%APPDATA%\npm`, …).

Cómo se comunica cada agente:

| Agente    | Transporte                                  | Historial / sesiones                        |
|-----------|---------------------------------------------|---------------------------------------------|
| opencode  | HTTP (`opencode serve`) + SQLite (`opencode.db`) | REST (`/session`, `/api/question`) / BD local (`session`, `message`, `part`) |
| claude    | stdio JSON (`claude --print --output-format stream-json`) | JSONL en `~/.claude/projects/<cwd>/`        |
| kiro      | ACP (`kiro-cli acp`)                        | JSONL en `~/.kiro/sessions/<ws>/<id>/`       |
| copilot   | ACP (CLI) / LSP (bundle de VS Code)         | `session-store.db` de VS Code globalStorage |
| codex     | headless (`codex exec --json -`)            | JSONL en `~/.codex/sessions/**/rollout-*`   |
| antigravity | headless (`agy -p --output-format stream-json`) | JSONL en `~/.gemini/antigravity-cli/...` |

Puertos reservados por agente (cada uno override-able por env):

| Agente    | Puerto por defecto |
|-----------|--------------------|
| opencode  | 4096               |
| claude    | 4097               |
| kiro      | 4099               |
| copilot   | 4100               |
| codex     | 4101               |
| antigravity | 4102             |

### Auto-arranque de los agentes

Agentower arranca los agentes **de forma perezosa**: no lanza nada al
inicio. Mientras no le escribas, el bot se limita a seguir las sesiones
leyendo su historial en disco (para opencode, directamente su base SQLite
`~/.local/share/opencode/opencode.db`, así que sigue tanto la TUI local
como `opencode serve`). Cuando envías texto libre, tocás **▶️ Continuar
sesión** o usás `/continue` por primera vez sobre una sesión, el bot
arranca el agente en la carpeta de ese proyecto (la que reporta el
locator) y recién ahí le manda el prompt.

Para opencode, si ya tenés un `opencode serve` corriendo lo **adopta** en
vez de levantar un segundo servidor; si no hay ninguno, lanza
`opencode serve --port 4096` en el directorio del proyecto. Los agentes
stdio (Claude Code, Kiro, Copilot, Codex, Antigravity) lanzan su
subproceso la primera vez que reciben un prompt a través del bot.

El bot apaga los subprocesos que sí haya arrancado él cuando recibe
`Ctrl+C` o una señal de terminación: con `SIGTERM`/`SIGKILL` en
macOS/Linux, y con `taskkill /T /F` del árbol de procesos en Windows. Un
`opencode serve` adoptado (arrancado por vos) nunca se mata.

## Comandos

| Comando                | Descripción                                             |
|------------------------|---------------------------------------------------------|
| `/start` / `/help`     | Bienvenida y lista de comandos.                         |
| `/status`              | Qué agente y sesión está siguiendo el bot.              |
| `/continue`            | Retoma la última tarea completada (la activa para responder). |
| `/resume`              | Detecta la sesión que se está ejecutando en tu máquina y te ofrece seguirla desde Telegram. |
| texto libre            | Responde a la sesión activa del agente. Si hay una pregunta pendiente del agente, el texto se envía como **respuesta a esa pregunta**. |

Los Inline Keyboards manejan el resto: **▶️ Continuar sesión** y **📝 Ver
cambios** en las notificaciones, y las opciones de las preguntas del
agente.

## Qué sesión sigue el bot

Agentower no necesita que le indiques proyecto ni agente. Cada agente con
locator tiene un observador que auto-sigue la sesión más reciente del
usuario (por ejemplo la que estás usando en Kiro, VS Code o en `opencode`
—tanto la TUI como `opencode serve`—) y registra el completado cuando deja
de cambiar.
Ignora sesiones que no se tocaron desde que el bot arrancó, así que un
reinicio no rerescribe completados viejos.

Cuando tocas **▶️ Continuar sesión** (o usás `/continue`), esa sesión pasa
a ser la activa y el texto libre que escribas se envía ahí. `/resume` hace
lo mismo pero consultando en el momento qué sesión está viva en tu
máquina.

## Notificaciones por inactividad

El wrapper observa cuánto tiempo llevas sin tocar el teclado o el mouse
(`CGEventSource` en macOS, `GetLastInputInfo` en Windows) y consulta el
estado del bot por un socket local. Con eso dispara dos flujos, pensados
para cuando te alejas de la computadora:

### Tarea completada (2 minutos)

1. El `SessionWatcher` detecta que la sesión dejó de cambiar y guarda un
   snapshot de completado.
2. Si pasan **2 minutos sin actividad local**, la app le pide al bot que
   te avise por Telegram.
3. Recibes un mensaje con proyecto, sesión, previsualización y botones
   **▶️ Continuar sesión** y **📝 Ver cambios**.

### Pregunta del agente (1 minuto)

Aplica a agentes que pueden pausar a mitad de una tarea para pedir una
decisión con opciones (hoy **opencode**, vía su *question tool*).

1. El `SessionWatcher` detecta la pregunta pendiente y **no** marca la
   tarea como completada (el agente no terminó, está esperando).
2. Si pasan **1 minuto sin actividad local**, la pregunta se reenvía a
   Telegram: encabezado, pregunta y una opción por botón (más el aviso de
   que podés responder con texto si la pregunta admite respuesta libre).
3. Respondés tocando una opción o escribiendo el texto. La respuesta se
   envía al agente que corre en tu máquina, que continúa la tarea.
4. Si son varias preguntas, se muestran una por una; al responderlas
   todas se envían juntas al agente. Cuando la tarea termina, entra el
   flujo normal de "tarea completada".

Los umbrales (1 y 2 minutos) están en `IdleNotifier.swift` (macOS) y
`IdleNotifier.cs` (Windows). El aviso Telegram para una pregunta ya
enviada no se repite; si la respondiste en la terminal, el bot descarta
el aviso pendiente.

> En macOS, al abrir la app (incluido el arranque al login) el bot
> arranca solo si la configuración es válida. En Windows pasa lo mismo:
> `Agentower.exe` se inicia al encender el equipo y **arranca el bot de
> inmediato** para que quede en segundo plano apenas empieces a trabajar.

## App nativa de macOS

Además del binario CLI hay un wrapper Swift opcional para macOS que vive
en la barra de menús (estilo Bluetooth), guarda la configuración en
`UserDefaults` + un `.env` con permisos `0600`, y arranca/para el bot con
un toggle.

### Build

```bash
make app
```

Esto:

1. Compila `remote-bot` (`arm64`) y lo coloca en
   `macos/Agentower/Resources/`.
2. Genera el `MenuBarIcon` monocromo (template) y el `AppIcon.icns` desde
   `macos/Agentower/assets/app-icon.jpeg` con `sips` + Python/Pillow.
3. Regenera el `.xcodeproj` con XcodeGen.
4. Compila con `xcodebuild` (`arm64`, sin firma de Apple Developer).
5. Empaqueta en `dist/Agentower.app` y lo firma ad-hoc.

Salida: `dist/Agentower.app` (~10.5 MB).

### Instalación y primer uso

```bash
# Mover a Aplicaciones
cp -R dist/Agentower.app /Applications/

# Quitar la marca de Gatekeeper (porque no tiene Developer ID)
xattr -dr com.apple.quarantine /Applications/Agentower.app

# Abrir
open /Applications/Agentower.app
```

Aparece un icono en la barra de menús. **Click izquierdo** abre el popover
con un toggle. **Click derecho** abre menú contextual.

**Primera vez**: el toggle está deshabilitado. Click en **Settings…**, llena
`WORKSPACE_ROOT`, `TELEGRAM_BOT_TOKEN`, `ALLOWED_CHAT_ID`. La sección
**Agentes de IA** muestra qué CLIs detectó en PATH (botón "Reintentar
detección" para refrescar) y te permite habilitar opencode, Claude,
Kiro o Copilot individualmente. Al guardar, la app escribe
`~/Library/Application Support/Agentower/.env` con permisos `0600` y a
partir de ahí el toggle funciona.

### Auto-inicio al login

En Settings marcá la casilla "Iniciar Agentower al arrancar macOS".
La app llama `SMAppService.mainApp.register()` y aparece en *Ajustes del
sistema → General → Ítems de inicio*. Solo funciona si la app vive en
`/Applications/` o `~/Applications/`.

Al abrir la app (incluido el arranque al login), si la configuración es
válida el bot arranca solo; no hace falta pulsar Iniciar.

### Cómo funciona el toggle

- **Apagado → click**: arranca `Resources/remote-bot` con env vars
  (`ENV_FILE=…/.env`, `AGENTOWER_STATE_PATH=…/state.db`, `GIN_MODE=release`,
  y opcionalmente `TELEGRAM_API_ROOT` y `TELEGRAM_PROXY_URL` si los
  llenaste en Settings). El binario lee el `.env` y entra en long-polling
  de Telegram.
- **Encendido → click**: `terminate()` (SIGTERM), espera 5 s, `SIGKILL` si
  no salió.
- **Cambios en Settings**: no rearrancan el bot en caliente; aparece un
  banner "Los cambios aplican al próximo inicio". Restart manual desde el
  toggle.

### Ubicaciones

| Recurso | Ruta |
|---|---|
| Settings (UserDefaults) | `~/Library/Preferences/dev.agentower.app.plist` |
| `.env` para el bot | `~/Library/Application Support/Agentower/.env` (`chmod 600`) |
| State DB (SQLite) | `~/Library/Application Support/Agentower/state.db` |
| Logs | `~/Library/Logs/Agentower/bot.log` |

### Prerrequisitos del entorno de build

- macOS con **Xcode** y **Xcode command-line tools** (`xcode-select --install`).
- **XcodeGen** (`brew install xcodegen` si falta).
- **Go** 1.22+ en `PATH`.
- **Python 3** con **Pillow** (`pip3 install Pillow`) para regenerar
  los iconos monocromos. Requerido solo si corres `make icons`.

## App nativa de Windows

Además del binario CLI hay un wrapper WinForms para Windows que vive en
el **área de notificación** de la barra de tareas (junto a Steam, Epic,
Discord…), guarda la configuración como JSON + `.env`, y arranca/para el
bot con un toggle. Es el equivalente funcional de la app de macOS.

### Build

```powershell
.\windows\build.ps1
```

Esto:

1. Compila `remote-bot` (`windows/amd64`) en
   `windows\Agentower\Resources\remote-bot.exe`.
2. Publica el wrapper como único fichero `dist\Agentower.exe`
   (self-contained, sin requerir .NET instalado).

Salida: `dist\Agentower.exe`.

Requiere **Go 1.22+** y el **.NET 8 SDK**. Detalles en
[`windows/README.md`](windows/README.md).

### Uso

`Agentower.exe` es portable. Al ejecutarlo por primera vez **ofrece
instalarse**: copia el exe a `%LOCALAPPDATA%\Programs\Agentower\`, crea
los accesos directos (menú Inicio y, opcionalmente, escritorio) y
registra el arranque con Windows. A partir de ahí no hace falta volver a
buscar el `.exe`: arranca solo al encender el equipo. También puedes
forzarlo por CLI (`--install`, `--uninstall`, `--portable`).

Ya en el área de notificación, **click izquierdo** abre el popover
(toggle + acciones) y **click derecho** el menú contextual. Abre
**Configuración…**, rellena `WORKSPACE_ROOT`, `TELEGRAM_BOT_TOKEN` y
`ALLOWED_CHAT_ID`, y en **Agentes** habilita los CLIs detectados. Al
guardar, el wrapper escribe `%APPDATA%\Agentower\settings.json` y `.env`.
El toggle arranca y detiene el bot.

### Ubicaciones

| Recurso | Ruta |
|---|---|
| App instalada | `%LOCALAPPDATA%\Programs\Agentower\Agentower.exe` |
| Acceso directo | `%APPDATA%\Microsoft\Windows\Start Menu\Programs\Agentower.lnk` |
| Settings (`settings.json`) | `%APPDATA%\Agentower\settings.json` |
| `.env` para el bot | `%APPDATA%\Agentower\.env` |
| State DB (SQLite) | `%APPDATA%\Agentower\state.db` |
| Binario extraído | `%LOCALAPPDATA%\Agentower\remote-bot.exe` |
| Logs | `%LOCALAPPDATA%\Agentower\logs\bot.log` |

### Notificación de tarea terminada y auto-inicio

El wrapper usa `GetLastInputInfo` para medir la inactividad y consulta el
servidor de control local del bot (`control.json` → `/state` →
`/notify` y `/question-notify`) para enviar el mensaje de Telegram y
mostrar una notificación de Windows cuando una tarea termina o el agente
hace una pregunta mientras no estás. La instalación registra el auto-
inicio en la clave `Run` del usuario (activado por defecto); al
arrancar, el wrapper **arranca el bot de inmediato** si la configuración
es válida, de modo que queda corriendo en segundo plano apenas empiezas a
trabajar.

## Automatización del inicio (CLI sin wrapper)

Si prefieres seguir con el binario Go puro y automatizar su arranque, las
opciones documentadas originalmente siguen vigentes: en macOS (Raycast
Script Command, Atajos de macOS o `launchd`) y en Windows (Programador de
tareas, la carpeta de Inicio o `HKCU\...\Run`). Los detalles y plantillas
están en el historial del repo; los wrappers cubren el mismo caso de uso
con menos fricción.

## Seguridad

- El bot solo responde al `ALLOWED_CHAT_ID` configurado. El resto se descarta
  silenciosamente sin llegar al handler.
- Los callbacks de los Inline Keyboards se validan contra un registro de
  navegación de corta vida por chat. Otros chats reciben "menú expirado".
- Todas las rutas que vienen de Telegram son relativas; se resuelven, se
  evaluan los symlinks y la ruta final debe permanecer dentro de
  `WORKSPACE_ROOT`.
- El token se lee del `.env` al arrancar. SQLite solo guarda proyecto
  activo, sesión activa y estado efímero — nunca contenido de prompts.
- Cuando se usa el wrapper de macOS, el `.env` regenerado se escribe con
  permisos `0600` y vive dentro de `~/Library/Application Support/`,
  accesible solo al usuario actual. En Windows, el `.env`, `settings.json`
  y `state.db` viven bajo `%APPDATA%\Agentower\`, que por defecto solo es
  accesible para la cuenta del usuario.

Consulta `SECURITY.md` para el modelo de confianza completo.

## Arquitectura

Hexagonal / Clean Architecture en Go, con tres capas concéntricas:

```text
internal/
  domain/      entidades (Project, Session, RuntimeState, NavigationState,
               FileChange, Message, CompletedSession, PendingQuestion,
               BotButton, BotResponse) y puertos (WorkspaceFS,
               StateRepository, NavigationRepository, AgentAdapter,
               AgentRegistry, AgentServerManager, SessionLocator,
               SessionEventLog, SnapshotPublisher, CompletionPublisher,
               QuestionAdapter, QuestionBroker, ChatNotifier, BotHandler)
  usecase/     navegador del workspace, navegación, Handler de comandos,
               SessionWatcher (polling de la sesión: completados y
               preguntas) y sentinels de error del dominio
  adapter/
    agents/    opencode (HTTP), claude (stdio JSON), kiro (ACP + JSONL),
               copilot (ACP/LSP), codex (headless JSON), antigravity
               (headless stream-json) + registry y detector (PATH + bundles)
    telegram/  long polling con telebot.v3, whitelist, callbacks,
               parser markdown → HTML
    storage/   repositorio SQLite (WAL, una conexión)
    control/   servidor HTTP local para los wrappers (/state, /notify,
               /question-notify)
    workspace/ adaptador de filesystem
    config/    cargador de .env (godotenv)
cmd/remote-bot/  composition root
```

La capa de dominio no tiene dependencias externas; todo lo demás va detrás
de interfaces declaradas por el dominio. Esto permite que los tests
sustituyan SQLite por un store en memoria y OpenCode por un servidor
`httptest`.

Los wrappers (macOS y Windows) y el bot hablan por un socket local
(`/state`, `/notify`, `/question-notify`): el bot nunca abre puertos hacia
afuera y los wrappers nunca hacen llamadas de red propias, solo consultan
ese socket en `127.0.0.1`.

Sobre el binario viven opcionalmente los wrappers nativos: `Agentower.app`
(SwiftUI + AppKit) en la barra de menús de macOS y `Agentower.exe`
(C#/.NET WinForms) en el área de notificación de Windows. Ambos actúan
como launcher con UI, persistencia y supervisión de logs.

Consulta `docs/DESIGN.md` para el documento de diseño completo.

## Producto

Consulta `docs/PRODUCT.md` para el alcance actual del producto, lo que
está dentro y fuera de alcance, y la hoja de ruta planificada.

## Desarrollo

```bash
go test ./...
go vet ./...
go build -o remote-bot ./cmd/remote-bot
```

Para construir el wrapper Swift:

```bash
make app
```

Para construir el wrapper de Windows:

```powershell
.\windows\build.ps1
```

CI: cada push y PR ejecuta `go test -race` con `-coverprofile` sobre
Go 1.23 (Ubuntu) y `golangci-lint` sobre Ubuntu, más un job de Windows
que compila `Agentower.exe` y corre su `--selftest`. Las dependencias se
mantienen al día vía Dependabot (`gomod`, `github-actions`, `swift`),
agrupadas en PRs separados por tipo. La configuración del linter vive en
`.golangci.yml`.

El repositorio conserva el binario `remote-bot` compilado en la raíz por
comodidad (es el único artefacto que produce el proyecto). Bórralo antes de
publicar una rama si no quieres enviar el binario compilado junto con el
código fuente.

## Estructura

```text
.
├── cmd/remote-bot/              punto de entrada Go
├── internal/
│   ├── adapter/                 agentes (opencode/claude/kiro/copilot/
│   │                            codex/antigravity), Telegram, SQLite,
│   │                            control local, filesystem
│   ├── config/                  cargador de .env
│   ├── domain/                  entidades y puertos
│   └── usecase/                 navegador del workspace, navegación, handler,
│                                session watcher (completados y preguntas)
├── macos/
│   └── Agentower/          wrapper Swift (status bar, settings, login item)
├── windows/
│   ├── Agentower/          wrapper C# / .NET 8 WinForms (tray, settings, idle)
│   └── build.ps1           compila remote-bot.exe y Agentower.exe
├── docs/
│   ├── DESIGN.md                arquitectura y decisiones
│   └── PRODUCT.md               alcance y hoja de ruta
├── .github/
│   ├── workflows/ci.yml         tests Go + golangci-lint + build Windows
│   ├── dependabot.yml           actualizaciones semanales de dependencias
│   ├── ISSUE_TEMPLATE/          bug report y feature request
│   └── PULL_REQUEST_TEMPLATE.md checklist para contribuidores
├── .golangci.yml                configuración del linter
├── AGENTS.md                    guía para agentes de IA / contribuidores
├── CODE_OF_CONDUCT.md
├── CONTRIBUTING.md
├── LICENSE
├── Makefile                     build del wrapper macOS
├── README.md
├── SECURITY.md
├── go.mod
├── go.sum
└── remote-bot                   binario compilado (arm64)
```

## Licencia

MIT. Consulta `LICENSE`.
