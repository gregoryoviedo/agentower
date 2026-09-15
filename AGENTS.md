# AGENTS.md

Guidance for AI coding agents (and humans) working in this repository.
Keep it short, factual, and up to date.

## What this is

Agentower is a Telegram remote control for local AI agents (opencode,
Claude Code, Kiro, GitHub Copilot, Codex, Antigravity). One Go binary
(`remote-bot`) does the work; an optional native wrapper launches it,
stores settings, and drives the idle notifications — `Agentower.app`
(Swift, macOS menu bar) or `Agentower.exe` (C#/.NET WinForms, Windows
notification area).

## Commands

```bash
go build ./...                              # compile everything
go test ./...                               # full test suite
go test ./... -count=1                      # ignore the test cache
go test ./internal/usecase/ -run TestName   # a single test
go vet ./...                                # standard vet
golangci-lint run ./...                     # lint (config: .golangci.yml)

go build -o remote-bot ./cmd/remote-bot     # build the binary
make app                                    # build dist/Agentower.app (macOS)
make icons                                  # regenerate the menu-bar/app icons
```

For the Windows wrapper (PowerShell):

```powershell
.\windows\build.ps1                         # build dist\Agentower.exe (win-x64)
.\dist\Agentower.exe --selftest             # headless smoke test (config + forms + detection)
```

There are no Makefile targets for Go tests/lint; use the `go`/`golangci-lint`
commands directly. Always run `go test ./...` and `golangci-lint run ./...`
before considering a change done. For Swift changes, at minimum typecheck:

```bash
xcrun swiftc -typecheck macos/Agentower/Agentower/*.swift \
  -sdk "$(xcrun --show-sdk-path --sdk macosx)"
```

Build the Windows wrapper with the .NET 8 SDK (`dotnet publish`); the Go
bot there is compiled with `GOOS=windows GOARCH=amd64`. Note that several
Go tests fail on Windows for unrelated reasons (fake agent binaries are
extensionless shell scripts and symlink tests need privileges); CI runs
the Go suite on Ubuntu and only builds/smokes the wrapper on Windows.

## Architecture

Hexagonal / Clean Architecture in three concentric layers:

- `internal/domain` — entities and ports only. No external imports beyond
  the standard library. New capabilities are usually a new **port**
  (interface) here, implemented by adapters.
- `internal/usecase` — application rules: `Handler` (Telegram command
  router), `SessionWatcher` (polls the active agent for completions and
  pending questions), `NavigationService`, `WorkspaceBrowser`. No
  Telegram-specific types; only `domain.BotResponse` goes back.
- `internal/adapter` — real-world implementations:
  - `agents/{opencode,claude,kiro,copilot,codex,antigravity}` +
    `agents/registry` + `agents/detector` (PATH + app-bundle/installer
    probing; `bundles.go` is per-OS).
  - `telegram` (telebot.v3 long polling, whitelist, markdown→HTML).
  - `storage/sqlite` (WAL, single connection).
  - `control` (local HTTP server the wrappers talk to).
  - `workspace`, `config`.
- `cmd/remote-bot/main.go` — the composition root. **All wiring happens
  here**; if you add a port, wire it in `main.go` (and in tests).
