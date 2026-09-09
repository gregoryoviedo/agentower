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
  para hablar con `opencode serve`; en sesiones de Claude/Codex/Kiro
  el bot abre pipes stdin/stdout contra el binario del usuario; con
  Copilot habla JSON-RPC sobre stdio con el language server. El polling
  a Telegram siempre va contra `api.telegram.org` (o el override de
  `TELEGRAM_API_ROOT`).
- **Storage**: un único archivo SQLite con estado de runtime; nunca
  credenciales.

## Qué puede y qué no puede hacer el bot

**Puede**, mientras el chat esté en la whitelist:

- Navegar recursivamente por `WORKSPACE_ROOT` con un selector de
  carpetas.
- Elegir entre los agentes detectados (`/agent`) y activar o
  deshabilitar cada uno (`/agents`).
- Crear, listar y seleccionar sesiones del agente y proyecto activos.
- Enviar prompts de texto libre a la sesión activa.
- Pedir el diff (`/diff`, `/changes`) y revertir (`/undo`) sobre la
  sesión activa cuando el agente lo soporte.
- Arrancar, rearrancar y apagar el subproceso del agente activo mediante
  `/init`, `/projects → "Usar esta carpeta"` y `/agent`.

**No puede**:

- Salir de `WORKSPACE_ROOT`. Toda ruta que llega desde Telegram es
  relativa al workspace y se valida antes de cualquier efecto (ver
  siguiente sección).
- Ejecutar comandos de shell arbitrarios.
- Escribir archivos arbitrarios — solo lo que OpenCode decida escribir
  como consecuencia de un prompt.
- Recibir mensajes de multimedia (voz, imágenes, documentos).
- Atender a varios chats en paralelo: la whitelist es un único ID.

## Seguridad del workspace

La invariante "toda ruta que toque el bot debe quedar dentro de
`WORKSPACE_ROOT`" se aplica en **un solo lugar**:
`WorkspaceBrowser.resolve` (`internal/usecase/workspace_browser.go`).
Todo lo demás — `Enter`, `Select`, `Back`, `Home` y `/init <ruta>` —
pasa por ahí.

La validación rechaza:

- Rutas absolutas (incluidas las que vienen en `/init`).
- Componentes `..` que escaparían del workspace.
- Symlinks cuyo destino queda fuera de la raíz.
- Rutas que no existen o no son directorios.

Al listar, además se omiten:

- Directorios ocultos (los que empiezan por `.`).
- Symlinks cuyo destino está fuera del workspace, incluso si el enlace
  sí está dentro.

Los IDs de navegación son aleatorios y efímeros (TTL 15 min); cada
callback se valida contra un registro por chat, así un chat no puede
manipular el navegador de otro aunque conozca un ID válido.

## Almacenamiento

- El token del bot y el `ALLOWED_CHAT_ID` viven **solo** en `.env` (o en
  variables de shell). No se persisten en SQLite ni en logs.
- SQLite guarda: `runtime_state` (workspace, proyecto, sesión activa) y
  `directory_navigation` (registros de navegación efímeros).
- SQLite **no** guarda: contenidos de prompts, respuestas de OpenCode,
  historial de chat, ni credenciales.
- El archivo `state.db` se crea con permisos `0600` (sólo el usuario
  que ejecuta el bot).

## Variables de entorno sensibles

| Variable             | Por qué es sensible                                       |
|----------------------|-----------------------------------------------------------|
| `TELEGRAM_BOT_TOKEN` | Acceso completo al bot; quien lo tenga puede suplantarte. |
| `ALLOWED_CHAT_ID`    | Si se cambia, el bot deja de responderte (o responde a otro). |

Recomendaciones:

- `.env` en `.gitignore`. La app macOS escribe el suyo en
  `~/Library/Application Support/Agentower/.env` con permisos `0600`;
  no commitees nunca archivos con credenciales reales.
- Si usás el binario CLI directamente, mantené tu `.env` con
  `chmod 600`.
- No lo pegues en issues, screenshots ni logs.

## Distribución

- Binario único compilado por el usuario desde el código fuente. No hay
  binario firmado ni `Developer ID` de Apple; nada pasa por Gatekeeper.
- Sin telemetría, sin auto-actualización, sin llamadas a servicios de
  terceros.

## Lista de comprobación antes de desplegar

1. `WORKSPACE_ROOT` apunta a un directorio real, sin symlinks que
   apunten fuera.
2. `.env` con `chmod 600` y commit ignorado.
3. `ALLOWED_CHAT_ID` coincide con tu chat real (verifica con
   `@userinfobot`).
4. Has compilado con `go build -o remote-bot ./cmd/remote-bot` desde
   una copia limpia del repo.
5. El puerto del agente activo (`AGENT_OPENCODE_PORT`, `AGENT_CLAUDE_PORT`,
   …) está libre y bindea solo a `127.0.0.1`.

Si tocas el código que aplica la invariante de workspace, añade un test
en `internal/usecase/workspace_browser_test.go` o en
`internal/usecase/bot_handler_init_test.go` que cubra el nuevo vector
de escape antes de pedir review.