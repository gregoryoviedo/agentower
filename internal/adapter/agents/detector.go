// Package agents owns the multi-agent surface: detector, registry,
// subprocess manager and one adapter per AI agent (opencode today;
// claude, codex, kiro and copilot in later PRs).
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
	DefaultOpenCodePort = 4096
	DefaultClaudePort   = 4097
	DefaultCodexPort    = 4098
	DefaultKiroPort     = 4099
	DefaultCopilotPort  = 4100
)

// Detector probes the local environment for every known agent and
// returns the boot-time descriptor list the registry uses.
type Detector struct {
	// LookPath is overridable so tests can stub the PATH lookup.
	LookPath func(string) (string, error)
}

// NewDetector builds a detector that uses exec.LookPath under the hood.
func NewDetector() *Detector {
	return &Detector{LookPath: exec.LookPath}
}

// Scan walks the supported agent kinds in stable order and returns one
// descriptor per kind. The Available flag is true only when (a) the
// binary is in PATH (or the configured bundle path) AND (b) the
// adapter has shipped. Today only opencode has shipped adapters; the
// others are surfaced as Detected=true Available=false Reason="Próximamente"
// so the Settings UI and Telegram picker can render them.
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
	case domain.AgentCodex:
		return d.scanCodex(ctx)
	case domain.AgentKiro:
		return d.scanKiro(ctx)
	case domain.AgentCopilot:
		// Copilot ships an LSP over stdio; no HTTP probe needed.
		return d.scanCopilot()
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

func (d *Detector) scanCodex(ctx context.Context) domain.AgentDescriptor {
	bin, _ := d.LookPath("codex")
	desc := domain.AgentDescriptor{
		Kind:        domain.AgentCodex,
		DisplayName: "Codex",
		Bin:         bin,
		Port:        DefaultCodexPort,
		Detected:    bin != "",
		Available:   bin != "",
		Capabilities: domain.AgentCapabilities{
			Health:        true,
			ListProjects:  false,
			ListSessions:  false, // Codex CLI does not yet expose a session index
			CreateSession: true,
			SendPrompt:    true,
			Revert:        false,
			FileStatus:    true,
			ListMessages:  false,
		},
	}
	if bin == "" {
		desc.Reason = "no se encontró el binario codex en PATH"
	}
	desc.Running = d.detectPortOpen(ctx, DefaultCodexPort, "/")
	return desc
}

func (d *Detector) scanKiro(ctx context.Context) domain.AgentDescriptor {
	bin, _ := d.LookPath("kiro")
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
			ListSessions:  false,
			CreateSession: true,
			SendPrompt:    true,
			Revert:        false,
			FileStatus:    false, // Kiro does not expose file diffs natively
			ListMessages:  false,
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
		ListMessages:  false,
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
