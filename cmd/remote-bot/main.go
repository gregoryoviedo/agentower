package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gregoryoviedo/agentower/internal/adapter/agents"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/claude"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/codex"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/copilot"
	"github.com/gregoryoviedo/agentower/internal/adapter/agents/kiro"
	agents_opencode "github.com/gregoryoviedo/agentower/internal/adapter/agents/opencode"
	"github.com/gregoryoviedo/agentower/internal/adapter/storage/sqlite"
	"github.com/gregoryoviedo/agentower/internal/adapter/telegram"
	"github.com/gregoryoviedo/agentower/internal/adapter/workspace"
	"github.com/gregoryoviedo/agentower/internal/config"
	"github.com/gregoryoviedo/agentower/internal/control"
	"github.com/gregoryoviedo/agentower/internal/domain"
	"github.com/gregoryoviedo/agentower/internal/usecase"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(2)
	}
	if cfg.EnvFile != "" {
		logger.Info("configuration loaded from .env file", "path", cfg.EnvFile)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.StatePath), 0o700); err != nil {
		logger.Error("create state directory", "error", err)
		os.Exit(1)
	}

	browser, err := usecase.NewWorkspaceBrowser(workspace.OSFileSystem{}, cfg.WorkspaceRoot)
	if err != nil {
		logger.Error("initialize workspace", "error", err, "workspace", cfg.WorkspaceRoot)
		os.Exit(1)
	}

	repository, err := sqlite.Open(cfg.StatePath)
	if err != nil {
		logger.Error("open state repository", "error", err)
		os.Exit(1)
	}
	defer repository.Close()

	stopContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	detector := agents.NewDetector()
	descriptors := detector.Scan(stopContext)
	opencodeDescriptor := descriptors[0]
	for _, d := range descriptors {
		if d.Kind == domain.AgentOpenCode {
			opencodeDescriptor = d
			break
		}
	}
	opencodeClient, err := agents_opencode.NewClient("http://127.0.0.1:"+strconv.Itoa(agents.DefaultOpenCodePort), nil)
	if err != nil {
		logger.Error("initialize opencode client", "error", err)
		os.Exit(1)
	}
	opencodeManager := agents_opencode.NewManager(agents_opencode.ManagerOptions{
		Bin:    opencodeBin(descriptors, opencodeDescriptor),
		Port:   agents.DefaultOpenCodePort,
		Logger: logger.With("component", "opencode-server"),
	})
	claudeManager := claude.NewManager("claude", agents.DefaultClaudePort)
	codexManager := codex.NewManager("codex", agents.DefaultCodexPort)
	kiroManager := kiro.NewManager("kiro", agents.DefaultKiroPort)
	copilotDescriptor := findCopilotDescriptor(descriptors)
	copilotManager := copilot.NewManager(copilotLaunchConfig(copilotDescriptor))

	registry := agents.NewRegistry(agents.RegistryOptions{
		Descriptors: descriptors,
		State:       repository,
	})
	registry.Register(domain.AgentOpenCode, func() (domain.AgentAdapter, error) {
		return opencodeClient, nil
	})
	// The third-party adapters are wired only when the binary was
	// detected at boot so users without the CLI installed don't see a
	// registered adapter that the manager cannot drive.
	if hasDetectedClaude(descriptors) {
		registry.Register(domain.AgentClaude, func() (domain.AgentAdapter, error) {
			return claude.NewAdapter(claudeManager), nil
		})
	}
	if hasDetectedCodex(descriptors) {
		registry.Register(domain.AgentCodex, func() (domain.AgentAdapter, error) {
			return codex.NewAdapter(codexManager), nil
		})
	}
	if hasDetectedKiro(descriptors) {
		registry.Register(domain.AgentKiro, func() (domain.AgentAdapter, error) {
			return kiro.NewAdapter(kiroManager), nil
		})
	}
	if copilotDescriptor.Bin != "" {
		registry.Register(domain.AgentCopilot, func() (domain.AgentAdapter, error) {
			return copilot.NewAdapter(copilotManager), nil
		})
	}
	if !opencodeDescriptor.Available {
		// If opencode is missing we still expose the descriptor so the
		// UI can show the Próximamente card, but the registry's
		// Available() returns false. The boot log makes this loud.
		logger.Warn("opencode binary not found in PATH", "expected_bin", "opencode")
	}
	serverManager := agents.NewOpenCodeServerManager(opencodeManager)
	_ = claudeManager  // kept alive for the lifetime of the bot; sessions spawn on first use
	_ = codexManager   // same: spawned per session on first use
	_ = kiroManager    // same
	_ = copilotManager // same: lazily dials the LSP server on first use

	logger.Info("opencode autostart disabled; send /init from Telegram to bring the server up",
		"port", agents.DefaultOpenCodePort)
	defer serverManager.StopAll()

	navigation := usecase.NewNavigationService(browser, repository)
	publisher := control.NewPublisher()
	handler := usecase.NewHandler(navigation, repository, registry, serverManager, browser)
	handler.SetSessionEventLog(repository)
	handler.SetSnapshotPublisher(publisher)

	watcher := usecase.NewSessionWatcher(opencodeClient, repository, repository, publisher, usecase.SessionWatcherOptions{
		PollInterval:  5 * time.Second,
		IdleThreshold: 30 * time.Second,
		Logger:        logger.With("component", "session-watcher"),
	})
	handler.SetSessionController(watcher)
	go func() {
		watcher.Run(stopContext)
	}()

	bot, err := telegram.New(telegram.Config{
		Token:         cfg.TelegramToken,
		AllowedChatID: cfg.AllowedChatID,
		APIRoot:       cfg.TelegramAPIRoot,
		ProxyURL:      cfg.TelegramProxy,
	}, handler, logger.With("component", "bot"))
	if err != nil {
		logger.Error("initialize Telegram adapter", "error", err)
		os.Exit(1)
	}
	handler.SetNotifier(bot)

	controlServer := control.NewServer(publisher, repository, publisher, bot, logger.With("component", "control"))
	controlAddr := os.Getenv("AGENTOWER_CONTROL_ADDR")
	if controlAddr == "" {
		controlAddr = "127.0.0.1:0"
	}
	if err := controlServer.Start(stopContext, controlAddr); err != nil {
		logger.Error("start control server", "error", err)
		os.Exit(1)
	}
	if err := writeControlEndpoint(filepath.Dir(cfg.StatePath), controlServer.Addr()); err != nil {
		logger.Warn("write control endpoint file", "error", err)
	}
	defer func() {
		if err := removeControlEndpoint(filepath.Dir(cfg.StatePath)); err != nil {
			logger.Warn("remove control endpoint file", "error", err)
		}
	}()
	logger.Info("control server listening", "addr", controlServer.Addr())

	go func() {
		<-stopContext.Done()
		bot.Stop()
	}()

	logger.Info("remote bot started",
		"workspace", browser.Root(),
		"opencode_port", agents.DefaultOpenCodePort,
		"state_path", cfg.StatePath,
		"available_agents", registry.Available(),
	)
	bot.Start()
}

