/*
 * ChatCLI - Chat-mode request pipeline helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * processLLMRequest used to host the whole chat-mode pipeline inline, which
 * pushed its cyclomatic complexity well above the project's Quality Gate
 * budget. The helpers in this file extract each pipeline phase so the main
 * function can be read top-to-bottom as an orchestrator and so each phase
 * can be unit-tested independently.
 *
 * The split mirrors the conceptual phases the chat turn actually goes
 * through, in order:
 *
 *   1. assembleChatSystemPrompt — build the structured system prompt
 *      (workspace, contexts, manual + pinned + auto skills, MCP, K8s),
 *      and the model/effort hints derived from the active skills.
 *   2. buildChatTempHistory     — splice the new system message into the
 *      conversation history without mutating cli.history.
 *   3. resolveActiveClient      — honor any skill model hint, with a
 *      user-visible notice when the swap is cross-provider.
 *   4. applyChatEffortHint      — attach the reasoning-effort hint to
 *      ctx so the provider can opt into extended thinking.
 *   5. executeLLMTurn           — send the prompt (streaming if the client
 *      supports it, buffered otherwise).
 *   6. handleChatTurnResult     — append history, record cost, render.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
	"go.uber.org/zap"
)

// chatSystemAssembly is the result of assembling the system prompt for a
// single chat turn. Caller passes it on to history construction and to the
// model resolver. The *Hit and filePaths fields are diagnostic only —
// emitted to debug logs and consumed by tests.
type chatSystemAssembly struct {
	parts     []models.ContentBlock
	modelHint string
	effort    client.SkillEffort
	pinnedHit int
	autoHit   int
	manualHit bool
	filePaths []string
}

// assembleChatSystemPrompt builds every structured system-prompt block for
// a chat-mode turn in stable order. Caching hints (cache_control: ephemeral)
// are attached on the blocks that are stable across turns so the provider
// can hit warm-cache reads. Per-turn-volatile blocks omit the hint.
//
// Block layout (top → bottom of system prompt):
//
//	Part 0  — mode-awareness banner + language directive
//	Part 1  — workspace context (SOUL/USER/IDENTITY/RULES/MEMORY + dynamic)
//	Part 2  — attached `/context` entries (per session)
//	Part 2.5 — manually invoked skill (`/<skill-name>`) — consumed once
//	Part 3a — pinned skills (`/skill pin`) — stable across turns
//	Part 3b — auto-activated skills (triggers + path globs)
//	Part 5  — MCP channel messages
//	Part 6  — MCP tools catalog (name+description only)
//	Part 4  — K8s watcher context
//
// The function also captures the skill model/effort hints so the caller can
// route the turn to the right provider/model.
func (cli *ChatCLI) assembleChatSystemPrompt(
	ctx context.Context, userInput, additionalContext string,
) chatSystemAssembly {
	var out chatSystemAssembly
	out.parts = append(out.parts, modeAndLanguagePart())

	if part, ok := cli.workspaceContextPart(ctx, userInput); ok {
		out.parts = append(out.parts, part)
	}
	out.parts = append(out.parts, cli.attachedContextParts()...)

	manualSkill, manualSkillArgs := cli.consumePendingManualSkill()
	if manualSkill != nil {
		out.manualHit = true
		if block := renderManualSkillBlock(manualSkill, manualSkillArgs); block != "" {
			out.parts = append(out.parts, models.ContentBlock{Type: "text", Text: block})
		}
	}

	pinned, autoActivated, filePaths := cli.resolveSkillsForTurn(userInput, additionalContext)
	out.pinnedHit = len(pinned)
	out.autoHit = len(autoActivated)
	out.filePaths = filePaths
	out.parts = append(out.parts, skillContentBlocks(pinned, autoActivated)...)

	out.modelHint, out.effort = cli.pickSkillHints(pinned, autoActivated, filePaths)

	if manualSkill != nil {
		applyManualSkillHints(manualSkill, &out.modelHint, &out.effort)
	}

	if part, ok := cli.mcpChannelPart(); ok {
		out.parts = append(out.parts, part)
	}
	if part, ok := cli.mcpToolsPart(); ok {
		out.parts = append(out.parts, part)
	}
	if part, ok := cli.watcherContextPart(); ok {
		out.parts = append(out.parts, part)
	}
	return out
}

// modeAndLanguagePart returns the Part 0 block — mode-awareness banner
// concatenated with the i18n response-language directive. Always cacheable.
func modeAndLanguagePart() models.ContentBlock {
	return models.ContentBlock{
		Type:         "text",
		Text:         ChatModeSystemHint + "\n" + i18n.T("ai.response_language"),
		CacheControl: &models.CacheControl{Type: "ephemeral"},
	}
}

// workspaceContextPart builds Part 1 — bootstrap files plus the smart-memory
// retrieval block. Returns (zero, false) when there is no workspace context
// to inject (e.g. on a fresh repo with no SOUL.md / MEMORY.md).
func (cli *ChatCLI) workspaceContextPart(ctx context.Context, userInput string) (models.ContentBlock, bool) {
	if cli.contextBuilder == nil {
		return models.ContentBlock{}, false
	}
	hints := cli.recentHistoryHints()
	wsCtx := cli.retrieveWorkspaceContext(ctx, userInput, hints)
	if wsCtx == "" {
		return models.ContentBlock{}, false
	}
	if dyn := cli.contextBuilder.BuildDynamicContext(); dyn != "" {
		wsCtx += "\n\n" + dyn
	}
	return models.ContentBlock{
		Type:         "text",
		Text:         wsCtx,
		CacheControl: &models.CacheControl{Type: "ephemeral"},
	}, true
}

// retrieveWorkspaceContext selects between the HyDE-aware retriever (when
// /config quality has HyDE enabled) and the plain bootstrap-prefix builder.
// The non-HyDE branch matches the historical byte-for-byte behavior for
// users who never opt in.
func (cli *ChatCLI) retrieveWorkspaceContext(ctx context.Context, userInput string, hints []string) string {
	if qcfg := quality.LoadFromEnv(); qcfg.HyDE.Enabled && qcfg.Enabled {
		return cli.hydeRetrieveContext(ctx, userInput, hints, qcfg)
	}
	return cli.contextBuilder.BuildSystemPromptPrefixWithHints(hints)
}

// recentHistoryHints returns up to three keyword hints extracted from the
// tail of cli.history. Empty slice when history is empty.
func (cli *ChatCLI) recentHistoryHints() []string {
	const hintWindow = 3
	window := hintWindow
	if len(cli.history) < window {
		window = len(cli.history)
	}
	if window == 0 {
		return nil
	}
	texts := make([]string, 0, window)
	for _, msg := range cli.history[len(cli.history)-window:] {
		texts = append(texts, msg.Content)
	}
	return memory.ExtractKeywords(texts)
}

// attachedContextParts collects every `/context attach`ed entry for the
// current session and turns each into its own cacheable ContentBlock.
func (cli *ChatCLI) attachedContextParts() []models.ContentBlock {
	sessionID := cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}
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
		return nil
	}
	out := make([]models.ContentBlock, 0, len(contextMessages))
	for _, msg := range contextMessages {
		out = append(out, models.ContentBlock{
			Type:         "text",
			Text:         msg.Content,
			CacheControl: &models.CacheControl{Type: "ephemeral"},
		})
	}
	return out
}

// consumePendingManualSkill reads and clears the manual-skill staging slot.
// Returns (nil, "") when nothing is staged.
func (cli *ChatCLI) consumePendingManualSkill() (*persona.Skill, string) {
	if cli.pendingManualSkill == nil {
		return nil, ""
	}
	skill := cli.pendingManualSkill
	args := cli.pendingManualSkillArgs
	cli.pendingManualSkill = nil
	cli.pendingManualSkillArgs = ""
	return skill, args
}

// resolveSkillsForTurn loads pinned and auto-activated skills for the given
// input. Auto-activated skills are deduplicated against the pinned set so
// nothing is injected twice. Returns the extracted file paths for diagnostics.
func (cli *ChatCLI) resolveSkillsForTurn(
	userInput, additionalContext string,
) (pinned, autoActivated []*persona.Skill, filePaths []string) {
	if cli.personaHandler == nil {
		return nil, nil, nil
	}
	mgr := cli.personaHandler.GetManager()
	if mgr == nil {
		return nil, nil, nil
	}
	if cli.skillHandler != nil {
		pinned = cli.skillHandler.GetPinnedSkills()
	}
	filePaths = extractFilePaths(userInput + " " + additionalContext)
	autoActivated = mgr.FindAutoActivatedSkills(userInput, filePaths)
	autoActivated = dedupAutoAgainstPinned(autoActivated, pinned)
	return pinned, autoActivated, filePaths
}

// skillContentBlocks emits the pinned-skills block (cache-friendly, stable)
// followed by the auto-activated block (volatile, no cache hint). Either may
// be omitted when its source slice is empty.
func skillContentBlocks(pinned, autoActivated []*persona.Skill) []models.ContentBlock {
	var blocks []models.ContentBlock
	if len(pinned) > 0 {
		if block := buildPinnedSkillInjectionBlock(pinned); block != "" {
			blocks = append(blocks, models.ContentBlock{
				Type:         "text",
				Text:         block,
				CacheControl: &models.CacheControl{Type: "ephemeral"},
			})
		}
	}
	if len(autoActivated) > 0 {
		if block := buildSkillInjectionBlock(autoActivated); block != "" {
			blocks = append(blocks, models.ContentBlock{Type: "text", Text: block})
		}
	}
	return blocks
}

// pickSkillHints resolves model/effort hints across pinned + auto-activated
// skills. Pinned skills come first so they win ties under the
// "first non-empty wins" rule from pickSkillModelAndEffort. filePaths is
// forwarded only to the diagnostic Debug log so operators can see why a
// path-matched skill fired without rerunning the request with --verbose.
func (cli *ChatCLI) pickSkillHints(
	pinned, autoActivated []*persona.Skill,
	filePaths []string,
) (modelHint string, effort client.SkillEffort) {
	merged := append([]*persona.Skill(nil), pinned...)
	merged = append(merged, autoActivated...)
	if len(merged) == 0 {
		return "", client.SkillEffort("")
	}
	model, effortRaw, conflict := pickSkillModelAndEffort(merged)
	if conflict != "" {
		cli.logger.Warn("multiple skills disagree on model; using the first",
			zap.String("losing_skill", conflict),
			zap.String("chosen_model", model))
	}
	normalized := client.NormalizeEffort(effortRaw)
	cli.logger.Debug("skills injected (pinned + auto)",
		zap.Int("pinned", len(pinned)),
		zap.Int("auto", len(autoActivated)),
		zap.Strings("file_paths", filePaths),
		zap.String("skill_model", model),
		zap.String("skill_effort", string(normalized)))
	return model, normalized
}

// applyManualSkillHints lets manual `/<skill-name>` invocation override any
// pinned/auto hint for the current turn. Empty fields on the manual skill
// leave the existing values alone, so the precedence is
// manual > pinned > auto > base.
func applyManualSkillHints(manualSkill *persona.Skill, modelHint *string, effort *client.SkillEffort) {
	if manualSkill == nil {
		return
	}
	if m := strings.TrimSpace(manualSkill.Model); m != "" {
		*modelHint = m
	}
	if e := strings.TrimSpace(manualSkill.Effort); e != "" {
		*effort = client.NormalizeEffort(e)
	}
}

// mcpChannelPart renders the recent push-message ring from connected MCP
// servers. Returns (zero, false) when MCP is not configured or the ring is
// empty for this turn.
func (cli *ChatCLI) mcpChannelPart() (models.ContentBlock, bool) {
	if cli.mcpManager == nil {
		return models.ContentBlock{}, false
	}
	channelCtx := cli.mcpManager.Channels().FormatForPrompt(5)
	if channelCtx == "" {
		return models.ContentBlock{}, false
	}
	return models.ContentBlock{Type: "text", Text: channelCtx}, true
}

// mcpToolsPart emits a small catalog (name + description only) of the MCP
// tools the client has access to, plus a hint that they are callable only
// in agent/coder mode. The full tool schema is deferred to the agent loop
// to avoid burning tokens in chat mode.
func (cli *ChatCLI) mcpToolsPart() (models.ContentBlock, bool) {
	if cli.mcpManager == nil {
		return models.ContentBlock{}, false
	}
	if cli.pluginManager != nil {
		cli.pluginManager.SetShadowedBuiltins(cli.mcpManager.GetShadowedBuiltins())
	}
	mcpTools := cli.mcpManager.GetToolsSummary()
	if len(mcpTools) == 0 {
		return models.ContentBlock{}, false
	}
	var b strings.Builder
	b.WriteString("# Available MCP Tools\n\n")
	b.WriteString("The following external tools are available via MCP servers. ")
	b.WriteString("In agent/coder mode they can be invoked directly.\n\n")
	for _, t := range mcpTools {
		fmt.Fprintf(&b, "- **%s**: %s\n", t.Function.Name, t.Function.Description)
	}
	return models.ContentBlock{Type: "text", Text: b.String()}, true
}

// watcherContextPart returns the K8s watcher snapshot when an active watcher
// is producing context. Volatile (no cache hint) because the watcher updates
// continuously.
func (cli *ChatCLI) watcherContextPart() (models.ContentBlock, bool) {
	if cli.WatcherContextFunc == nil {
		return models.ContentBlock{}, false
	}
	k8sCtx := cli.WatcherContextFunc()
	if k8sCtx == "" {
		return models.ContentBlock{}, false
	}
	return models.ContentBlock{Type: "text", Text: k8sCtx}, true
}

// buildChatTempHistory splices the assembled system message at the head of
// the temporary history, then re-emits the prior history split into the
// "system messages first / user+assistant after" order Anthropic expects.
// The current user turn is appended last. cli.history is NOT mutated — that
// happens only after a successful LLM response.
func (cli *ChatCLI) buildChatTempHistory(
	parts []models.ContentBlock, userInput, additionalContext string,
) []models.Message {
	tempHistory := make([]models.Message, 0, len(cli.history)+4)
	if len(parts) > 0 {
		tempHistory = append(tempHistory, combinedSystemMessage(parts))
	}
	for _, msg := range cli.history {
		if msg.Role == "system" {
			tempHistory = append(tempHistory, msg)
		}
	}
	for _, msg := range cli.history {
		if msg.Role != "system" {
			tempHistory = append(tempHistory, msg)
		}
	}
	tempHistory = append(tempHistory, models.Message{
		Role:    "user",
		Content: userInput + additionalContext,
	})
	return tempHistory
}

// combinedSystemMessage flattens the structured parts into a single
// `Content`-string message (used by non-Anthropic providers) while keeping
// the per-block `SystemParts` slice intact (used by Anthropic for
// fine-grained cache-control). The two views never disagree.
func combinedSystemMessage(parts []models.ContentBlock) models.Message {
	var combined strings.Builder
	for i, part := range parts {
		if i > 0 {
			combined.WriteString("\n\n---\n\n")
		}
		combined.WriteString(part.Text)
	}
	return models.Message{
		Role:        "system",
		Content:     combined.String(),
		SystemParts: parts,
	}
}

// noticeSkillResolution surfaces a user-visible notice when the skill model
// hint forced a cross-provider client swap, and a yellow warning when the
// hint could not be honored at all. Silent when the resolution stayed on
// the user's current provider/model.
func (cli *ChatCLI) noticeSkillResolution(resolution SkillClientResolution) {
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
		return
	}
	if resolution.UserMessage != "" {
		fmt.Printf("  %s\n", colorize("⚠ "+resolution.UserMessage, ColorYellow))
	}
}

// applyChatEffortHint attaches the reasoning-effort hint to ctx so the
// provider's SendPrompt can opt into extended thinking for this turn. When
// the user has issued a `/thinking` override the override wins; an explicit
// EffortUnset override means "thinking off" and detaches the hint entirely.
func (cli *ChatCLI) applyChatEffortHint(ctx context.Context, skillEffort client.SkillEffort) context.Context {
	if eff, overridden := cli.applyThinkingOverride(skillEffort); overridden {
		if eff != client.EffortUnset {
			return client.WithEffortHint(ctx, eff)
		}
		return ctx
	}
	return client.WithEffortHint(ctx, skillEffort)
}

// executeLLMTurn delegates to the streaming path when the active client
// implements StreamingClient, falling back to the buffered path otherwise.
// stopSpinner is called as soon as the LLM has acknowledged the request so
// the prompt prefix stops animating before we render the response.
func (cli *ChatCLI) executeLLMTurn(
	ctx context.Context,
	activeClient client.LLMClient,
	userInput, additionalContext string,
	tempHistory []models.Message,
	effectiveMaxTokens int,
	resolution SkillClientResolution,
	stopSpinner func(),
) (string, error) {
	if sc, ok := client.AsStreamingClient(activeClient); ok {
		return cli.executeStreamingTurn(ctx, sc, activeClient, userInput, additionalContext,
			tempHistory, effectiveMaxTokens, resolution, stopSpinner)
	}
	return cli.executeBufferedTurn(ctx, activeClient, userInput, additionalContext,
		tempHistory, effectiveMaxTokens, stopSpinner)
}

// executeStreamingTurn runs the streaming path. It also drives the
// watchdog for stalled streams and pushes provider-reported usage into the
// cost tracker against whichever provider+model actually served the turn.
func (cli *ChatCLI) executeStreamingTurn(
	ctx context.Context,
	sc client.StreamingClient,
	activeClient client.LLMClient,
	userInput, additionalContext string,
	tempHistory []models.Message,
	effectiveMaxTokens int,
	resolution SkillClientResolution,
	stopSpinner func(),
) (string, error) {
	chunks, err := sc.SendPromptStream(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
	if err != nil && cli.refreshClientOnAuthError(err) {
		chunks, err = sc.SendPromptStream(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
	}

	cli.animation.StopThinkingAnimation()
	stopSpinner()
	cli.interactionState = StateNormal
	cli.forceRefreshPrompt()
	time.Sleep(50 * time.Millisecond)

	if err != nil {
		return "", err
	}
	fmt.Printf("%s\n", colorize(activeClient.GetModelName()+":", ColorPurple))

	result := client.WatchStream(ctx, chunks, client.DefaultWatchdogConfig(), cli.logger)
	if result.WasStalled {
		fmt.Printf("\n%s\n", colorize(i18n.T("sw.cmd.stream_stalled"), ColorYellow))
	}
	if result.Usage != nil && cli.costTracker != nil {
		cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model, result.Usage)
	}
	return result.Text, nil
}

// executeBufferedTurn runs the non-streaming path. The animation/spinner
// must stop as soon as the provider responds (success or failure) so the
// caller can render the answer or the error cleanly.
func (cli *ChatCLI) executeBufferedTurn(
	ctx context.Context,
	activeClient client.LLMClient,
	userInput, additionalContext string,
	tempHistory []models.Message,
	effectiveMaxTokens int,
	stopSpinner func(),
) (string, error) {
	aiResponse, err := activeClient.SendPrompt(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
	if cli.refreshClientOnAuthError(err) {
		aiResponse, err = activeClient.SendPrompt(ctx, userInput+additionalContext, tempHistory, effectiveMaxTokens)
	}
	cli.animation.StopThinkingAnimation()
	stopSpinner()
	cli.interactionState = StateNormal
	cli.forceRefreshPrompt()
	time.Sleep(50 * time.Millisecond)
	return aiResponse, err
}

// handleChatTurnResult is the post-execution branch. On error it surfaces
// the error to the user; on success it appends the user+assistant pair to
// history, tracks cost (buffered path), and renders the assistant message.
// elapsed is measured from just before executeLLMTurn so it covers both
// latency to first byte and full streaming time.
func (cli *ChatCLI) handleChatTurnResult(
	err error,
	userMessage models.Message,
	aiResponse string,
	activeClient client.LLMClient,
	resolution SkillClientResolution,
	userInput, additionalContext string,
	elapsed time.Duration,
) {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Println(i18n.T("status.operation_canceled"))
			return
		}
		fmt.Println(i18n.T("error.generic", err.Error()))
		return
	}

	cli.history = append(cli.history, userMessage)
	cli.history = append(cli.history, models.Message{Role: "assistant", Content: aiResponse})

	usage := client.GetUsageOrEstimate(activeClient, len(userInput+additionalContext), len(aiResponse))
	if cli.costTracker != nil && !client.IsStreamingCapable(activeClient) {
		cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model, usage)
	}
	cli.renderAssistantResponse(activeClient, aiResponse, elapsed, usage)

	if cli.memWorker != nil {
		cli.memWorker.nudge()
	}
}

// renderAssistantResponse draws the assistant message wrapped in a
// lipgloss envelope:
//
//	╭─ <model> ─────────── <latency> · <tokens> ─╮
//	│  <markdown body>                           │
//	╰────────────────────────────────────────────╯
//
// Streaming clients already painted raw chunks inline during the call;
// we clear that line and overwrite with the markdown-rendered version
// so the user sees the same boxed output regardless of transport.
func (cli *ChatCLI) renderAssistantResponse(
	activeClient client.LLMClient,
	aiResponse string,
	elapsed time.Duration,
	usage *models.UsageInfo,
) {
	rendered := ensureANSIReset(cli.renderMarkdown(aiResponse))
	if client.IsStreamingCapable(activeClient) {
		// Clear the in-progress stream line so the envelope draws cleanly.
		fmt.Print("\033[2K\r")
	}

	header := cli.buildChatEnvelopeHeader(activeClient, elapsed, usage)
	footer := cli.buildChatEnvelopeFooter(header)

	fmt.Println()
	fmt.Println(header)
	// Indent body so it visually sits inside the envelope. The body
	// already contains its own glamour-rendered formatting, which we
	// preserve as-is — we only add a left gutter.
	for _, ln := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		fmt.Println("  " + ln)
	}
	fmt.Println(footer)
	fmt.Println()
}

// buildChatEnvelopeHeader produces the top border line:
//
//	╭─ claude-opus-4-7 ──────────── 1.4s · 312↑ 1.8k↓ ─╮
//
// The model name lives on the left in purple+bold; the latency/token
// summary sits on the right in gray. The dashes in between expand to
// fill the configured screen width.
func (cli *ChatCLI) buildChatEnvelopeHeader(activeClient client.LLMClient, elapsed time.Duration, usage *models.UsageInfo) string {
	model := activeClient.GetModelName()
	latency := formatLatency(elapsed)
	tokens := formatTokenSummary(usage)

	leftLabel := colorize(" "+model+" ", ColorPurple+ColorBold)
	rightLabel := colorize(" "+latency+" · "+tokens+" ", ColorGray)

	target := screenWidth - 2 // leave room for "╭" + "╮"
	leftLen := visibleLen(leftLabel)
	rightLen := visibleLen(rightLabel)
	pad := target - leftLen - rightLen
	if pad < 4 {
		pad = 4
	}
	fill := colorize(strings.Repeat("─", pad), ColorGray)
	return colorize("╭─", ColorGray) + leftLabel + fill + rightLabel + colorize("─╮", ColorGray)
}

// buildChatEnvelopeFooter mirrors the header's width so the envelope
// reads as a balanced box. We measure the header's visible width and
// produce a footer of the same total length.
func (cli *ChatCLI) buildChatEnvelopeFooter(header string) string {
	width := visibleLen(header)
	if width < 4 {
		width = 4
	}
	inner := width - 2
	return colorize("╰"+strings.Repeat("─", inner)+"╯", ColorGray)
}

// formatLatency renders a duration in a human-friendly shape:
// 380ms / 1.4s / 12.3s. Picked over time.Duration.String() so the
// header stays compact at any latency.
func formatLatency(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// formatTokenSummary renders usage as "312↑ 1.8k↓". Returns the i18n
// placeholder when usage is unreported (provider didn't return counts).
func formatTokenSummary(u *models.UsageInfo) string {
	if u == nil || (u.PromptTokens == 0 && u.CompletionTokens == 0) {
		return i18n.T("chat.envelope.no_tokens")
	}
	return i18n.T("chat.envelope.tokens", u.PromptTokens, u.CompletionTokens)
}
