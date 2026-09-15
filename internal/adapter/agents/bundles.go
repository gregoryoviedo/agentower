package agents

import (
	"os"
	"path/filepath"
)

// BundleBinLookup resolves agent binaries that installers drop inside
// app bundles instead of PATH:
//
//   - Kiro ships as "Kiro CLI.app" (and the Kiro IDE as "Kiro.app");
//     the CLI binary lives under Contents/MacOS.
//   - GitHub Copilot is built into VS Code; the extension bundle lives
//     under the app's Resources/app/extensions/copilot.
//
// It is wired as the Detector's ExtraBins in the composition root so
// GUI/launchd-launched processes — which start with a minimal PATH —
// still detect these agents. The default Detector leaves ExtraBins nil
// so tests stay hermetic.
func BundleBinLookup(name string) string {
	return bundleLookup(name)
}

// bundleLookup is overridable in tests.
var bundleLookup = func(name string) string {
	home, _ := os.UserHomeDir()
	switch name {
	case "kiro":
		cli := "/Applications/Kiro CLI.app/Contents/MacOS/kiro-cli"
		ide := "/Applications/Kiro.app/Contents/MacOS/kiro"
		if home != "" {
			return firstExisting(
				cli,
				ide,
				filepath.Join(home, "Applications", "Kiro CLI.app", "Contents", "MacOS", "kiro-cli"),
				filepath.Join(home, "Applications", "Kiro.app", "Contents", "MacOS", "kiro"),
			)
		}
		return firstExisting(cli, ide)
	case "copilot":
		// Prefer the official Copilot CLI (npm @github/copilot), which
		// exposes the ACP server (`copilot --acp`) the adapter drives.
		// Fall back to the Copilot extension bundled with the VS Code
		// app so detection still lights up for editor-only installs.
		if cli := findCopilotCLI(); cli != "" {
			return cli
		}
		bundle := "/Contents/Resources/app/extensions/copilot/dist/extension.js"
		return firstExisting(
			"/Applications/Visual Studio Code.app"+bundle,
			"/Applications/Visual Studio Code - Insiders.app"+bundle,
			"/Applications/Visual Studio Code - Exploration.app"+bundle,
		)
	case "codex":
		return findNamedCLI("codex")
	case "antigravity":
		// Newer Antigravity IDE builds ship an `agy` binary inside the
		// app bundle; the standalone CLI installer drops it in
		// ~/.local/bin (already covered by findNamedCLI). Probe both.
		if cli := findNamedCLI("agy"); cli != "" {
			return cli
		}
		return firstExisting(
			"/Applications/Antigravity.app/Contents/Resources/app/bin/agy",
			"/Applications/Antigravity.app/Contents/MacOS/agy",
			"/Applications/Antigravity IDE.app/Contents/Resources/app/bin/agy",
		)
	default:
		return ""
	}
}

// findNamedCLI scans the common user/npm/Homebrew binary directories
// for a bare executable name. Used by codex and antigravity, whose
// installers do not always place the binary on the GUI PATH.
func findNamedCLI(name string) string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, "bin"),
			filepath.Join(home, ".codex", "bin"),
		)
		if matches, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); len(matches) > 0 {
			dirs = append(dirs, matches...)
		}
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	for _, dir := range dirs {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// findCopilotCLI scans the common npm-global binary directories for the
// `copilot` executable.
func findCopilotCLI() string {
	home, _ := os.UserHomeDir()
	var dirs []string
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".local", "bin"),
		)
		if matches, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); len(matches) > 0 {
			dirs = append(dirs, matches...)
		}
	}
	dirs = append(dirs, "/usr/local/bin", "/opt/homebrew/bin")
	for _, dir := range dirs {
		p := filepath.Join(dir, "copilot")
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// firstExisting returns the first path that exists and is a regular
// file; empty when none do.
func firstExisting(paths ...string) string {
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}
