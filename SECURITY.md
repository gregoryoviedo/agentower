# Seguridad

Este documento describe el modelo de confianza de Agentower y los
límites que el bot promete cumplir. Si vas a modificar el código que toca
la whitelist, la validación del workspace o el manejo del token, léelo
antes — todo cambio en esa superficie requiere un test focalizado.

## Modelo de confianza

El bot está pensado para un único usuario. Solo responde al chat
configurado en `ALLOWED_CHAT_ID`; cualquier otro mensaje o callback se
descarta silenciosamente, sin error ni eco, en el middleware de
`telebot.v3`.

- **Inbound**: únicamente la API de Telegram mediante long polling. El
  binario no abre ningún socket.
- **Outbound**: el agente activo. Por defecto `127.0.0.1:4096`
  para hablar con `opencode serve`; en sesiones de Claude/Kiro el bot
  abre pipes stdin/stdout (Claude) o ACP sobre stdio (Kiro); con Copilot
  habla JSON-RPC sobre stdio con el language server o el CLI ACP; Codex y
  Antigravity son CLIs headless que el bot lanza por stdin/stdout. El
  polling a Telegram siempre va contra `api.telegram.org` (o el override
  de `TELEGRAM_API_ROOT`).
- **Control local**: los wrappers (macOS/Windows) consultan un servidor
  HTTP efímero en `127.0.0.1` (`internal/control`, dirección en
  `control.json`) para el aviso de inactividad. No escucha en interfaces
  externas.
- **Storage**: un único archivo SQLite con estado de runtime; nunca
  credenciales.

## Qué puede y qué no puede hacer el bot

**Puede**, mientras el chat esté en la whitelist:

- Avisarte cuando una tarea termina o cuando el agente se queda esperando
  una respuesta.
- Continuar la última tarea completada (`/continue`) o la sesión que
  detecta viva en tu máquina (`/resume`).
- Enviar prompts de texto libre a la sesión seguida.
- Pedir el diff de una sesión completada (botón **📝 Ver cambios**).
- Responder las preguntas del agente con botones o texto.

**No puede**:

- Salir de `WORKSPACE_ROOT`. Ya no recibe rutas desde Telegram; el root
  solo acota el proyecto que deriva de una sesión.
- Lanzar, rearrancar ni apagar agentes (`/init`, `/projects`, `/agent`
  ya no existen).
- Crear, listar ni seleccionar sesiones arbitrarias desde el chat.
- Revertir cambios (`/undo` ya no existe).
- Ejecutar comandos de shell arbitrarios.
- Recibir mensajes de multimedia (voz, imágenes, documentos).
- Atender a varios chats en paralelo: la whitelist es un único ID.

## Seguridad del workspace

La invariante "toda ruta que toque el bot debe quedar dentro de
`WORKSPACE_ROOT`" se aplica en **un solo lugar**:
`WorkspaceBrowser.resolve` (`internal/usecase/workspace_browser.go`).

El handler ya no acepta rutas desde Telegram: no hay navegador de
carpetas ni `/init <ruta>`. La única ruta que el bot maneja es la que
viene en una `ActiveSession`/`CompletedSession` (por ejemplo el
`directory` de una sesión de opencode), y se acota con
`relativeUnderWorkspace` antes de guardarla.

La validación rechaza:

- Rutas absolutas.
- Componentes `..` que escaparían del workspace.
- Symlinks cuyo destino queda fuera de la raíz.
- Rutas que no existen o no son directorios.

Al listar, además se omiten:

- Directorios ocultos (los que empiezan por `.`).
- Symlinks cuyo destino está fuera del workspace, incluso si el enlace
  sí está dentro.

## Almacenamiento

- El token del bot y el `ALLOWED_CHAT_ID` viven **solo** en `.env` (o en
  variables de shell). No se persisten en SQLite ni en logs.
- SQLite guarda: `runtime_state` (workspace, proyecto, sesión seguida),
  `agent_state` y `completed_session`. La tabla `directory_navigation`
  es un remanente del antiguo navegador de carpetas y ya no la usa el
  handler.
- SQLite **no** guarda: contenidos de prompts, respuestas de OpenCode,
  historial de chat, ni credenciales.
- El archivo `state.db` se crea con permisos `0600` (sólo el usuario
  que ejecuta el bot). En Windows, el estado y la config del wrapper
  viven bajo `%APPDATA%\Agentower\` (perfil del usuario) y los logs bajo
  `%LOCALAPPDATA%\Agentower\logs\`.

## Variables de entorno sensibles

| Variable             | Por qué es sensible                                       |
|----------------------|-----------------------------------------------------------|
| `TELEGRAM_BOT_TOKEN` | Acceso completo al bot; quien lo tenga puede suplantarte. |
| `ALLOWED_CHAT_ID`    | Si se cambia, el bot deja de responderte (o responde a otro). |

Recomendaciones:

- `.env` en `.gitignore`. El wrapper de macOS escribe el suyo en
  `~/Library/Application Support/Agentower/.env` con permisos `0600`, y
  el de Windows en `%APPDATA%\Agentower\.env` (perfil del usuario); no
  commitees nunca archivos con credenciales reales.
- Si usás el binario CLI directamente, mantené tu `.env` con `chmod 600`
  (Unix) o dentro de tu perfil de usuario (Windows).
- No lo pegues en issues, screenshots ni logs.

## Distribución

- Binario único compilado por el usuario desde el código fuente. No hay
  binario firmado ni `Developer ID` de Apple, así que el `.app` requiere
  quitar la cuarentena; el `.exe` de Windows no está firmado y puede
  disparar SmartScreen ("Más información → Ejecutar de todas formas").
- Sin telemetría, sin auto-actualización, sin llamadas a servicios de
  terceros.

## Lista de comprobación antes de desplegar

1. `WORKSPACE_ROOT` apunta a un directorio real, sin symlinks que
   apunten fuera.
2. `.env` con `chmod 600` (Unix) o dentro de tu perfil (Windows) y commit
   ignorado.
3. `ALLOWED_CHAT_ID` coincide con tu chat real (verifica con
   `@userinfobot`).
4. Has compilado con `go build -o remote-bot ./cmd/remote-bot` desde una
   copia limpia del repo (o con el wrapper: `make app` / `.\windows\build.ps1`).
5. El puerto del agente activo (`AGENT_OPENCODE_PORT`, `AGENT_CLAUDE_PORT`,
   …) está libre y bindea solo a `127.0.0.1`.

Si tocas el código que aplica la invariante de workspace, añade un test
en `internal/usecase/workspace_browser_test.go` o en
`internal/usecase/bot_handler_init_test.go` que cubra el nuevo vector
de escape antes de pedir review.