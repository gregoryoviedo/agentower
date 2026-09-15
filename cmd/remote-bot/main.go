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

	// GUI/launchd launches start with a minimal PATH that omits the
	// user's shell additions (e.g. ~/.local/bin where Claude Code's
	// native installer drops the `claude` binary). Augment PATH so the
	// detector finds every agent and the subprocess managers can spawn
	// them by bare name.
	ensureUserBinPath()

	detector := agents.NewDetector()
	// GUI/launchd launches also miss agents installed inside app
	// bundles (Kiro CLI, Copilot built into VS Code), so consult them
	// when PATH has no answer.
	detector.ExtraBins = agents.BundleBinLookup
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
	kiroManager := kiro.NewManager(kiroBin(descriptors), agents.DefaultKiroPort)
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
	navigation := usecase.NewNavigationService(browser, repository)
	publisher := control.NewPublisher()
	locators := usecase.NewActiveLocatorRegistry()

	// The Claude locator needs a workdir at build time; the manager's
	// workdir is empty until the first Start, so seed it with the
	// workspace root and let the server manager keep it in sync below.
	var claudeLoc *claude.SessionLocator
	if opencodeDescriptor.Available {
		locators.Add(agents_opencode.NewSessionLocator(opencodeClient))
	}
	if hasDetectedClaude(descriptors) {
		loc, err := claude.NewSessionLocator(claude.SessionLocatorOptions{
			Workdir:  cfg.WorkspaceRoot,
			StateDir: cfg.ClaudeStateDir,
		})
		if err != nil {
			logger.Warn("build claude locator", "error", err)
		} else {
			claudeLoc = loc
			locators.Add(loc)
		}
	}
	if hasDetectedKiro(descriptors) {
		kiroLoc, err := kiro.NewSessionLocator(kiro.SessionLocatorOptions{
			StateDir: cfg.KiroStateDir,
		})
		if err == nil {
			locators.Add(kiroLoc)
		} else {
			logger.Warn("build kiro locator", "error", err)
		}
	}
	if copilotDescriptor.Bin != "" {
		copilotLoc, err := copilot.NewSessionLocator(copilot.SessionLocatorOptions{
			StateDir: cfg.CopilotStateDir,
		})
		if err == nil {
			locators.Add(copilotLoc)
		} else {
			logger.Warn("build copilot locator", "error", err)
		}
	}

	// One manager per detected agent. Undetected slots stay nil so the
	// server manager reports them unavailable, matching the registry.
	serverOpts := agents.MultiServerManagerOptions{
		OpenCode: opencodeManager,
		OnWorkdir: func(kind domain.AgentKind, workdir string) {
			if kind == domain.AgentClaude && claudeLoc != nil {
				claudeLoc.SetWorkdir(workdir)
			}
		},
	}
	if hasDetectedClaude(descriptors) {
		serverOpts.Claude = claudeManager
	}
	if hasDetectedKiro(descriptors) {
		serverOpts.Kiro = kiroManager
	}
	if copilotDescriptor.Bin != "" {
		serverOpts.Copilot = copilotManager
	}
	serverManager := agents.NewMultiServerManager(serverOpts)

	logger.Info("opencode autostart disabled; send /init from Telegram to bring the server up",
		"port", agents.DefaultOpenCodePort)
	defer serverManager.StopAll()

	handler := usecase.NewHandler(navigation, repository, registry, serverManager, browser)
	handler.SetSessionEventLog(repository)
	handler.SetSnapshotPublisher(publisher)
	handler.SetActiveLocators(locators)
	handler.SetStaleAfter(cfg.StaleAfter)

	watcher := usecase.NewSessionWatcher(registry, repository, repository, publisher, usecase.SessionWatcherOptions{
		PollInterval:  5 * time.Second,
		IdleThreshold: 30 * time.Second,
		Logger:        logger.With("component", "session-watcher"),
	})
	handler.SetSessionController(watcher)
	handler.SetCompletionPublisher(publisher)
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

// ensureUserBinPath appends the user-local binary directories to PATH
// when they are missing. Launchd-launched processes (the macOS wrapper
// spawns remote-bot from the GUI app) inherit a minimal PATH that does
// not include the user's shell additions, so binaries like Claude Code
// installed in ~/.local/bin would otherwise be invisible to
// exec.LookPath. Idempotent: existing entries are left untouched.
func ensureUserBinPath() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dirs := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".bin"),
		filepath.Join(home, "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
	path := os.Getenv("PATH")
	for _, dir := range dirs {
		if dir == "" || strings.Contains(path, dir) {
			continue
		}
		path = path + string(os.PathListSeparator) + dir
	}
	_ = os.Setenv("PATH", path)
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

// kiroBin returns the Kiro CLI binary the kiro manager should spawn.
// Prefers the absolute path the detector found (PATH or the Kiro CLI
// app bundle) and falls back to the bare "kiro" name for PATH
// resolution at spawn time.
func kiroBin(descriptors []domain.AgentDescriptor) string {
	for _, d := range descriptors {
		if d.Kind == domain.AgentKiro && d.Bin != "" {
			return d.Bin
		}
	}
	return "kiro"
}
