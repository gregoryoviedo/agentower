package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// controlEndpointFilename is the file the macOS wrapper reads to discover
// which TCP port the bot's local control server is listening on. The name
// is intentionally short so it survives log rotation and shows up cleanly
// in the bot's directory listing.
const controlEndpointFilename = "control.json"

type controlEndpoint struct {
	Addr string `json:"addr"`
}

// writeControlEndpoint persists the bound control address so the macOS
// wrapper can poll it without re-implementing the port picker. Errors are
// returned to the caller; missing directories are created on demand.
func writeControlEndpoint(dir, addr string) error {
	if dir == "" {
		return fmt.Errorf("control endpoint directory must not be empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, controlEndpointFilename)
	data, err := json.Marshal(controlEndpoint{Addr: addr})
	if err != nil {
		return fmt.Errorf("marshal control endpoint: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// removeControlEndpoint clears the persisted endpoint. Called when the bot
// shuts down so a stale address doesn't linger for the wrapper to read.
func removeControlEndpoint(dir string) error {
	path := filepath.Join(dir, controlEndpointFilename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
