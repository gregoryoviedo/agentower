// Package agents owns the multi-agent surface: detector, registry,
// subprocess manager and one adapter per AI agent (opencode today;
// claude, kiro and copilot ship as well).
package agents

import (
	"context"
	"os/exec"
	"path/filepath"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// DefaultPort is the loopback port each agent uses when no override is
// supplied. Stable across runs so the user only has to remember one
// number per agent.
const (
	DefaultOpenCodePort    = 4096
	DefaultClaudePort      = 4097
	DefaultKiroPort        = 4099
	DefaultCopilotPort     = 4100
	DefaultCodexPort       = 4101
	DefaultAntigravityPort = 4102
)

// Detector probes the local environment for every known agent and
// returns the boot-time descriptor list the registry uses.
type Detector struct {
	// LookPath is overridable so tests can stub the PATH lookup.
	LookPath func(string) (string, error)
	// ExtraBins is consulted after LookPath (and the built-in bundle
	// scans) fail, for binaries installed outside PATH — e.g. Kiro's
	// CLI inside /Applications/Kiro CLI.app or Copilot built into the
	// VS Code app bundle. Leave nil to keep detection hermetic (tests).
	ExtraBins func(name string) string
}

// NewDetector builds a detector that uses exec.LookPath under the hood.
func NewDetector() *Detector {
	return &Detector{LookPath: exec.LookPath}
}

// Scan walks the supported agent kinds in stable order and returns one
// descriptor per kind. The Available flag is true only when (a) the
// binary is in PATH (or the configured bundle path) AND (b) the
// adapter has shipped. Every supported kind has an adapter today, so
// Available mirrors Detected; the Reason field explains the miss when
// the binary is absent.
func (d *Detector) Scan(ctx context.Context) []domain.AgentDescriptor {
	kinds := domain.AllAgentKinds()
	out := make([]domain.AgentDescriptor, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, d.scanOne(ctx, kind))
	}
	return out
}

func (d *Detector) scanOne(ctx context.Context, kind domain.AgentKind) domain.AgentDescriptor {
	switch kind {
	case domain.AgentOpenCode:
		return d.scanOpenCode(ctx)
	case domain.AgentClaude:
		return d.scanClaude(ctx)
	case domain.AgentKiro:
		return d.scanKiro(ctx)
	case domain.AgentCopilot:
		// Copilot ships an LSP over stdio; no HTTP probe needed.
		return d.scanCopilot()
	case domain.AgentCodex:
		return d.scanCodex()
	case domain.AgentAntigravity:
		return d.scanAntigravity()
	default:
		return domain.AgentDescriptor{
			Kind:        kind,
			DisplayName: string(kind),
			Reason:      "unknown agent kind",
		}
	}
}

func (d *Detector) scanOpenCode(ctx context.Context) domain.AgentDescriptor {
	bin, _ := d.LookPath("opencode")
	desc := domain.AgentDescriptor{
		Kind:         domain.AgentOpenCode,
		DisplayName:  "opencode",
		Bin:          bin,
		Port:         DefaultOpenCodePort,
		Detected:     bin != "",
		Available:    bin != "",
		Capabilities: opencodeCapabilities(),
	}
	if bin == "" {
		desc.Reason = "no se encontró el binario opencode en PATH"
	}
	desc.Running = d.detectPortOpen(ctx, DefaultOpenCodePort, "/global/health")
	return desc
}

func (d *Detector) scanClaude(ctx context.Context) domain.AgentDescriptor {
	bin, _ := d.LookPath("claude")
	desc := domain.AgentDescriptor{
		Kind:        domain.AgentClaude,
		DisplayName: "Claude",
		Bin:         bin,
		Port:        DefaultClaudePort,
		Detected:    bin != "",
		Available:   bin != "",
		Capabilities: domain.AgentCapabilities{
			Health:        true,
			ListProjects:  false, // Claude Code does not expose a server-side project list
			ListSessions:  true,
			CreateSession: true,
			SendPrompt:    true,
			Revert:        false, // Claude Code has no revert endpoint
			FileStatus:    true,  // falls back to `git diff` in the adapter
			ListMessages:  true,
		},
	}
	if bin == "" {
		desc.Reason = "no se encontró el binario claude en PATH"
	}
	desc.Running = d.detectPortOpen(ctx, DefaultClaudePort, "/") // best-effort HTTP probe
	return desc
}

func (d *Detector) scanKiro(ctx context.Context) domain.AgentDescriptor {
	bin, _ := d.LookPath("kiro")
	if bin == "" && d.ExtraBins != nil {
		bin = d.ExtraBins("kiro")
	}
	desc := domain.AgentDescriptor{
		Kind:        domain.AgentKiro,
		DisplayName: "Kiro",
		Bin:         bin,
		Port:        DefaultKiroPort,
		Detected:    bin != "",
		Available:   bin != "",
		Capabilities: domain.AgentCapabilities{
			Health:        true,
			ListProjects:  false,
			ListSessions:  true,
			CreateSession: true,
			SendPrompt:    true,
			Revert:        false,
			FileStatus:    true, // falls back to `git diff` in the adapter
			ListMessages:  true, // reads ~/.kiro messages.jsonl
		},
	}
	if bin == "" {
		desc.Reason = "no se encontró el binario kiro en PATH"
	}
	desc.Running = d.detectPortOpen(ctx, DefaultKiroPort, "/")
	return desc
}

