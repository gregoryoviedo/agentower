# Product

This document defines what Agentower is, what it does today, what it
deliberately does not do, and where it is going next.

## Vision

Agentower turns Telegram into a thin remote control for one of several
local AI agents — opencode, Claude Code, Codex, Kiro, GitHub Copilot.
The bot is the only surface you interact with on your phone; whichever
agent is active keeps doing the work locally on your Mac.

A small Swift menu-bar wrapper for macOS is shipped alongside the bot so
that the same single-user workflow can be launched, supervised, and
configured from the system tray without bespoke shell glue.

## Target scenario

1. You work on a project locally with one of the supported agents.
2. You leave the computer.
3. Later, from your phone, you open the bot in Telegram.
4. You pick a project inside your workspace, choose which agent you
   want to drive it (or stick with the one you used yesterday), then
   switch to or create a session and send prompts.
5. You check progress, run `/diff` and `/undo` (on agents that support
   them), or jump back into `/sessions`.
6. When you return home, the local machine has already done the work.

On macOS the wrapper is the convenient launcher:

1. First run: open the app, fill `Settings…`, save. The Agentes de IA
   section detects which CLIs you have installed.
2. Subsequent runs: the icon in the menu bar shows whether the bot is
   running; click toggles it, right-click opens the menu.
3. The "Iniciar al login" toggle makes the wrapper start itself at boot.

## Functional scope

### Implemented

#### Go bot (`remote-bot`)

- Long polling against `api.telegram.org` with a strict `ALLOWED_CHAT_ID`
  whitelist.
- Recursive workspace navigation limited to `WORKSPACE_ROOT`; traversal and
  escaping symlinks are rejected.
- Multi-agent picker after `/projects → Usar esta carpeta` so the user
  chooses (or confirms) which agent drives the folder.
- Per-chat active agent persisted in `agent_state` (SQLite).
- Session list, switching and inline creation via the active agent's
  adapter.
- Commands: `/start`, `/help`, `/status`, `/projects`, `/agent`,
  `/agents`, `/agents migrate`, `/init`, `/sessions`, `/diff`,
  `/changes`, `/undo`, `/watch`, `/continue`, `/continuar`.
- Free-form text prompts forwarded to the active agent.
- SQLite-backed runtime state (workspace, project, session, agent,
  navigation).
- Configuration loaded from `.env` (with parent directory walk and
  `ENV_FILE` override). Per-agent settings live under
  `AGENT_<KIND>_ENABLED/BIN/PORT/ARGS`.
- Agent subprocess lifecycle: each adapter owns the per-kind lifecycle
  (opencode via HTTP+SIGTERM, Claude/Codex/Kiro via stdio JSON-RPC,
  Copilot via LSP). The `AgentServerManager` interface routes
  `Start/Stop/Started/OwnsSubprocess/WorkingDir` per kind.
- `TELEGRAM_PROXY_URL` and `TELEGRAM_API_ROOT` for restricted networks.
- Clean shutdown on `SIGINT` / `SIGTERM`.
- Sentinel errors in `domain/errors.go` so every recoverable failure can
  be matched programmatically (including `ErrAgentUnavailable` and
  `ErrAgentCapabilitiesLimited` for adapters with limited features).

#### macOS wrapper (`Agentower.app`)

- Menu-bar status icon with template rendering (auto-adapts to dark /
  light menu bars).
- Popover with a Bluetooth-style toggle for start / stop.
- Context menu (right-click) with status label, toggle, *Settings…*,
  *Abrir registro*, *Salir*.
- SwiftUI Settings window with: `WORKSPACE_ROOT` (con folder picker),
  `TELEGRAM_BOT_TOKEN`, `ALLOWED_CHAT_ID`, sección **Agentes de IA** (una
  fila por agente con toggle enable/disable, binario, puerto y args),
  y la sección **Avanzado** con `AGENTOWER_STATE_PATH`,
  `TELEGRAM_API_ROOT`, `TELEGRAM_PROXY_URL`.
- Persistence in `UserDefaults` plus a `chmod 600` `.env` regenerated on
  every save inside `~/Library/Application Support/Agentower/`.
- Logs at `~/Library/Logs/Agentower/bot.log`, accessible from the
  popover menu.
