# Product

This document defines what Agentower is, what it does today, what it
deliberately does not do, and where it is going next.

## Vision

Agentower turns Telegram into a thin **companion** for one of several
local AI agents — opencode, Claude Code, Kiro, GitHub Copilot, Codex,
Antigravity.
The agent keeps doing the work locally on your machine and in your own
IDE/TUI; the bot watches its sessions, pings you when a task finishes or a
question is waiting, and lets you continue the session from your phone.
It is not a full remote control: you don't launch agents, pick projects
or manage sessions from the chat.

A small native wrapper is shipped alongside the bot so that the same
single-user workflow can be launched, supervised, and configured from the
system tray without bespoke shell glue: `Agentower.app` (Swift, macOS menu
bar) and `Agentower.exe` (C#/.NET WinForms, Windows notification area).

## Target scenario

1. You work on a project locally with one of the supported agents.
2. You leave the computer.
3. The agent finishes (or asks a question). The bot notices and, once you
   have been away for the idle threshold, pings you on Telegram.
4. From your phone you tap **▶️ Continuar sesión** to keep working on that
   session, check progress with **📝 Ver cambios**, or answer a question.
5. When you return home, the local machine has already done the work.

The wrapper is the convenient launcher on both platforms:

1. First run: open the app, fill the Settings form, save. The Agentes de
   IA section detects which CLIs you have installed.
2. Subsequent runs: the tray/menu-bar icon shows whether the bot is
   running; left click toggles it, right click opens the menu.
3. The auto-start-at-login toggle makes the wrapper start itself at boot,
   and on launch it **auto-starts the bot too** when the configuration is
   valid, so the agent is supervised from the moment you sit down.

## Functional scope

### Implemented

#### Go bot (`remote-bot`)

- Long polling against `api.telegram.org` with a strict `ALLOWED_CHAT_ID`
  whitelist.
- Auto-follow completion detection: every agent with a `SessionLocator`
  gets a watcher that tracks the freshest session and records the
  completion without a Telegram prompt. opencode is followed through its
  SQLite store (`~/.local/share/opencode/opencode.db`), so a local TUI
  with no HTTP port works; Claude's locator scans every
  `~/.claude/projects/*/`, so any folder works.
- Commands: `/start`, `/help`, `/status`, `/continue`, `/resume`.
- Free-form text prompts forwarded to the followed session, plus inline
  answers to the agent's structured questions.
- SQLite-backed runtime state (workspace, project, session, agent).
- Configuration loaded from `.env` (with parent directory walk and
  `ENV_FILE` override). Per-agent settings live under
  `AGENT_<KIND>_ENABLED/BIN/PORT/ARGS`.
- Agent subprocess lifecycle: each adapter owns the per-kind lifecycle
  (opencode via HTTP+SIGTERM, Claude via stdio JSON, Kiro via ACP over
  `kiro-cli acp`, Copilot via ACP CLI or LSP). The `AgentServerManager`
  interface routes `Start/Stop/Started/OwnsSubprocess/WorkingDir` per
  kind.
- Agent detection probes `PATH` first and then the platform install
  locations (macOS app bundles for Kiro CLI / VS Code Copilot /
  Antigravity; Windows `%APPDATA%\npm`, `%LOCALAPPDATA%\Programs`,
  `~/.opencode`, `~/.bun`, `~/.codex`), and augments `PATH` with the
  usual per-user bin directories so GUI launches find the CLIs.
- Kiro sessions are discovered from the JSONL store under
  `~/.kiro/sessions/<workspace>/<id>/messages.jsonl`, which is also what
  backs `ListMessages` for completion detection.
- Structured questions: when the active agent pauses on a multiple-choice
  question (opencode's *question tool*, exposed as `GET /api/question`),
  the watcher suppresses the completion snapshot and routes the question
  through the bot. The user answers with inline buttons or free text and
  the answer is posted back to the agent so the turn resumes.
- Idle notifications, driven by the wrappers' local control socket
  (`/state`, `/notify`, `/question-notify`): a completion message after 2
  minutes of local inactivity, and a question message after 1 minute.
  The question notification never fires twice for the same request and
  is dropped if the user answered locally first.
- `TELEGRAM_PROXY_URL` and `TELEGRAM_API_ROOT` for restricted networks.
- Clean shutdown on `SIGINT` / `SIGTERM` (Unix) or signal/`taskkill`
  (Windows).
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
- Auto-start at login via `SMAppService.mainApp` (Settings toggle); on
  launch the app also starts the bot automatically when the configuration
  is valid.
- `IdleNotifier`: polls the bot's local control socket over `127.0.0.1`,
  measures local inactivity with `CGEventSource`, and fires native
  notifications plus the Telegram completion/question prompts described
  above.
- arm64-only build pipeline (`make app`) that compiles the Go binary,
  generates the icon set with `sips` + Pillow, generates the
  `.xcodeproj` with XcodeGen, and produces an ad-hoc-signed `.app` in
  `dist/`.

#### Windows wrapper (`Agentower.exe`)

- Notification-area icon (`NotifyIcon`) with a popover on left click
  (toggle, status, uptime, quick actions) and a context menu on right
  click.
- WinForms Settings window with the same four sections as macOS:
  Telegram, Agentes de IA (one row per agent: enabled / bin / port /
  args, with detection), Avanzado and Inicio.
- Persistence as `settings.json` plus a regenerated `.env` in
  `%APPDATA%\Agentower\`; logs in `%LOCALAPPDATA%\Agentower\logs\`.
- Agent detection with Windows install locations (`%APPDATA%\npm`,
  `%LOCALAPPDATA%\Programs`, `~/.opencode`, `~/.bun`, `~/.codex`,
  the VS Code extension for Copilot), respecting `PATHEXT`.
- `BotController` embeds `remote-bot.exe`, extracts it to
  `%LOCALAPPDATA%\Agentower\` and supervises it, killing the whole
  process tree on stop.
- Auto-start at login via the per-user `HKCU\...\Run` key, enabled by
  default on first configuration; the app auto-starts the bot on launch.
- `IdleNotifier`: `GetLastInputInfo` for idle time plus the same
  control-socket completion/question flow, surfacing tray notifications.
- x64-only, single-file, self-contained build (`windows/build.ps1`) with
  a headless `--selftest` used by CI.

### Out of scope (today)

- Multi-user access. The whitelist is a single ID.
- Concurrent interactive flows. One Telegram chat at a time.
- Multimedia (voice, image, document attachments).
- Universal macOS binary (arm64-only; no Intel slice) or non-x64
  Windows binary (win-x64 only).
- Scheduled tasks.
- i18n.
- Unit tests for the wrappers (the Go side has full coverage; the
  wrappers rely on manual QA plus the Windows `--selftest` smoke test).
- Sandboxing or notarisation (the `.app` is ad-hoc signed and needs
  `xattr -dr com.apple.quarantine` on first launch; the Windows `.exe` is
  unsigned and may trigger SmartScreen).
- Wrapper↔bot IPC beyond spawning plus the one-way loopback control
  socket (`/state`, `/notify`, `/question-notify`). The wrappers never
  parse the bot's stdout.
- Structured-question forwarding for agents other than opencode. Claude
  Code runs headless (`--print`) where `AskUserQuestion` is disabled, and
  Kiro/Copilot only surface `session/request_permission`, which the ACP
  adapters auto-approve (`TrustAll`). Permission prompts and ACP
  elicitation are not forwarded to Telegram today.
- Detecting structured questions for locally-driven sessions: the
  opencode question API needs `opencode serve` (the TUI exposes no port),
  and the file/stdio locators used by the other agents do not surface
  questions.

## Trust boundaries

- The Telegram Bot API is the only network surface the Go bot depends
  on.
- The active agent is assumed to be reachable on a loopback port
  (`AGENT_OPENCODE_PORT`, `AGENT_CLAUDE_PORT`, …), via stdin/stdout when
  the adapter is stdio-based, or through its local session store
  (opencode's SQLite DB, Claude's `~/.claude/projects/`).
- The bot token and chat ID live in `.env` (or shell env), never in
  SQLite.
- SQLite stores the active project, active session, per-chat active
  agent and short-lived navigation state only.
- When the macOS wrapper is used, the `.env` it regenerates is written
  with mode `0600` inside `~/Library/Application Support/`, accessible
  only to the current user. On Windows it lives in `%APPDATA%\Agentower\`
  (per-user), together with `settings.json` and `state.db`.
- The wrappers never make outbound network calls. They spawn the Go
  binary, supervise its lifecycle, and poll the bot's loopback control
  socket (`127.0.0.1`, address in `control.json`).

## Project infrastructure

- **CI** (`.github/workflows/ci.yml`): `go test -race -coverprofile` on
  Go 1.23 (Ubuntu) plus `golangci-lint` on Ubuntu, and a `windows` job
  that cross-compiles the bot, builds `Agentower.exe` and runs its
  `--selftest`. Runs on every push and PR to `main`.
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
   Telegram as the agent emits it (today we wait for the final event
   even for the copilot LSP path, which buffers the whole response).
2. Pinned live status message (project, session, agent, changed files).
3. Persistent reply keyboard with the most common actions.
4. Auto-restart of the active agent when health checks fail.
5. Scheduled tasks (`/task`, `/tasklist`).
6. Voice transcription via a Whisper-compatible API (opt-in).
7. `TELEGRAM_FORCE_IPV4` and richer proxy options for restricted networks
   (today only `TELEGRAM_PROXY_URL` is supported).
8. Wrapper unit tests (Swift for `ConfigStore`, `AppState`,
   `LoginItemManager`; C# for `ConfigStore`, `AgentDetector`,
   `BotController`) to lock in wrapper behaviour without a manual QA
   cycle. The Windows side already has the `--selftest` smoke test.
9. Branch-specific agent overrides (different agents for different
   git branches on the same project).
10. Kiro session storage format: Kiro is a moving target (Code OSS
    fork). We now read `~/.kiro/sessions/<workspace>/<id>/messages.jsonl`
    and tolerate both the string and typed-parts `content` shapes. If a
    future release moves the data, the reader will return
    `ErrNoActiveSession` and we'll need to point it at the new location.
11. Forward tool-permission prompts to Telegram. Kiro/Copilot ask via
    ACP `session/request_permission` (options like allow-once /
    allow-always / reject) and today the adapters auto-approve them.
    Wiring those into the same pending-input pipeline would let the user
    approve or deny from the phone. It reuses the `QuestionAdapter` /
    `QuestionBroker` abstraction introduced for opencode questions.

## Change policy

Each item on the task list is small enough to land in one focused change
with tests. Anything that touches the trust model (whitelist, workspace
validation, persistence of secrets, action capabilities per agent)
requires a focused review and a documented test.

Items that change the interaction model (e.g. multi-user, multi-chat
parallel flows) are explicitly out of scope for the moment. See
`docs/DESIGN.md` for the rationale.
