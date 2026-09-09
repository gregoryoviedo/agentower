package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gregoryoviedo/agentower/internal/config"
)

func TestLoadFromEnvFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(`
WORKSPACE_ROOT=/Users/me/dev
TELEGRAM_BOT_TOKEN=123:abc
ALLOWED_CHAT_ID=42
AGENTOWER_STATE_PATH=/tmp/state.db
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	for _, key := range []string{"WORKSPACE_ROOT", "TELEGRAM_BOT_TOKEN", "ALLOWED_CHAT_ID", "AGENTOWER_STATE_PATH"} {
		t.Setenv(key, "")
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.WorkspaceRoot != "/Users/me/dev" {
		t.Errorf("WorkspaceRoot=%q", got.WorkspaceRoot)
	}
	if got.AllowedChatID != 42 {
		t.Errorf("AllowedChatID=%d", got.AllowedChatID)
	}
	if got.TelegramToken != "123:abc" {
		t.Errorf("TelegramToken=%q", got.TelegramToken)
	}
	if got.StatePath != "/tmp/state.db" {
		t.Errorf("StatePath=%q", got.StatePath)
	}
}

func TestLoadRequiresMandatoryFields(t *testing.T) {
	dir := t.TempDir()
	for _, key := range []string{"WORKSPACE_ROOT", "TELEGRAM_BOT_TOKEN", "ALLOWED_CHAT_ID"} {
		t.Setenv(key, "")
	}
	t.Setenv("ENV_FILE", filepath.Join(dir, "missing.env"))
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error when mandatory fields are missing")
	}
}
