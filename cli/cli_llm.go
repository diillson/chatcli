package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// processLLMRequest is the chat-mode turn entrypoint. It owns the
// animation/spinner lifecycle and the typed-ahead message queue; the
// per-turn pipeline phases (system-prompt assembly, history splicing,
// model/effort resolution, LLM execution, response handling) live in
// chat_pipeline.go so each step can be read and tested in isolation.
func (cli *ChatCLI) processLLMRequest(in string) {
	stopSpinner := cli.startProcessingLifecycle()
	defer cli.endProcessingLifecycle(stopSpinner)

	ctx, releaseCtx := cli.acquireOperationContext()
	defer releaseCtx()

	cli.saveCheckpoint()
	cli.fireUserPromptSubmitHook(in)
	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())

	userInput, additionalContext := cli.processSpecialCommands(in)
	cli.compactHistoryIfNeeded(ctx)

	assembly := cli.assembleChatSystemPrompt(ctx, userInput, additionalContext)
	tempHistory := cli.buildChatTempHistory(assembly.parts, userInput, additionalContext)
	userMessage := models.Message{Role: "user", Content: userInput + additionalContext}

	effectiveMaxTokens := cli.getMaxTokensForCurrentLLM()
	cli.ensureModelCacheWarm()
	resolution := cli.resolveSkillClient(assembly.modelHint)
	cli.noticeSkillResolution(resolution)
	ctx = cli.applyChatEffortHint(ctx, assembly.effort)

	aiResponse, llmErr := cli.executeLLMTurn(
		ctx, resolution.Client, userInput, additionalContext,
		tempHistory, effectiveMaxTokens, resolution, stopSpinner,
	)
	cli.handleChatTurnResult(
		llmErr, userMessage, aiResponse, resolution.Client, resolution,
		userInput, additionalContext,
	)
}

// startProcessingLifecycle suppresses the foreground animation (so it never
// fights go-prompt's prefix), starts the prompt-prefix spinner goroutine,
// and flips the executing flag. The returned stopSpinner closure is safe to
// call multiple times and must be invoked once the LLM acknowledges the
// request so the prefix stops animating before we render output.
func (cli *ChatCLI) startProcessingLifecycle() func() {
	cli.animation.SetSuppressed(true)

	spinnerDone := make(chan struct{})
	var spinnerStopped atomic.Bool
	stopSpinner := func() {
		if spinnerStopped.CompareAndSwap(false, true) {
			close(spinnerDone)
			atomic.StoreInt32(&cli.prefixSpinnerIdx, 0)
		}
	}
	if runtime.GOOS != "windows" {
		go cli.runPrefixSpinner(spinnerDone)
	}
	cli.isExecuting.Store(true)
	return stopSpinner
}

// runPrefixSpinner ticks the prefix-spinner counter and forces a prompt
// redraw at ~4 Hz until spinnerDone closes. Exits cleanly on shutdown.
func (cli *ChatCLI) runPrefixSpinner(spinnerDone <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-spinnerDone:
			return
		case <-ticker.C:
			atomic.AddInt32(&cli.prefixSpinnerIdx, 1)
			cli.forceRefreshPrompt()
		}
	}
}

// endProcessingLifecycle reverses startProcessingLifecycle, then drains any
// message the user queued while the prior turn was running. The recursive
// re-entry is bounded by the queue cap (10 messages); when the queue is
// empty it restores the idle prompt.
func (cli *ChatCLI) endProcessingLifecycle(stopSpinner func()) {
	defer cli.animation.SetSuppressed(false)
	stopSpinner()

	if nextMsg := cli.dequeueMessage(); nextMsg != "" {
		cli.announceQueueDrain()
		cli.processLLMRequest(nextMsg)
		return
	}
	cli.isExecuting.Store(false)
	cli.interactionState = StateNormal
	fmt.Print("\033[0m")
	_ = os.Stdout.Sync()
	cli.forceRefreshPrompt()
}

