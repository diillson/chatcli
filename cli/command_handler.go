/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"

	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/k8s"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

type CommandHandler struct {
	cli *ChatCLI
}

func NewCommandHandler(cli *ChatCLI) *CommandHandler {
	return &CommandHandler{cli: cli}
}

func (ch *CommandHandler) HandleCommand(userInput string) bool {
	switch {
	case userInput == "/exit" || userInput == "exit" || userInput == "/quit" || userInput == "quit":
		fmt.Println(i18n.T("status.exiting"))
		return true
	case userInput == "/reload":
		ch.cli.reloadConfiguration()
		return false
	case strings.HasPrefix(userInput, "/agent"):
		// /agent pode ser gerenciamento de personas OU iniciar modo agente
		if !ch.handleAgentPersonaSubcommand(userInput) {
			// Não é um subcomando, inicia modo agente
			ch.cli.pendingAction = "agent"
			panic(agentModeRequest)
		}
		return false
	case strings.HasPrefix(userInput, "/run"):
		// /run inicia o modo agente (com ou sem persona ativa)
		ch.cli.pendingAction = "agent"
		panic(agentModeRequest)
	case strings.HasPrefix(userInput, "/coder"):
		ch.cli.pendingAction = "coder"
		panic(coderModeRequest)
	case strings.HasPrefix(userInput, "/switch"):
		ch.cli.handleSwitchCommand(userInput)
		return false
	case userInput == "/help":
		ch.cli.showHelp()
		return false
	case userInput == "/config" || userInput == "/status" || userInput == "/settings":
		ch.cli.showConfig()
		return false
	case userInput == "/version" || userInput == "/v":
		ch.handleVersionCommand()
		return false
	case userInput == "/nextchunk":
		return ch.cli.handleNextChunk()
	case userInput == "/retry":
		return ch.cli.handleRetryLastChunk()
	case userInput == "/retryall":
		return ch.cli.handleRetryAllChunks()
	case userInput == "/skipchunk":
		return ch.cli.handleSkipChunk()
	case userInput == "/newsession":
		ch.cli.history = []models.Message{}
		ch.cli.currentSessionName = ""
		fmt.Println(i18n.T("session.new_session_started"))
		return false
	case strings.HasPrefix(userInput, "/session"):
		ch.handleSessionCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/context"):
		ch.handleContextCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/auth"):
		ch.handleAuthCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/plugin"):
		ch.handlePluginCommand(userInput)
		return false
	case strings.HasPrefix(userInput, "/connect"):
		ch.handleConnectCommand(userInput)
		return false
	case userInput == "/disconnect":
		ch.handleDisconnectCommand()
		return false
	case strings.HasPrefix(userInput, "/watch"):
		ch.handleWatchCommand(userInput)
		return false
	case userInput == "/metrics":
		ch.handleMetricsCommand()
		return false
	case userInput == "/reset" || userInput == "/redraw" || userInput == "/clear":
		fmt.Print("\033[0m")
		os.Stdout.Sync()
		ch.cli.restoreTerminal()
		time.Sleep(50 * time.Millisecond)
		ch.cli.forceRefreshPrompt()
		return false
	default:
		fmt.Println(i18n.T("error.unknown_command"))
		return false
	}
}

