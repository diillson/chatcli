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

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/ctxmgr"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
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

// assembleChatSystemPrompt builds every structured system-prompt block for a
// chat-mode turn, ordered most-stable-first so the cache_control:ephemeral
// breakpoints sit on a CONTIGUOUS prefix the provider can serve as warm-cache
// reads. Every per-turn-volatile block is pushed AFTER the last cached
// breakpoint and carries NO hint, so it can never invalidate a stable prefix
// above it.
//
// Anthropic caches by prefix: a breakpoint only hits when every byte before
// it is identical to a prior request. A volatile block placed early (e.g. the
// hint-driven memory block, or the wall-clock timestamp) therefore poisons
// every cached block below it — paying cache-creation cost each turn while
// never earning a read. Hence the split:
//
//	── Stable cached prefix (warm-cache reads across turns) ──
//	Part 0  — mode-awareness banner + language directive
//	Part 1  — attached `/context` entries (per session)
//	Part 2  — pinned skills (`/skill pin`) — stable across turns
//	Part 3  — MCP tools catalog (name+description only)
//	── Volatile suffix (no cache hint; changes per turn) ──
//	Part 4  — workspace context (SOUL/USER/RULES + MEMORY, hint-driven)
//	Part 5  — manually invoked skill (`/<skill-name>`) — consumed once
//	Part 6  — auto-activated skills (triggers + path globs)
//	Part 7  — MCP channel messages (push ring)
//	Part 8  — K8s watcher context
//	Part 9  — dynamic context (wall-clock time + cwd disambiguation)
//
// The function also captures the skill model/effort hints so the caller can
// route the turn to the right provider/model.
func (cli *ChatCLI) assembleChatSystemPrompt(
	ctx context.Context, userInput, additionalContext string,
) chatSystemAssembly {
	var out chatSystemAssembly

	// Resolve skills up front: this produces the model/effort hints AND the
	// content blocks, which are emitted in different cache regions below
	// (pinned in the stable prefix, manual + auto in the volatile suffix).
	manualSkill, manualSkillArgs := cli.consumePendingManualSkill()
	out.manualHit = manualSkill != nil
	pinned, autoActivated, filePaths := cli.resolveSkillsForTurn(userInput, additionalContext)
	out.pinnedHit = len(pinned)
	out.autoHit = len(autoActivated)
	out.filePaths = filePaths
	out.modelHint, out.effort = cli.pickSkillHints(pinned, autoActivated, filePaths)
	if manualSkill != nil {
		applyManualSkillHints(manualSkill, &out.modelHint, &out.effort)
	}

	// ── Stable cached prefix ──
	out.parts = append(out.parts, modeAndLanguagePart())         // Part 0
	out.parts = append(out.parts, cli.attachedContextParts()...) // Part 1
	if block, ok := pinnedSkillBlock(pinned); ok {               // Part 2
		out.parts = append(out.parts, block)
	}
	if part, ok := cli.mcpToolsPart(); ok { // Part 3
		out.parts = append(out.parts, part)
	}

	// ── Volatile suffix (no cache hints) ──
	if part, ok := cli.workspaceContextPart(ctx, userInput); ok { // Part 4
		out.parts = append(out.parts, part)
	}
	// Part 4b: semantic /context retrieval (--rag). Query-driven, so it lives
	// here in the volatile zone — never the cached prefix it would otherwise
	// poison for every block after it.
	out.parts = append(out.parts, cli.retrievedContextParts(ctx, userInput)...)
	if manualSkill != nil { // Part 5
		if block := renderManualSkillBlock(manualSkill, manualSkillArgs); block != "" {
			out.parts = append(out.parts, models.ContentBlock{Type: "text", Text: block})
		}
	}
	if block, ok := autoSkillBlock(autoActivated); ok { // Part 6
		out.parts = append(out.parts, block)
	}
	if part, ok := cli.mcpChannelPart(); ok { // Part 7
		out.parts = append(out.parts, part)
	}
	if part, ok := cli.watcherContextPart(); ok { // Part 8
		out.parts = append(out.parts, part)
	}
	if part, ok := cli.dynamicContextPart(); ok { // Part 9
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

// workspaceContextPart builds the bootstrap-files-plus-smart-memory block.
// Returns (zero, false) when there is no workspace context to inject (e.g. on
// a fresh repo with no SOUL.md / MEMORY.md).
//
// VOLATILE: memory retrieval is hint-driven (recentHistoryHints changes every
// turn), so this block's text varies turn to turn. It therefore carries NO
// cache hint and lives in the volatile suffix — caching it would force a
// cache-creation write each turn while never earning a read, and (worse) would
// poison any cached block placed after it. The wall-clock timestamp that used
// to be appended here now lives in its own trailing block (dynamicContextPart)
// so it can't bust the prefix cache.
func (cli *ChatCLI) workspaceContextPart(ctx context.Context, userInput string) (models.ContentBlock, bool) {
	if cli.contextBuilder == nil {
		return models.ContentBlock{}, false
	}
	hints := cli.recentHistoryHints()
	wsCtx := cli.retrieveWorkspaceContext(ctx, userInput, hints)
	if wsCtx == "" {
		return models.ContentBlock{}, false
	}
	return models.ContentBlock{Type: "text", Text: wsCtx}, true
}

// dynamicContextPart emits the time-sensitive context (current wall-clock time
// plus the cwd disambiguation directive). It is the single most volatile block
// — the timestamp changes on every turn by definition — so it is appended last
// and never carries a cache hint. Keeping it out of every cached block is what
// lets the stable prefix above produce warm-cache reads.
func (cli *ChatCLI) dynamicContextPart() (models.ContentBlock, bool) {
	if cli.contextBuilder == nil {
		return models.ContentBlock{}, false
	}
	dyn := cli.contextBuilder.BuildDynamicContext()
	if dyn == "" {
		return models.ContentBlock{}, false
	}
	return models.ContentBlock{Type: "text", Text: dyn}, true
}

// retrieveWorkspaceContext assembles the workspace context for a chat turn,
// honoring the memory injection mode. Chat is tool-less by design, so it
// cannot pull on demand: "index" degrades to "full" here, and only "off"
// suppresses memory (bootstrap files still apply). HyDE augmentation is wired
// when /config quality has it enabled, matching the historical behavior for
// users on the default "full" mode.
func (cli *ChatCLI) retrieveWorkspaceContext(ctx context.Context, userInput string, hints []string) string {
	mode := loadMemoryMode()
	if mode == memModeIndex {
		mode = memModeFull // chat cannot recall; fall back to the push model
	}
	var aug *memory.HyDEAugmenter
	if qcfg := quality.LoadFromEnv(); qcfg.HyDE.Enabled && qcfg.Enabled {
		cli.ensureHyDEVectors(qcfg)
		aug = cli.hydeAugmenter(qcfg)
	}
	return cli.contextBuilder.BuildWorkspaceContextMode(ctx, userInput, hints, aug, mode, "")
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

// retrievedContextParts runs semantic retrieval for any `--rag` attached context
// and returns volatile (uncached) blocks holding only the passages relevant to
// this turn. Volatile because the content is query-driven: placing it in the
// cached prefix would defeat the cache for every block after it. Returns nil when
// no context opted into retrieval or no embedding provider is configured.
func (cli *ChatCLI) retrievedContextParts(ctx context.Context, userInput string) []models.ContentBlock {
	sessionID := cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}
	msgs, err := cli.contextHandler.GetManager().BuildRetrievedContextMessages(ctx, sessionID, userInput)
	if err != nil {
		cli.logger.Warn("Erro ao recuperar contexto semântico", zap.Error(err))
		return nil
	}
	out := make([]models.ContentBlock, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, models.ContentBlock{Type: "text", Text: msg.Content})
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

// pinnedSkillBlock renders the pinned-skills (`/skill pin`) block. Pinned
// skills are stable across turns within a session, so the block lives in the
// cached prefix and carries a cache_control:ephemeral hint. Returns ok=false
// when there are no pinned skills (or they render empty).
func pinnedSkillBlock(pinned []*persona.Skill) (models.ContentBlock, bool) {
	if len(pinned) == 0 {
		return models.ContentBlock{}, false
	}
	block := buildPinnedSkillInjectionBlock(pinned)
	if block == "" {
		return models.ContentBlock{}, false
	}
	return models.ContentBlock{
		Type:         "text",
		Text:         block,
		CacheControl: &models.CacheControl{Type: "ephemeral"},
	}, true
}

// autoSkillBlock renders the auto-activated skills (triggers + path globs)
// block. Auto-activation is query-driven, so the block is volatile: it carries
// NO cache hint and belongs in the volatile suffix. Returns ok=false when no
// skills auto-activated (or they render empty).
func autoSkillBlock(autoActivated []*persona.Skill) (models.ContentBlock, bool) {
	if len(autoActivated) == 0 {
		return models.ContentBlock{}, false
	}
	block := buildSkillInjectionBlock(autoActivated)
	if block == "" {
		return models.ContentBlock{}, false
	}
	return models.ContentBlock{Type: "text", Text: block}, true
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
	// Stable across a session (the catalog changes only on connect/disconnect),
	// so it sits in the cached prefix and carries a cache hint.
	return models.ContentBlock{
		Type:         "text",
		Text:         b.String(),
		CacheControl: &models.CacheControl{Type: "ephemeral"},
	}, true
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
	// Purge stale `[ACTIVE MODE: …]` system messages left behind by a
	// previous /agent or /coder run in the same session. Without this,
	// the chat LLM call would receive the fresh ChatModeSystemHint in
	// slot 0 AND the leftover CoderSystemPrompt / agent system prompt
	// further down — contradictory format rules ("don't emit tool_call"
	// next to "you MUST emit tool_call"), which historically caused
	// smaller models to vacillate mid-response.
	filtered := purgeStaleModeSystems(cli.history, ModeChat)

	tempHistory := make([]models.Message, 0, len(filtered)+4)
	if len(parts) > 0 {
		tempHistory = append(tempHistory, combinedSystemMessage(parts))
	}
	for _, msg := range filtered {
		if msg.Role == "system" {
			tempHistory = append(tempHistory, msg)
		}
	}
	for _, msg := range filtered {
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
	// Controlled chat exception: when CHATCLI_CHAT_ASK is on and the provider
	// supports native tools, chat may use ONLY ask_user (no execution tools).
	// Off by default, so chat keeps streaming on every turn.
	if out, handled, err := cli.maybeChatAskTurn(ctx, activeClient, userInput, additionalContext,
		tempHistory, effectiveMaxTokens, resolution, stopSpinner); handled {
		return out, err
	}

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
	ctx context.Context,
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

	// Mirror the turn onto the shared cross-channel conversation so other
	// channels (Telegram/Slack) see it as context.
	cli.mirrorHubTurn(ctx, userMessage.Content, aiResponse)

	usage := client.GetUsageOrEstimate(activeClient, len(userInput+additionalContext), len(aiResponse))
	if cli.costTracker != nil && !client.IsStreamingCapable(activeClient) {
		cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model, usage)
	}
	cli.renderAssistantResponse(activeClient, aiResponse, elapsed, usage)

	if cli.memWorker != nil {
		cli.memWorker.nudge(ctx)
	}
}

// renderAssistantResponse draws the assistant message wrapped in a
// fully bordered, responsive envelope:
//
//	╭─ <model> ─────────── <latency> · <tokens> ─╮
//	│  <wrapped markdown body>                   │
//	│  …                                         │
//	╰────────────────────────────────────────────╯
//
// All width math is delegated to agent.RenderResponseEnvelope, which:
//   - reads the live terminal width (no hardcoded columns),
//   - wraps the body to the inner width preserving ANSI escapes,
//   - normalizes emoji width so the right border never drifts,
//   - paints both vertical borders so long lines stay inside the box,
//   - drives the typewriter effect on the body.
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

	left, right := chatEnvelopeLabels(activeClient, elapsed, usage)
	footerRight := cli.chatEnvelopeFooter(usage)
	renderer := agent.NewUIRendererWithStyle(cli.logger, agent.UIStyleFull)
	renderer.RenderResponseEnvelope(agent.ResponseEnvelopeOptions{
		HeaderLeft:  left,
		HeaderRight: right,
		FooterRight: footerRight,
		Body:        rendered,
		Color:       agent.ColorGray,
		Typewriter:  true,
	})
	fmt.Println()
}

// chatEnvelopeFooter builds the bottom-border telemetry shown on the right of
// the chat reply: the estimated cost of THIS turn and how full the model's
// context window is after it. Both derive from data already in hand — the
// usage counts plus the model's pricing/context-window from the catalog — so
// the footer adds no new bookkeeping. It returns "" (no footer drawn) when
// usage is unreported, keeping the box clean for providers that omit counts.
func (cli *ChatCLI) chatEnvelopeFooter(usage *models.UsageInfo) string {
	if usage == nil || (usage.PromptTokens == 0 && usage.CompletionTokens == 0) {
		return ""
	}

	inputCost, outputCost := getModelPricing(cli.Provider, cli.Model)
	turnCost := float64(usage.PromptTokens)/1_000_000*inputCost +
		float64(usage.CompletionTokens)/1_000_000*outputCost

	parts := make([]string, 0, 2)
	if turnCost > 0 {
		parts = append(parts, formatTurnCost(turnCost))
	}
	if window := catalog.GetContextWindow(cli.Provider, cli.Model); window > 0 {
		pct := float64(usage.PromptTokens) / float64(window) * 100
		parts = append(parts, i18n.T("chat.envelope.context_pct", clampPct(pct)))
	}
	if len(parts) == 0 {
		return ""
	}
	return colorize(" "+strings.Join(parts, " · ")+" ", ColorGray)
}

// formatTurnCost renders a per-turn cost compactly: sub-cent costs keep four
// decimals ($0.0004) so cheap turns are not all shown as "$0.00", while
// larger costs use two ($0.12).
func formatTurnCost(usd float64) string {
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// clampPct bounds a percentage to [0,100] so an over-window prompt (possible
// with provider-side counting differences) never prints "ctx 103%".
func clampPct(p float64) int {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return int(p + 0.5)
	}
}

// chatEnvelopeLabels builds the bilateral labels for the chat reply
// envelope: model name on the left (purple + bold), latency · tokens
// on the right (gray). Each label includes the leading/trailing space
// the envelope renderer expects so the dash fill bites cleanly into
// the labels instead of sitting flush against the text.
//
// Returning the two strings separately (instead of a pre-built header
// line) lets the unified envelope renderer own the border math while
// the chat path still owns its content. Locale-aware token formatting
// flows through formatTokenSummary, which respects the active i18n
// locale (en/pt) for the digit grouping separator.
func chatEnvelopeLabels(activeClient client.LLMClient, elapsed time.Duration, usage *models.UsageInfo) (string, string) {
	model := activeClient.GetModelName()
	latency := formatLatency(elapsed)
	tokens := formatTokenSummary(usage)

	left := colorize(" "+model+" ", ColorPurple+ColorBold)
	right := colorize(" "+latency+" · "+tokens+" ", ColorGray)
	return left, right
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
