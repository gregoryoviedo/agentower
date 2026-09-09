package agents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProbeHTTPReturnsTrueFor2xx spins up an httptest server that
// responds 200 OK and checks the probe accepts it. This is the only
// HTTP-positive path; the detector uses it to decide whether an agent
// is already running on its loopback port.
func TestProbeHTTPReturnsTrueFor2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !probeHTTP(context.Background(), srv.URL) {
		t.Fatalf("probeHTTP(%s) = false, want true for 2xx response", srv.URL)
	}
}

// TestProbeHTTPReturnsFalseForNon2xx confirms the probe refuses
// anything outside the 2xx range. 404 must NOT count as "running".
func TestProbeHTTPReturnsFalseForNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if probeHTTP(context.Background(), srv.URL) {
		t.Fatalf("probeHTTP(%s) = true, want false for 404 response", srv.URL)
	}
}

// TestProbeHTTPReturnsFalseWhenServerDown makes sure a connection
// refusal is treated as "not running". The detector relies on this to
// not mislabel a closed port as a healthy agent.
func TestProbeHTTPReturnsFalseWhenServerDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL
	srv.Close() // immediately close so the port is dead
	if probeHTTP(context.Background(), url) {
		t.Fatalf("probeHTTP(%s) = true, want false for closed port", url)
	}
}

// TestFirstDirWithPrefixPicksLexicographicallyLast is the contract
// findVSCodeCopilotBundle relies on: when VS Code ships multiple
// extension versions the user upgrades to (github.copilot-1.2.3,
// github.copilot-1.3.0, etc.), the probe must return the newest one.
// We simulate that with two sibling directories whose names sort
// alphabetically.
func TestFirstDirWithPrefixPicksLexicographicallyLast(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"github.copilot-1.0.0", "github.copilot-2.0.0", "unrelated"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := firstDirWithPrefix(filepath.Join(parent, "github.copilot-"))
	if !ok {
		t.Fatal("firstDirWithPrefix did not match the prefix")
	}
	want := filepath.Join(parent, "github.copilot-2.0.0")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestFirstDirWithPrefixReturnsFalseOnNoMatch exercises the
// "extension not installed" branch used by scanCopilot: only dirs that
// do not share the prefix are present.
func TestFirstDirWithPrefixReturnsFalseOnNoMatch(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"unrelated-1.0.0", "another-tool-2.0.0"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := firstDirWithPrefix(filepath.Join(parent, "github.copilot-")); ok {
		t.Fatal("firstDirWithPrefix returned a match when no dir shares the prefix")
	}
}

// TestFirstDirWithPrefixReturnsFalseOnMissingParent mirrors the case
// where ~/.vscode/extensions doesn't exist yet (fresh install). The
// probe must return false instead of panicking on a non-existent
// directory.
func TestFirstDirWithPrefixReturnsFalseOnMissingParent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	if _, ok := firstDirWithPrefix(filepath.Join(missing, "github.copilot-")); ok {
		t.Fatal("firstDirWithPrefix must return false for a missing parent dir")
	}
}

// TestUserHomeDirIsOverridable confirms tests can redirect
// userHomeDir so the helpers do not accidentally read the developer's
// real home directory.
func TestUserHomeDirIsOverridable(t *testing.T) {
	original := userHomeDir
	t.Cleanup(func() { userHomeDir = original })
	want := "/some/fake/home"
	userHomeDir = func() (string, error) { return want, nil }
	got, err := userHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("userHomeDir = %q, want %q", got, want)
	}
	if !strings.HasPrefix(want, "/") {
		t.Fatal("test fixture must look like a unix path")
	}
}

// TestFindVSCodeCopilotBundlePicksNewestInstalledVersion is the
// end-to-end check the detector relies on: when both
// github.copilot-1.2.3 and github.copilot-2.0.0 are installed, the
// probe must surface the 2.0.0 bundle. We override userHomeDir so the
// test does not depend on the developer's local VS Code install.
func TestFindVSCodeCopilotBundlePicksNewestInstalledVersion(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"github.copilot-1.2.3", "github.copilot-2.0.0"} {
		if err := os.MkdirAll(filepath.Join(home, ".vscode", "extensions", name, "dist"), 0o700); err != nil {
			t.Fatal(err)
		}
		// Every candidate bundle has to land a dist/extension.js
		// file; findVSCodeCopilotBundle would otherwise ignore the
		// directory.
		if err := os.WriteFile(
			filepath.Join(home, ".vscode", "extensions", name, "dist", "extension.js"),
			[]byte("module.exports = {}"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	original := userHomeDir
	t.Cleanup(func() { userHomeDir = original })
	userHomeDir = func() (string, error) { return home, nil }

	got, ok := findVSCodeCopilotBundle()
	if !ok {
		t.Fatal("findVSCodeCopilotBundle did not return a match")
	}
	want := filepath.Join(home, ".vscode", "extensions", "github.copilot-2.0.0", "dist", "extension.js")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestFindVSCodeCopilotBundleReturnsFalseWhenUninstalled makes sure
// the detector does not falsely report Copilot as available when no
// extension bundle exists on disk.
func TestFindVSCodeCopilotBundleReturnsFalseWhenUninstalled(t *testing.T) {
	home := t.TempDir()
	original := userHomeDir
	t.Cleanup(func() { userHomeDir = original })
	userHomeDir = func() (string, error) { return home, nil }
	if _, ok := findVSCodeCopilotBundle(); ok {
		t.Fatal("findVSCodeCopilotBundle returned a match with no extensions installed")
	}
}