// handleConnectCommand handles the /connect <address> [flags] command.
// It connects to a remote ChatCLI gRPC server and swaps the LLM client.
func (ch *CommandHandler) handleConnectCommand(userInput string) {
	args := strings.Fields(userInput)

	if len(args) < 2 {
		fmt.Println(colorize(" Usage: /connect <host:port> [--token <t>] [--use-local-auth] [--provider <p>] [--model <m>] [--llm-key <k>]", ColorYellow))
		fmt.Println(colorize("   StackSpot: --client-id <id> --client-key <key> --realm <r> --agent-id <a>", ColorYellow))
		fmt.Println(colorize("   Ollama:    --ollama-url <url>", ColorYellow))
		fmt.Println(colorize("   TLS:       --tls [--ca-cert <path>]", ColorYellow))
		return
	}

	if ch.cli.isRemote {
		fmt.Println(colorize(" Already connected to a remote server. Use /disconnect first.", ColorYellow))
		return
	}

	// Parse arguments manually (same pattern as /switch)
	address := args[1]
	var token, provider, model, llmKey, caCert string
	var clientID, clientKey, realm, agentID, ollamaURL string
	var useLocalAuth, useTLS bool

	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--token":
			if i+1 < len(args) {
				token = args[i+1]
				i++
			}
		case "--provider":
			if i+1 < len(args) {
				provider = args[i+1]
				i++
			}
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--llm-key":
			if i+1 < len(args) {
				llmKey = args[i+1]
				i++
			}
		case "--ca-cert":
			if i+1 < len(args) {
				caCert = args[i+1]
				i++
			}
		case "--client-id":
			if i+1 < len(args) {
				clientID = args[i+1]
				i++
			}
		case "--client-key":
			if i+1 < len(args) {
				clientKey = args[i+1]
				i++
			}
		case "--realm":
			if i+1 < len(args) {
				realm = args[i+1]
				i++
			}
		case "--agent-id":
			if i+1 < len(args) {
				agentID = args[i+1]
				i++
			}
		case "--ollama-url":
			if i+1 < len(args) {
				ollamaURL = args[i+1]
				i++
			}
		case "--use-local-auth":
			useLocalAuth = true
		case "--tls":
			useTLS = true
		}
	}

	// Resolve local auth if requested
	if useLocalAuth && llmKey == "" {
		resolvedKey, resolvedProvider, err := ch.resolveLocalAuth(provider)
		if err != nil {
			fmt.Println(colorize(fmt.Sprintf(" Failed to resolve local auth: %v", err), ColorRed))
			return
		}
		llmKey = resolvedKey
		if provider == "" {
			provider = resolvedProvider
		}
	}

	// Build provider-specific config
	providerConfig := make(map[string]string)
	if clientID != "" {
		providerConfig["client_id"] = clientID
	}
	if clientKey != "" {
		providerConfig["client_key"] = clientKey
	}
	if realm != "" {
		providerConfig["realm"] = realm
	}
	if agentID != "" {
		providerConfig["agent_id"] = agentID
	}
	if ollamaURL != "" {
		providerConfig["base_url"] = ollamaURL
	}

	fmt.Println(colorize(fmt.Sprintf(" Connecting to %s...", address), ColorCyan))

	// Create remote client
	cfg := remote.Config{
		Address:        address,
		Token:          token,
		TLS:            useTLS,
		CertFile:       caCert,
		ClientAPIKey:   llmKey,
		Provider:       provider,
		Model:          model,
		ProviderConfig: providerConfig,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	remoteClient, err := remote.NewClient(cfg, ch.cli.logger)
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf(" Connection failed: %v", err), ColorRed))
		return
	}

	// Health check
	healthy, ver, err := remoteClient.Health(ctx)
	if err != nil {
		remoteClient.Close()
		fmt.Println(colorize(fmt.Sprintf(" Health check failed: %v", err), ColorRed))
		return
	}
	if !healthy {
		remoteClient.Close()
		fmt.Println(colorize(" Server is not healthy", ColorRed))
		return
	}

	// Save current local state
	ch.cli.localClient = ch.cli.Client
	ch.cli.localProvider = ch.cli.Provider
	ch.cli.localModel = ch.cli.Model

	// Swap to remote
	ch.cli.Client = remoteClient
	ch.cli.Provider = remoteClient.GetProvider()
	ch.cli.Model = remoteClient.GetModelName()
	ch.cli.remoteConn = remoteClient
	ch.cli.isRemote = true

	connInfo := fmt.Sprintf("version: %s, provider: %s, model: %s", ver, ch.cli.Provider, ch.cli.Model)
	if useLocalAuth {
		connInfo += ", using local OAuth credentials"
	} else if llmKey != "" {
		connInfo += ", using your API key"
	}
	fmt.Println(colorize(fmt.Sprintf(" Connected to remote server (%s)", connInfo), ColorGreen))

	// Show watcher status and remote resources if server has them
	infoCtx, infoCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer infoCancel()
	if info, err := remoteClient.GetServerInfo(infoCtx); err == nil {
		if info.WatcherActive {
			fmt.Println(colorize(fmt.Sprintf(" K8s watcher active: %s (context injected into prompts)", info.WatcherTarget), ColorCyan))
		}
		if info.PluginCount > 0 || info.AgentCount > 0 || info.SkillCount > 0 {
			fmt.Println(colorize(fmt.Sprintf(" %s", i18n.T("remote.resources.available", info.PluginCount, info.AgentCount, info.SkillCount)), ColorCyan))
		}
	}

	// Discover and register remote plugins
	ch.discoverRemoteResources(remoteClient)

	fmt.Println(colorize(" Use /disconnect to return to local mode.", ColorCyan))
}

// discoverRemoteResources fetches remote plugins/agents/skills and registers them.
func (ch *CommandHandler) discoverRemoteResources(remoteClient *remote.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Register remote plugins
	if remotePlugins, err := remoteClient.ListRemotePlugins(ctx); err == nil && len(remotePlugins) > 0 {
		for _, p := range remotePlugins {
			rp := remote.NewRemotePluginFromInfo(p, remoteClient)
			ch.cli.pluginManager.RegisterRemotePlugin(rp)
		}
		ch.cli.logger.Info("Remote plugins registered", zap.Int("count", len(remotePlugins)))
	}

	// Cache remote agents and skills info for listing
	if agents, err := remoteClient.ListRemoteAgents(ctx); err == nil {
		ch.cli.remoteAgents = agents
	}
	if skills, err := remoteClient.ListRemoteSkills(ctx); err == nil {
		ch.cli.remoteSkills = skills
	}
}