// announceQueueDrain prints either the "processing remaining N" or
// "processing" notice before recursing into the queued turn.
func (cli *ChatCLI) announceQueueDrain() {
	cli.messageQueueMu.Lock()
	remaining := len(cli.messageQueue)
	cli.messageQueueMu.Unlock()
	cli.interactionState = StateProcessing
	fmt.Print("\033[0m")
	_ = os.Stdout.Sync()
	if remaining > 0 {
		fmt.Printf("\n  %s\n", colorize(i18n.T("queue.processing_remaining", remaining), ColorGray))
		return
	}
	fmt.Printf("\n  %s\n", colorize(i18n.T("queue.processing"), ColorGray))
}

// acquireOperationContext creates the cancellable context that drives the
// turn and stashes its cancel func in cli.operationCancel so /reset can
// interrupt mid-turn. The returned release closure clears the slot and
// fires cancel exactly once.
func (cli *ChatCLI) acquireOperationContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	cli.mu.Lock()
	cli.operationCancel = cancel
	cli.mu.Unlock()
	return ctx, func() {
		cli.mu.Lock()
		cli.operationCancel = nil
		cli.mu.Unlock()
		cancel()
	}
}

// fireUserPromptSubmitHook publishes the UserPromptSubmit event when the
// hook manager is enabled. Best-effort: hooks run asynchronously and any
// failure logs internally without blocking the turn.
func (cli *ChatCLI) fireUserPromptSubmitHook(in string) {
	if cli.hookManager == nil {
		return
	}
	wd, _ := os.Getwd()
	cli.hookManager.FireAsync(hooks.HookEvent{
		Type:       hooks.EventUserPromptSubmit,
		Timestamp:  time.Now(),
		UserPrompt: in,
		SessionID:  cli.currentSessionName,
		WorkingDir: wd,
	})
}

// compactHistoryIfNeeded runs the proxy-payload pre-flight warning and
// triggers history compaction when the current chat history exceeds the
// configured budget. Compaction errors leave cli.history untouched so the
// turn can still proceed with the un-compacted history.
func (cli *ChatCLI) compactHistoryIfNeeded(ctx context.Context) {
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	cli.warnIfHistoryExceedsProxyCap(cfg)
	if !cli.historyCompactor.NeedsCompaction(cli.history, cfg) {
		return
	}
	cli.historyCompactor.SetStatusCallback(func(stage CompactStage, msg string) {
		fmt.Printf("\r\033[K  %s\n", msg)
		_ = os.Stdout.Sync()
	})
	compacted, err := cli.historyCompactor.Compact(ctx, cli.history, cli.Client, cfg)
	cli.historyCompactor.SetStatusCallback(nil)
	if err == nil {
		cli.history = compacted
	}
}

// warnIfHistoryExceedsProxyCap fires a once-per-session notice when the
// uncompacted history is large enough that a corporate proxy could start
// rejecting requests. Only meaningful when the user has not set an explicit
// payload cap via DefaultCompactConfig.
func (cli *ChatCLI) warnIfHistoryExceedsProxyCap(cfg CompactConfig) {
	if cfg.MaxPayloadBytes != 0 || cli.proxyPayloadWarned {
		return
	}
	totalChars := 0
	for _, m := range cli.history {
		totalChars += len(m.Content)
	}
	if totalChars <= 2_500_000 {
		return
	}
	cli.proxyPayloadWarned = true
	cli.logger.Warn("Chat history exceeds 2.5 MB, no payload cap set",
		zap.Int("total_chars", totalChars))
	fmt.Printf("\r\033[K  ℹ %s\n",
		i18n.T("agent.preflight.warn_no_cap", FormatPayloadSize(totalChars)))
	_ = os.Stdout.Sync()
}

