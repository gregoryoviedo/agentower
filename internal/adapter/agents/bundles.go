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
		bundle := "/Contents/Resources/app/extensions/copilot/dist/extension.js"
		return firstExisting(
			"/Applications/Visual Studio Code.app"+bundle,
			"/Applications/Visual Studio Code - Insiders.app"+bundle,
			"/Applications/Visual Studio Code - Exploration.app"+bundle,
		)
	default:
		return ""
	}
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