// handleDisconnectCommand handles the /disconnect command.
// It closes the remote connection and restores the local LLM client.
func (ch *CommandHandler) handleDisconnectCommand() {
	if !ch.cli.isRemote {
		fmt.Println(colorize(" Not connected to a remote server.", ColorYellow))
		return
	}

	// Clean up remote resources
	if ch.cli.pluginManager != nil {
		ch.cli.pluginManager.ClearRemotePlugins()
	}
	ch.cli.remoteAgents = nil
	ch.cli.remoteSkills = nil

	// Close remote connection
	if ch.cli.remoteConn != nil {
		ch.cli.remoteConn.Close()
	}

	// Restore local state
	ch.cli.Client = ch.cli.localClient
	ch.cli.Provider = ch.cli.localProvider
	ch.cli.Model = ch.cli.localModel
	ch.cli.remoteConn = nil
	ch.cli.isRemote = false
	ch.cli.localClient = nil
	ch.cli.localProvider = ""
	ch.cli.localModel = ""

	fmt.Println(colorize(" Disconnected from remote server.", ColorGreen))
	if ch.cli.Client != nil {
		fmt.Println(colorize(fmt.Sprintf(" Back to local mode: %s (%s)", ch.cli.Model, ch.cli.Provider), ColorCyan))
	} else {
		fmt.Println(colorize(" Back to local mode (no LLM provider configured).", ColorYellow))
	}
}

// handleWatchCommand routes /watch subcommands: start, stop, status.
func (ch *CommandHandler) handleWatchCommand(userInput string) {
	args := strings.Fields(userInput)

	// /watch alone or /watch status
	if len(args) < 2 || args[1] == "status" {
		ch.handleWatchStatusCommand()
		return
	}

	switch args[1] {
	case "start":
		ch.handleWatchStartCommand(args[2:])
	case "stop":
		ch.handleWatchStopCommand()
	default:
		fmt.Println(colorize(" Usage:", ColorYellow))
		fmt.Println(colorize("   /watch start --deployment <name> [--namespace <ns>] [--interval <dur>] [--window <dur>] [--max-log-lines <n>] [--kubeconfig <path>]", ColorYellow))
		fmt.Println(colorize("   /watch stop", ColorYellow))
		fmt.Println(colorize("   /watch status", ColorYellow))
	}
}

// handleWatchStartCommand starts a K8s watcher in background from interactive mode.
func (ch *CommandHandler) handleWatchStartCommand(args []string) {
	if ch.cli.isWatching {
		fmt.Println(colorize(" K8s watcher already running. Use /watch stop first.", ColorYellow))
		return
	}

	// Parse flags (same manual pattern as /connect)
	cfg := k8s.WatchConfig{
		Namespace:   "default",
		Interval:    30 * time.Second,
		Window:      2 * time.Hour,
		MaxLogLines: 100,
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--deployment":
			if i+1 < len(args) {
				i++
				cfg.Deployment = args[i]
			}
		case "--namespace":
			if i+1 < len(args) {
				i++
				cfg.Namespace = args[i]
			}
		case "--interval":
			if i+1 < len(args) {
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					fmt.Println(colorize(" Invalid --interval: "+err.Error(), ColorYellow))
					return
				}
				cfg.Interval = d
			}
		case "--window":
			if i+1 < len(args) {
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					fmt.Println(colorize(" Invalid --window: "+err.Error(), ColorYellow))
					return
				}
				cfg.Window = d
			}
		case "--max-log-lines":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Println(colorize(" Invalid --max-log-lines: "+err.Error(), ColorYellow))
					return
				}
				cfg.MaxLogLines = n
			}
		case "--kubeconfig":
			if i+1 < len(args) {
				i++
				cfg.Kubeconfig = args[i]
			}
		default:
			fmt.Println(colorize(" Unknown flag: "+args[i], ColorYellow))
			fmt.Println(colorize(" Usage: /watch start --deployment <name> [--namespace <ns>] [--interval <dur>] [--window <dur>]", ColorYellow))
			return
		}
	}

	if cfg.Deployment == "" {
		fmt.Println(colorize(" --deployment is required.", ColorYellow))
		fmt.Println(colorize(" Usage: /watch start --deployment <name> [--namespace <ns>] [--interval <dur>] [--window <dur>]", ColorYellow))
		return
	}

	fmt.Println(colorize(fmt.Sprintf(" Starting K8s watcher for deployment/%s in namespace/%s ...", cfg.Deployment, cfg.Namespace), ColorCyan))
	fmt.Println(colorize(fmt.Sprintf("   Interval: %s | Window: %s | Max log lines: %d", cfg.Interval, cfg.Window, cfg.MaxLogLines), ColorCyan))

	if err := ch.cli.StartWatcher(cfg); err != nil {
		fmt.Println(colorize(" Failed to start watcher: "+err.Error(), ColorYellow))
		return
	}

	fmt.Println(colorize(" K8s watcher started. Context will be injected into all prompts.", ColorGreen))
	fmt.Println(colorize(" Use /watch status to check, /watch stop to stop.", ColorGreen))
}

