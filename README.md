# Agentower

[![CI](https://github.com/gregoryoviedo/agentower/actions/workflows/ci.yml/badge.svg)](https://github.com/gregoryoviedo/agentower/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/gregoryoviedo/agentower)](https://goreportcard.com/report/github.com/gregoryoviedo/agentower)
[![Latest Release](https://img.shields.io/github/v/release/gregoryoviedo/agentower)](https://github.com/gregoryoviedo/agentower/releases/latest)
[![License: MIT](https://img.shields.io/github/license/gregoryoviedo/agentower)](https://github.com/gregoryoviedo/agentower/blob/main/LICENSE)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gregoryoviedo/agentower)](https://github.com/gregoryoviedo/agentower/blob/main/go.mod)
[![Dependabot](https://img.shields.io/badge/dependabot-enabled-025e8c?logo=dependabot)](https://github.com/gregoryoviedo/agentower/network/dependencies)

Control remoto desde Telegram para una o varias instancias locales de
agentes de IA — **opencode, Claude Code, Codex, Kiro y GitHub Copilot**.
Dos artefactos: un binario Go único (`remote-bot`) que hace el trabajo, y
opcionalmente una app nativa para macOS (`Agentower.app`) que vive en la
barra de menús, guarda la configuración y lanza/para el binario por ti.

La idea es permitirte ejecutar, monitorear y guiar sesiones de tu agente
desde el teléfono mientras tu MacBook hace el trabajo real, con cero
superficie de ataque pública más allá de la API de bots de Telegram.

## Características

- **Multi-agente** — un solo bot maneja opencode (HTTP), Claude Code y
  Codex (stdio JSON), Kiro (stdio JSON, capacidades limitadas) y
  GitHub Copilot (LSP). Cada chat puede cambiar de agente sobre la
  marcha desde Telegram.
- **Long polling a Telegram** — sin puertos expuestos, sin túneles, sin
  webhooks.
- **Whitelist estricta por usuario** — solo el `ALLOWED_CHAT_ID` configurado
  puede usar el bot; cualquier otro intento se descarta silenciosamente.
- **Navegación limitada al workspace** — selector recursivo de carpetas que
  rechaza salir del workspace raíz, incluso con symlinks o `..`.
- **Estado persistente en SQLite** — workspace, proyecto activo, sesión
  activa, agente activo por chat y estados efímeros de navegación
  sobreviven a reinicios.
- **App nativa para macOS (opcional)** — menú-barra con toggle, settings
  con formulario, auto-start al login, sección multi-agente con detección
  en PATH y logs accesibles desde Finder. Construye un `.app` ad-hoc
  firmado con `make app` que embebe el binario Go.
- **Binario único en Go** — sin daemon, sin GUI obligatoria, sin firma.
  Tú lo compilas, lo ejecutas y lo paras con `Ctrl+C`.

## Requisitos

- macOS, Linux o Windows para el binario Go.
- macOS 13.0+ para la app nativa de menú-barra.
- Go 1.22 o superior.
- Al menos uno de los siguientes agentes instalado localmente:
  - `opencode` (CLI; instala vía `brew install anomalyco/tap/opencode`)
  - `claude` (Claude Code CLI)
  - `codex` (OpenAI Codex CLI)
  - `kiro` (AWS Kiro CLI; capacidades limitadas)
  - `copilot` o `copilot-language-server` (GitHub Copilot LSP)
- Un token de bot de Telegram desde `@BotFather`.
- Tu ID personal de chat desde `@userinfobot`.

## Inicio rápido

Agentower se distribuye como app nativa de macOS que **embebe** el binario
Go. La forma recomendada de usarlo es construir la app y configurar las
credenciales desde la UI:

```bash
make app
cp -R dist/Agentower.app /Applications/
xattr -dr com.apple.quarantine /Applications/Agentower.app
open /Applications/Agentower.app
```

Al primer arranque, abrí **Settings…** desde el menú de la barra, completá
`WORKSPACE_ROOT`, `TELEGRAM_BOT_TOKEN` y `ALLOWED_CHAT_ID`. La sección
**Agentes de IA** muestra qué CLIs detectó en PATH (botón "Reintentar
detección" para refrescar) y te permite habilitar opencode, Claude, Codex,
Kiro o Copilot individualmente. Al guardar, la app escribe un `.env`
con permisos `0600` en `~/Library/Application Support/Agentower/`. A
partir de ahí el toggle del popover arranca y detiene el bot.

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
`AGENT_CODEX_`, `AGENT_KIRO_`, `AGENT_COPILOT_`). Las claves
disponibles son:

| Clave                      | Por defecto | Descripción                                                                 |
|----------------------------|-------------|-----------------------------------------------------------------------------|
| `AGENT_<KIND>_ENABLED`      | `true`      | Habilita este agente para el bot. `false` lo apaga.                          |
| `AGENT_<KIND>_BIN`          | derivado    | Ruta al binario. Si está vacío se autodetecta vía `PATH` o el bundle de VS Code (Copilot). |
| `AGENT_<KIND>_PORT`         | derivado    | Puerto loopback (opencode, codex, kiro). Para stdio agents no se usa.        |
| `AGENT_<KIND>_ARGS`         | _(vacío)_   | Argumentos extra a pasarle al binario (avanzado).                            |

### Otras variables opcionales

| Variable              | Por defecto                                            | Descripción                                                                                              |
|-----------------------|--------------------------------------------------------|----------------------------------------------------------------------------------------------------------|
| `AGENTOWER_STATE_PATH`   | `<WORKSPACE_ROOT>/.agentower/state.db`           | Ubicación de la base SQLite.                                                                             |
| `TELEGRAM_API_ROOT`   | (predeterminado de `telebot.v3`)                        | Override del endpoint de la API de Telegram (útil para mirrors o tests). Se lee del `.env` o del shell. |
| `TELEGRAM_PROXY_URL`  | _(vacío)_                                               | URL de proxy HTTP para las llamadas a la API de Telegram (formato `http://host:port`).                  |
| `ENV_FILE`            | `.env` subiendo desde el directorio actual              | Forzar un archivo `.env` específico.                                                                     |

## Multi-agente

El bot puede manejar varios agentes de IA simultáneamente. Cada chat
de Telegram elige su agente activo en `/projects → Usar esta carpeta`
o vía el comando `/agent`. Los agentes disponibles son los que el
detector encuentra en `PATH` (o en el bundle de VS Code para Copilot).
Cambiar de agente no apaga los demás: Agentower puede mantener
varios procesos vivos a la vez y cambiar el "activo" en Telegram sin
rearrancar.

Puertos reservados por agente (cada uno override-able por env):

| Agente    | Puerto por defecto |
|-----------|--------------------|
| opencode  | 4096               |
| claude    | 4097               |
| codex     | 4098               |
| kiro      | 4099               |
| copilot   | 4100               |

### Auto-arranque del servidor

Por defecto el servidor OpenCode **no** se levanta al iniciar el bot.
Arrancarlo es responsabilidad tuya vía `/init` (atajo manual) o
`/projects → Usar esta carpeta` (cuando seleccionas un proyecto). Esto se
debe a que `opencode serve` queda atado a la carpeta desde la que lo
arrancas, así que el bot prefiere esperar a que le digas qué proyecto
quieres antes de gastar un puerto.

Sea como sea, el bot siempre apaga el servidor con `SIGTERM` cuando recibe
`Ctrl+C` o una señal de terminación.

## Comandos

| Comando                | Descripción                                             |
|------------------------|---------------------------------------------------------|
| `/start` / `/help`     | Bienvenida y lista de comandos.                         |
| `/agent`               | Muestra el picker de agentes (opencode, Claude, Codex, Kiro, Copilot). |
| `/agents`              | Lista los agentes detectados con toggle enable/disable.  |
| `/agents migrate`      | Marca sesiones heredadas como `opencode` (compatibilidad). |
| `/status`              | Salud del agente activo y proyecto/sesión activos.       |
| `/projects`            | Selector recursivo de carpetas; al confirmar, **muestra el picker de agente** y (re)arranca el servidor elegido. |
| `/init`                | (Re)arranca el agente activo en la carpeta activa. Acepta una ruta **relativa al workspace** (`/init work/proyecto`). |
| `/sessions`            | Lista o crea sesiones del proyecto y agente activos. Acepta `new` (crea y activa) o un `id` de sesión. |
| `/diff` / `/changes`   | Archivos modificados por la sesión activa.              |
| `/undo`                | Revierte el último cambio.                              |
| texto libre            | Prompt directo a la sesión activa del agente.            |

Los Inline Keyboards manejan el resto: carpetas, "Atrás", "Inicio", "Usar
esta carpeta", selección de agente, selección de sesión y "Nueva sesión".

## Modelo de agente activo

Cada agente se ata a un directorio desde el que lo arrancas, de manera
similar al `opencode serve` original. Agentower maneja varios agentes a
la vez y el bot cambia el "activo" en Telegram sin matar los demás:

- **`/projects` → "Usar esta carpeta"**: el bot muestra el picker de agente
  y, una vez elegido, (re)arranca el agente seleccionado con la carpeta
  recién seleccionada como CWD.
- **`/agent`**: cambia el agente activo del chat en cualquier momento, sin
  tocar la carpeta ni los procesos de los demás agentes.
- **`/init [ruta]`**: atajo para rearrancar el agente activo sin cambiar de
  agente. Si pasas una ruta, se usa como nueva CWD; si no, se reutiliza
  la carpeta activa.

> Si el agente activo está apagado, cualquier comando (`/status`,
> `/sessions`, `/diff`, `/undo`, texto libre) devuelve un mensaje pidiéndote
> ejecutar `/init`.

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
detección" para refrescar) y te permite habilitar opencode, Claude, Codex,
Kiro o Copilot individualmente. Al guardar, la app escribe
`~/Library/Application Support/Agentower/.env` con permisos `0600` y a
partir de ahí el toggle funciona.

### Auto-inicio al login

En Settings marcá la casilla "Iniciar Agentower al arrancar macOS".
La app llama `SMAppService.mainApp.register()` y aparece en *Ajustes del
sistema → General → Ítems de inicio*. Solo funciona si la app vive en
`/Applications/` o `~/Applications/`.

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

## Automatización del inicio (CLI sin wrapper Swift)

Si prefieres seguir con el binario Go puro y automatizar su arranque en
macOS, las opciones documentadas originalmente siguen vigentes
(Raycast Script Command, Atajos de macOS o `launchd`). Los detalles y
plantillas están en el historial del repo; el wrapper Swift cubre
el mismo caso de uso con menos fricción.

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
- Cuando se usa el wrapper Swift, el `.env` regenerado se escribe con
  permisos `0600` y vive dentro de `~/Library/Application Support/`,
  accesible solo al usuario actual.

Consulta `SECURITY.md` para el modelo de confianza completo.

## Arquitectura

Hexagonal / Clean Architecture en Go, con tres capas concéntricas:

```text
internal/
  domain/      entidades (Project, Session, RuntimeState, NavigationState,
               FileChange, BotButton, BotResponse) y puertos
               (WorkspaceFS, StateRepository, NavigationRepository,
               OpenCodeClient, BotHandler, OpenCodeServerManager)
  usecase/     navegador del workspace, navegación, handler de comandos
               (Handler) con sentinels de error (ErrNavigationNotFound,
               ErrUnauthorizedNavigation, ErrServerNotRunning, …)
  adapter/
    opencode/  cliente REST para opencode serve + manager de subprocess
    telegram/  long polling con telebot.v3, whitelist, callbacks,
               parser markdown → HTML
    storage/   repositorio SQLite (WAL, una conexión)
    workspace/ adaptador de filesystem
    config/    cargador de .env (godotenv)
cmd/remote-bot/  composition root
```

La capa de dominio no tiene dependencias externas; todo lo demás va detrás
de interfaces declaradas por el dominio. Esto permite que los tests
sustituyan SQLite por un store en memoria y OpenCode por un servidor
`httptest`.

Sobre el binario, en macOS, vive opcionalmente la app nativa
`Agentower.app` (SwiftUI + AppKit) que actúa como launcher con UI,
persistencia y supervisión de logs.

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

CI: cada push y PR ejecuta `go test -race` con `-coverprofile` sobre
Go 1.23 (Ubuntu) y `golangci-lint` sobre Ubuntu. Las dependencias se
mantienen al día vía Dependabot (`gomod`, `github-actions`, `swift`)
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
│   ├── adapter/                 OpenCode, Telegram, SQLite, filesystem
│   ├── config/                  cargador de .env
│   ├── domain/                  entidades y puertos
│   └── usecase/                 navegador del workspace, navegación, handler
├── macos/
│   └── Agentower/          wrapper Swift (status bar, settings, login item)
├── docs/
│   ├── DESIGN.md                arquitectura y decisiones
│   └── PRODUCT.md               alcance y hoja de ruta
├── .github/
│   ├── workflows/ci.yml         matriz de tests + golangci-lint
│   ├── dependabot.yml           actualizaciones semanales de dependencias
│   ├── ISSUE_TEMPLATE/          bug report y feature request
│   └── PULL_REQUEST_TEMPLATE.md checklist para contribuidores
├── .golangci.yml                configuración del linter
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
