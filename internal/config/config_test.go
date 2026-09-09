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
OPENCODE_PORT=4096
AGENTOWER_STATE_PATH=/tmp/state.db
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	for _, key := range []string{"WORKSPACE_ROOT", "TELEGRAM_BOT_TOKEN", "ALLOWED_CHAT_ID", "OPENCODE_PORT", "AGENTOWER_STATE_PATH"} {
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
	if got.OpenCodePort != 4096 {
		t.Errorf("OpenCodePort=%d", got.OpenCodePort)
	}
	if got.TelegramToken != "123:abc" {
		t.Errorf("TelegramToken=%q", got.TelegramToken)
	}
	if got.AutoStart {
		t.Errorf("AutoStart=%v, want false (default)", got.AutoStart)
	}
	if got.OpenCodeBin != "opencode" {
		t.Errorf("OpenCodeBin=%q, want %q", got.OpenCodeBin, "opencode")
	}
}

func TestLoadAutoStartDefault(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(`
WORKSPACE_ROOT=/Users/me/dev
TELEGRAM_BOT_TOKEN=123:abc
ALLOWED_CHAT_ID=42
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	for _, key := range []string{"WORKSPACE_ROOT", "TELEGRAM_BOT_TOKEN", "ALLOWED_CHAT_ID"} {
		t.Setenv(key, "")
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AutoStart {
		t.Errorf("AutoStart=%v, want false (default)", got.AutoStart)
	}
}

func TestLoadAutoStartOverride(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(`
WORKSPACE_ROOT=/Users/me/dev
TELEGRAM_BOT_TOKEN=123:abc
ALLOWED_CHAT_ID=42
OPENCODE_BIN=/opt/custom/opencode
OPENCODE_AUTOSTART=false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	for _, key := range []string{"WORKSPACE_ROOT", "TELEGRAM_BOT_TOKEN", "ALLOWED_CHAT_ID", "OPENCODE_BIN", "OPENCODE_AUTOSTART"} {
		t.Setenv(key, "")
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AutoStart {
		t.Errorf("AutoStart=%v, want false", got.AutoStart)
	}
	if got.OpenCodeBin != "/opt/custom/opencode" {
		t.Errorf("OpenCodeBin=%q", got.OpenCodeBin)
	}
}

func TestLoadAutoStartRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte(`
WORKSPACE_ROOT=/Users/me/dev
TELEGRAM_BOT_TOKEN=123:abc
ALLOWED_CHAT_ID=42
OPENCODE_AUTOSTART=maybe
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENV_FILE", envPath)
	for _, key := range []string{"WORKSPACE_ROOT", "TELEGRAM_BOT_TOKEN", "ALLOWED_CHAT_ID", "OPENCODE_AUTOSTART"} {
		t.Setenv(key, "")
	}

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for invalid OPENCODE_AUTOSTART")
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