// handleWatchStopCommand stops the running K8s watcher.
func (ch *CommandHandler) handleWatchStopCommand() {
	if !ch.cli.isWatching {
		fmt.Println(colorize(" No K8s watcher is running.", ColorYellow))
		return
	}

	ch.cli.StopWatcher()
	fmt.Println(colorize(" K8s watcher stopped.", ColorGreen))
}

// handleWatchStatusCommand displays the current K8s watcher status.
func (ch *CommandHandler) handleWatchStatusCommand() {
	// Check local watcher first
	if ch.cli.isWatching {
		if ch.cli.watchStatusFunc != nil {
			status := ch.cli.watchStatusFunc()
			fmt.Println(colorize(" K8s Watcher: "+status, ColorCyan))
		} else {
			fmt.Println(colorize(" K8s Watcher: active (no status available)", ColorCyan))
		}
		return
	}

	// If connected to remote, query server's watcher status
	if ch.cli.isRemote {
		if rc, ok := ch.cli.Client.(*remote.Client); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ws, err := rc.GetWatcherStatus(ctx)
			if err != nil {
				fmt.Println(colorize(" Failed to query remote watcher status: "+err.Error(), ColorYellow))
				return
			}
			if !ws.Active {
				fmt.Println(colorize(" Server has no K8s watcher active.", ColorYellow))
				return
			}
			fmt.Println(colorize(fmt.Sprintf(" K8s Watcher (remote): %s", ws.StatusSummary), ColorCyan))
			fmt.Println(colorize(fmt.Sprintf("   Target: %s/%s | Pods: %d | Alerts: %d | Snapshots: %d",
				ws.Namespace, ws.Deployment, ws.PodCount, ws.AlertCount, ws.SnapshotCount), ColorCyan))
			return
		}
	}

	fmt.Println(colorize(" No K8s watcher active. Start with: /watch start --deployment <name>", ColorYellow))
}

// resolveLocalAuth reads the local auth store and returns the API key/OAuth token.
func (ch *CommandHandler) resolveLocalAuth(provider string) (apiKey string, resolvedProvider string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if provider != "" {
		authProvider, ok := llmProviderToAuthProvider(provider)
		if !ok {
			return "", "", fmt.Errorf(
				"--use-local-auth only supports OAuth providers (CLAUDEAI, OPENAI). "+
					"Provider '%s' requires --llm-key with an API key instead", provider)
		}

		resolved, err := auth.ResolveAuth(ctx, authProvider, ch.cli.logger)
		if err != nil {
			return "", "", fmt.Errorf("no local credentials found for %s: %w\n"+
				"Run '/auth login %s' first", provider, err, string(authProvider))
		}
		return resolved.APIKey, provider, nil
	}

	// No provider specified: try each OAuth provider in order
	for _, candidate := range []struct {
		authProvider auth.ProviderID
		llmProvider  string
	}{
		{auth.ProviderAnthropic, "CLAUDEAI"},
		{auth.ProviderOpenAI, "OPENAI"},
	} {
		resolved, err := auth.ResolveAuth(ctx, candidate.authProvider, ch.cli.logger)
		if err == nil && resolved.APIKey != "" {
			ch.cli.logger.Info("Auto-resolved local auth",
				zap.String("provider", candidate.llmProvider),
				zap.String("source", resolved.Source),
				zap.String("mode", string(resolved.Mode)),
			)
			return resolved.APIKey, candidate.llmProvider, nil
		}
	}

	return "", "", fmt.Errorf("no local OAuth credentials found. Run '/auth login anthropic' or '/auth login openai-codex' first")
}

// llmProviderToAuthProvider maps LLMManager provider names to auth.ProviderID.
func llmProviderToAuthProvider(provider string) (auth.ProviderID, bool) {
	switch strings.ToUpper(provider) {
	case "CLAUDEAI":
		return auth.ProviderAnthropic, true
	case "OPENAI":
		return auth.ProviderOpenAI, true
	default:
		return "", false
	}
}

