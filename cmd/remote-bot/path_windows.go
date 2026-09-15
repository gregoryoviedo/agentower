//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// userBinDirs returns the extra directories that hold user-installed
// agent CLIs on Windows. The tray wrapper spawns the bot with the
// environment it inherited from Explorer, which may omit per-user
// install dirs (npm shims, Bun's global bin, the opencode/Claude native
// installers, per-user Programs). nvm-windows keeps one dir per node
// version too.
func userBinDirs(home string) []string {
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".bun", "bin"),
		filepath.Join(home, ".opencode", "bin"),
		filepath.Join(home, ".npm-global", "bin"),
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		dirs = append(dirs, filepath.Join(appData, "npm"))
	}
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		dirs = append(dirs, filepath.Join(localAppData, "Programs"))
	}
	if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
		dirs = append(dirs, filepath.Join(programFiles, "nodejs"))
	}
	// nvm-windows installs node versions under NVM_HOME or ~/.nvm.
	nvmHome := os.Getenv("NVM_HOME")
	if nvmHome == "" {
		nvmHome = filepath.Join(home, ".nvm")
	}
	if matches, _ := filepath.Glob(filepath.Join(nvmHome, "v*")); len(matches) > 0 {
		dirs = append(dirs, matches...)
	}
	return dirs
}