func (cli *ChatCLI) handleProviderSelection(in string) {
	availableProviders := cli.manager.GetAvailableProviders()
	choiceIndex, err := strconv.Atoi(in)
	if err != nil || choiceIndex < 1 || choiceIndex > len(availableProviders) {
		fmt.Println(i18n.T("error.invalid_choice_normal_mode"))
		return
	}

	newProvider := availableProviders[choiceIndex-1]
	var newModel string
	if newProvider == "OPENAI" {
		newModel = utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAIModel)
	}
	if newProvider == "CLAUDEAI" {
		newModel = utils.GetEnvOrDefault("ANTHROPIC_MODEL", config.DefaultClaudeAIModel)
	}
	if newProvider == "OPENAI_ASSISTANT" {
		newModel = utils.GetEnvOrDefault("OPENAI_ASSISTANT_MODEL", utils.GetEnvOrDefault("OPENAI_MODEL", config.DefaultOpenAiAssistModel))
	}
	if newProvider == "GOOGLEAI" {
		newModel = utils.GetEnvOrDefault("GOOGLEAI_MODEL", config.DefaultGoogleAIModel)
	}
	if newProvider == "XAI" {
		newModel = utils.GetEnvOrDefault("XAI_MODEL", config.DefaultXAIModel)
	}
	if newProvider == "ZAI" {
		newModel = utils.GetEnvOrDefault("ZAI_MODEL", config.DefaultZAIModel)
	}
	if newProvider == "MINIMAX" {
		newModel = utils.GetEnvOrDefault("MINIMAX_MODEL", config.DefaultMiniMaxModel)
	}
	if newProvider == "OLLAMA" {
		newModel = utils.GetEnvOrDefault("OLLAMA_MODEL", config.DefaultOllamaModel)
	}
	if newProvider == "COPILOT" {
		newModel = utils.GetEnvOrDefault("COPILOT_MODEL", config.DefaultCopilotModel)
	}
	if newProvider == "GITHUB_MODELS" {
		newModel = utils.GetEnvOrDefault("GITHUB_MODELS_MODEL", config.DefaultGitHubModelsModel)
	}

	newClient, err := cli.manager.GetClient(newProvider, newModel)
	if err != nil {
		cli.logger.Error("Erro ao trocar de provedor", zap.Error(err))
		fmt.Println(i18n.T("cli.error.switch_provider_failed"))
		return
	}

	cli.Client = newClient
	cli.Provider = newProvider
	cli.Model = newModel
	fmt.Println(i18n.T("status.provider_switched", cli.Client.GetModelName(), cli.Provider))
	fmt.Println()
	cli.refreshModelCache()
}

func (cli *ChatCLI) handleSwitchCommand(userInput string) {
	args := strings.Fields(userInput)
	var newModel, newRealm, newAgentID string
	shouldSwitchModel, shouldUpdateStackSpot := false, false
	maxTokensOverride := -1

	listModels := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 < len(args) {
				newModel = args[i+1]
				shouldSwitchModel = true
				i++ // Pular o valor
			} else {
				listModels = true
			}
		case "--max-tokens":
			if i+1 < len(args) {
				val, err := strconv.Atoi(args[i+1])
				if err == nil && val >= 0 {
					maxTokensOverride = val
				} else {
					fmt.Println(i18n.T("cli.switch.invalid_max_tokens", args[i+1]))
				}
				i++
			}
		case "--realm":
			if i+1 < len(args) {
				newRealm = args[i+1]
				shouldUpdateStackSpot = true
				i++
			}
		case "--agent-id":
			if i+1 < len(args) {
				newAgentID = args[i+1]
				shouldUpdateStackSpot = true
				i++
			}
		}
	}
	if maxTokensOverride != -1 {
		cli.UserMaxTokens = maxTokensOverride
		fmt.Println(i18n.T("cli.switch.max_tokens_set", cli.UserMaxTokens))
	}

	if shouldUpdateStackSpot {
		if cli.Provider != "STACKSPOT" {
			fmt.Println(i18n.T("cli.switch.stackspot_only_flags"))
			return
		}
		if newRealm != "" {
			cli.manager.SetStackSpotRealm(newRealm)
			fmt.Println(i18n.T("cli.switch.realm_updated", newRealm))
		}
		if newAgentID != "" {
			cli.manager.SetStackSpotAgentID(newAgentID)
			newClient, err := cli.manager.GetClient("STACKSPOT", "")
			if err != nil {
				fmt.Println(i18n.T("cli.switch.agent_id_error", err))
			} else {
				cli.Client = newClient
				fmt.Println(i18n.T("cli.switch.agent_id_updated", newAgentID))
			}
		}
	}

	if listModels {
		cli.listAvailableModels()
		return
	}

	if shouldSwitchModel {
		fmt.Println(i18n.T("cli.switch.changing_model", newModel, cli.Provider))
		newClient, err := cli.manager.GetClient(cli.Provider, newModel)
		if err != nil {
			fmt.Println(i18n.T("cli.switch.change_model_error", newModel, err))
			cli.listAvailableModels()
		} else {
			cli.Client = newClient
			cli.Model = newModel
			fmt.Println(i18n.T("cli.switch.change_model_success", cli.Client.GetModelName(), cli.Provider))
		}
		return
	}

	if !shouldSwitchModel && maxTokensOverride == -1 && len(args) == 1 {
		cli.switchProvider()
	}
}