// handleContextCommand - Nova função para rotear comandos de contexto
func (ch *CommandHandler) handleContextCommand(userInput string) {
	sessionID := ch.cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}

	if err := ch.cli.contextHandler.HandleContextCommand(sessionID, userInput); err != nil {
		fmt.Println(colorize(fmt.Sprintf(" ❌ %s", err.Error()), ColorYellow))
	}
}

// handleSessionCommand foi movido para cá para consolidar a lógica do handler
func (ch *CommandHandler) handleSessionCommand(userInput string) {
	args := strings.Fields(userInput)
	if len(args) < 2 {
		fmt.Println(i18n.T("session.usage_header"))
		fmt.Println(i18n.T("session.usage_save"))
		fmt.Println(i18n.T("session.usage_load"))
		fmt.Println(i18n.T("session.usage_list"))
		fmt.Println(i18n.T("session.usage_delete"))
		fmt.Println(i18n.T("session.usage_new"))
		return
	}

	command := args[1]
	var name string
	if len(args) > 2 {
		name = args[2]
	}

	switch command {
	case "save":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_save"))
			return
		}
		ch.cli.handleSaveSession(name)
	case "load":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_load"))
			return
		}
		ch.cli.handleLoadSession(name)
	case "list":
		ch.cli.handleListSessions()
	case "delete":
		if name == "" {
			fmt.Println(i18n.T("session.error_name_required_delete"))
			return
		}
		ch.cli.handleDeleteSession(name)
	case "new":
		ch.cli.history = []models.Message{}
		ch.cli.currentSessionName = ""
		fmt.Println(i18n.T("session.new_session_started"))
	default:
		// CORREÇÃO: Usar Println com i18n.T
		fmt.Println(i18n.T("session.unknown_command", command))
	}
}

// autoSwitchProvider switches the active provider and model after a successful OAuth login.
func (ch *CommandHandler) autoSwitchProvider(provider, model string) {
	newClient, err := ch.cli.manager.GetClient(provider, model)
	if err != nil {
		ch.cli.logger.Warn("Auto-switch after OAuth login failed, use /switch manually",
			zap.String("provider", provider), zap.Error(err))
		fmt.Println(i18n.T("cli.switch.change_model_error", model, err))
		return
	}
	ch.cli.Client = newClient
	ch.cli.Provider = provider
	ch.cli.Model = model
	fmt.Println(i18n.T("status.provider_switched", ch.cli.Client.GetModelName(), ch.cli.Provider))
}

func (ch *CommandHandler) handleAuthCommand(userInput string) {
	args := strings.Fields(userInput)
	if len(args) < 2 {
		fmt.Println("Usage: /auth status | login <anthropic|openai-codex> | logout <anthropic|openai-codex>")
		return
	}
	sub := strings.ToLower(args[1])
	switch sub {
	case "status":
		fmt.Println(auth.FormatAuthStatus(ch.cli.logger))
	case "login":
		if len(args) < 3 {
			fmt.Println("Use: /auth login anthropic | openai-codex")
			return
		}
		prov := strings.ToLower(args[2])
		ctx := context.Background()
		switch prov {
		case "anthropic", "claude", "claudeai":
			id, err := auth.LoginAnthropicOAuth(ctx, ch.cli.logger)
			if err != nil {
				fmt.Println("Login failed:", err)
				return
			}
			fmt.Println("Logged in (Anthropic) profile:", id)
			ch.cli.manager.RefreshProviders()
			ch.autoSwitchProvider("CLAUDEAI",
				utils.GetEnvOrDefault("ANTHROPIC_MODEL", config.DefaultClaudeAIModel))
			return
		case "openai-codex", "codex":
			id, err := auth.LoginOpenAICodexOAuth(ctx, ch.cli.logger)
			if err != nil {
				fmt.Println("Login failed:", err)
				return
			}
			fmt.Println("Logged in (OpenAI Codex) profile:", id)
			ch.cli.manager.RefreshProviders()
			ch.autoSwitchProvider("OPENAI",
				utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAIModel))
			return
		default:
			fmt.Println("Unknown provider. Use: anthropic | openai-codex")
			return
		}
	case "logout":
		if len(args) < 3 {
			fmt.Println("Use: /auth logout anthropic | openai-codex")
			return
		}
		prov := strings.ToLower(args[2])
		ctx := context.Background()
		_ = ctx // keep for symmetry
		var pid auth.ProviderID
		switch prov {
		case "anthropic", "claude", "claudeai":
			pid = auth.ProviderAnthropic
		case "openai-codex", "codex":
			pid = auth.ProviderOpenAICodex
		default:
			fmt.Println("Unknown provider. Use: anthropic | openai-codex")
			return
		}
		if err := auth.Logout(pid, ch.cli.logger); err != nil {
			fmt.Println("Logout failed:", err)
			return
		}
		fmt.Println("Logout ok")
		return
	default:
		fmt.Println("Unknown subcommand. Use: status | login | logout")
		return
	}
}

