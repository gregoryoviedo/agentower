package copilot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/gregoryoviedo/agentower/internal/domain"
)

// Adapter is the GitHub Copilot AgentAdapter. It drives the Copilot
// Language Server over JSON-RPC and uses
// textDocument/inlineCompletion as the SendPrompt transport today.
// Other capabilities return ErrAgentCapabilitiesLimited because the
// upstream LSP surface does not yet expose stable endpoints for them.
type Adapter struct {
	manager *Manager
}

func NewAdapter(mgr *Manager) *Adapter {
	return &Adapter{manager: mgr}
}

var _ domain.AgentAdapter = (*Adapter)(nil)

func (a *Adapter) Kind() domain.AgentKind { return domain.AgentCopilot }

func (a *Adapter) DisplayName() string { return "GitHub Copilot" }

func (a *Adapter) Health(_ context.Context) (domain.HealthStatus, error) {
	if a.manager.Started() {
		return domain.HealthStatus{Healthy: true}, nil
	}
	return domain.HealthStatus{}, errors.New("copilot manager not running")
}

func (a *Adapter) ListProjects(_ context.Context) ([]domain.Project, error) {
	return nil, nil
}

func (a *Adapter) ListSessions(_ context.Context) ([]domain.Session, error) {
	return nil, nil
}

// CreateSession allocates a fresh session id and primes a stub text
// document on the language server side so subsequent inline completion
// requests have something to operate on.
func (a *Adapter) CreateSession(ctx context.Context, _ string) (domain.Session, error) {
	id := newSessionID()
	cl, err := a.manager.Client(ctx)
	if err != nil {
		return domain.Session{}, err
	}
	uri := "file:///" + a.manager.WorkingDir() + "/" + id + ".md"
	doc := map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": "markdown",
			"version":    1,
			"text":       "",
		},
	}
	if err := cl.notify("textDocument/didOpen", doc); err != nil {
		return domain.Session{}, fmt.Errorf("didOpen: %w", err)
	}
	return domain.Session{ID: id, ProjectID: "", Title: "Copilot " + id[:8]}, nil
}

// SendPrompt posts the prompt to the Copilot LSP by writing it as
// the document text and requesting an inline completion. The
// completion's `insertText` is returned as the reply.
func (a *Adapter) SendPrompt(ctx context.Context, sessionID, text string) (string, error) {
	cl, err := a.manager.Client(ctx)
	if err != nil {
		return "", err
	}
	uri := "file:///" + a.manager.WorkingDir() + "/" + sessionID + ".md"
	didChange := map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 2},
		"contentChanges": []map[string]any{{
			"text": text,
		}},
	}
	if err := cl.notify("textDocument/didChange", didChange); err != nil {
		return "", fmt.Errorf("didChange: %w", err)
	}
	var completion struct {
		Items []struct {
			InsertText string `json:"insertText"`
		} `json:"items"`
	}
	params := map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 0, "character": len(text)},
		"context":      map[string]any{"triggerKind": 1},
	}
	if err := cl.call(ctx, "textDocument/inlineCompletion", params, &completion); err != nil {
		return "", fmt.Errorf("inlineCompletion: %w", err)
	}
	if len(completion.Items) == 0 {
		return "", nil
	}
	return completion.Items[0].InsertText, nil
}

// Revert returns ErrAgentCapabilitiesLimited. The Copilot LSP does
// not expose a per-prompt revert endpoint today.
func (a *Adapter) Revert(_ context.Context, _ string) error {
	return fmt.Errorf("%w: Copilot LSP does not expose a revert endpoint", domain.ErrAgentCapabilitiesLimited)
}

// FileStatus falls back to git diff because Copilot does not expose
// file diffs natively.
func (a *Adapter) FileStatus(ctx context.Context, _ string) ([]domain.FileChange, error) {
	workdir := a.manager.WorkingDir()
	if workdir == "" {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", workdir, "diff", "--name-status")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	changes := []domain.FileChange{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		changes = append(changes, domain.FileChange{Path: fields[1], Status: gitStatusToLabel(fields[0])})
	}
	return changes, nil
}

// ListMessages is not implemented today because the Copilot LSP does
// not expose a session history format we can parse.
func (a *Adapter) ListMessages(_ context.Context, _ string) ([]domain.Message, error) {
	return nil, fmt.Errorf("%w: Copilot LSP does not expose a session history", domain.ErrAgentCapabilitiesLimited)
}

func newSessionID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func gitStatusToLabel(code string) string {
	switch code {
	case "M":
		return "modified"
	case "A":
		return "added"
	case "D":
		return "deleted"
	case "R", "C":
		return "renamed"
	default:
		return code
	}
}