func (cli *ChatCLI) listAvailableModels() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	models, err := cli.manager.ListModelsForProvider(ctx, cli.Provider)
	if err != nil {
		fmt.Printf("  %s\n", i18n.T("sw.cmd.could_not_list_models", cli.Provider, err))
		return
	}
	if len(models) == 0 {
		fmt.Printf("  %s\n", i18n.T("sw.cmd.no_models_found", cli.Provider))
		return
	}

	// Count sources
	apiCount, catalogCount := 0, 0
	for _, m := range models {
		if m.Source == client.ModelSourceAPI {
			apiCount++
		} else {
			catalogCount++
		}
	}

	sourceInfo := "catalog"
	if apiCount > 0 && catalogCount > 0 {
		sourceInfo = fmt.Sprintf("API: %d + catalog: %d", apiCount, catalogCount)
	} else if apiCount > 0 {
		sourceInfo = "API"
	}
	fmt.Printf("\n  %s\n", i18n.T("sw.cmd.available_models_header", cli.Provider, sourceInfo))
	for i, m := range models {
		tag := ""
		if m.Source == client.ModelSourceAPI {
			tag = " [api]"
		}
		if m.DisplayName != "" && m.DisplayName != m.ID {
			fmt.Printf("  %d. %s (%s)%s\n", i+1, m.ID, m.DisplayName, tag)
		} else {
			fmt.Printf("  %d. %s%s\n", i+1, m.ID, tag)
		}
	}
	fmt.Println()
}

// refreshModelCache fetches available models for the current provider in background
// and caches them for autocomplete suggestions.
func (cli *ChatCLI) refreshModelCache() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		models, err := cli.manager.ListModelsForProvider(ctx, cli.Provider)
		if err != nil {
			cli.logger.Debug("Failed to refresh model cache", zap.String("provider", cli.Provider), zap.Error(err))
			return
		}

		cli.cachedModelsMu.Lock()
		cli.cachedModels = models
		cli.cachedModelsMu.Unlock()

		cli.logger.Debug("Model cache refreshed", zap.String("provider", cli.Provider), zap.Int("count", len(models)))
	}()
}

// getCachedModels returns the cached model list (thread-safe).
func (cli *ChatCLI) getCachedModels() []client.ModelInfo {
	cli.cachedModelsMu.RLock()
	defer cli.cachedModelsMu.RUnlock()
	if len(cli.cachedModels) > 0 {
		return cli.cachedModels
	}
	// Fallback: use static catalog
	metas := catalog.ListByProvider(cli.Provider)
	result := make([]client.ModelInfo, len(metas))
	for i, m := range metas {
		result[i] = client.ModelInfo{ID: m.ID, DisplayName: m.DisplayName, Source: client.ModelSourceCatalog}
	}
	return result
}

func (cli *ChatCLI) switchProvider() {
	fmt.Println(i18n.T("cli.switch.available_providers"))
	availableProviders := cli.manager.GetAvailableProviders()
	for i, provider := range availableProviders {
		fmt.Printf("%d. %s\n", i+1, provider)
	}
	cli.interactionState = StateSwitchingProvider
}

func (cli *ChatCLI) getTokenEstimatorForCurrentLLM() func(string) int {
	// Função padrão - estimativa conservadora
	return func(text string) int {
		// Aproximadamente 4 caracteres por token para a maioria dos modelos
		return len(text) / 4
	}
}

