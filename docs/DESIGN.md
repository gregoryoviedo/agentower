# Design

This document explains how Agentower is built and why. It complements
`README.md` (user-facing) and `PRODUCT.md` (scope) by recording the
architectural decisions and trade-offs that shaped the code.

## Goals and non-goals

### Goals

- Single Go binary as the system of record. The Swift macOS wrapper is an
  optional launcher UI; the bot can run headless from a terminal.
- Strict layering: pure domain, swappable adapters, easy testing.
- Multi-agent: one Telegram chat at a time can drive any of the bundled
  adapters (opencode HTTP, Claude/Kiro stdio JSON, GitHub Copilot
  LSP). Each agent has its own slot in the AgentServerManager.
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
- Universal macOS binary (arm64-only for now).
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
        │  browser · navigation ·       │
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
  `CompletedSession`, `Snapshot`, `BotButton`, `BotResponse`.
- Ports: `WorkspaceFS`, `StateRepository`, `NavigationRepository`,
  `AgentAdapter`, `AgentRegistry`, `AgentServerManager`, `BotHandler`,
  `SnapshotPublisher`, `CompletionPublisher`, `SessionEventLog`,
  `ChatNotifier`. The legacy `OpenCodeClient` and `OpenCodeServerManager`
  remain as deprecated aliases.
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

- `WorkspaceBrowser`: walks the workspace, resolves symlinks, enforces the
  "must stay inside the root" invariant.
- `NavigationService`: short-lived per-chat navigation records with
  expiration; only the owning chat can drive the buttons.
- `Handler`: command router. Resolves the active agent via the
  `AgentRegistry` and dispatches via the corresponding `AgentAdapter`.
  After `/projects → Usar esta carpeta` the handler shows the agent
  picker instead of starting the agent immediately so the user can
  switch agents at the same time. `/agent` shows the picker again, and
  `/agents` lists every known agent with an enable/disable toggle plus
  `/agents migrate` for legacy session migration. Pure dispatch —
  no knowledge of Telegram specifics; only `BotResponse` values come
  back. Returns `domain.Err*` sentinels for every recoverable failure
  so the adapter layer can map them to user-facing replies.
- `SessionWatcher`: polls the active agent and records completion
  snapshots so the macOS wrapper can fire its idle-notification flow.
  Each agent has its own capabilities; capabilities-limited adapters
  return `ErrAgentCapabilitiesLimited` for actions the watcher cannot
  exercise.

### `internal/adapter`

Adapters that implement the ports and depend on real-world libraries.

- `adapter/agents/opencode`: HTTP client for `http://127.0.0.1:<port>`,
  plus a subprocess manager that starts, probes, and kills
  `opencode serve` with a graceful SIGTERM → SIGKILL shutdown. JSON
  responses are capped at 32 MB via `io.LimitReader`.
- `adapter/agents/claude`: stdio JSON transport over
  `claude --print --output-format stream-json --verbose --session-id
  <uuid> --cwd <dir>`. Sessions are spawned lazily on first prompt;
  the manager reaps the subprocess once the trailing `result` event
  lands.
- `adapter/agents/kiro`: minimal adapter — only SendPrompt +
  Health; everything else returns `ErrAgentCapabilitiesLimited` because
  Kiro's CLI surface is still poorly documented.
- `adapter/agents/copilot`: real LSP client (Content-Length framed
  JSON-RPC 2.0). Spawns the modern `copilot` CLI when present, or a
  VS Code extension bundle via `node`. SendPrompt writes the prompt as
  a synthetic text document and requests an inline completion.
- `adapter/agents/detector`: PATH + bundle probing for every supported
  agent; emits the boot-time `AgentDescriptor` list the registry
  consumes.
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
cases, start the bot, react to `SIGINT` / `SIGTERM`.

### `macos/Agentower`

Optional Swift menu-bar wrapper around the Go binary. It is a launcher,
not a peer service: the only IPC is `Process` spawning plus the `.env`
file. Files of note:

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
- `ConfigStore.swift`, `AppState.swift`: persistence glue. State updates
  flow through a single `onStateChange` closure so the status icon, the
  uptime label, and the Settings UI see the same `BotStatus`.

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

  (Optional, macOS only)