- `macos/Agentower/` (Swift) and `windows/Agentower/` (C#/.NET WinForms) —
  the optional launchers. They never talk to agents or Telegram directly;
  they write `.env`/settings, spawn `remote-bot`, stream its logs, and
  poll the control socket for the idle completion/question flows. Keep
  the two wrappers feature-equivalent.

Legacy aliases `OpenCodeClient` and `OpenCodeServerManager` exist for
compatibility; prefer `AgentAdapter` / `AgentRegistry` /
`AgentServerManager` in new code.

## Agent transports

| Agent    | Transport | History |
|----------|-----------|---------|
| opencode | HTTP `opencode serve` | REST (`/session`, `/api/question`) |
| claude   | stdio JSON (`claude --print --output-format stream-json`) | `~/.claude/projects/<cwd>/<id>.jsonl` |
| kiro     | ACP (`kiro-cli acp`, newline framing) | `~/.kiro/sessions/<ws>/<id>/messages.jsonl` |
| copilot  | ACP CLI, or LSP for the VS Code bundle | VS Code `session-store.db` |
| codex    | headless `codex exec --json -` / `codex exec resume <id> --json -` | `~/.codex/sessions/**/rollout-*.jsonl` (`CODEX_SESSIONS_DIR`) |
| antigravity | headless `agy -p --output-format stream-json` (`--conversation <id>` to resume) | `~/.gemini/antigravity-cli/brain/<id>/.../transcript_full.jsonl` (CLI) + `~/.gemini/antigravity/...` (IDE) |

Notes and gotchas:

- GUI launches start with a minimal `PATH` (Finder/launchd on macOS, the
  Explorer environment on Windows). `ensureUserBinPath` in `main.go`
  appends the per-OS user bin dirs (`~/.local/bin`, `~/.nvm/...`,
  Homebrew; `%APPDATA%\npm`, `~/.bun`, `~/.opencode`, …) before detection.
- Kiro/Copilot ACP agents are created with `TrustAll: true`, so
  `session/request_permission` is auto-approved and ACP `elicitation` is
  not advertised. There is currently no interactive question flow for
  them (Claude Code disables `AskUserQuestion` in `--print` mode).
- Codex and Antigravity are headless CLIs with no persistent server and
  no TTY to answer permission prompts, so their managers pass
  auto-approval flags by default (`--dangerously-bypass-approvals-and-sandbox`
  and `--dangerously-skip-permissions` respectively). The flag sets can be
  overridden with `AGENT_CODEX_ARGS` / `AGENT_ANTIGRAVITY_ARGS`.
- Both new adapters map a client-side synthetic session id to the real
  upstream id (Codex `thread_id`, Antigravity `conversation_id`) learned
  on the first run, because neither lets the caller choose the id.
- Codex needs a git repo unless `--skip-git-repo-check` is passed (it is,
  by default). Antigravity's per-conversation `.db` files are opaque
  protobuf; the adapters read the readable `transcript_full.jsonl` and
  `history.jsonl` instead.
- opencode's question API has changed shape/path across releases
  (`/api/question`, `/question`, `/api/question/request`). The adapter
  tries the known paths and tolerates both a `questions` array and a
  single flattened `question` object; keep it defensive.

## Completion & question pipeline

The wrappers own idle detection (`CGEventSource` on macOS,
`GetLastInputInfo` on Windows); the Go bot owns agent polling. They talk
over a loopback HTTP control socket (`internal/control`, default
`127.0.0.1:0`, address written to `control.json`). Both wrappers implement
the same flows; mirror any change in `IdleNotifier.swift` and
`IdleNotifier.cs`.

- **Completion:** `SessionWatcher` sees the session stop changing and calls
  `Publisher.RequestNotification` → `/state` exposes `PendingNotifChat` →
  wrapper (idle ≥ 5 min) posts `/notify` → control server sends the
  Telegram message with buttons.
- **Question:** `SessionWatcher.questionBlocks` polls
  `domain.QuestionAdapter.ListQuestions`; a pending question is stored on
  the `Publisher` (`QuestionBroker`) and **suppresses** the completion.
  The wrapper (idle ≥ 3 min) posts `/question-notify`; the control server
  renders one Telegram message per question with `q|chat|qIdx|optIdx`
  buttons (and `qd|chat|qIdx` to finalize multi-select). Taps and free
  text are handled in `Handler` (`HandleCallback`, `HandleText`), which
  calls `ReplyQuestion` and clears the pending question.
- `Publisher.SetPendingQuestion` is idempotent for the same `RequestID`:
  it preserves the user's partial answers, `Settled` flags, `AskedAt`
  and `NotifiedAt`, because the watcher republishes every tick.

## Conventions

- User-facing strings are Spanish; code, comments and identifiers are
  English.
- Recoverable failures use sentinel errors in `internal/domain/errors.go`
  (`ErrAgentUnavailable`, `ErrAgentCapabilitiesLimited`, …). Adapters
  return them; the handler maps them to messages.
- HTTP responses from agents are size-capped (`io.LimitReader`).
- Do **not** add comments to code unless they explain non-obvious intent;
  match the surrounding style.
- Tests are table-driven where it helps and use `httptest` to fake agents;
  the domain and usecase layers have no real network calls in tests.
- Never commit secrets. `.env`, `state.db` and `control.json` live outside
  the repo (or under `.agentower/`, ignored).

## Recent features (context for changes)

- **Multi-agent**: per-chat active agent persisted in `agent_state`; `/agent`,
  `/agents`, `/agents migrate`.
- **Session resume**: `/resume` fans out to `SessionLocator`s
  (opencode HTTP, Claude JSONL, Kiro/VS Code) and `/continue` reactivates
  the last completed session.
- **Structured questions (opencode)**: entities `PendingQuestion` /
  `QuestionPrompt` / `QuestionOption`, ports `QuestionAdapter` /
  `QuestionBroker`, `POST /question-notify`, callbacks `q|` / `qd|`.
- **Kiro**: migrated from the old VS Code `state.vscdb` reader to ACP plus
  the JSONL history store.
- **Agent detection**: PATH then app bundles/installers, with PATH
  augmentation for GUI launches (per-OS paths in `bundles.go` and
  `path_windows.go` / `path_unix.go`).
- **Codex & Antigravity**: headless CLI adapters (no server, no TTY).
- **Windows wrapper**: C#/.NET 8 WinForms tray app (`windows/`), feature
  parity with macOS — settings, detection, idle notifications, login
  auto-start. It auto-starts the bot on launch when config is valid;
  macOS does the same. The Go bot cross-compiles for Windows via build
  tags (`process_unix.go` / `process_windows.go`).

See `docs/DESIGN.md` for the full design and `docs/PRODUCT.md` for scope
and roadmap.