func (ch *CommandHandler) handlePluginCommand(userInput string) {
	if ch.cli.pluginManager == nil {
		ch.cli.logger.Error("O gerenciador de plugins não está inicializado. O comando /plugin está desabilitado.")
		fmt.Println(i18n.T("plugin.error.manager_disabled"))
		return
	}

	args := strings.Fields(userInput)
	if len(args) < 2 {
		fmt.Println(i18n.T("plugin.usage_header"))
		return
	}

	subcommand := args[1]
	pluginManager := ch.cli.pluginManager

	switch subcommand {
	case "list":
		plugins := pluginManager.GetPlugins()
		if len(plugins) == 0 {
			fmt.Println(i18n.T("plugin.list.empty"))
			return
		}
		fmt.Println(i18n.T("plugin.list.header"))
		for _, p := range plugins {
			fmt.Printf("  %s %s - %s\n", colorize(p.Name(), ColorCyan), colorize(p.Version(), ColorGray), p.Description())
		}

	case "show":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.show.usage"))
			return
		}
		p, found := pluginManager.GetPlugin(args[2])
		if !found {
			fmt.Println(i18n.T("plugin.error.not_found", args[2]))
			return
		}
		fmt.Println(i18n.T("plugin.show.details_for", p.Name()))
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.show.description"), ColorCyan), p.Description())
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.show.usage_label"), ColorCyan), p.Usage())
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.show.version"), ColorCyan), p.Version())

	case "inspect":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.inspect.usage"))
			return
		}
		p, found := pluginManager.GetPlugin(args[2])
		if !found {
			fmt.Println(i18n.T("plugin.error.not_found", args[2]))
			return
		}
		fmt.Println(i18n.T("plugin.inspect.details_for", p.Name()))
		fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.inspect.path"), ColorCyan), p.Path())
		if info, err := os.Stat(p.Path()); err == nil {
			fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.inspect.permissions"), ColorCyan), info.Mode().String())
		} else {
			fmt.Printf("  %s: %s\n", colorize(i18n.T("plugin.inspect.permissions"), ColorCyan), "N/A (builtin)")
		}

	case "install":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.install.usage"))
			return
		}
		rawURL := args[2]

		// Parse the URL: detect GitHub/GitLab tree URLs and extract repo, branch, subdir.
		cloneURL, branch, subDir := parseGitURL(rawURL)

		// AVISO DE SEGURANÆA
		fmt.Println(colorize(i18n.T("plugin.install.security_warning"), ColorYellow))

		if runtime.GOOS != "windows" {
			cmd := exec.Command("stty", "sane")
			cmd.Stdin = os.Stdin
			_ = cmd.Run()
		}

		fmt.Print(i18n.T("plugin.install.confirm", rawURL))
		reader := bufio.NewReader(os.Stdin)
		confirm, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(confirm)) != "s" {
			fmt.Println(i18n.T("plugin.install.cancelled"))
			return
		}

		fmt.Println(i18n.T("plugin.install.installing", rawURL))

		tempDir, err := os.MkdirTemp("", "chatcli-plugin-")
		if err != nil {
			fmt.Println(i18n.T("plugin.install.error.tempdir", err))
			return
		}
		defer os.RemoveAll(tempDir)

		// Build git clone args: use --branch if we parsed one from the URL.
		cloneArgs := []string{"clone", "--depth=1"}
		if branch != "" {
			cloneArgs = append(cloneArgs, "--branch", branch)
		}
		cloneArgs = append(cloneArgs, cloneURL, tempDir)

		cloneCmd := exec.Command("git", cloneArgs...)
		if output, err := cloneCmd.CombinedOutput(); err != nil {
			fmt.Println(i18n.T("plugin.install.error.clone", err))
			fmt.Println(string(output))
			return
		}

		// Determine the build directory (repo root or subdirectory).
		buildDir := tempDir
		if subDir != "" {
			buildDir = filepath.Join(tempDir, subDir)
			if info, err := os.Stat(buildDir); err != nil || !info.IsDir() {
				fmt.Println(i18n.T("plugin.install.error.build",
					fmt.Errorf("subdirectory '%s' not found in repository", subDir), ""))
				return
			}
		}

		// Plugin name comes from the subdirectory (if present) or the repo name.
		pluginName := filepath.Base(buildDir)
		pluginName = strings.TrimSuffix(pluginName, ".git")
		if runtime.GOOS == "windows" {
			pluginName += ".exe"
		}

		buildCmd := exec.Command("go", "build", "-o", filepath.Join(pluginManager.PluginsDir(), pluginName), ".")
		buildCmd.Dir = buildDir
		if output, err := buildCmd.CombinedOutput(); err != nil {
			fmt.Println(i18n.T("plugin.install.error.build", err, string(output)))
			return
		}

		// Torna o arquivo executável para garantir
		if err := os.Chmod(filepath.Join(pluginManager.PluginsDir(), pluginName), 0755); err != nil {
			fmt.Println(i18n.T("plugin.install.error.chmod", err))
			return
		}

		fmt.Println(i18n.T("plugin.reloading"))
		pluginManager.Reload()
		fmt.Println(i18n.T("plugin.reload_success"))

	case "uninstall":
		if len(args) < 3 {
			fmt.Println(i18n.T("plugin.uninstall.usage"))
			return
		}
		p, found := pluginManager.GetPlugin(args[2])
		if !found {
			fmt.Println(i18n.T("plugin.error.not_found", args[2]))
			return
		}
		if p.Path() == "[builtin]" || p.Path() == "[remote]" {
			fmt.Println(i18n.T("plugin.uninstall.error.not_local"))
			return
		}
		if err := os.Remove(p.Path()); err != nil {
			fmt.Println(i18n.T("plugin.uninstall.error", p.Name(), err))
			return
		}
		fmt.Println(i18n.T("plugin.uninstall.success", p.Name()))
		pluginManager.Reload()

	case "reload":
		fmt.Println(i18n.T("plugin.reloading"))
		pluginManager.Reload()
		fmt.Println(i18n.T("plugin.reload_success"))

	default:
		fmt.Println(i18n.T("plugin.error.unknown_subcommand", subcommand))
	}
}

