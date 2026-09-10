package usecase_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

// newContinuarFixture wires a Handler with the bare minimum the
// /continuar flow needs: a workspace browser, a SQLite state, the
// fake opencode server (the handler does not call it for /continuar
// directly, but other handler methods may), and an empty locator
// registry the test can populate.
//
// The clock is a fixed time the handler uses for staleness math
// (replacing the default time.Now via SetStaleAfter tricks is not
// enough because the freshness text relies on Now too; we accept
// the small drift and assert on second-precise outputs).
func newContinuarFixture(t *testing.T, workspaceDir string, now time.Time) (*usecase.Handler, *sqlite.Repository, domain.ActiveLocatorRegistry) {
	t.Helper()
	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, workspaceDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/global/health":
			_, _ = w.Write([]byte(`{"healthy":true,"version":"dev"}`))
		case "/session":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, err := agents_opencode.NewClient(server.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	reg := usecase.NewActiveLocatorRegistry()
	fake := &fakeServer{started: true, cwd: workspaceDir}
	handler := usecase.NewHandler(
		usecase.NewNavigationService(browser, store),
		store,
		&fakeRegistry{client: client},
		fake,
		browser,
	)
	handler.SetActiveLocators(reg)
	handler.SetClock(func() time.Time { return now })
	_ = server
	return handler, store, reg
}

// contains is a case-sensitive substring check kept local so the
// tests can express intent without pulling in strings.Contains at
// every call site.
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// primaryConfirmButton finds the first button whose data starts
// with "cc|" — that is the "yes, continue" quick-tap.
func primaryConfirmButton(resp domain.BotResponse) string {
	for _, row := range resp.Buttons {
		for _, b := range row {
			if strings.HasPrefix(b.Data, "cc|") {
				return b.Data
			}
		}
	}
	return ""
}

// silenceUnused keeps `context` imported even if every test goes
// through HandleCommand/HandleCallback; it costs nothing and keeps
// the test file compiling as we add more cases.
var _ = context.Background