// binary is not always in PATH; we also look inside the VS Code
// extension bundles for github.copilot and github.copilot-chat.
func (d *Detector) scanCopilot() domain.AgentDescriptor {
	bin, _ := d.LookPath("copilot")
	if bin == "" {
		bin, _ = d.LookPath("copilot-language-server")
	}
	if bin == "" {
		if match, ok := findVSCodeCopilotBundle(); ok {
			bin = match
		}
	}
	if bin == "" && d.ExtraBins != nil {
		bin = d.ExtraBins("copilot")
	}
	desc := domain.AgentDescriptor{
		Kind:         domain.AgentCopilot,
		DisplayName:  "GitHub Copilot",
		Bin:          bin,
		Port:         DefaultCopilotPort,
		Detected:     bin != "",
		Available:    bin != "", // PR-5: the LSP adapter ships today
		Capabilities: copilotCapabilities(),
	}
	if bin == "" {
		desc.Reason = "no se encontró copilot ni copilot-language-server en PATH ni en las extensiones de VS Code"
	}
	return desc
}

// copilotCapabilities lists what the LSP adapter supports today.
// The upstream Copilot LSP does not expose stable endpoints for
// ListSessions / ListMessages / Revert yet, so the adapter returns
// ErrAgentCapabilitiesLimited for those and the Telegram UI hides
// the corresponding buttons for copilot-driven chats.
func copilotCapabilities() domain.AgentCapabilities {
	return domain.AgentCapabilities{
		Health:        true,
		ListProjects:  false,
		ListSessions:  false,
		CreateSession: true,
		SendPrompt:    true,
		Revert:        false,
		FileStatus:    true, // falls back to `git diff`
		ListMessages:  true, // reads VS Code session-store.db
	}
}

// scanCodex probes for OpenAI's Codex CLI. There is no HTTP server:
// the adapter drives `codex exec --json` over stdio, so Running stays
// false and a port is reserved only for parity with the other agents.
func (d *Detector) scanCodex() domain.AgentDescriptor {
	bin, _ := d.LookPath("codex")
	if bin == "" && d.ExtraBins != nil {
		bin = d.ExtraBins("codex")
	}
	desc := domain.AgentDescriptor{
		Kind:         domain.AgentCodex,
		DisplayName:  "Codex",
		Bin:          bin,
		Port:         DefaultCodexPort,
		Detected:     bin != "",
		Available:    bin != "",
		Capabilities: codexCapabilities(),
	}
	if bin == "" {
		desc.Reason = "no se encontró el binario codex en PATH"
	}
	return desc
}

// scanAntigravity probes for Google's Antigravity CLI (`agy`). It is a
// stdio-only headless transport; the IDE is watched through the shared
// ~/.gemini state root by the locator.
func (d *Detector) scanAntigravity() domain.AgentDescriptor {
	bin, _ := d.LookPath("agy")
	if bin == "" && d.ExtraBins != nil {
		bin = d.ExtraBins("antigravity")
	}
	desc := domain.AgentDescriptor{
		Kind:         domain.AgentAntigravity,
		DisplayName:  "Antigravity",
		Bin:          bin,
		Port:         DefaultAntigravityPort,
		Detected:     bin != "",
		Available:    bin != "",
		Capabilities: antigravityCapabilities(),
	}
	if bin == "" {
		desc.Reason = "no se encontró el binario agy (Antigravity CLI) en PATH"
	}
	return desc
}

func codexCapabilities() domain.AgentCapabilities {
	return domain.AgentCapabilities{
		Health:        true,
		ListProjects:  false,
		ListSessions:  true, // reads ~/.codex/sessions/**/rollout-*.jsonl
		CreateSession: true,
		SendPrompt:    true,
		Revert:        false,
		FileStatus:    true, // falls back to `git diff`
		ListMessages:  true, // reads the rollout JSONL
	}
}

func antigravityCapabilities() domain.AgentCapabilities {
	return domain.AgentCapabilities{
		Health:        true,
		ListProjects:  false,
		ListSessions:  true, // reads ~/.gemini/antigravity*/brain + history.jsonl
		CreateSession: true,
		SendPrompt:    true,
		Revert:        false,
		FileStatus:    true, // falls back to `git diff`
		ListMessages:  true, // reads transcript_full.jsonl
	}
}

// findVSCodeCopilotBundle looks for the copilot extension bundle inside
// the user's VS Code install. Returns the absolute path to the
// extension.js that wraps the language server.
func findVSCodeCopilotBundle() (string, bool) {
	home, err := userHomeDir()
	if err != nil {
		return "", false
	}
	candidates := []string{
		filepath.Join(home, ".vscode", "extensions", "github.copilot-"),
		filepath.Join(home, ".vscode", "extensions", "github.copilot-chat-"),
		filepath.Join(home, ".vscode-server", "extensions", "github.copilot-"),
		filepath.Join(home, ".vscode-server", "extensions", "github.copilot-chat-"),
	}
	for _, prefix := range candidates {
		if match, ok := firstDirWithPrefix(prefix); ok {
			bundle := filepath.Join(match, "dist", "extension.js")
			return bundle, true
		}
	}
	return "", false
}

func opencodeCapabilities() domain.AgentCapabilities {
	return domain.AgentCapabilities{
		Health:        true,
		ListProjects:  true,
		ListSessions:  true,
		CreateSession: true,
		SendPrompt:    true,
		Revert:        true,
		FileStatus:    true,
		ListMessages:  true,
	}
}

// detectPortOpen probes an HTTP endpoint on the loopback port and
// returns true if it answers 2xx in a short window.
func (d *Detector) detectPortOpen(ctx context.Context, port int, path string) bool {
	if port <= 0 {
		return false
	}
	return probeHTTP(ctx, "http://127.0.0.1:"+itoa(port)+path)
}