┌──────────────────────────────┐  Process.spawn  ┌────────────────────┐
│ Agentower.app (Swift)   │ ───────────────►│ remote-bot binary  │
│  NSStatusItem + SwiftUI form │ .env + env vars │  the same code   │
└──────────────────────────────┘                 └────────────────────┘
```

- **Inbound (Go bot)**: only `api.telegram.org`. No listening sockets.
- **Outbound (Go bot)**: loopback only — `127.0.0.1:4096` for opencode,
  or stdin/stdout pipes for Claude / Kiro, or a loopback
  JSON-RPC stream for the GitHub Copilot LSP.
- **Storage**: a single SQLite file with the runtime state.
- **Storage (macOS wrapper)**: `UserDefaults` for Settings, a `0600`
  `.env` for the bot, and the bot's own SQLite file at
  `~/Library/Application Support/Agentower/state.db`.
- **No IPC between wrapper and bot**: the wrapper never parses the
  bot's stdout. Lifecycle is supervised via `Process` + `terminationHandler`.

## Why two different architectures in one project

Go uses hexagonal / clean architecture; the Swift wrapper uses MVVM.
That is intentional, not a leak, and the two do not need to be
"unified".

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

### What the two architectures share

Although the vocabulary differs, the underlying philosophy is the same:

| Hexagonal (Go)        | MVVM (Swift)             | Same principle                |
|-----------------------|--------------------------|-------------------------------|
| Domain at the centre  | Model at the centre      | Stable core, slow to change   |
| Ports = interfaces    | ViewModel = `ObservableObject` | Observable / replaceable contract |
| Adapters at the edge  | View + services at the edge | Replaceable boundaries, easy to mock |
| Dependency rule inward | UI does not touch the model | Dependencies point at the core  |

### The seam between the two architectures

The two runtimes share **no code, no types, no FFI, no gRPC, no
protobuf**. They talk through a process boundary whose contract is
deliberately minimal and stable:

1. **The `.env` schema** — the set of variable names and value formats
   documented in `README.md`. This is the only shared schema.
2. **The `Process` API** — `executableURL`, `environment`,
   `terminationHandler`, and the captured stdout/stderr piped to
   `bot.log`.
3. **An implicit log format** — the wrapper does not parse the bot's
   stdout; it only streams it to a file. If the bot's log format ever
   needs to change, the wrapper does not notice.

### Why this is the right answer (and not "one architecture to rule them all")

A unified architecture across languages only makes sense when the
domains are shared in code: a single proto schema, a generated client,
FFI bindings, or a service mesh. None of those exist here, and adding
them would multiply the project's complexity for no benefit — the Go
binary and the Swift wrapper are two genuinely independent programs
with one small, well-defined contract between them.

The wrapper is intentionally a **dumb launcher**: it reads Settings,
writes a `0600` `.env`, spawns the binary, and supervises its
lifecycle. Its own "domain" is trivial (toggle state + last
configuration); the real business domain lives 100% inside the Go
binary. Duplicating that domain in Swift would create a second source
of truth that would silently drift from the Go implementation.

If a future contributor proposes "let's unify the architectures", the
question to ask is: *which new shared piece of code would force the
unification, and what does it actually buy us?* If the answer is "no
new shared code, just consistency for its own sake", the answer here
is to keep the two architectures separate.

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

Navigation is always relative. The handler never receives an absolute path
from Telegram; it only receives a navigation record ID and a relative path
validated against that record.

## Concurrency model

- The bot is single-threaded for Telegram handlers: telebot.v3 runs them
  in goroutines, but every operation that touches shared state goes
  through SQLite, which we serialize with a single connection
  (`db.SetMaxOpenConns(1)`).
- The CLI process shuts down on `SIGINT` / `SIGTERM` via
  `signal.NotifyContext`; the bot stops its long poller and the SQLite
  connection is closed.
- The `opencode serve` subprocess is started with `Setpgid: true` so
  that its whole process group can be terminated with a single
  `SIGTERM`. If it does not exit within `shutdownGrace` (5 s) the
  manager escalates to `SIGKILL`. The wrapper applies the same 5-second
  escalation window via `Process.terminate()`.
- The Swift wrapper observes the child via `Process.terminationHandler`
  on the main queue. State updates fan out through a single
  `onStateChange` closure (the previous Combine + closure duplication was
  removed).
- Navigation records expire after `navigationTTL` (15 minutes); each
  successful `Enter` / `Back` / `Home` resets the timer. Records are
  bound to the originating `chatID`, so a callback pressed in another
  chat returns `ErrUnauthorizedNavigation`.

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

## Why a Swift menu-bar wrapper (and not pure CLI)

A wrapper was added on top of the Go binary for three reasons:

1. **Discoverability.** A macOS user wants the bot to launch at login,
   surface a "running / stopped" status, and offer one-click access to
   the log. The wrapper does exactly that without bespoke shell glue.
2. **Configuration as a form.** `WORKSPACE_ROOT`, the bot token, the
   chat ID, the OpenCode port and bin, the login-item toggle and the
   optional proxy/API-root settings are all first-class fields in a
   SwiftUI form, persisted to `UserDefaults` and a `0600` `.env`.
3. **Zero extra attack surface.** The wrapper does not speak to
   OpenCode or to Telegram itself. It only spawns the existing Go
   binary, which already enforces the trust model in `SECURITY.md`.

The wrapper is optional. Users who prefer a pure CLI workflow can keep
running `remote-bot` directly from a terminal, Raycast, or `launchd` —
that workflow is still documented in the repo history.

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
- New failure modes that the wrapper or another adapter might need to
  handle get a sentinel in `domain/errors.go` first; string-matching
  error checks are a smell.
- CI runs `go test -race -coverprofile` on Go 1.23 (Ubuntu) plus
  `golangci-lint` (config in `.golangci.yml`). Dependabot opens weekly
  PRs grouped by ecosystem (`gomod`, `github-actions`, `swift`) so
  dependency churn never sneaks in unreviewed.
