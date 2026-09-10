# Contribuir

Gracias por el interés en Agentower.

## Flujo de trabajo

1. Crea una rama desde `main` con un nombre descriptivo:
   `feat/sessions-revert`, `fix/init-path-validation`, etc.
2. Antes de hacer push ejecuta siempre:
   ```bash
   go test -race ./...
   go vet ./...
   go build -o remote-bot ./cmd/remote-bot
   ```
3. Si tocas la capa de dominio (`internal/domain`), asegúrate de que
   sigue sin importar nada externo más allá de la librería estándar.
4. Abre un pull request describiendo el cambio y enlazando el comando
   o escenario que lo cubre.

## Estilo de código

- Go idiomático, `gofmt` sin cambios, `go vet` limpio.
- Logs vía `log/slog`. No introduzcas un segundo stack de logging (en
  concreto, no `log.Logger`).
- Sin comentarios obvios. Si el código necesita explicación, es
  candidato a refactor.
- Cero secretos en commits. El token del bot se lee del `.env` en
  tiempo de ejecución; no lo copies ni lo pegues en fixtures.

## Arquitectura en capas

El proyecto sigue hexagonal / clean architecture:

```text
internal/
  domain/      entidades y puertos (sin imports externos)
  usecase/     navegador del workspace, navegación, bot handler, session watcher
  adapter/
    agents/
      opencode/   cliente REST + manager del subproceso
      claude/     stdio JSON contra `claude --print --output-format stream-json`
      kiro/       stdio JSON contra `kiro chat --session ...`
      copilot/    cliente LSP JSON-RPC 2.0 contra `copilot --stdio`
      detector.go PATH + bundle probing
      registry.go  AgentRegistry + per-chat active pick
    telegram/  long polling, whitelist, callbacks
    storage/   repositorio SQLite (runtime_state, agent_state, directory_navigation, completed_session)
    workspace/ adaptador de filesystem
    config/      cargador de .env (AGENT_<KIND>_*)
cmd/remote-bot/ composition root
```

Las flechas de dependencia apuntan hacia adentro: el dominio no sabe
nada de Telegram ni de SQLite. Si necesitas algo externo en el dominio,
lo modelas como puerto (`internal/domain/ports.go`) y lo implementas en
un adapter.

## Añadir un comando nuevo de Telegram

Hay cuatro sitios que deben quedar en sincronía:

1. `internal/adapter/telegram/bot.go` → slice `commands` (registro del
   handler con whitelist).
2. `internal/adapter/telegram/bot.go` → método `registerCommands` (lo
   que Telegram muestra en el menú autocompletar).
3. `internal/usecase/bot_handler.go` → case en `HandleCommand` con la
   lógica.
4. `internal/usecase/` → test del flujo nuevo (usa un
   `WorkspaceBrowser` real y un `*recordingServer` o equivalente;
   `bot_handler_init_test.go` es un buen ejemplo).

Si el comando requiere subcomandos parseados a mano (`/sessions new`,
`/sessions <id>`), conviene centralizar el parsing en una función
pequeña y cubrir cada rama en el test.

## Añadir una variable de entorno nueva

1. Declara el campo en `internal/config/config.go` (`Config`).
2. Léela con `os.Getenv` en `Load()` con un valor por defecto seguro y
   validación (rangos numéricos, booleanos normalizados, etc.).
 3. Documenta la variable en:
   - la tabla "Variables opcionales" de `README.md`,
   - y, si aplica, la lista de "Variables de entorno sensibles" de
     `SECURITY.md`.
4. Pasa el valor al componente que lo necesite desde
   `cmd/remote-bot/main.go`.

## Añadir un endpoint nuevo a un agente

1. Añade el método al puerto `AgentAdapter` en
   `internal/domain/ports.go` (sólo si todavía no existe).
2. Implementa el método en el adapter concreto:
   `internal/adapter/agents/<kind>/` (opencode usa HTTP REST + manager;
   Claude/Kiro usan stdio JSON; Copilot usa LSP). Si el endpoint
   es un stream, usa un cliente HTTP sin timeout; si es una operación
   normal, usa el cliente con `Timeout`.
3. Marca la capacidad en el `scan<Agent>` del detector
   (`internal/adapter/agents/detector.go`) para que la UI de Telegram
   sepa qué botones renderizar.
4. Cubre con un test usando `httptest.NewServer` o un `fakebin` (mira
   `internal/adapter/agents/<kind>/client_test.go` como referencia).
5. Conéctalo en el caso del comando o flujo que corresponda en
   `internal/usecase/bot_handler.go`.

## Añadir un agente nuevo

1. Declara la constante `AgentKind` en
   `internal/domain/ports.go` y añádela a `AllAgentKinds()`.
2. Crea `internal/adapter/agents/<kind>/` con `client.go`,
   `manager.go` y un `testdata/fake<kind>.go` que hable el protocolo
   que el CLI exponga. Implementa `domain.AgentAdapter` (el compilador
   te obliga a través del `var _ domain.AgentAdapter = (*Adapter)(nil)`
   que ponemos en cada adapter).
3. Añade la capacidad por defecto en `scan<Kind>` dentro del
   `internal/adapter/agents/detector.go`.
4. Registra el adapter en `cmd/remote-bot/main.go` detrás de la guarda
   `hasDetected<Kind>(descriptors)` así un usuario sin el binario no ve
   un adapter roto.
5. Añade una entrada para el nuevo kind en `ConfigStore.agentKinds` /
   `displayName(for:)` en la app nativa para que la sección **Agentes
   de IA** del Settings la muestre.
6. Si el agente expone un endpoint HTTP en localhost, documéntalo en
   `SECURITY.md` para mantener la superficie de ataque al día.

## Tocar el modelo de seguridad

Si tu cambio afecta a la whitelist, a la validación del workspace o al
manejo del token:

1. Escribe primero el test que cubre el vector de ataque que cierras
   (o el que impides romper).
2. Cambia el código en `WorkspaceBrowser.resolve` o donde aplique.
3. Verifica que `bot_handler_init_test.go` y
   `workspace_browser_test.go` siguen pasando — son la red de seguridad
   de la invariante "stay inside root".
4. Documenta el cambio en `SECURITY.md`.

## Tests

- `go test -race ./...` debe pasar siempre. Hay componentes con estado
  compartido (manager de OpenCode, SQLite con `MaxOpenConns(1)`) y los
  races salen rápido con el detector.
- Cobertura mínima esperada por capa:
  - `internal/usecase/`: 40% o más (los flujos de seguridad viven
    aquí).
  - `internal/adapter/`: cobertura > 60% cuando el adapter tenga
    lógica no trivial.
- Para el adapter de Telegram, los tests unitarios llegan hasta donde
  llega `telebot.v3` mockeando el endpoint HTTP (`bot_test.go`).

## Releasing

- Etiqueta el commit en `main` con versionado semver:
  `git tag v0.x.y`.
- No publiques binarios firmados; los usuarios compilan con
  `go build -o remote-bot ./cmd/remote-bot`.
- Tras etiquetar, actualiza `docs/PRODUCT.md` (sección "Implemented" u
  "Open task list" según corresponda) y `README.md` si hay nuevos
  comandos o variables.