// handleAgentPersonaSubcommand verifica se o comando /agent contém um subcomando de persona
// Retorna true se foi tratado como subcomando, false se deve entrar no modo agente
func (ch *CommandHandler) handleAgentPersonaSubcommand(userInput string) bool {
	if ch.cli.personaHandler == nil {
		return false
	}

	args := strings.Fields(userInput)
	if len(args) < 2 {
		// Apenas "/agent" sem argumentos - inicia modo agente (igual /run)
		return false
	}

	subcommand := strings.ToLower(args[1])

	// Subcomandos de gerenciamento de personas
	switch subcommand {
	case "list":
		ch.cli.personaHandler.ListAgents()
		return true
	case "load":
		if len(args) < 3 {
			fmt.Println(colorize(i18n.T("agent.persona.load.usage"), ColorYellow))
			return true
		}
		ch.cli.personaHandler.LoadAgent(args[2])
		return true
	case "attach", "add":
		if len(args) < 3 {
			fmt.Println(colorize("Uso: /agent attach <nome>", ColorYellow))
			return true
		}
		ch.cli.personaHandler.AttachAgent(args[2])
		return true
	case "detach", "remove", "rm":
		if len(args) < 3 {
			fmt.Println(colorize("Uso: /agent detach <nome>", ColorYellow))
			return true
		}
		ch.cli.personaHandler.DetachAgent(args[2])
		return true
	case "show":
		full := false
		if len(args) > 2 && args[2] == "--full" || args[2] == "-f" {
			full = true
		}
		ch.cli.personaHandler.ShowActive(full)
		return true
	case "status", "attached", "list-attached":
		ch.cli.personaHandler.ShowAttachedAgents()
		return true
	case "off", "unload", "reset":
		ch.cli.personaHandler.UnloadAgent()
		return true
	case "skills":
		ch.cli.personaHandler.ListSkills()
		return true
	case "help":
		ch.cli.personaHandler.ShowHelp()
		return true
	default:
		// Não é um subcomando de persona, deve ser uma tarefa para o modo agente
		return false
	}
}

