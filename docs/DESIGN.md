# Design

This document explains how Agentower is built and why. It complements
`README.md` (user-facing) and `PRODUCT.md` (scope) by recording the
architectural decisions and trade-offs that shaped the code.

## Goals and non-goals

### Goals

- Single Go binary as the system of record. The native wrappers (Swift on
  macOS, C#/.NET on Windows) are optional launcher UIs; the bot can run
  headless from a terminal.
- Strict layering: pure domain, swappable adapters, easy testing.
- Multi-agent: one Telegram chat at a time can drive any of the bundled
  adapters (opencode HTTP + SQLite history, Claude/Kiro stdio JSON, GitHub
  Copilot LSP, Codex `exec --json`, Antigravity `agy --output-format stream-json`).
  Each agent has its own slot in the AgentServerManager.
- Strict security by default: workspace-bounded, single-user, no public
  ports.
- Small surface area: only Telegram long polling plus loopback to the
  chosen agent's daemon (or stdin/stdout for stdio-based agents).

### Non-goals

- Multi-user access.
- Multi-chat concurrent flows.
- Multimedia inputs (voice, documents, images).
- Hosting agent subprocesses from inside the bot for non-loopback
  transports (they are always reachable on localhost or via the
  user's own CLI).
- Universal macOS binary (arm64-only for now) and, on Windows, a
  non-x64 build (win-x64 only).
- i18n of user-facing strings.

## Layered architecture

The Go backend follows hexagonal / clean architecture. Three concentric
rings, dependency arrows pointing inward only.

```text
        ┌───────────────────────────────┐
        │          adapters             │
        │  telegram · agents/* · sqlite │
        │  workspace · config (.env)    │
        └───────────────┬───────────────┘
                        │ implements
        ────────────────▼───────────────┐
        │           usecase             │
        │  browser ·                    │
        │  handler · session_watcher     │
        └───────────────┬───────────────┘
                        │ uses
        ┌───────────────▼───────────────┐
        │           domain              │
        │  entities + ports             │
        │  (zero external deps)         │
        └───────────────────────────────┘
```

### `internal/domain`

Pure data and interfaces.

- Entities: `AgentKind`, `AgentCapabilities`, `AgentDescriptor`,
  `Project`, `Session`, `RuntimeState`, `AgentState`, `NavigationState`,
  `DirectoryEntry`, `HealthStatus`, `FileChange`, `Message`,
  `CompletedSession`, `Snapshot`, `ActiveSession`, `PendingQuestion`,
  `QuestionPrompt`, `QuestionOption`, `BotButton`, `BotResponse`.
- Ports: `WorkspaceFS`, `StateRepository`, `NavigationRepository`,
  `AgentAdapter`, `AgentRegistry`, `AgentServerManager`, `BotHandler`,
  `SnapshotPublisher`, `CompletionPublisher`, `SessionEventLog`,
  `SessionLocator`, `QuestionAdapter`, `QuestionBroker`, `ChatNotifier`.
  The legacy `OpenCodeClient` and `OpenCodeServerManager` remain as
  deprecated aliases.
- Errors: `ErrOutsideWorkspace`, `ErrNotDirectory`, `ErrNavigationNotFound`,
  `ErrUnauthorizedNavigation`, `ErrServerNotRunning`, `ErrSessionRequired`,
  `ErrProjectRequired`, `ErrWorkspaceNotConfigured`, `ErrNoActiveAgent`,
  `ErrAgentUnavailable`, `ErrAgentCapabilitiesLimited`,
  `ErrUnknownAgentKind`, `ErrMigrationNotNeeded`.

The domain package imports nothing from the rest of the project and nothing
external besides the standard library. This is what keeps every test fast
and every adapter swappable.

### `internal/usecase`

Use cases — the rules of the product.

- `WorkspaceBrowser`: walks and validates the workspace root and resolves
  relative paths under it. The bot uses it to bound the directories it
  will accept when it derives a project path from a session.
- `Handler`: companion router for the two commands that survived the
  remote-control era, `/status` and `/continue`, plus `/resume`, free
  text and the notification/question callbacks. Resolves the followed
  agent via the `AgentRegistry` and dispatches via the corresponding
  `AgentAdapter`. Pure dispatch — no knowledge of Telegram specifics;
  only `BotResponse` values come back. Returns `domain.Err*` sentinels
  for every recoverable failure so the adapter layer can map them to
  user-facing replies.
- `SessionWatcher`: polls the active agent and records completion
  snapshots so the wrappers can fire their idle-notification flow. It
  also polls `QuestionAdapter.ListQuestions`; a pending question
  suppresses the completion and is published on the `QuestionBroker` so
  the idle question flow can forward it to Telegram. Each agent has its
  own capabilities; capabilities-limited adapters return
  `ErrAgentCapabilitiesLimited` for actions the watcher cannot exercise.

### `internal/adapter`

Adapters that implement the ports and depend on real-world libraries.

- `adapter/agents/opencode`: HTTP client for `http://127.0.0.1:<port>`,
  plus a subprocess manager that starts, probes, and kills
  `opencode serve` with a graceful SIGTERM → SIGKILL shutdown. JSON
  responses are capped at 32 MB via `io.LimitReader`. Reads
  (`HistoryLocator`, `ListMessages`) prefer the SQLite store at
  `~/.local/share/opencode/opencode.db` (`session`, `message`, `part`)
  and fall back to HTTP, so a locally-launched TUI — which binds no HTTP
  port (`--port` defaults to 0) — is still followed.
- `adapter/agents/claude`: stdio JSON transport over
  `claude --print --output-format stream-json --verbose --session-id
  <uuid> --cwd <dir>`. Sessions are spawned lazily on first prompt;
  the manager reaps the subprocess once the trailing `result` event
  lands. The session locator scans **every** `~/.claude/projects/*/`
  (global mode) and resolves the real cwd from the JSONL, so a `claude`
  launched in any folder is followed.
- `adapter/agents/kiro`: ACP client over
  `kiro-cli acp --agent-engine v3 --auth-method cli` (newline framing)
  with `TrustAll: true` (permission requests auto-approved). Sessions can
  be created or resumed (`session/load`, falling back to `session/resume`
  for agents that advertise it); history is read from the
  JSONL store at `~/.kiro/sessions/<ws>/<id>/messages.jsonl`.
- `adapter/agents/copilot`: real LSP client (Content-Length framed
  JSON-RPC 2.0). Spawns the modern `copilot` CLI when present, or a
  VS Code extension bundle via `node`. SendPrompt writes the prompt as
  a synthetic text document and requests an inline completion.
- `adapter/agents/codex`: headless transport over
  `codex exec --json -` (new thread) and `codex exec resume <id> --json -`
  (continue). The prompt goes through stdin, the JSONL event stream
  (`thread.started` / `item.completed` / `turn.completed`) is parsed for
  the reply and the real thread id. History is read from
  `~/.codex/sessions/**/rollout-*.jsonl` (override `CODEX_SESSIONS_DIR`).
- `adapter/agents/antigravity`: headless transport over
  `agy -p <prompt> --output-format stream-json` (new thread) plus
  `--conversation <id>` (continue). Parses `init` / `step_update` /
  `result` events. History is read from the readable transcript at
  `~/.gemini/antigravity-cli/brain/<id>/.system_generated/logs/transcript_full.jsonl`
  and the IDE's `~/.gemini/antigravity/...`; `history.jsonl` drives the
  session locator.
- `adapter/agents/detector`: PATH + app-bundle/installer probing for every
  supported agent; emits the boot-time `AgentDescriptor` list the registry
  consumes. `bundles.go` implements the per-OS lookup (macOS app bundles,
  Windows `%APPDATA%\npm` / `%LOCALAPPDATA%\Programs` / `~/.opencode`).
- `adapter/agents/registry`: per-chat active pick, lazy adapter
  construction, and the `Available` filter the Telegram picker uses.
- `adapter/telegram`: telebot.v3 long polling, whitelist middleware,
  commands, `OnText`, and a single inline-button endpoint that decodes
  `kind|id|payload` callback data. Includes a hand-rolled Markdown →
  Telegram HTML converter with placeholder substitution to keep escaping
  idempotent.
- `adapter/storage/sqlite`: SQLite repository with WAL mode, single
  connection to avoid locking. Holds `runtime_state`
  (now with an `agent_kind` column), `directory_navigation`,
  `completed_session`, and `agent_state`.
- `adapter/workspace`: thin wrapper around the standard `os` filesystem
  helpers, exposed as a `WorkspaceFS` port to keep tests hermetic.
- `config`: `.env` loader (godotenv.Overload), parent-directory walk,
  `ENV_FILE` override. Reads the mandatory Telegram/workspace vars and
  any per-agent `AGENT_<KIND>_*` overrides passed by the wrapper.

### `cmd/remote-bot/main.go`

The composition root: parse config, initialize adapters, wire up use
cases, start the bot, react to `SIGINT` / `SIGTERM`. Platform-specific
process handling lives behind build tags: `internal/adapter/agents/opencode/process_unix.go`
(`Setpgid` + `SIGTERM`) and `process_windows.go` (`taskkill /T /F`), and
`cmd/remote-bot/path_unix.go` / `path_windows.go` for the PATH
augmentation.

### `macos/Agentower`

Optional Swift menu-bar wrapper around the Go binary. It is a launcher and
control-socket client, not a peer service. Files of note:

- `BotController.swift`: spawns the bundled `remote-bot`, sets
  `ENV_FILE`, `AGENTOWER_STATE_PATH`, `GIN_MODE`, and forwards
  `TELEGRAM_API_ROOT` / `TELEGRAM_PROXY_URL` from the user-facing
  Settings into the child's environment. Captures stdout/stderr into
  `~/Library/Logs/Agentower/bot.log`.
- `StatusBarController.swift`: `NSStatusItem` + `NSPopover`. Left click
  toggles popover, right click opens a context menu.
- `SettingsView.swift`: SwiftUI form that writes both `UserDefaults` and
  a `0600` `.env` to `~/Library/Application Support/Agentower/`.
- `LoginItemManager.swift`: wrapper around `SMAppService.mainApp` for
  the "auto-start at login" toggle. The wrapper recognises
  `/Applications/` and `~/Applications/` as valid install locations.
- `IdleNotifier.swift`: polls the bot's control socket (`/state`) using
  `CGEventSource` for idle time, then posts `/notify` or
  `/question-notify` and raises `UNUserNotification`s.
- `ConfigStore.swift`, `AppState.swift`: persistence glue. State updates
  flow through a single `onStateChange` closure so the status icon, the
  uptime label, and the Settings UI see the same `BotStatus`.

### `windows/Agentower`

Feature-parity WinForms (.NET 8) tray wrapper. Same responsibilities as
the macOS app, with Windows idioms:

- `TrayApplicationContext.cs`: `NotifyIcon` in the notification area,
  context menu and popover, and the one-shot auto-start of the bot on
  launch (deferred a UI tick so the WinForms `SynchronizationContext`
  exists).
- `BotController.cs`: extracts the embedded `remote-bot.exe` to
  `%LOCALAPPDATA%\Agentower\` and spawns it with the same env vars;
  logs to `%LOCALAPPDATA%\Agentower\logs\bot.log`; kills the process
  tree via `Kill(entireProcessTree: true)`.
- `SettingsForm.cs`: the four tabs (Telegram, Agentes, Avanzado, Inicio),
  matching the macOS fields; `AgentDetector.cs` supplies the detection.
- `IdleNotifier.cs`: `GetLastInputInfo` for idle time plus the same
  control-socket polling/`notify`/`question-notify` flow, surfacing
  tray balloons.
- `AutostartManager.cs`: per-user `HKCU\...\Run` entry (no elevation).
- `SelfTest.cs`: `--selftest` builds every form and runs detection
  headlessly, used by CI.

### Shared wrapper contract

Both wrappers share the same seam with the bot and must stay
feature-equivalent:

- Write the `.env` schema (`WORKSPACE_ROOT`, `TELEGRAM_BOT_TOKEN`,
  `ALLOWED_CHAT_ID`, `AGENT_<KIND>_*`, optional overrides).
- Set `ENV_FILE`, `AGENTOWER_STATE_PATH`, `GIN_MODE` and forward
  `TELEGRAM_API_ROOT` / `TELEGRAM_PROXY_URL`.
- Spawn `remote-bot`, stream stdout/stderr to a log file, and stop it
  as a process tree.
- Read `control.json` and poll `/state`, `/notify`, `/question-notify`.

## Trust boundaries

```text
┌─────────────┐  long polling  ─────────────┐  HTTP / JSON-RPC / LSP  ┌────────────┐
│  Telegram   │ ────────────►│  remote-bot │ ─────────────────────► │  agent     │
│   user      │ ◄──────────── │   (Go)      │ ◄───────────────────── │  (varies)  │
└─────────────┘   callbacks    └──────┬──────┘                         └────────────┘
                                      │
                                      ▼
                                 ┌──────────┐
                                 │  SQLite  │
                                 └──────────┘

  (Optional wrappers)

┌───────────────────────────────┐  spawn + .env   ┌────────────────────┐
│ Agentower.app (Swift, macOS)  │ ───────────────►│ remote-bot binary  │
│ Agentower.exe (C#, Windows)   │ .env + env vars │  the same Go code  │
└───────────────┬───────────────┘                 └─────────┬──────────┘
                │  GET /state · POST /notify · /question-notify
                └────────── 127.0.0.1 control socket ◄───────┘
```

- **Inbound (Go bot)**: only `api.telegram.org`. No listening sockets
  apart from the loopback control socket used by the wrappers.
- **Outbound (Go bot)**: loopback only — `127.0.0.1:4096` for opencode
  writes, plus on-disk reads of opencode's SQLite store
  (`~/.local/share/opencode/opencode.db`) and Claude's
  `~/.claude/projects/*/` JSONL. stdin/stdout pipes for Claude / Kiro /
  Codex / Antigravity, or a loopback JSON-RPC stream for the GitHub
  Copilot LSP.
- **Storage**: a single SQLite file with the runtime state.
- **Storage (macOS wrapper)**: `UserDefaults` for Settings, a `0600`
  `.env` for the bot, and the bot's own SQLite file at
  `~/Library/Application Support/Agentower/state.db`.
- **Storage (Windows wrapper)**: `settings.json` + `.env` + `state.db`
  under `%APPDATA%\Agentower\`, logs under `%LOCALAPPDATA%\Agentower\logs\`.
- **Wrapper ↔ bot IPC**: one-way over the loopback control socket — the
  wrapper polls `/state` and posts `/notify` / `/question-notify`. The
  wrapper never parses the bot's stdout; lifecycle is supervised via the
  `Process` API (`terminationHandler` on macOS, `Exited` on Windows).

## Why different architectures in one project

Go uses hexagonal / clean architecture; the Swift wrapper uses MVVM and
the C# wrapper uses the WinForms event-driven model. That is intentional,
not a leak, and the three do not need to be "unified".

Each runtime uses the idioms of its ecosystem:

- **Go** has explicit, lightweight interfaces and a package model that
  maps 1:1 to ports & adapters. A backend with several external
  actors (Telegram, OpenCode, SQLite, the filesystem, a subprocess)
  naturally expresses itself as a stable `internal/domain` core
  surrounded by swappable adapters. The "domain package imports only
  stdlib" rule is enforced by tooling (`goimports`, `go vet`) and the
  compiler itself.
- **Swift** (with SwiftUI + Combine + `@Published` / `ObservableObject`)
  *is* MVVM by construction: the view binds to an observable view-model,
  the view-model exposes state, and a model layer (here `BotController` +
  `ConfigStore`) does the work. Forcing hexagonal onto a SwiftUI app
  would fight the framework, and forcing MVVM onto Go would fight
  `net/http`, `database/sql`, and the lack of a notification bus.
- **C# / .NET WinForms** is event-driven by construction: controls raise
  events handled in a `TrayApplicationContext`, state lives in plain
  classes (`BotController`, `ConfigStore`), and background work is
  marshalled back to the UI thread through the WinForms
  `SynchronizationContext`. That is the native Windows idiom; a port/mesh
  framework would add ceremony without buying anything.

### What the three architectures share

Although the vocabulary differs, the underlying philosophy is the same:

| Hexagonal (Go)        | MVVM (Swift)             | WinForms (C#)            | Same principle                |
|-----------------------|--------------------------|--------------------------|-------------------------------|
| Domain at the centre  | Model at the centre      | State classes at the centre | Stable core, slow to change |
| Ports = interfaces    | ViewModel = `ObservableObject` | Event handlers + services | Observable / replaceable contract |
| Adapters at the edge  | View + services at the edge | Forms + services at the edge | Replaceable boundaries, easy to mock |
| Dependency rule inward | UI does not touch the model | UI does not touch the model | Dependencies point at the core  |

### The seam between the architectures

The three runtimes share **no code, no types, no FFI, no gRPC, no
protobuf**. They talk through a process boundary whose contract is
deliberately minimal and stable:

1. **The `.env` schema** — the set of variable names and value formats
   documented in `README.md`. This is the only shared schema.
2. **The `Process` API** — `executableURL`, `environment`,
   `terminationHandler` (macOS) / `Exited` (Windows), and the captured
   stdout/stderr piped to `bot.log`.
3. **The control socket** — `control.json` + `/state`, `/notify`,
   `/question-notify` over `127.0.0.1`. The wrappers only poll it; they
   never parse the bot's stdout.
4. **An implicit log format** — the wrapper does not parse the bot's
   stdout; it only streams it to a file. If the bot's log format ever
   needs to change, the wrappers do not notice.

### Why this is the right answer (and not "one architecture to rule them all")

A unified architecture across languages only makes sense when the
domains are shared in code: a single proto schema, a generated client,
FFI bindings, or a service mesh. None of those exist here, and adding
them would multiply the project's complexity for no benefit — the Go
binary and the two wrappers are genuinely independent programs with one
small, well-defined contract between them.

The wrappers are intentionally **dumb launchers**: they read Settings,
write a `.env`, spawn the binary, supervise its lifecycle, and poll the
control socket. Their own "domain" is trivial (toggle state + last
configuration + idle detection); the real business domain lives 100%
inside the Go binary. Duplicating that domain in Swift or C# would
create a second source of truth that would silently drift from the Go
implementation.

If a future contributor proposes "let's unify the architectures", the
question to ask is: *which new shared piece of code would force the
unification, and what does it actually buy us?* If the answer is "no
new shared code, just consistency for its own sake", the answer here
is to keep the architectures separate.

## Workspace safety

The whole product hinges on one invariant: every path the bot touches must
resolve to a directory strictly inside `WORKSPACE_ROOT`. This is enforced in
one place — `WorkspaceBrowser.resolve` — and exercised by tests for:

- `..` traversal.
- Absolute paths outside the root.
- Symlinks whose target escapes the root.
- Hidden directories (skipped on listing).
- Symlinks inside the root that themselves point outside (skipped on
  listing).

The handler never receives a path from Telegram anymore. The workspace
root is only used to bound the directory derived from a session when the
bot records a completion.

## Concurrency model

- The bot is single-threaded for Telegram handlers: telebot.v3 runs them
  in goroutines, but every operation that touches shared state goes
  through SQLite, which we serialize with a single connection
  (`db.SetMaxOpenConns(1)`).
- The CLI process shuts down on `SIGINT` / `SIGTERM` via
  `signal.NotifyContext`; the bot stops its long poller and the SQLite
  connection is closed.
- The `opencode serve` subprocess is started in its own process group on
  Unix (`Setpgid: true`) so it can be signalled with a single `SIGTERM`;
  if it does not exit within `shutdownGrace` (5 s) the manager escalates
  to `SIGKILL`. On Windows there is no process-group signal, so
  `process_windows.go` shells out to `taskkill /T /F` to kill the tree,
  with `Process.Kill()` as a fallback. The wrappers apply the same
  5-second window (`Process.terminate()` on macOS,
  `Kill(entireProcessTree: true)` on Windows).
- The macOS wrapper observes the child via `Process.terminationHandler`
  on the main queue; the Windows wrapper via the `Exited` event, posting
  back to the UI `SynchronizationContext`. State updates fan out through
  a single change callback so the icon, uptime label and Settings agree.

## Error handling

The domain layer exposes sentinel errors (`errors.Is`-friendly) so that
adapters and the bot handler don't need to inspect string content:

| Sentinel                       | Meaning                                       |
|--------------------------------|-----------------------------------------------|
| `ErrOutsideWorkspace`          | Path resolved outside `WORKSPACE_ROOT`.       |
| `ErrNotDirectory`              | A navigation target is not a directory.       |
| `ErrNavigationNotFound`        | Navigation record expired or never existed.   |
| `ErrUnauthorizedNavigation`    | A different chat tried to use the same record.|
| `ErrServerNotRunning`          | `opencode serve` is not up.                   |
| `ErrSessionRequired`           | No active session for the requested action.   |
| `ErrProjectRequired`           | No active project for the requested action.   |
| `ErrWorkspaceNotConfigured`    | No workspace and no project to fall back on.  |

The bot handler maps these into user-friendly Spanish replies. Anything
not matching a sentinel surfaces as a generic error message; if you want
to handle a new category of failure programmatically, add a sentinel and
handle it.

## Why Go

- Single static binary, trivial to distribute.
- Strong standard library for HTTP, JSON parsing, and context
  cancellation.
- `modernc.org/sqlite` removes the CGO toolchain dependency so the
  binary remains a single artifact with no system SQLite requirement.
- `gopkg.in/telebot.v3` is a small, focused Telegram library with long
  polling and inline keyboards.

## Why native wrappers (and not pure CLI)

A wrapper was added on top of the Go binary for three reasons:

1. **Discoverability.** A macOS user wants the bot in the menu bar and a
   Windows user wants it in the notification area, with a "running /
   stopped" status, auto-start at login, and one-click access to the log.
   The wrappers do exactly that without bespoke shell glue.
2. **Configuration as a form.** `WORKSPACE_ROOT`, the bot token, the
   chat ID, each agent's bin/port/args, the login-item toggle and the
   optional proxy/API-root settings are all first-class fields in a
   SwiftUI form (macOS, persisted to `UserDefaults` + a `0600` `.env`)
   or a WinForms form (Windows, persisted to `settings.json` + `.env`).
3. **Zero extra attack surface.** The wrappers do not speak to any agent
   or to Telegram itself. They only spawn the existing Go binary, which
   already enforces the trust model in `SECURITY.md`.

The wrappers are optional. Users who prefer a pure CLI workflow can keep
running `remote-bot` directly from a terminal, Raycast, `launchd`, the
Windows Task Scheduler, or the Startup folder — that workflow is still
documented in the repo history.

## What we keep in mind when adding code

- Anything that touches the trust model (whitelist, workspace validation,
  secret handling) gets tests first.
- New Telegram commands are added in exactly three places: the
  `commands` slice in the adapter, the dispatcher in the bot handler,
  and a unit test. The bot's command menu (`SetCommands`) is registered
  in `registerCommands()` so that Telegram's autocomplete stays in sync.
- Adapters depend inward only; the domain package must remain
  import-free.
- Test coverage on the workspace browser and navigation service is
  non-negotiable. They are the security perimeter.
- New failure modes that the wrappers or another adapter might need to
  handle get a sentinel in `domain/errors.go` first; string-matching
  error checks are a smell.
- When you add or change a settings field, an agent, or a notification
  flow, update **both** wrappers (`macos/Agentower` and
  `windows/Agentower`) so they stay feature-equivalent.
- CI runs `go test -race -coverprofile` on Go 1.23 (Ubuntu) plus
  `golangci-lint` (config in `.golangci.yml`), and a Windows job that
  cross-compiles the bot, builds `Agentower.exe`, and runs its
  `--selftest`. Dependabot opens weekly PRs grouped by ecosystem
  (`gomod`, `github-actions`, `swift`) so dependency churn never sneaks
  in unreviewed.