// refreshClientOnAuthError checks if the error is a 401/auth error from an OAuth provider.
// If so, it invalidates the auth cache, refreshes credentials, and recreates the client.
// Returns true if the client was refreshed and the caller should retry.
//
// A 403 that carries WAF / proxy / firewall signals is deliberately NOT
// treated as an auth error — invalidating the user's OAuth cache there
// would destroy valid credentials for no benefit (the token was fine;
// the corporate proxy rejected the payload or URL). Such 403s fall
// through for payload-recovery handling further up the call chain.
func (cli *ChatCLI) refreshClientOnAuthError(err error) bool {
	if err == nil {
		return false
	}
	if agent.IsProxyWAFRejection(err) {
		return false
	}
	var apiErr *utils.APIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == 401 || apiErr.StatusCode == 403) {
		cli.logger.Info("Auth error detected, refreshing OAuth credentials",
			zap.Int("status", apiErr.StatusCode),
			zap.String("provider", cli.Provider))
		auth.InvalidateCache()
		cli.manager.RefreshProviders()
		if newClient, cerr := cli.manager.GetClient(cli.Provider, cli.Model); cerr == nil {
			cli.mu.Lock()
			cli.Client = newClient
			cli.mu.Unlock()
			return true
		}
		cli.logger.Warn("Failed to recreate client after auth refresh",
			zap.String("provider", cli.Provider))
	}
	return false
}

// getClient returns the current LLM client, safe for use from background goroutines.
func (cli *ChatCLI) getClient() client.LLMClient {
	cli.mu.Lock()
	c := cli.Client
	cli.mu.Unlock()
	return c
}

// providerMaxTokensEnv maps each known provider (canonical, upper-cased name)
// to its operator-facing override env var. Keeping this as a table rather
// than a long if/else chain lets a new provider plug in with a single line
// and keeps the lookup function inside the project's cyclomatic budget.
var providerMaxTokensEnv = map[string]string{
	"OPENAI":        "OPENAI_MAX_TOKENS",
	"CLAUDEAI":      "ANTHROPIC_MAX_TOKENS",
	"GOOGLEAI":      "GOOGLEAI_MAX_TOKENS",
	"XAI":           "XAI_MAX_TOKENS",
	"ZAI":           "ZAI_MAX_TOKENS",
	"MINIMAX":       "MINIMAX_MAX_TOKENS",
	"OLLAMA":        "OLLAMA_MAX_TOKENS",
	"STACKSPOT":     "STACKSPOT_MAX_TOKENS",
	"COPILOT":       "COPILOT_MAX_TOKENS",
	"GITHUB_MODELS": "GITHUB_MODELS_MAX_TOKENS",
}

// getMaxTokensForCurrentLLM picks the per-turn `max_tokens` for the active
// LLM by walking the precedence chain:
//
//  1. session override set by `/switch --max-tokens` (cli.UserMaxTokens),
//  2. provider-specific env var (see providerMaxTokensEnv) — operational
//     escape hatch when an operator needs to force a value at process start,
//  3. the static catalog's recommended ceiling for (provider, model).
//
// Returns the catalog default whenever none of the override sources resolve
// to a positive integer.
func (cli *ChatCLI) getMaxTokensForCurrentLLM() int {
	if cli.UserMaxTokens > 0 {
		return cli.UserMaxTokens
	}
	override := providerMaxTokensOverride(cli.Provider)
	return catalog.GetMaxTokens(cli.Provider, cli.Model, override)
}

// providerMaxTokensOverride reads the configured env var for the provider
// and returns its positive-integer value, or 0 when the env is unset,
// empty, non-numeric, or non-positive. Unknown providers (or providers
// without a registered env var) silently yield 0 so the caller falls back
// to the catalog default.
func providerMaxTokensOverride(provider string) int {
	envName, ok := providerMaxTokensEnv[strings.ToUpper(provider)]
	if !ok {
		return 0
	}
	raw := os.Getenv(envName)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// estimateBytesFromTokens estima a quantidade de bytes baseada em tokens
func estimateBytesFromTokens(tokens int, estimator func(string) int) int64 {
	// Teste com uma string comum para calcular a razão bytes/token
	testString := strings.Repeat("typical code sample with various chars 12345!@#$%", 100)
	tokensInTest := estimator(testString)
	bytesPerToken := float64(len(testString)) / float64(tokensInTest)

	// Retorna bytes estimados com margem de segurança de 90%
	return int64(float64(tokens) * bytesPerToken * 0.9)
}
