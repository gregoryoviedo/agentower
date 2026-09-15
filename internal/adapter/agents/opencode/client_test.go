package agents_opencode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
)

func TestClientUsesOpenCodeAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/global/health":
			fmt.Fprint(w, `{"healthy":true,"version":"dev"}`)
		case r.URL.Path == "/project":
			fmt.Fprint(w, `[{"id":"p1","worktree":"/dev/work1","time":{"updated":1700000000000}}]`)
		case r.URL.Path == "/session" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[{"id":"s1","projectID":"p1","title":"Main","directory":"/dev/work1","slug":"main","summary":{},"time":{"created":1700000000000,"updated":1700000000000}}]`)
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"id":"s2","projectID":"p1","title":"New","directory":"/dev/work1","slug":"new","summary":{},"time":{"created":1700000000000,"updated":1700000000000}}`)
		case r.URL.Path == "/session/s1/diff":
			fmt.Fprint(w, `[{"path":"README.md","status":"modified"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := agents_opencode.NewClient(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	health, err := client.Health(context.Background())
	if err != nil || !health.Healthy || health.Version != "dev" {
		t.Fatalf("health = %#v, err = %v", health, err)
	}
	projects, err := client.ListProjects(context.Background())
	if err != nil || len(projects) != 1 || projects[0].ID != "p1" || projects[0].AbsolutePath != "/dev/work1" {
		t.Fatalf("projects = %#v, err = %v", projects, err)
	}
	sessions, err := client.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].ID != "s1" || sessions[0].Directory != "/dev/work1" {
		t.Fatalf("sessions = %#v, err = %v", sessions, err)
	}
	created, err := client.CreateSession(context.Background(), "")
	if err != nil || created.ID != "s2" {
		t.Fatalf("create = %#v, err = %v", created, err)
	}
	changes, err := client.FileStatus(context.Background(), "s1")
	if err != nil || len(changes) != 1 || changes[0].Path != "README.md" {
		t.Fatalf("changes = %#v, err = %v", changes, err)
	}
}

func TestClientSendPromptReturnsAssistantText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/s1/message" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"info":{"id":"m2","role":"assistant"},"parts":[
			{"type":"step-start","text":""},
			{"type":"text","text":"primera parte"},
			{"type":"text","text":"segunda parte"}
		]}`)
	}))
	defer server.Close()

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := client.SendPrompt(context.Background(), "s1", "hola")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if want := "primera parte\n\nsegunda parte"; reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
	}
}

// TestClientListQuestionsParsesOptionsAndFiltersSession covers the
// canonical /api/question response: one request with options, filtered to
// the requested session.
func TestClientListQuestionsParsesOptionsAndFiltersSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/question":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[
				{"id":"q1","sessionID":"s1","questions":[
					{"header":"Modo","question":"¿Cómo lo hago?","options":[
						{"label":"Opción 1","description":"Recomendado"},
						{"label":"Opción 2","description":"Otra"}
					],"multiple":false,"custom":true}
				]},
				{"id":"q9","sessionID":"other","questions":[{"question":"x","options":[{"label":"y"}]}]}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	questions, err := client.ListQuestions(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(questions))
	}
	q := questions[0]
	if q.RequestID != "q1" || q.SessionID != "s1" {
		t.Fatalf("request = %+v", q)
	}
	if len(q.Questions) != 1 || q.Questions[0].Header != "Modo" {
		t.Fatalf("prompt = %+v", q.Questions)
	}
	if len(q.Questions[0].Options) != 2 || q.Questions[0].Options[0].Label != "Opción 1" {
		t.Fatalf("options = %+v", q.Questions[0].Options)
	}
	if !q.Questions[0].Custom {
		t.Fatal("custom should default to true")
	}
}

// TestClientListQuestionsFallsBackToLegacyPath covers an older opencode
// that only exposes /question and returns a single flattened question.
func TestClientListQuestionsFallsBackToLegacyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/question" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"q2","sessionId":"s1","question":"¿Cuál?","header":"H","options":[{"label":"A"}],"custom":false}`)
	}))
	defer server.Close()

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	questions, err := client.ListQuestions(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(questions) != 1 || questions[0].RequestID != "q2" {
		t.Fatalf("questions = %+v", questions)
	}
	if len(questions[0].Questions) != 1 || questions[0].Questions[0].Question != "¿Cuál?" {
		t.Fatalf("prompt = %+v", questions[0].Questions)
	}
	if questions[0].Questions[0].Custom {
		t.Fatal("custom=false should be preserved")
	}
}

// TestClientReplyQuestionPostsAnswers checks the V2 reply route and body,
// falling back to the V1 route when the first returns 404.
func TestClientReplyQuestionPostsAnswers(t *testing.T) {
	var gotPath string
	var gotAnswers [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/session/s1/question/q1/reply" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/api/session/s1/question/request/q1/reply" {
			http.NotFound(w, r)
			return
		}
		gotPath = r.URL.Path
		var body struct {
			Answers [][]string `json:"answers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotAnswers = body.Answers
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ReplyQuestion(context.Background(), "s1", "q1", [][]string{{"Opción 1"}}); err != nil {
		t.Fatalf("ReplyQuestion: %v", err)
	}
	if gotPath != "/api/session/s1/question/request/q1/reply" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(gotAnswers) != 1 || len(gotAnswers[0]) != 1 || gotAnswers[0][0] != "Opción 1" {
		t.Fatalf("answers = %+v", gotAnswers)
	}
}

func TestClientRevertPicksLastUserMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/session/s1/revert":
			var body struct {
				MessageID string `json:"messageID"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if body.MessageID != "msg_user_2" {
				http.Error(w, "expected last user id", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `{"ok":true}`)
		case "/session/s1/message":
			fmt.Fprint(w, `[
				{"info":{"id":"msg_user_1","role":"user"}},
				{"info":{"id":"msg_assist_1","role":"assistant"}},
				{"info":{"id":"msg_user_2","role":"user"}}
			]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Revert(context.Background(), "s1"); err != nil {
		t.Fatalf("revert: %v", err)
	}
}
