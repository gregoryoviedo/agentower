package usecase_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/control"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

// questionStubAdapter is an opencode adapter that records the answers it
// receives through the QuestionAdapter port.
type questionStubAdapter struct {
	replied   [][]string
	sessionID string
	requestID string
}

func (a *questionStubAdapter) Kind() domain.AgentKind { return domain.AgentOpenCode }
func (a *questionStubAdapter) DisplayName() string    { return "opencode" }
func (a *questionStubAdapter) Health(context.Context) (domain.HealthStatus, error) {
	return domain.HealthStatus{Healthy: true}, nil
}
func (a *questionStubAdapter) ListProjects(context.Context) ([]domain.Project, error) {
	return nil, nil
}
func (a *questionStubAdapter) ListSessions(context.Context) ([]domain.Session, error) {
	return nil, nil
}
func (a *questionStubAdapter) CreateSession(context.Context, string) (domain.Session, error) {
	return domain.Session{}, nil
}
func (a *questionStubAdapter) SendPrompt(context.Context, string, string) (string, error) {
	return "", nil
}
func (a *questionStubAdapter) Revert(context.Context, string) error { return nil }
func (a *questionStubAdapter) FileStatus(context.Context, string) ([]domain.FileChange, error) {
	return nil, nil
}
func (a *questionStubAdapter) ListMessages(context.Context, string) ([]domain.Message, error) {
	return nil, nil
}
func (a *questionStubAdapter) ListQuestions(context.Context, string) ([]domain.PendingQuestion, error) {
	return nil, nil
}
func (a *questionStubAdapter) ReplyQuestion(_ context.Context, sessionID, requestID string, answers [][]string) error {
	a.sessionID = sessionID
	a.requestID = requestID
	a.replied = answers
	return nil
}

func newQuestionHandler(t *testing.T, adapter *questionStubAdapter) (*usecase.Handler, *control.Publisher) {
	t.Helper()
	root := t.TempDir()
	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	navigation := usecase.NewNavigationService(browser, store)
	handler := usecase.NewHandler(navigation, store, &fakeRegistry{client: adapter}, &fakeServer{started: true}, browser)
	publisher := control.NewPublisher()
	handler.SetQuestionBroker(publisher)
	return handler, publisher
}

func singleSelectPending() domain.PendingQuestion {
	return domain.PendingQuestion{
		ChatID:    42,
		SessionID: "ses1",
		AgentKind: domain.AgentOpenCode,
		RequestID: "req1",
		Questions: []domain.QuestionPrompt{{
			Header:   "Modo",
			Question: "¿Cómo lo hago?",
			Options:  []domain.QuestionOption{{Label: "Opción 1"}, {Label: "Opción 2"}},
			Custom:   true,
		}},
	}
}

func TestHandlerAnswersSingleQuestionViaButton(t *testing.T) {
	adapter := &questionStubAdapter{}
	handler, publisher := newQuestionHandler(t, adapter)
	publisher.SetPendingQuestion(singleSelectPending())

	resp, err := handler.HandleCallback(context.Background(), 42, "q|42|0|1")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if len(adapter.replied) != 1 || len(adapter.replied[0]) != 1 || adapter.replied[0][0] != "Opción 2" {
		t.Fatalf("replied = %+v, want [[Opción 2]]", adapter.replied)
	}
	if adapter.sessionID != "ses1" || adapter.requestID != "req1" {
		t.Fatalf("routing = %s/%s, want ses1/req1", adapter.sessionID, adapter.requestID)
	}
	if _, ok := publisher.PendingQuestion(42); ok {
		t.Fatal("pending question should be cleared after the last answer")
	}
	if !strings.Contains(resp.Text, "Listo") {
		t.Fatalf("response = %q, want confirmation", resp.Text)
	}
}

func TestHandlerAnswersMultipleQuestionsSequentially(t *testing.T) {
	adapter := &questionStubAdapter{}
	handler, publisher := newQuestionHandler(t, adapter)
	publisher.SetPendingQuestion(domain.PendingQuestion{
		ChatID:    42,
		SessionID: "ses1",
		AgentKind: domain.AgentOpenCode,
		RequestID: "req2",
		Questions: []domain.QuestionPrompt{
			{Header: "Uno", Question: "primera", Options: []domain.QuestionOption{{Label: "A"}, {Label: "B"}}},
			{Header: "Dos", Question: "segunda", Options: []domain.QuestionOption{{Label: "Sí"}, {Label: "No"}}},
		},
	})

	if _, err := handler.HandleCallback(context.Background(), 42, "q|42|0|1"); err != nil {
		t.Fatal(err)
	}
	if len(adapter.replied) != 0 {
		t.Fatalf("must not submit before all answers: %+v", adapter.replied)
	}
	pending, ok := publisher.PendingQuestion(42)
	if !ok || len(pending.Answers) < 1 || pending.Answers[0][0] != "B" {
		t.Fatalf("first answer not stored: %+v", pending)
	}

	if _, err := handler.HandleCallback(context.Background(), 42, "q|42|1|0"); err != nil {
		t.Fatal(err)
	}
	if len(adapter.replied) != 2 || adapter.replied[1][0] != "Sí" {
		t.Fatalf("replied = %+v, want [[B] [Sí]]", adapter.replied)
	}
	if _, ok := publisher.PendingQuestion(42); ok {
		t.Fatal("pending question should be cleared after all answers")
	}
}

func TestHandlerAnswersQuestionWithFreeText(t *testing.T) {
	adapter := &questionStubAdapter{}
	handler, publisher := newQuestionHandler(t, adapter)
	publisher.SetPendingQuestion(singleSelectPending())

	resp, err := handler.HandleText(context.Background(), 42, "hazlo de la otra forma")
	if err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if len(adapter.replied) != 1 || adapter.replied[0][0] != "hazlo de la otra forma" {
		t.Fatalf("replied = %+v, want the typed text", adapter.replied)
	}
	if _, ok := publisher.PendingQuestion(42); ok {
		t.Fatal("pending question should be cleared after the typed answer")
	}
	if !strings.Contains(resp.Text, "Listo") {
		t.Fatalf("response = %q, want confirmation", resp.Text)
	}
}

func TestHandlerRejectsForeignChatQuestionCallback(t *testing.T) {
	adapter := &questionStubAdapter{}
	handler, publisher := newQuestionHandler(t, adapter)
	publisher.SetPendingQuestion(singleSelectPending())

	if _, err := handler.HandleCallback(context.Background(), 43, "q|42|0|0"); err != nil {
		t.Fatal(err)
	}
	if len(adapter.replied) != 0 {
		t.Fatalf("foreign chat must not answer: %+v", adapter.replied)
	}
	if _, ok := publisher.PendingQuestion(42); !ok {
		t.Fatal("pending question should remain")
	}
}
