/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/client/remote"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// handleConnectCommand handles the /connect <address> [flags] command.
// It connects to a remote ChatCLI gRPC server and swaps the LLM client.
func (ch *CommandHandler) handleConnectCommand(userInput string) {
	args := strings.Fields(userInput)

	if len(args) < 2 {
		fmt.Println(colorize(i18n.T("connect.usage.main"), ColorYellow))
		fmt.Println(colorize(i18n.T("connect.usage.stackspot"), ColorYellow))
		fmt.Println(colorize(i18n.T("connect.usage.ollama"), ColorYellow))
		fmt.Println(colorize(i18n.T("connect.usage.tls"), ColorYellow))
		return
	}

	if ch.cli.isRemote {
		fmt.Println(colorize(i18n.T("connect.error.already_connected"), ColorYellow))
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
			fmt.Println(colorize(i18n.T("connect.error.resolve_local_auth", err), ColorRed))
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

	fmt.Println(colorize(i18n.T("connect.status.connecting", address), ColorCyan))

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
		fmt.Println(colorize(i18n.T("connect.error.connection_failed", err), ColorRed))
		return
	}

	// Health check
	healthy, ver, err := remoteClient.Health(ctx)
	if err != nil {
		_ = remoteClient.Close()
		fmt.Println(colorize(i18n.T("connect.error.health_check_failed", err), ColorRed))
		return
	}
	if !healthy {
		_ = remoteClient.Close()
		fmt.Println(colorize(i18n.T("connect.error.server_not_healthy"), ColorRed))
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
	ch.cli.remoteAddress = address
	ch.cli.refreshModelCache()

	connInfo := fmt.Sprintf("version: %s, provider: %s, model: %s", ver, ch.cli.Provider, ch.cli.Model)
	if useLocalAuth {
		connInfo += ", " + i18n.T("connect.info.using_local_oauth")
	} else if llmKey != "" {
		connInfo += ", " + i18n.T("connect.info.using_api_key")
	}
	fmt.Println(colorize(i18n.T("connect.status.connected", connInfo), ColorGreen))

	// Show watcher status and remote resources if server has them
	infoCtx, infoCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer infoCancel()
	if info, err := remoteClient.GetServerInfo(infoCtx); err == nil {
		if info.WatcherActive {
			fmt.Println(colorize(i18n.T("connect.info.watcher_active", info.WatcherTarget), ColorCyan))
		}
		if info.PluginCount > 0 || info.AgentCount > 0 || info.SkillCount > 0 {
			fmt.Println(colorize(fmt.Sprintf(" %s", i18n.T("remote.resources.available", info.PluginCount, info.AgentCount, info.SkillCount)), ColorCyan))
		}
	}

	// Discover and register remote plugins
	ch.discoverRemoteResources(remoteClient)

	fmt.Println(colorize(i18n.T("connect.hint.disconnect"), ColorCyan))
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
		fmt.Println(colorize(i18n.T("connect.error.not_connected"), ColorYellow))
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
		_ = ch.cli.remoteConn.Close()
	}

	// Restore local state
	ch.cli.Client = ch.cli.localClient
	ch.cli.Provider = ch.cli.localProvider
	ch.cli.Model = ch.cli.localModel
	ch.cli.remoteConn = nil
	ch.cli.isRemote = false
	ch.cli.remoteAddress = ""
	ch.cli.localClient = nil
	ch.cli.localProvider = ""
	ch.cli.localModel = ""
	ch.cli.refreshModelCache()

	fmt.Println(colorize(i18n.T("connect.status.disconnected"), ColorGreen))
	if ch.cli.Client != nil {
		fmt.Println(colorize(i18n.T("connect.status.back_to_local", ch.cli.Model, ch.cli.Provider), ColorCyan))
	} else {
		fmt.Println(colorize(i18n.T("connect.status.back_to_local_no_provider"), ColorYellow))
	}
}

// resolveLocalAuth reads the local auth store and returns the API key/OAuth token.
func (ch *CommandHandler) resolveLocalAuth(provider string) (apiKey string, resolvedProvider string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if provider != "" {
		authProvider, ok := llmProviderToAuthProvider(provider)
		if !ok {
			return "", "", fmt.Errorf(
				"--use-local-auth only supports OAuth providers (CLAUDEAI, OPENAI, COPILOT). "+
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
		{auth.ProviderGitHubCopilot, "COPILOT"},
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

	return "", "", fmt.Errorf("no local OAuth credentials found. Run '/auth login anthropic' or '/auth login openai-codex' or '/auth login github-copilot' first")
}

// llmProviderToAuthProvider maps LLMManager provider names to auth.ProviderID.
func llmProviderToAuthProvider(provider string) (auth.ProviderID, bool) {
	switch strings.ToUpper(provider) {
	case "CLAUDEAI":
		return auth.ProviderAnthropic, true
	case "OPENAI":
		return auth.ProviderOpenAI, true
	case "COPILOT":
		return auth.ProviderGitHubCopilot, true
	default:
		return "", false
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
	ch.cli.refreshModelCache()
}
