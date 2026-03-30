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
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func (cli *ChatCLI) processLLMRequest(in string) {
	// Suppress animation so the spinner goroutine doesn't conflict with
	// go-prompt's rendering. The go-prompt prefix (changeLivePrefix) shows
	// processing status instead.
	cli.animation.SetSuppressed(true)
	defer cli.animation.SetSuppressed(false)

	// Animate the go-prompt prefix: a goroutine increments the spinner index
	// and sends SIGWINCH so go-prompt redraws with the updated prefix.
	// stopSpinner is safe to call multiple times.
	spinnerDone := make(chan struct{})
	var spinnerStopped atomic.Bool
	stopSpinner := func() {
		if spinnerStopped.CompareAndSwap(false, true) {
			close(spinnerDone)
			atomic.StoreInt32(&cli.prefixSpinnerIdx, 0)
		}
	}
	if runtime.GOOS != "windows" {
		go func() {
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
		}()
	}

	cli.isExecuting.Store(true)

	defer func() {
		// Stop prefix spinner (idempotent — may already be stopped before response)
		stopSpinner()

		// Check queue before going idle: process next queued message if any
		if nextMsg := cli.dequeueMessage(); nextMsg != "" {
			cli.messageQueueMu.Lock()
			remaining := len(cli.messageQueue)
			cli.messageQueueMu.Unlock()

			// Re-enter processing state for the queued message
			cli.interactionState = StateProcessing

			fmt.Print("\033[0m")
			os.Stdout.Sync()
			if remaining > 0 {
				fmt.Printf("\n  %s\n", colorize(i18n.T("queue.processing_remaining", remaining), ColorGray))
			} else {
				fmt.Printf("\n  %s\n", colorize(i18n.T("queue.processing"), ColorGray))
			}

			// Recursive call: isExecuting stays true, bounded by queue cap (10)
			cli.processLLMRequest(nextMsg)
			return
		}

		cli.isExecuting.Store(false)
		cli.interactionState = StateNormal

		// Limpar terminal antes de refresh
		fmt.Print("\033[0m") // Reset ANSI
		os.Stdout.Sync()

		cli.forceRefreshPrompt()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cli.mu.Lock()
	cli.operationCancel = cancel
	cli.mu.Unlock()

	defer func() {
		cli.mu.Lock()
		cli.operationCancel = nil
		cli.mu.Unlock()
		cancel()
	}()

	// Save checkpoint before processing (for rewind support)
	cli.saveCheckpoint()

	// Fire UserPromptSubmit hook
	if cli.hookManager != nil {
		wd, _ := os.Getwd()
		cli.hookManager.FireAsync(hooks.HookEvent{
			Type:       hooks.EventUserPromptSubmit,
			Timestamp:  time.Now(),
			UserPrompt: in,
			SessionID:  cli.currentSessionName,
			WorkingDir: wd,
		})
	}

	cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())

	userInput, additionalContext := cli.processSpecialCommands(in)

	// Compact history if over budget (before building tempHistory)
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)
	if cli.historyCompactor.NeedsCompaction(cli.history, cfg) {
		if compacted, err := cli.historyCompactor.Compact(ctx, cli.history, cli.Client, cfg); err == nil {
			cli.history = compacted
		}
	}

	// Build unified system prompt: bootstrap + memory + attached contexts + K8s watcher
	// All stable context goes into a single system message for provider-level prompt caching
	// (Anthropic cache_control:ephemeral, OpenAI automatic prompt caching).
	sessionID := cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}

	var systemParts []models.ContentBlock

	// Part 0: Mode awareness + language instruction
	modeAndLang := ChatModeSystemHint + "\n" + i18n.T("ai.response_language")
	systemParts = append(systemParts, models.ContentBlock{
		Type:         "text",
		Text:         modeAndLang,
		CacheControl: &models.CacheControl{Type: "ephemeral"},
	})

	// Part 1: Workspace context (SOUL.md, USER.md, IDENTITY.md, RULES.md, MEMORY.md)
	// Extract hints from recent messages for smart memory retrieval
	if cli.contextBuilder != nil {
		var hints []string
		hintWindow := 3
		if len(cli.history) < hintWindow {
			hintWindow = len(cli.history)
		}
		if hintWindow > 0 {
			var recentTexts []string
			for _, msg := range cli.history[len(cli.history)-hintWindow:] {
				recentTexts = append(recentTexts, msg.Content)
			}
			hints = memory.ExtractKeywords(recentTexts)
		}

		if wsCtx := cli.contextBuilder.BuildSystemPromptPrefixWithHints(hints); wsCtx != "" {
			dynCtx := cli.contextBuilder.BuildDynamicContext()
			wsContent := wsCtx
			if dynCtx != "" {
				wsContent += "\n\n" + dynCtx
			}
			systemParts = append(systemParts, models.ContentBlock{
				Type:         "text",
				Text:         wsContent,
				CacheControl: &models.CacheControl{Type: "ephemeral"},
			})
		}
	}

	// Part 2: Attached contexts → system prompt (cacheable, not user messages)
	contextMessages, err := cli.contextHandler.GetManager().BuildPromptMessages(
		sessionID,
		ctxmgr.FormatOptions{
			IncludeMetadata:  true,
			IncludeTimestamp: false,
			Compact:          false,
			Role:             "system",
		},
	)
	if err != nil {
		cli.logger.Warn("Erro ao construir mensagens de contexto", zap.Error(err))
	}
	for _, msg := range contextMessages {
		systemParts = append(systemParts, models.ContentBlock{
			Type:         "text",
			Text:         msg.Content,
			CacheControl: &models.CacheControl{Type: "ephemeral"},
		})
	}

	// Part 3: Auto-triggered skills based on user input keywords
	if cli.personaHandler != nil {
		mgr := cli.personaHandler.GetManager()
		if mgr != nil {
			triggered := mgr.FindTriggeredSkills(userInput)
			if len(triggered) > 0 {
				var skillCtx strings.Builder
				skillCtx.WriteString("# Auto-loaded Skills\n\n")
				for _, skill := range triggered {
					skillCtx.WriteString(fmt.Sprintf("## Skill: %s\n\n", skill.Name))
					if skill.Description != "" {
						skillCtx.WriteString(skill.Description + "\n\n")
					}
					skillCtx.WriteString(skill.Content + "\n\n")
				}
				systemParts = append(systemParts, models.ContentBlock{
					Type: "text",
					Text: skillCtx.String(),
				})
			}
		}
	}

	// Part 5: MCP Channel messages (recent push messages from servers)
	if cli.mcpManager != nil {
		channelCtx := cli.mcpManager.Channels().FormatForPrompt(5)
		if channelCtx != "" {
			systemParts = append(systemParts, models.ContentBlock{
				Type: "text",
				Text: channelCtx,
			})
		}
	}

	// Part 6: MCP tools context (deferred — name+description only, saves tokens)
	if cli.mcpManager != nil {
		mcpTools := cli.mcpManager.GetToolsSummary()
		if len(mcpTools) > 0 {
			var mcpCtx strings.Builder
			mcpCtx.WriteString("# Available MCP Tools\n\n")
			mcpCtx.WriteString("The following external tools are available via MCP servers. ")
			mcpCtx.WriteString("In agent/coder mode they can be invoked directly.\n\n")
			for _, t := range mcpTools {
				mcpCtx.WriteString(fmt.Sprintf("- **%s**: %s\n", t.Function.Name, t.Function.Description))
			}
			systemParts = append(systemParts, models.ContentBlock{
				Type: "text",
				Text: mcpCtx.String(),
			})
		}
	}

	// Part 4: K8s watcher context (small, changes often — no cache hint)
	if cli.WatcherContextFunc != nil {
		if k8sCtx := cli.WatcherContextFunc(); k8sCtx != "" {
			systemParts = append(systemParts, models.ContentBlock{
				Type: "text",
				Text: k8sCtx,
			})
		}
	}

	// Build tempHistory with unified system message.
	// SystemParts carries structured blocks with cache hints (used by Anthropic tool_use).
	// Content carries the same text concatenated (used by all other providers/flows).
	tempHistory := make([]models.Message, 0, len(cli.history)+4)

	if len(systemParts) > 0 {
		var combined strings.Builder
		for i, part := range systemParts {
			if i > 0 {
				combined.WriteString("\n\n---\n\n")
			}
			combined.WriteString(part.Text)
		}
		tempHistory = append(tempHistory, models.Message{
			Role:        "system",
			Content:     combined.String(),
			SystemParts: systemParts,
		})
	}

	// 1. Copiar mensagens do sistema existentes do histórico
	for _, msg := range cli.history {
		if msg.Role == "system" {
			tempHistory = append(tempHistory, msg)
		}
	}

	// 2. Adicionar restante do histórico (user/assistant)
	for _, msg := range cli.history {
		if msg.Role != "system" {
			tempHistory = append(tempHistory, msg)
		}
	}

	// 3. Adicionar mensagem atual do usuário
	userMessage := models.Message{
		Role:    "user",
		Content: userInput + additionalContext,
	}
	tempHistory = append(tempHistory, userMessage)

	effectiveMaxTokens := cli.getMaxTokensForCurrentLLM()

	// Usar histórico temporário com contextos injetados
	aiResponse, err := cli.Client.SendPrompt(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
	// Auto-retry on OAuth token expiration (401)
	if cli.refreshClientOnAuthError(err) {
		aiResponse, err = cli.Client.SendPrompt(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
	}

	cli.animation.StopThinkingAnimation()

	// Stop the prefix spinner before printing the response.
	// Without this, the SIGWINCH signals cause go-prompt to redraw the
	// [ModelName ⠹] prefix in the middle of the response text.
	stopSpinner()
	cli.interactionState = StateNormal
	cli.forceRefreshPrompt()

	// Pequena pausa para garantir que o terminal está limpo
	time.Sleep(50 * time.Millisecond)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println(i18n.T("status.operation_cancelled"))
			// Não adicionar ao histórico se cancelado
		} else {
			fmt.Println(i18n.T("error.generic", err.Error()))
		}
	} else {
		// Adicionar APENAS a mensagem do usuário e resposta ao histórico permanente
		// (Contextos não são salvos no histórico para não poluir)
		cli.history = append(cli.history, userMessage)
		cli.history = append(cli.history, models.Message{
			Role:    "assistant",
			Content: aiResponse,
		})

		// Track cost: estimate tokens from character count
		if cli.costTracker != nil {
			promptTokens := len(userInput+additionalContext) / 4
			completionTokens := len(aiResponse) / 4
			cli.costTracker.RecordUsage(cli.Provider, cli.Model, promptTokens, completionTokens)
		}

		// Exibir nome do modelo como label na linha do prompt
		modelName := cli.Client.GetModelName()
		fmt.Printf("%s\n", colorize(modelName+":", ColorPurple))

		// Garantir que markdown renderizado termina com reset
		renderedResponse := cli.renderMarkdown(aiResponse)
		renderedResponse = ensureANSIReset(renderedResponse)

		cli.typewriterEffect(renderedResponse, 2*time.Millisecond)
		fmt.Print("\033[0m") // Reset final
		fmt.Println()
		fmt.Println() // Linha extra para separar visualmente as mensagens

		// Nudge background memory worker to check if annotations are needed
		if cli.memWorker != nil {
			cli.memWorker.nudge()
		}
	}
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
	if newProvider == "OLLAMA" {
		newModel = utils.GetEnvOrDefault("OLLAMA_MODEL", config.DefaultOllamaModel)
	}
	if newProvider == "COPILOT" {
		newModel = utils.GetEnvOrDefault("COPILOT_MODEL", config.DefaultCopilotModel)
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
		fmt.Printf("  Could not list models for %s: %v\n", cli.Provider, err)
		return
	}
	if len(models) == 0 {
		fmt.Printf("  No models found for %s\n", cli.Provider)
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
	fmt.Printf("\n  Available models for %s (%s):\n", cli.Provider, sourceInfo)
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
func (cli *ChatCLI) refreshClientOnAuthError(err error) bool {
	if err == nil {
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

func (cli *ChatCLI) getMaxTokensForCurrentLLM() int {
	// 1. Prioridade máxima para o override do usuário via flag
	if cli.UserMaxTokens > 0 {
		return cli.UserMaxTokens
	}

	// Overrides por ENV têm precedência e dão flexibilidade operacional
	var override int
	if strings.ToUpper(cli.Provider) == "OPENAI" {
		if v := os.Getenv("OPENAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "CLAUDEAI" {
		if v := os.Getenv("ANTHROPIC_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "GOOGLEAI" {
		if v := os.Getenv("GOOGLEAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "XAI" {
		if v := os.Getenv("XAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "OLLAMA" {
		if v := os.Getenv("OLLAMA_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "STACKSPOT" {
		if v := os.Getenv("STACKSPOT_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	}
	return catalog.GetMaxTokens(cli.Provider, cli.Model, override)
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
