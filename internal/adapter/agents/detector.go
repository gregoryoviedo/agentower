// Package agents owns the multi-agent surface: detector, registry,
// subprocess manager and one adapter per AI agent (opencode today;
// claude, codex, kiro and copilot in later PRs).
package agents

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

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
		return d.scanProbeOnly(ctx, domain.AgentClaude, "claude", DefaultClaudePort, true, true, true, true, true, true, true)
	case domain.AgentCodex:
		return d.scanProbeOnly(ctx, domain.AgentCodex, "codex", DefaultCodexPort, true, true, true, true, true, true, true)
	case domain.AgentKiro:
		return d.scanProbeOnly(ctx, domain.AgentKiro, "kiro", DefaultKiroPort, true, false, false, false, true, false, false)
	case domain.AgentCopilot:
		return d.scanCopilot(ctx)
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

func (d *Detector) scanProbeOnly(ctx context.Context, kind domain.AgentKind, binName string, port int, send, list, create, revert, files, msgs, proj bool) domain.AgentDescriptor {
	bin, _ := d.LookPath(binName)
	desc := domain.AgentDescriptor{
		Kind:        kind,
		DisplayName: strings.Title(string(kind)),
		Bin:         bin,
		Port:        port,
		Detected:    bin != "",
		Available:   false,
		Reason:      "Próximamente",
		Capabilities: domain.AgentCapabilities{
			SendPrompt:    send,
			ListSessions:  list,
			CreateSession: create,
			Revert:        revert,
			FileStatus:    files,
			ListMessages:  msgs,
			ListProjects:  proj,
			Health:        true,
		},
	}
	if bin == "" {
		desc.Reason = "binario no encontrado en PATH"
	}
	return desc
}

// scanCopilot handles the special detection for GitHub Copilot. The
// binary is not always in PATH; we also look inside the VS Code
// extension bundles for github.copilot and github.copilot-chat.
func (d *Detector) scanCopilot(ctx context.Context) domain.AgentDescriptor {
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
		Kind:        domain.AgentCopilot,
		DisplayName: "GitHub Copilot",
		Bin:         bin,
		Port:        DefaultCopilotPort,
		Detected:    bin != "",
		Available:   false, // PR-5 will flip this once the LSP adapter ships.
		Capabilities: domain.AgentCapabilities{
			Health:     true,
			SendPrompt: true,
		},
	}
	if bin == "" {
		desc.Reason = "no se encontró copilot ni copilot-language-server en PATH ni en las extensiones de VS Code"
	} else {
		desc.Reason = "Próximamente (LSP)"
	}
	return desc
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
