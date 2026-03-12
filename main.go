// claw-go: local AI assistant with a persistent daemon and CLI client.
//
// Usage:
//
//	claw serve     [--config config.yaml] [--log-level debug|info|warn|error]
//	claw install   [--config config.yaml]
//	claw uninstall
//	claw           [--config config.yaml]
package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/XHao/claw-go/agent"
	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/client"
	"github.com/XHao/claw-go/config"
	"github.com/XHao/claw-go/dirs"
	"github.com/XHao/claw-go/ipc"
	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
	"github.com/XHao/claw-go/skill"
	"github.com/XHao/claw-go/startup"
	"github.com/XHao/claw-go/tool"
)

//go:embed config.example.yaml
var configTemplate []byte

func main() {
	sub := ""
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}

	// Subcommands that consume os.Args[1] before flag parsing.
	switch sub {
	case "serve", "install", "uninstall":
		os.Args = append(os.Args[:1], os.Args[2:]...)
	}

	configPath := flag.String("config", "", "path to config file (YAML or official openclaw JSON); auto-detected when empty")
	logLevel := flag.String("log-level", "info", "log level: debug|info|warn|error")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `claw – local AI assistant

Usage:
  claw serve       [flags]   Start the background daemon
  claw install     [flags]   Register daemon as a startup service
  claw uninstall             Remove startup service registration
  claw             [flags]   Connect to the daemon (interactive CLI)

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	switch sub {
	case "install":
		runInstall(*configPath)
	case "uninstall":
		runUninstall()
	default:
		cfg, err := config.AutoLoad(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		socketPath := cfg.SocketPath
		if socketPath == "" {
			socketPath = dirs.SocketPath()
		}
		if sub == "serve" {
			runServe(cfg, socketPath, *logLevel)
		} else {
			runConnect(cfg, socketPath)
		}
	}
}

// runInstall registers the daemon as a system startup service.
func runInstall(configPath string) {
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolve binary path: %v\n", err)
		os.Exit(1)
	}

	// 1. Create data directories (~/.claw/sessions, ~/.claw/logs …)
	if err := dirs.MkdirAll(); err != nil {
		fmt.Fprintf(os.Stderr, "error: create data dirs: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Data directory: %s\n", dirs.Data())

	// 2. Copy the config template to the data directory if no config exists yet.
	defaultCfg := dirs.ConfigFile()
	if _, err := os.Stat(defaultCfg); os.IsNotExist(err) {
		if writeErr := os.WriteFile(defaultCfg, configTemplate, 0o600); writeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write config template: %v\n", writeErr)
		} else {
			fmt.Printf("Config template copied to: %s\n", defaultCfg)
			fmt.Println("  → Edit it and set provider.api_key (or export OPENAI_API_KEY).")
		}
	} else {
		fmt.Printf("Config already exists, skipping: %s\n", defaultCfg)
	}

	// 3. Resolve the effective config path for the startup service entry.
	resolvedPath := config.ResolveConfigPath(configPath)
	if startup.IsInstalled() {
		fmt.Println("Startup service is already installed. Run 'claw uninstall' first to replace it.")
		return
	}
	if err := startup.Install(bin, resolvedPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runUninstall removes the startup service registration.
func runUninstall() {
	if !startup.IsInstalled() {
		fmt.Println("No startup service found.")
		return
	}
	if err := startup.Uninstall(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runServe starts the daemon: creates the Agent and listens on the Unix socket.
func runServe(cfg *config.Config, socketPath, logLevel string) {
	// Ensure data directories exist before any I/O.
	if err := dirs.MkdirAll(); err != nil {
		fmt.Fprintf(os.Stderr, "error: create data dirs: %v\n", err)
		os.Exit(1)
	}

	log := setupLogger(logLevel, dirs.LogFile())

	if err := config.ValidateServe(cfg); err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	log.Info("claw daemon starting",
		"data_dir", dirs.Data(),
		"socket", socketPath,
		"primary_model", cfg.PrimaryModel,
	)

	llm := buildLLM(cfg)
	if cfg.Log.DebugLLM {
		llm = provider.WrapDebug(llm, cfg.Log.DebugFile)
		log.Info("已启用 LLM 调试日志", "file", cfg.Log.DebugFile)
	}
	if cfg.Log.MetricsEnabled {
		llm = provider.WrapMetrics(llm, cfg.Log.MetricsFile)
		log.Info("已启用 LLM 指标收集", "file", cfg.Log.MetricsFile)
	}

	sessions := session.NewStore(cfg.MaxHistoryTurns, config.ExpandPrompt(cfg.Provider.SystemPrompt), dirs.Sessions())

	// Ensure the built-in "main" session always exists from the first run.
	// It is created (or loaded from disk) here; it cannot be deleted at runtime.
	sessions.Get(session.MainSessionKey)
	log.Info("main session ready", "key", session.MainSessionKey)

	// Build tool definitions for the agentic loop.
	var tools []provider.ToolDef
	if cfg.Tools.Enabled {
		tools = tool.AllDefs(cfg.Tools.Allowed)
		log.Info("tool calling enabled", "tools", len(tools), "max_iterations", cfg.Tools.MaxIterations)
	}

	ag := agent.New(llm, sessions, tools, log)
	ag.SetMaxIterations(cfg.Tools.MaxIterations)
	memMgr := memory.NewManager(dirs.MemoryDir())
	ag.SetMemory(memMgr)

	// Wire skill router — skills run server-side via natural language.
	expStore := knowledge.NewExperienceStore(dirs.ExperiencesDir())
	skillReg := skill.BuildRegistry(llm, memMgr, expStore, sessions)
	ag.SetSkillRouter(skill.NewRouter(skillReg))
	ag.SetExperienceStore(expStore)
	log.Info("skill router ready", "skills", len(skillReg.AsToolDefs()))

	// Wire auto-router: automatically selects task vs. thinking tier per message.
	// Only enabled when a cheap routing-tier model is configured.
	if cfg.RoutingPolicy.IsEnabled() && cfg.RoutingPolicy.RoutingModel != "" {
		if pc, ok := cfg.Models[cfg.RoutingPolicy.RoutingModel]; ok {
			ag.SetAutoRouter(provider.NewAutoRouter(llm, cfg.RoutingPolicy.ThinkingKeywords))
			log.Info("自动路由已启用", "classifier_model", pc.Model)
		}
	}

	sock := channel.NewSocketChannel("default", socketPath, sessions)
	ag.RegisterChannel(sock)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dispatch := func(msg channel.InboundMessage, exchange ipc.ToolExchangeFn) {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		ag.Dispatch(dctx, msg, exchange)
	}

	log.Info("daemon ready", "socket", socketPath)
	if err := sock.Start(ctx, dispatch); err != nil && ctx.Err() == nil {
		log.Error("socket channel error", "err", err)
		os.Exit(1)
	}
	log.Info("daemon stopped cleanly")
}

// runConnect starts the interactive CLI client and connects to the daemon.
func runConnect(cfg *config.Config, socketPath string) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cli := cfg.CLI
	llm := buildLLM(cfg)
	if cfg.Log.DebugLLM {
		llm = provider.WrapDebug(llm, cfg.Log.DebugFile)
	}
	if err := client.Run(
		ctx,
		socketPath,
		cli.Prompt,
		cli.HistoryFile,
		cli.ShellEnabled,
		cli.ShellTimeoutSeconds,
		cli.AllowedCommands,
		cfg.Tools.Enabled,
		cfg.Tools.BashTimeoutSeconds,
		cfg.Theme,
		llm,
		dirs.MemoryDir(),
		dirs.ExperiencesDir(),
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// buildLLM constructs the Provider from config.
// When routing_policy is configured, a RouterProvider is returned.
// Otherwise the single-model provider is used.
func buildLLM(cfg *config.Config) provider.Provider {
	getByName := func(name string) *config.ProviderConfig {
		if name == "" {
			return nil
		}
		pc, ok := cfg.Models[name]
		if !ok {
			return nil
		}
		v := pc
		return &v
	}
	newOAI := func(pc *config.ProviderConfig) provider.Provider {
		if pc == nil {
			return nil
		}
		return provider.NewOpenAI(pc.BaseURL, pc.APIKey, pc.Model, pc.MaxTokens, pc.TimeoutSeconds)
	}

	defaultProvider := newOAI(getByName(cfg.PrimaryModel))

	if cfg.RoutingPolicy.IsEnabled() {
		rp, err := provider.NewRouterProvider(
			newOAI(getByName(cfg.RoutingPolicy.TaskModel)),
			newOAI(getByName(cfg.RoutingPolicy.RoutingModel)),
			newOAI(getByName(cfg.RoutingPolicy.SummaryModel)),
			newOAI(getByName(cfg.RoutingPolicy.ThinkingModel)),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: build router provider: %v\n", err)
			os.Exit(1)
		}
		return provider.WrapFallback(rp, defaultProvider)
	}

	// No routing policy: use default model directly.
	return defaultProvider
}

func setupLogger(level, logFile string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: l}
	// When a log file is configured, write to the file only — the daemon
	// typically runs in the background and stdout would pollute the user's
	// terminal.  Without a log file, fall back to stdout.
	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			return slog.New(slog.NewTextHandler(f, opts))
		}
		// If the file can't be opened, fall back to stdout.
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// multiHandler fans out log records to multiple slog.Handler instances.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var last error
	for _, h := range m.handlers {
		if err := h.Handle(ctx, r); err != nil {
			last = err
		}
	}
	return last
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newHandlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		newHandlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: newHandlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	newHandlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		newHandlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: newHandlers}
}
