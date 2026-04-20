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
	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/hooks"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
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
			_ = os.Stdout.Sync()
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
		_ = os.Stdout.Sync()

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

	// Compact history if over budget (before building tempHistory).
	// Status callback feeds progress into the prompt-prefix animation so the
	// user never stares at a silent terminal during a long summarization.
	cfg := DefaultCompactConfig(cli.Provider, cli.Model)

	// Pre-flight: warn once per session when history is large enough that a
	// corporate proxy could start rejecting. Attached contexts (/context
	// attach) in chat mode can silently balloon the payload.
	if cfg.MaxPayloadBytes == 0 && !cli.proxyPayloadWarned {
		totalChars := 0
		for _, m := range cli.history {
			totalChars += len(m.Content)
		}
		if totalChars > 2_500_000 {
			cli.proxyPayloadWarned = true
			cli.logger.Warn("Chat history exceeds 2.5 MB, no payload cap set",
				zap.Int("total_chars", totalChars))
			fmt.Printf("\r\033[K  ℹ %s\n",
				i18n.T("agent.preflight.warn_no_cap", FormatPayloadSize(totalChars)))
			_ = os.Stdout.Sync()
		}
	}

	if cli.historyCompactor.NeedsCompaction(cli.history, cfg) {
		cli.historyCompactor.SetStatusCallback(func(stage CompactStage, msg string) {
			// Overwrite prompt-prefix spinner line with a compaction update,
			// keep cursor on a new line so the spinner resumes cleanly after.
			fmt.Printf("\r\033[K  %s\n", msg)
			_ = os.Stdout.Sync()
		})
		compacted, err := cli.historyCompactor.Compact(ctx, cli.history, cli.Client, cfg)
		cli.historyCompactor.SetStatusCallback(nil)
		if err == nil {
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

		// Phase 3 (#4): when HyDE is enabled in /config quality, hand
		// off to the HyDE-aware builder. The non-HyDE branch is kept
		// untouched to preserve byte-for-byte behaviour for users who
		// never opt in.
		var wsCtx string
		if qcfg := quality.LoadFromEnv(); qcfg.HyDE.Enabled && qcfg.Enabled {
			wsCtx = cli.hydeRetrieveContext(ctx, userInput, hints, qcfg)
		} else {
			wsCtx = cli.contextBuilder.BuildSystemPromptPrefixWithHints(hints)
		}
		if wsCtx != "" {
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

	// Part 2.5: Manually invoked skill via `/<skill-name>` (Fase 2).
	// Consumed once — cleared right after reading so it only affects this
	// turn. Goes BEFORE the auto-activated block so manual intent has
	// precedence when both happen simultaneously.
	var manualSkill *persona.Skill
	var manualSkillArgs string
	if cli.pendingManualSkill != nil {
		manualSkill = cli.pendingManualSkill
		manualSkillArgs = cli.pendingManualSkillArgs
		cli.pendingManualSkill = nil
		cli.pendingManualSkillArgs = ""
		if block := renderManualSkillBlock(manualSkill, manualSkillArgs); block != "" {
			systemParts = append(systemParts, models.ContentBlock{
				Type: "text",
				Text: block,
			})
		}
	}

	// Part 3: Auto-activated skills (triggers + path globs).
	//
	// Honors the full advanced frontmatter contract from the docs:
	//  - `triggers:` → keyword match against userInput
	//  - `paths:`    → glob match against file tokens extracted from userInput
	//                  and @file commands (supports `*`, `**`, basename match)
	//  - `disable-model-invocation` → skill is excluded from auto-activation
	//  - `model:` / `effort:` → forwarded to provider for this single turn
	//                           (see skillEffort / skillModelHint below)
	var skillEffort client.SkillEffort
	var skillModelHint string
	if cli.personaHandler != nil {
		mgr := cli.personaHandler.GetManager()
		if mgr != nil {
			filePaths := extractFilePaths(userInput + " " + additionalContext)
			activated := mgr.FindAutoActivatedSkills(userInput, filePaths)
			if len(activated) > 0 {
				block := buildSkillInjectionBlock(activated)
				if block != "" {
					systemParts = append(systemParts, models.ContentBlock{
						Type: "text",
						Text: block,
					})
				}
				// Resolve model/effort hints from the activated set.
				model, effort, conflict := pickSkillModelAndEffort(activated)
				skillModelHint = model
				skillEffort = client.NormalizeEffort(effort)
				if conflict != "" {
					cli.logger.Warn("multiple auto-activated skills disagree on model; using the first",
						zap.String("losing_skill", conflict),
						zap.String("chosen_model", skillModelHint))
				}
				cli.logger.Debug("auto-activated skills",
					zap.Int("count", len(activated)),
					zap.Strings("file_paths", filePaths),
					zap.String("skill_model", skillModelHint),
					zap.String("skill_effort", string(skillEffort)))
			}
		}
	}

	// Manual invocation hints override auto-activation hints (explicit
	// user intent wins), but only when the manual skill actually sets them.
	if manualSkill != nil {
		if m := strings.TrimSpace(manualSkill.Model); m != "" {
			skillModelHint = m
		}
		if e := strings.TrimSpace(manualSkill.Effort); e != "" {
			skillEffort = client.NormalizeEffort(e)
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
		// Sync MCP shadow state: hide built-ins overridden by connected MCP servers
		if cli.pluginManager != nil {
			cli.pluginManager.SetShadowedBuiltins(cli.mcpManager.GetShadowedBuiltins())
		}

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

	// Resolve the per-turn client from any skill model hint. The resolver
	// tries (in order):
	//   1. the user provider's API-cached model list,
	//   2. the static catalog (exact, alias, prefix),
	//   3. a family-prefix heuristic (claude-*, gpt-*, gemini-*, ...),
	//   4. an optimistic same-provider attempt.
	// Cross-provider swaps are honored when the target provider has an API
	// key configured; otherwise we stay on the user's client and surface a
	// user-visible notice so the skill's preference is never silently
	// dropped. cli.Client / cli.Provider / cli.Model are NOT mutated.
	cli.ensureModelCacheWarm()
	resolution := cli.resolveSkillClient(skillModelHint)
	activeClient := resolution.Client
	if resolution.Changed {
		cli.logger.Info("skill model hint honored",
			zap.String("note", resolution.Note),
			zap.String("from_provider", cli.Provider),
			zap.String("to_provider", resolution.Provider),
			zap.String("from_model", cli.Model),
			zap.String("to_model", resolution.Model))
		if resolution.CrossProvider {
			fmt.Printf("  %s\n", colorize(
				i18n.T("sw.cmd.skill_swap_provider", resolution.Provider, resolution.Model),
				ColorGray))
		}
	} else if resolution.UserMessage != "" {
		fmt.Printf("  %s\n", colorize("⚠ "+resolution.UserMessage, ColorYellow))
	}

	// Attach effort hint to ctx so the provider's SendPrompt can opt into
	// extended thinking / reasoning_effort for this turn. /thinking
	// (Phase 1 of the seven-pattern rollout) lets the user override the
	// skill-derived hint for the next turn — when the override is set
	// to EffortUnset, no hint is attached so the provider falls back to
	// its no-thinking default.
	if eff, overridden := cli.applyThinkingOverride(skillEffort); overridden {
		if eff != client.EffortUnset {
			ctx = client.WithEffortHint(ctx, eff)
		}
	} else {
		ctx = client.WithEffortHint(ctx, skillEffort)
	}

	// Try streaming if supported, fall back to buffered request
	var aiResponse string

	if sc, ok := client.AsStreamingClient(activeClient); ok {
		// Real-time streaming: display chunks as they arrive
		chunks, streamErr := sc.SendPromptStream(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
		if streamErr != nil && cli.refreshClientOnAuthError(streamErr) {
			chunks, streamErr = sc.SendPromptStream(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
		}

		cli.animation.StopThinkingAnimation()
		stopSpinner()
		cli.interactionState = StateNormal
		cli.forceRefreshPrompt()
		time.Sleep(50 * time.Millisecond)

		if streamErr != nil {
			err = streamErr
		} else {
			modelName := activeClient.GetModelName()
			fmt.Printf("%s\n", colorize(modelName+":", ColorPurple))

			// Stream chunks with watchdog protection
			result := client.WatchStream(ctx, chunks, client.DefaultWatchdogConfig(), cli.logger)
			aiResponse = result.Text

			if result.WasStalled {
				fmt.Printf("\n%s\n", colorize(i18n.T("sw.cmd.stream_stalled"), ColorYellow))
			}

			// Store usage from streaming result against whichever
			// provider+model actually served the turn.
			if result.Usage != nil {
				cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model, result.Usage)
			}
		}
	} else {
		// Non-streaming: buffered request
		aiResponse, err = activeClient.SendPrompt(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
		if cli.refreshClientOnAuthError(err) {
			aiResponse, err = activeClient.SendPrompt(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
		}

		cli.animation.StopThinkingAnimation()
		stopSpinner()
		cli.interactionState = StateNormal
		cli.forceRefreshPrompt()
		time.Sleep(50 * time.Millisecond)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println(i18n.T("status.operation_cancelled"))
		} else {
			fmt.Println(i18n.T("error.generic", err.Error()))
		}
	} else {
		cli.history = append(cli.history, userMessage)
		cli.history = append(cli.history, models.Message{
			Role:    "assistant",
			Content: aiResponse,
		})

		// Track cost for non-streaming path (streaming path tracks above)
		if cli.costTracker != nil {
			if !client.IsStreamingCapable(activeClient) {
				usage := client.GetUsageOrEstimate(activeClient, len(userInput+additionalContext), len(aiResponse))
				cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model, usage)
			}
		}

		// Display response (streaming path already displayed inline)
		if !client.IsStreamingCapable(activeClient) {
			modelName := activeClient.GetModelName()
			fmt.Printf("%s\n", colorize(modelName+":", ColorPurple))

			renderedResponse := cli.renderMarkdown(aiResponse)
			renderedResponse = ensureANSIReset(renderedResponse)
			cli.typewriterEffect(renderedResponse, 2*time.Millisecond)
		} else {
			// For streaming, render the final markdown (streaming showed raw text)
			renderedResponse := cli.renderMarkdown(aiResponse)
			renderedResponse = ensureANSIReset(renderedResponse)
			// Overwrite streaming output with markdown-rendered version
			fmt.Print("\033[2K\r") // clear current line
			fmt.Print(renderedResponse)
		}
		fmt.Print("\033[0m")
		fmt.Println()
		fmt.Println()

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
	} else if strings.ToUpper(cli.Provider) == "ZAI" {
		if v := os.Getenv("ZAI_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "MINIMAX" {
		if v := os.Getenv("MINIMAX_MAX_TOKENS"); v != "" {
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
	} else if strings.ToUpper(cli.Provider) == "COPILOT" {
		if v := os.Getenv("COPILOT_MAX_TOKENS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				override = n
			}
		}
	} else if strings.ToUpper(cli.Provider) == "GITHUB_MODELS" {
		if v := os.Getenv("GITHUB_MODELS_MAX_TOKENS"); v != "" {
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
