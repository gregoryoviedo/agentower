//go:build !windows

package main

import "path/filepath"

// userBinDirs returns the extra directories that hold user-installed
// agent CLIs on Unix-like systems. GUI/launchd launches start with a
// minimal PATH that omits the user's shell additions, so the detector
// (and the subprocess managers that spawn by bare name) need these.
func userBinDirs(home string) []string {
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, ".npm-global", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
	// nvm-managed node versions install global binaries (e.g. the
	// Copilot CLI) under one dir per version.
	if matches, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); len(matches) > 0 {
		dirs = append(dirs, matches...)
	}
	return dirs
}
