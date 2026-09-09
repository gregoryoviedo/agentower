package agents_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func TestDetectorReturnsOneDescriptorPerKind(t *testing.T) {
	d := agents.NewDetector()
	d.LookPath = func(name string) (string, error) {
		switch name {
		case "opencode", "claude", "codex", "kiro":
			return "/usr/local/bin/" + name, nil
		default:
			return "", errors.New("not found")
		}
	}
	got := d.Scan(context.Background())
	if len(got) != len(domain.AllAgentKinds()) {
		t.Fatalf("expected %d descriptors, got %d", len(domain.AllAgentKinds()), len(got))
	}
	for _, desc := range got {
		switch desc.Kind {
		case domain.AgentOpenCode, domain.AgentClaude:
			if !desc.Available {
				t.Errorf("%s should be Available when binary is present", desc.Kind)
			}
		default:
			if desc.Available {
				t.Errorf("%s should be Available=false until its adapter lands", desc.Kind)
			}
			if (desc.Bin != "") != desc.Detected {
				t.Errorf("%s Detected mismatch: Bin=%q Detected=%v", desc.Kind, desc.Bin, desc.Detected)
			}
		}
	}
}

func TestDetectorMarksUnavailableWhenBinaryMissing(t *testing.T) {
	d := agents.NewDetector()
	d.LookPath = func(string) (string, error) { return "", errors.New("nope") }
	got := d.Scan(context.Background())
	for _, desc := range got {
		if desc.Available {
			t.Errorf("%s should be Available=false when binary missing", desc.Kind)
		}
		if desc.Detected {
			t.Errorf("%s should be Detected=false when binary missing", desc.Kind)
		}
	}
}