- Auto-start at login via `SMAppService.mainApp` (Settings toggle).
- arm64-only build pipeline (`make app`) that compiles the Go binary,
  generates the icon set with `sips` + Pillow, generates the
  `.xcodeproj` with XcodeGen, and produces an ad-hoc-signed `.app` in
  `dist/`.

### Out of scope (today)

- Multi-user access. The whitelist is a single ID.
- Concurrent interactive flows. One Telegram chat at a time.
- Multimedia (voice, image, document attachments).
- Universal macOS binary (arm64-only; no Intel slice).
- Scheduled tasks.
- i18n.
- SwiftUI tests (the Go side has full coverage; the wrapper relies on
  manual QA).
- Sandboxing or notarisation (the `.app` is ad-hoc signed and needs
  `xattr -dr com.apple.quarantine` on first launch).
- IPC beyond `Process` spawning between the Swift wrapper and the Go
  bot. The wrapper does not parse the bot's output.

## Trust boundaries

- The Telegram Bot API is the only network surface the Go bot depends
  on.
- The active agent is assumed to be reachable on a loopback port
  (`AGENT_OPENCODE_PORT`, `AGENT_CLAUDE_PORT`, …) or via stdin/stdout
  when the adapter is stdio-based.
- The bot token and chat ID live in `.env` (or shell env), never in
  SQLite.
- SQLite stores the active project, active session, per-chat active
  agent and short-lived navigation state only.
- When the macOS wrapper is used, the `.env` it regenerates is written
  with mode `0600` inside `~/Library/Application Support/`, accessible
  only to the current user.
- The Swift wrapper never makes network calls. It only spawns the Go
  binary and supervises its lifecycle.

## Project infrastructure

- **CI** (`.github/workflows/ci.yml`): `go test -race -coverprofile` on
  Go 1.23 (Ubuntu) plus `golangci-lint` on Ubuntu. Runs on every push and
  PR to `main`.
- **Linting** (`.golangci.yml`): `errcheck`, `govet`, `staticcheck`,
  `revive`, `gocritic`, `goimports` (with `local-prefixes` matching the
  module path), and friends — exclusions scoped to `_test.go` helpers
  that intentionally swallow errors.
- **Dependency updates** (`.github/dependabot.yml`): weekly PRs grouped
  by ecosystem (`gomod`, `github-actions`, `swift`), labelled and
  prefixed (`chore(deps)`, `ci(actions)`, `chore(macos-deps)`) so the
  changelog stays readable.
- **Community** (`.github/ISSUE_TEMPLATE/`, `.github/PULL_REQUEST_TEMPLATE.md`,
  `CODE_OF_CONDUCT.md`): structured bug reports, feature requests and a
  PR checklist aligned with `CONTRIBUTING.md`.

## Open task list

Items in priority order, intentionally small and incremental:

1. Streaming replies: surface the assistant text part-by-part in
   Telegram as the agent emits it (today we wait for the final event).
2. Pinned live status message (project, session, agent, changed files).
3. Persistent reply keyboard with the most common actions.
4. Auto-restart of the active agent when health checks fail.
5. Scheduled tasks (`/task`, `/tasklist`).
6. Voice transcription via a Whisper-compatible API (opt-in).
7. `TELEGRAM_FORCE_IPV4` and richer proxy options for restricted networks
   (today only `TELEGRAM_PROXY_URL` is supported).
8. Swift unit tests for `ConfigStore`, `AppState` and
   `LoginItemManager` to lock in the wrapper behaviour without a
   manual QA cycle.
9. Branch-specific agent overrides (different agents for different
   git branches on the same project).
10. Kiro `SessionLocator`: ship when Kiro's session storage path is
    documented. The adapter and detector already support Kiro; the
    locator is the only piece missing.

## Change policy

Each item on the task list is small enough to land in one focused change
with tests. Anything that touches the trust model (whitelist, workspace
validation, persistence of secrets, action capabilities per agent)
requires a focused review and a documented test.

Items that change the interaction model (e.g. multi-user, multi-chat
parallel flows) are explicitly out of scope for the moment. See
`docs/DESIGN.md` for the rationale.
