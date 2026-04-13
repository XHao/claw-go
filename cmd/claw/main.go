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
	"path/filepath"
	"syscall"
	"time"

	"github.com/XHao/claw-go/agent"
	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/client"
	"github.com/XHao/claw-go/config"
	"github.com/XHao/claw-go/dirs"
	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
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
	case "serve", "install", "uninstall", "restart":
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
  claw restart               Restart the daemon service
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
	case "restart":
		runRestart()
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

	// Write default persona layer files to the prompts directory.
	writeDefaultPromptFiles(dirs.PromptsDir())
	fmt.Printf("Prompt files written to: %s\n", dirs.PromptsDir())
	fmt.Println("  → Edit them to customize Claw's persona, domain, and behavior.")

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

// runRestart restarts the daemon service.
func runRestart() {
	if !startup.IsInstalled() {
		fmt.Println("No startup service found. Run 'claw install' first.")
		return
	}
	if err := startup.Restart(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Daemon restarted.")
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

	llm, probeTargets := buildLLM(cfg)
	if cfg.Log.DebugLLM {
		llm = provider.WrapDebug(llm, cfg.Log.DebugFile)
		log.Info("已启用 LLM 调试日志", "file", cfg.Log.DebugFile)
	}
	if cfg.Log.MetricsEnabled {
		llm = provider.WrapMetrics(llm, cfg.Log.MetricsFile)
		log.Info("已启用 LLM 指标收集", "file", cfg.Log.MetricsFile)
	}
	var autoRouteP *provider.AutoRouteProvider
	if cfg.RoutingPolicy.IsEnabled() && cfg.RoutingPolicy.RoutingModel != "" {
		if pc, ok := cfg.Models[cfg.RoutingPolicy.RoutingModel]; ok {
			ar := provider.NewAutoRouter(llm, cfg.RoutingPolicy.ThinkingKeywords)
			autoRouteP = provider.WrapAutoRoute(llm, ar, log)
			llm = autoRouteP
			log.Info("自动路由已启用", "classifier_model", pc.Model)
		}
	}
	llm = provider.WrapObserve(llm)

	// Probe model capabilities in the background so the first user request
	// doesn't pay the negotiation/retry cost.
	go func() {
		probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, oai := range probeTargets {
			oai.ProbeCapabilities(probeCtx)
		}
		log.Info("capability probes complete", "providers", len(probeTargets))
	}()

	// Assemble system prompt: prompt directory takes precedence over config field.
	systemPrompt := cfg.Provider.SystemPrompt
	if assembled, err := config.LoadPromptDir(cfg.PromptDir); err != nil {
		log.Warn("prompt dir load failed, using config system_prompt", "err", err, "dir", cfg.PromptDir)
	} else if assembled != "" {
		systemPrompt = assembled
		log.Info("system prompt loaded from prompt dir", "dir", cfg.PromptDir)
	}
	// Append dynamic user profile if present.
	if dynProfile, err := memory.LoadDynamicProfile(dirs.DynamicProfileFile()); err != nil {
		log.Warn("dynamic profile load failed", "err", err)
	} else if dynProfile != "" {
		systemPrompt = systemPrompt + "\n\n" + dynProfile
		log.Info("dynamic user profile appended to system prompt")
	}
	systemPrompt = config.ExpandPrompt(systemPrompt)

	sessions := session.NewStore(cfg.MaxHistoryTurns, systemPrompt, dirs.Sessions())
	if cfg.MaxHistoryTokens > 0 {
		sessions.SetTokenBudget(cfg.MaxHistoryTokens)
	}
	if cfg.RecentRawTurns > 0 {
		sessions.SetRecentRawTurns(cfg.RecentRawTurns)
	}
	if cfg.HistoryCharsPerToken > 0 {
		sessions.SetCharsPerToken(cfg.HistoryCharsPerToken)
	}
	sessions.SetHintBudgetScale(provider.ModelHintRouter, cfg.HistoryBudgetScale.Router)
	sessions.SetHintBudgetScale(provider.ModelHintTask, cfg.HistoryBudgetScale.Task)
	sessions.SetHintBudgetScale(provider.ModelHintSummary, cfg.HistoryBudgetScale.Summary)
	sessions.SetHintBudgetScale(provider.ModelHintThinking, cfg.HistoryBudgetScale.Thinking)

	// Ensure the built-in "main" session always exists from the first run.
	// It is created (or loaded from disk) here; it cannot be deleted at runtime.
	sessions.Get(session.MainSessionKey)
	log.Info("main session ready", "key", session.MainSessionKey)

	// Build tool runner.  AllowedTools and AllowedCommands come directly from
	// config; BuiltinDefs() on the runner will honour them at call time.
	toolRunner := &tool.LocalRunner{
		BashTimeoutSeconds: cfg.Tools.BashTimeoutSeconds,
		AllowedCommands:    cfg.Tools.BashAllowedCommands,
		AllowedTools:       cfg.Tools.Allowed,
	}
	if cfg.Tools.Enabled {
		log.Info("tool calling enabled",
			"tools", len(toolRunner.BuiltinDefs()),
			"max_iterations", cfg.Tools.MaxIterations,
		)
	}

	ag := agent.New(llm, sessions, log)
	ag.SetMaxIterations(cfg.Tools.MaxIterations)
	if cfg.Tools.Enabled {
		ag.SetToolRunner(toolRunner)
	}
	memMgr := memory.NewManager(dirs.MemoryDir())
	ag.SetMemory(memMgr)

	expStore := knowledge.NewExperienceStore(dirs.ExperiencesDir())
	procStore := knowledge.NewProcedureStore(dirs.ProceduresDir())
	if cfg.Tools.Enabled {
		tool.RegisterDaemonTools(toolRunner, llm, memMgr, expStore, sessions)
		tool.RegisterWebSearch(toolRunner, cfg.Search)
		tool.RegisterFetchURL(toolRunner)
		tool.RegisterRecallMemory(toolRunner, memMgr)
		tool.RegisterSaveMemory(toolRunner, expStore, procStore, func() {
			ag.PersistProcedureLayer(dirs.PromptsDir())
		})
	}
	ag.SetExperienceStore(expStore)
	distiller := knowledge.NewDistiller(llm, memMgr, expStore)
	ag.SetDistiller(distiller)
	ag.SetProcedureStore(procStore)
	ag.SetTaskClassifier(knowledge.NewTaskClassifier(llm))
	log.Info("tools ready", "registered", len(toolRunner.RegisteredDefs()))
	if autoRouteP != nil {
		names := make([]string, 0, len(toolRunner.RegisteredDefs()))
		for _, d := range toolRunner.RegisteredDefs() {
			names = append(names, d.Name)
		}
		autoRouteP.SetToolNames(names)
	}

	// Wire up reload: re-reads prompt dir + dynamic profile and updates sessions.
	ag.SetReloadFunc(func() (string, error) {
		sp := cfg.Provider.SystemPrompt
		if assembled, err := config.LoadPromptDir(cfg.PromptDir); err != nil {
			log.Warn("prompt dir load failed during reload", "err", err)
		} else if assembled != "" {
			sp = assembled
		}
		if dynProfile, err := memory.LoadDynamicProfile(dirs.DynamicProfileFile()); err != nil {
			log.Warn("dynamic profile load failed during reload", "err", err)
		} else if dynProfile != "" {
			sp = sp + "\n\n" + dynProfile
		}
		return config.ExpandPrompt(sp), nil
	})

	sock := channel.NewSocketChannel("default", socketPath, sessions)
	sock.SetReloader(ag)
	ag.RegisterChannel(sock)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Start Dream Cycle if enabled: periodically scan memory and distill
	// high-frequency topics in the background.
	if cfg.Memory.DreamCycleEnabled {
		dream := knowledge.NewDreamCycle(
			distiller,
			memMgr,
			expStore,
			cfg.Memory.DreamCycleMinFreq,
			cfg.Memory.DreamCycleLookbackDays,
		)
		dream.Start(ctx, time.Duration(cfg.Memory.DreamCycleIntervalHours)*time.Hour)
		log.Info("dream cycle started",
			"interval_hours", cfg.Memory.DreamCycleIntervalHours,
			"min_freq", cfg.Memory.DreamCycleMinFreq,
			"lookback_days", cfg.Memory.DreamCycleLookbackDays,
		)
	}

	dispatch := func(ctx context.Context, msg channel.InboundMessage) {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		ag.Dispatch(dctx, msg)
	}

	// Start DingTalk channel if configured.
	if cfg.DingTalk.Enabled {
		dt := channel.NewDingTalkChannel("default", cfg.DingTalk.ClientID, cfg.DingTalk.ClientSecret, log)
		ag.RegisterChannel(dt)
		go func() {
			if err := dt.Start(ctx, dispatch); err != nil && ctx.Err() == nil {
				log.Error("dingtalk channel error", "err", err)
			}
		}()
		log.Info("dingtalk channel enabled (stream mode)")
	}

	// Start WeChat channel if configured.
	if cfg.Weixin.Enabled {
		tokenFile := cfg.Weixin.TokenFile
		if tokenFile == "" {
			tokenFile = dirs.WeixinTokenFile()
		}
		wx := channel.NewWeixinChannel("default", tokenFile, log)
		ag.RegisterChannel(wx)
		go func() {
			if err := wx.Start(ctx, dispatch); err != nil && ctx.Err() == nil {
				log.Error("weixin channel error", "err", err)
			}
		}()
		log.Info("weixin channel enabled")
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	cli := cfg.CLI
	llm, _ := buildLLM(cfg)
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
//
// It also returns the raw OpenAI providers so that the caller can run
// startup probes before any user request arrives.
func buildLLM(cfg *config.Config) (provider.Provider, []*provider.OpenAIProvider) {
	var probeTargets []*provider.OpenAIProvider
	seen := map[string]bool{} // deduplicate by model key

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
	newProvider := func(modelKey string, pc *config.ProviderConfig) provider.Provider {
		if pc == nil {
			return nil
		}
		var inner provider.Provider
		switch pc.Type {
		case "anthropic":
			streamEnabled := pc.Stream == nil || *pc.Stream
			inner = provider.NewAnthropic(pc.BaseURL, pc.APIKey, pc.Model, pc.MaxTokens, pc.TimeoutSeconds, pc.ThinkingBudget, streamEnabled, pc.Headers, pc.ExtraBody)
		default: // "openai" or empty string
			streamEnabled := pc.Stream == nil || *pc.Stream // default true
			oai := provider.NewOpenAI(pc.BaseURL, pc.APIKey, pc.Model, pc.MaxTokens, pc.TimeoutSeconds, pc.ThinkingBudget, streamEnabled, pc.ExtraBody)
			if !seen[modelKey] {
				seen[modelKey] = true
				probeTargets = append(probeTargets, oai)
			}
			inner = oai
		}
		return provider.WrapIdentity(inner, modelKey)
	}
	newProviderByName := func(modelKey string) provider.Provider {
		return newProvider(modelKey, getByName(modelKey))
	}

	defaultProvider := newProviderByName(cfg.PrimaryModel)

	if cfg.RoutingPolicy.IsEnabled() {
		rp, err := provider.NewRouterProvider(
			newProviderByName(cfg.RoutingPolicy.TaskModel),
			newProviderByName(cfg.RoutingPolicy.RoutingModel),
			newProviderByName(cfg.RoutingPolicy.SummaryModel),
			newProviderByName(cfg.RoutingPolicy.ThinkingModel),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: build router provider: %v\n", err)
			os.Exit(1)
		}
		return provider.WrapFallback(rp, defaultProvider), probeTargets
	}

	// No routing policy: use default model directly.
	return defaultProvider, probeTargets
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

// defaultPromptFiles holds the 5 editable default persona layer files.
// The safety layer is NOT written here — it is hardcoded in the assembly
// as a fallback and users should define their own safety constraints if needed.
var defaultPromptFiles = map[string]string{
	"01-persona.md": `---
name: persona
layer: persona
enabled: true
priority: 1
---

Your name is Claw. You are a local AI assistant running on {os}, built for
{user}. You are direct, opinionated, and technically precise. You have a
strong preference for simplicity — when you see multiple solutions, you
choose the one with the fewest moving parts. You treat the user as a capable
peer, not a beginner who needs hand-holding.
`,
	"02-domain.md": `---
name: domain
layer: domain
enabled: true
priority: 2
---

Your primary domain is software engineering. You are most fluent in systems
programming, backend development, and developer tooling. When asked about
topics outside your domain, you answer honestly about the limits of your
knowledge rather than speculating.
`,
	"10-behavior.md": `---
name: behavior
layer: behavior
enabled: true
priority: 10
---

When answering technical questions:
- Lead with the answer, not the context
- Show runnable examples before explanations
- Flag irreversible actions before executing them
- When multiple approaches exist, pick one and explain why

When you are uncertain, say so directly rather than hedging with disclaimers.
`,
	"11-communication.md": `---
name: communication
layer: communication
enabled: true
priority: 11
---

Respond in the same language the user writes in. Default to concise prose;
use bullet points only when listing genuinely enumerable items. Avoid
unnecessary preamble. Do not repeat the user's question back to them.
Use markdown formatting only when the output will be rendered.
`,
	"20-user-profile.md": `---
name: user-profile
layer: user-profile
enabled: true
priority: 20
---

## About me

(Edit this section to describe your background, role, and preferences.)

## My environment

(Edit this section to list your editor, primary languages, OS, and tools.)
`,
}

// writeDefaultPromptFiles writes the default persona layer files to dir,
// skipping any file that already exists.
func writeDefaultPromptFiles(dir string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create prompts dir: %v\n", err)
		return
	}
	for name, content := range defaultPromptFiles {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			fmt.Printf("Prompt file already exists, skipping: %s\n", path)
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", name, err)
		} else {
			fmt.Printf("Prompt file written: %s\n", path)
		}
	}
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