// parseGitURL parses a git URL that may contain a subdirectory path.
// It supports GitHub and GitLab "tree" URLs like:
//
//	https://github.com/owner/repo/tree/branch/path/to/plugin
//	https://gitlab.com/owner/repo/-/tree/branch/path/to/plugin
//
// Returns (cloneURL, branch, subDir). For plain repo URLs, branch and subDir are empty.
func parseGitURL(rawURL string) (cloneURL, branch, subDir string) {
	// GitHub: https://github.com/{owner}/{repo}/tree/{branch}/{path...}
	if idx := strings.Index(rawURL, "/tree/"); idx != -1 {
		repoBase := rawURL[:idx]
		rest := rawURL[idx+len("/tree/"):]

		// rest = "branch/path/to/plugin" or just "branch"
		if slashIdx := strings.IndexByte(rest, '/'); slashIdx != -1 {
			branch = rest[:slashIdx]
			subDir = rest[slashIdx+1:]
		} else {
			branch = rest
		}
		// Remove trailing slashes from subDir
		subDir = strings.TrimRight(subDir, "/")
		return repoBase + ".git", branch, subDir
	}

	// GitLab: https://gitlab.com/{owner}/{repo}/-/tree/{branch}/{path...}
	if idx := strings.Index(rawURL, "/-/tree/"); idx != -1 {
		repoBase := rawURL[:idx]
		rest := rawURL[idx+len("/-/tree/"):]

		if slashIdx := strings.IndexByte(rest, '/'); slashIdx != -1 {
			branch = rest[:slashIdx]
			subDir = rest[slashIdx+1:]
		} else {
			branch = rest
		}
		subDir = strings.TrimRight(subDir, "/")
		return repoBase + ".git", branch, subDir
	}

	// Plain URL — return as-is.
	return rawURL, "", ""
}

// handleMetricsCommand displays runtime telemetry in the terminal.
func (ch *CommandHandler) handleMetricsCommand() {
	c := ch.cli

	fmt.Println()
	fmt.Println(colorize("  ChatCLI Runtime Metrics", ColorLime))
	fmt.Println()

	// Provider & Model
	model := ""
	if c.Client != nil {
		model = c.Client.GetModelName()
	}
	fmt.Printf("    %s    %s\n", colorize("Provider:", ColorCyan), colorize(c.Provider, ColorGray))
	fmt.Printf("    %s    %s\n", colorize("Model:", ColorCyan), colorize(model, ColorGray))

	// Session info
	sessionName := c.currentSessionName
	if sessionName == "" {
		sessionName = "(unsaved)"
	}
	fmt.Printf("    %s    %s\n", colorize("Session:", ColorCyan), colorize(sessionName, ColorGray))
	fmt.Printf("    %s    %s\n", colorize("History msgs:", ColorCyan), colorize(strconv.Itoa(len(c.history)), ColorGray))

	// Token usage estimate
	tokenLimit := c.UserMaxTokens
	if tokenLimit <= 0 {
		tokenLimit = c.getMaxTokensForCurrentLLM()
	}
	tokenUsed := 0
	for _, msg := range c.history {
		tokenUsed += len(msg.Content) / 4 // rough estimate: ~4 chars per token
	}
	tokenPct := 0.0
	if tokenLimit > 0 {
		tokenPct = float64(tokenUsed) / float64(tokenLimit) * 100
	}
	fmt.Printf("    %s    %s\n", colorize("Tokens (est.):", ColorCyan),
		colorize(fmt.Sprintf("%d / %d (%.1f%%)", tokenUsed, tokenLimit, tokenPct), ColorGray))

	// Turn count
	turns := 0
	for _, msg := range c.history {
		if msg.Role == "user" {
			turns++
		}
	}
	fmt.Printf("    %s    %s\n", colorize("Turns:", ColorCyan), colorize(strconv.Itoa(turns), ColorGray))

	// Go runtime
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Println()
	fmt.Println(colorize("  Go Runtime", ColorLime))
	fmt.Println()
	fmt.Printf("    %s    %s\n", colorize("Goroutines:", ColorCyan), colorize(strconv.Itoa(runtime.NumGoroutine()), ColorGray))
	fmt.Printf("    %s    %s\n", colorize("Alloc:", ColorCyan), colorize(formatBytes(m.Alloc), ColorGray))
	fmt.Printf("    %s    %s\n", colorize("Sys:", ColorCyan), colorize(formatBytes(m.Sys), ColorGray))
	fmt.Printf("    %s    %s\n", colorize("GC cycles:", ColorCyan), colorize(strconv.FormatUint(uint64(m.NumGC), 10), ColorGray))

	// Remote connection
	fmt.Println()
	fmt.Println(colorize("  Connection", ColorLime))
	fmt.Println()
	if c.isRemote {
		fmt.Printf("    %s    %s\n", colorize("Remote:", ColorCyan), colorize("connected", ColorGreen))
	} else {
		fmt.Printf("    %s    %s\n", colorize("Remote:", ColorCyan), colorize("local", ColorGray))
	}

	// Watcher
	if c.watchStatusFunc != nil {
		status := c.watchStatusFunc()
		if status != "" {
			fmt.Printf("    %s    %s\n", colorize("Watcher:", ColorCyan), colorize(status, ColorGray))
		}
	} else {
		fmt.Printf("    %s    %s\n", colorize("Watcher:", ColorCyan), colorize("inactive", ColorGray))
	}

	fmt.Println()
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
