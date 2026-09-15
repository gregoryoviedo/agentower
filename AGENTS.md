# AGENTS.md

Guidance for AI coding agents (and humans) working in this repository.
Keep it short, factual, and up to date.

## What this is

Agentower is a Telegram remote control for local AI agents (opencode,
Claude Code, Kiro, GitHub Copilot). One Go binary (`remote-bot`) does the
work; an optional macOS menu-bar wrapper (`Agentower.app`, Swift) launches
it, stores settings, and drives the idle notifications.

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

There are no Makefile targets for Go tests/lint; use the `go`/`golangci-lint`
commands directly. Always run `go test ./...` and `golangci-lint run ./...`
before considering a change done. For Swift changes, at minimum typecheck:

```bash
xcrun swiftc -typecheck macos/Agentower/Agentower/*.swift \
  -sdk "$(xcrun --show-sdk-path --sdk macosx)"
```

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
  - `agents/{opencode,claude,kiro,copilot}` + `agents/registry` +
    `agents/detector` (PATH + app-bundle probing).
  - `telegram` (telebot.v3 long polling, whitelist, markdown→HTML).
  - `storage/sqlite` (WAL, single connection).
  - `control` (local HTTP server the macOS wrapper talks to).
  - `workspace`, `config`.
- `cmd/remote-bot/main.go` — the composition root. **All wiring happens
  here**; if you add a port, wire it in `main.go` (and in tests).

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

Notes and gotchas:

- GUI/launchd launches start with a minimal `PATH`. `ensureUserBinPath`
  in `main.go` appends `~/.local/bin`, `~/.nvm/versions/node/*/bin`,
  Homebrew dirs, etc. before detection.
- Kiro/Copilot ACP agents are created with `TrustAll: true`, so
  `session/request_permission` is auto-approved and ACP `elicitation` is
  not advertised. There is currently no interactive question flow for
  them (Claude Code disables `AskUserQuestion` in `--print` mode).
- opencode's question API has changed shape/path across releases
  (`/api/question`, `/question`, `/api/question/request`). The adapter
  tries the known paths and tolerates both a `questions` array and a
  single flattened `question` object; keep it defensive.

## Completion & question pipeline

The macOS wrapper owns idle detection (`CGEventSource`); the Go bot owns
agent polling. They talk over a loopback HTTP control socket
(`internal/control`, default `127.0.0.1:0`, address written to
`control.json`).

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
- **Agent detection**: PATH then app bundles, with PATH augmentation for
  GUI launches.

See `docs/DESIGN.md` for the full design and `docs/PRODUCT.md` for scope
and roadmap.
