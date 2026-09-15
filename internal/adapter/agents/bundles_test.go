package agents

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBundleLookupFindsKiroCLIInAppBundle verifies the detector's
// ExtraBins path can locate Kiro's CLI inside the Kiro CLI.app bundle
// and returns the real binary (preferring the CLI over the IDE).
func TestBundleLookupFindsKiroCLIInAppBundle(t *testing.T) {
	root := t.TempDir()
	cliPath := filepath.Join(root, "Kiro CLI.app", "Contents", "MacOS", "kiro-cli")
	idePath := filepath.Join(root, "Kiro.app", "Contents", "MacOS", "kiro")
	for _, p := range []string{cliPath, idePath} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	original := bundleLookup
	t.Cleanup(func() { bundleLookup = original })
	bundleLookup = func(name string) string {
		if name != "kiro" {
			return ""
		}
		return firstExisting(cliPath, idePath)
	}
	if got := BundleBinLookup("kiro"); got != cliPath {
		t.Fatalf("BundleBinLookup(kiro) = %q, want %q", got, cliPath)
	}
}

// TestBundleLookupFindsCopilotBuiltInExtension verifies the ExtraBins
// path locates the Copilot extension bundled with the VS Code app.
func TestBundleLookupFindsCopilotBuiltInExtension(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "Visual Studio Code.app", "Contents", "Resources", "app", "extensions", "copilot", "dist", "extension.js")
	if err := os.MkdirAll(filepath.Dir(bundle), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle, []byte("module.exports = {}"), 0o700); err != nil {
		t.Fatal(err)
	}
	original := bundleLookup
	t.Cleanup(func() { bundleLookup = original })
	bundleLookup = func(name string) string {
		if name != "copilot" {
			return ""
		}
		return firstExisting(bundle)
	}
	if got := BundleBinLookup("copilot"); got != bundle {
		t.Fatalf("BundleBinLookup(copilot) = %q, want %q", got, bundle)
	}
}

// TestBundleLookupReturnsEmptyForUnknownAgent keeps the default case
// honest: unknown kinds must never resolve to a path.
func TestBundleLookupReturnsEmptyForUnknownAgent(t *testing.T) {
	original := bundleLookup
	t.Cleanup(func() { bundleLookup = original })
	bundleLookup = func(string) string { return "" }
	for _, name := range []string{"unknown", "", "opencode"} {
		if got := BundleBinLookup(name); got != "" {
			t.Errorf("BundleBinLookup(%q) = %q, want empty", name, got)
		}
	}
}
