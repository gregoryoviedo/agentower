package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	WorkspaceRoot   string
	StatePath       string
	TelegramToken   string
	TelegramAPIRoot string
	TelegramProxy   string
	AllowedChatID   int64
	EnvFile         string
	StaleAfter      time.Duration
	CopilotStateDir string
	ClaudeStateDir  string
	KiroStateDir    string
}

func Load() (*Config, error) {
	cfg := &Config{}

	if path, ok := locateEnv(); ok {
		if err := godotenv.Overload(path); err != nil {
			return nil, fmt.Errorf("load env file %q: %w", path, err)
		}
		cfg.EnvFile = path
	}

	cfg.WorkspaceRoot = os.Getenv("WORKSPACE_ROOT")
	if cfg.WorkspaceRoot == "" {
		return nil, errors.New("WORKSPACE_ROOT is required (set it in .env or environment)")
	}
	cfg.WorkspaceRoot = filepath.Clean(cfg.WorkspaceRoot)

	cfg.StatePath = os.Getenv("AGENTOWER_STATE_PATH")
	if cfg.StatePath == "" {
		cfg.StatePath = filepath.Join(cfg.WorkspaceRoot, ".agentower", "state.db")
	}

	cfg.TelegramToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if cfg.TelegramToken == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is required")
	}

	cfg.TelegramAPIRoot = strings.TrimSpace(os.Getenv("TELEGRAM_API_ROOT"))
	cfg.TelegramProxy = strings.TrimSpace(os.Getenv("TELEGRAM_PROXY_URL"))

	chatIDRaw := os.Getenv("ALLOWED_CHAT_ID")
	if chatIDRaw == "" {
		return nil, errors.New("ALLOWED_CHAT_ID is required")
	}
	chatID, err := strconv.ParseInt(chatIDRaw, 10, 64)
	if err != nil || chatID == 0 {
		return nil, fmt.Errorf("ALLOWED_CHAT_ID must be a non-zero integer: %q", chatIDRaw)
	}
	cfg.AllowedChatID = chatID

	cfg.StaleAfter = parseDurationEnv("AGENTOWER_STALE_AFTER", 30*time.Minute)
	cfg.CopilotStateDir = strings.TrimSpace(os.Getenv("AGENTOWER_COPILOT_STATE_DIR"))
	cfg.ClaudeStateDir = strings.TrimSpace(os.Getenv("AGENTOWER_CLAUDE_STATE_DIR"))
	cfg.KiroStateDir = strings.TrimSpace(os.Getenv("AGENTOWER_KIRO_STATE_DIR"))

	return cfg, nil
}

// parseDurationEnv reads a duration env var, falling back to def
// when unset or unparseable. Negative or zero values are clamped to
// the default so a typo in the .env cannot accidentally disable the
// staleness filter.
func parseDurationEnv(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func locateEnv() (string, bool) {
	if path := os.Getenv("ENV_FILE"); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	if _, err := os.Stat(".env"); err == nil {
		abs, _ := filepath.Abs(".env")
		return abs, true
	}
	return "", false
}