// hasDetectedClaude returns true when the detector marked the Claude
// slot as available so the composition root knows whether to register
// the adapter with the registry.
func hasDetectedClaude(descriptors []domain.AgentDescriptor) bool {
	for _, d := range descriptors {
		if d.Kind == domain.AgentClaude && d.Available {
			return true
		}
	}
	return false
}

// hasDetectedCodex mirrors hasDetectedClaude for the codex adapter.
func hasDetectedCodex(descriptors []domain.AgentDescriptor) bool {
	for _, d := range descriptors {
		if d.Kind == domain.AgentCodex && d.Available {
			return true
		}
	}
	return false
}

// hasDetectedKiro mirrors hasDetectedClaude for the kiro adapter.
func hasDetectedKiro(descriptors []domain.AgentDescriptor) bool {
	for _, d := range descriptors {
		if d.Kind == domain.AgentKiro && d.Available {
			return true
		}
	}
	return false
}

// findCopilotDescriptor returns the copilot descriptor so we can pick
// the right binary (CLI vs VS Code bundle) for the LSP launch config.
func findCopilotDescriptor(descriptors []domain.AgentDescriptor) domain.AgentDescriptor {
	for _, d := range descriptors {
		if d.Kind == domain.AgentCopilot {
			return d
		}
	}
	return domain.AgentDescriptor{Kind: domain.AgentCopilot}
}

// copilotLaunchConfig returns the LaunchConfig that matches the
// descriptor the detector reported. If the binary lives in PATH the
// adapter spawns it directly via LaunchCLI; otherwise we assume the
// path is a VS Code extension.js bundle and run it through node via
// LaunchBundle.
func copilotLaunchConfig(desc domain.AgentDescriptor) copilot.LaunchConfig {
	if desc.Bin == "" {
		return copilot.LaunchConfig{}
	}
	if isVSCodeCopilotBundle(desc.Bin) {
		return copilot.LaunchConfig{Mode: copilot.LaunchBundle, Bin: "node", Bundle: desc.Bin}
	}
	return copilot.LaunchConfig{Mode: copilot.LaunchCLI, Bin: desc.Bin}
}

func isVSCodeCopilotBundle(path string) bool {
	return strings.HasSuffix(path, "/dist/extension.js") || strings.Contains(path, ".vscode") && strings.HasSuffix(path, "extension.js")
}

// opencodeBin returns the binary the opencode manager should spawn. If
// the detector located the binary in PATH we use that absolute path;
// otherwise we fall back to the bare "opencode" name so the manager
// resolves it again through the parent's PATH at spawn time.
func opencodeBin(descriptors []domain.AgentDescriptor, opencode domain.AgentDescriptor) string {
	if opencode.Bin != "" {
		return opencode.Bin
	}
	for _, d := range descriptors {
		if d.Kind == domain.AgentOpenCode && d.Bin != "" {
			return d.Bin
		}
	}
	return "opencode"
}
