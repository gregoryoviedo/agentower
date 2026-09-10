package agents_opencode_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/domain"
)

func TestSessionLocatorReturnsFreshest(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	stale := now.Add(-2 * time.Hour).UnixMilli()
	fresh := now.Add(-30 * time.Second).UnixMilli()
	mid := now.Add(-5 * time.Minute).UnixMilli()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/session":
			// Note: ListSessions collapses the DTO; the locator
			// then re-queries /session to keep the raw timestamps.
			// We respond with the rich DTO so both calls succeed.
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":        "ses_stale",
					"projectID": "proj-1",
					"title":     "old work",
					"directory": "/tmp/old",
					"time":      map[string]any{"created": stale, "updated": stale},
				},
				{
					"id":        "ses_fresh",
					"projectID": "proj-2",
					"title":     "current work",
					"directory": "/tmp/now",
					"time":      map[string]any{"created": mid, "updated": fresh},
				},
			})
		case "/session/ses_fresh/message":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"info": map[string]any{
					"id":        "m1",
					"sessionID": "ses_fresh",
					"role":      "assistant",
				},
				"parts": []map[string]any{{"type": "text", "text": "all done"}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := agents_opencode.NewClient(srv.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	loc := agents_opencode.NewSessionLocator(client)

	sess, err := loc.Locate(context.Background())
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if sess.SessionID != "ses_fresh" {
		t.Fatalf("session id = %q, want ses_fresh", sess.SessionID)
	}
	if sess.Project != "now" {
		t.Fatalf("project = %q, want now", sess.Project)
	}
	if sess.Title != "current work" {
		t.Fatalf("title = %q, want current work", sess.Title)
	}
	if sess.Preview != "all done" {
		t.Fatalf("preview = %q, want all done", sess.Preview)
	}
	if !sess.TouchedAt.Equal(time.UnixMilli(fresh).UTC()) {
		t.Fatalf("touchedAt = %s, want %s", sess.TouchedAt, time.UnixMilli(fresh).UTC())
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected at least 2 HTTP calls (list + preview), got %d", got)
	}
}

func TestSessionLocatorNoSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/session" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := agents_opencode.NewClient(srv.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	loc := agents_opencode.NewSessionLocator(client)

	_, err = loc.Locate(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, domain.ErrNoActiveSession) {
		t.Fatalf("expected ErrNoActiveSession, got %v", err)
	}
}

func TestSessionLocatorServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := agents_opencode.NewClient(srv.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	loc := agents_opencode.NewSessionLocator(client)
	if _, err := loc.Locate(context.Background()); err == nil {
		t.Fatal("expected error from server, got nil")
	}
}

func TestSessionLocatorKindMatches(t *testing.T) {
	loc := agents_opencode.NewSessionLocator(nil) // client is nil, we only need Kind()
	if loc.Kind() != domain.AgentOpenCode {
		t.Fatalf("Kind = %q, want opencode", loc.Kind())
	}
	_ = fmt.Sprint(loc) // ensure Stringer-free
}
