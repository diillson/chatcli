package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/mail"
	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/coder/engine"
	"go.uber.org/zap"
)

// resolvedToolCall is a unified representation of a tool call,
// regardless of whether it came from native function calling or XML parsing.
type resolvedToolCall struct {
	ID         string                 // tool call ID (native) or generated
	Name       string                 // native function name or @coder
	Subcmd     string                 // engine subcommand (read, write, patch, etc.)
	Args       []string               // CLI-style flags for engine.Execute
	RawArgs    string                 // original args string for display/logging
	Native     bool                   // true if from native function calling
	NativeArgs map[string]interface{} // structured args (native only)
	// pluginName is the canonical grant ("@browser", "mcp_x") when this
	// call targets a granted session plugin; empty otherwise. Set during
	// resolution so classification, policy and execution route on it.
	pluginName string
}

// validatedTC is a resolved tool call after policy/allowlist classification.
type validatedTC struct {
	index   int
	rtc     resolvedToolCall
	blocked bool
	msg     string
}

// execResult is the outcome of executing a single (possibly blocked) tool call.
type execResult struct {
	index  int
	record ToolCallRecord
	output string
	failed bool
	toolID string // for native tool result messages
}

// WorkerReActConfig controls the worker's internal ReAct loop.
type WorkerReActConfig struct {
	MaxTurns        int
	SystemPrompt    string
	AllowedCommands []string // which @coder subcommands this worker can use
	// PluginTools are per-task session-plugin grants (canonical names, see
	// NormalizePluginGrant). Usually injected from the dispatch context.
	PluginTools []string
	ReadOnly    bool // if true, only read/search/tree/git-read allowed
}

// DefaultWorkerMaxTurns is the default maximum number of ReAct turns per worker.
// Can be overridden via CHATCLI_AGENT_WORKER_MAX_TURNS env var.
const DefaultWorkerMaxTurns = 30

// MaxWorkerOutputBytes is the maximum size of worker output to prevent token overflow.
const MaxWorkerOutputBytes = 30 * 1024

// RunWorkerReAct executes a mini ReAct loop for a single worker agent.
// Each turn: send task to LLM → parse tool_calls → execute via Engine → feedback.
// If no tool_calls are emitted, the worker is done and returns the final text.
//
// When the LLM client supports native tool calling (ToolAwareClient), this function
// uses structured function calling — no XML parsing or base64 needed.
// Otherwise, falls back to XML/JSON parsing from response text.
func RunWorkerReAct(
	ctx context.Context,
	config WorkerReActConfig,
	task string,
	llmClient client.LLMClient,
	lockMgr *FileLockManager,
	skills *SkillSet,
	policyChecker PolicyChecker,
	logger *zap.Logger,
) (*AgentResult, error) {
	startTime := time.Now()
	callID := nextCallID()

	// Live progress handle: registered by whoever spawned this loop
	// (dispatcher, subagent, MoA, scheduler bridge). Nil-safe — a loop run
	// without a registered handle simply reports nothing.
	liveRun := runs.FromContext(ctx)

	maxTurns := resolveWorkerMaxTurns(config)

	// Per-task plugin grants ride the dispatch context so every agent type
	// inherits them without signature changes. Read-only workers never
	// receive grants: their contract is inspection, and plugin side effects
	// (@browser click, @forge create) would bypass isWriteCommand.
	if !config.ReadOnly {
		if grant := grantFromContext(ctx); len(grant) > 0 {
			config.PluginTools = NormalizePluginGrant(append(append([]string{}, config.PluginTools...), grant...))
		}
	}

	// Detect if we can use native function calling
	toolAware, useNativeTools := client.AsToolAware(llmClient)
	if useNativeTools && !toolAware.SupportsNativeTools() {
		useNativeTools = false
	}

	// Build tool definitions for native mode
	var toolDefs []models.ToolDefinition
	if useNativeTools {
		toolDefs = buildWorkerToolDefs(config)
		logger.Info("Using native function calling",
			zap.Int("tools", len(toolDefs)),
			zap.String("callID", callID))
	}

	systemPrompt := buildWorkerSystemPrompt(config, useNativeTools, task)

	history := []models.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: task},
	}

	// Attach the subagent context so a delegate tool call can recursively
	// invoke RunWorkerReAct with the same dependencies. If the parent already
	// set one (nested delegation), preserve its depth and overwrite the
	// dependency handles to match this invocation.
	parentSC := getSubagentContext(ctx)
	ctx = withSubagentContext(ctx, subagentContext{
		Depth:         parentSC.Depth,
		LLMClient:     llmClient,
		LockManager:   lockMgr,
		Skills:        skills,
		PolicyChecker: policyChecker,
		Logger:        logger,
	})

	var allToolCalls []ToolCallRecord
	var finalOutput strings.Builder
	maxParallel := 0

	allowed := make(map[string]bool, len(config.AllowedCommands)+2)
	for _, cmd := range config.AllowedCommands {
		allowed[cmd] = true
	}
	// Universal squad-mail capability (see MailToolDefinition).
	allowed["mail"] = true
	// Universal CCR recall (see RecallToolDefinition) — only when wired.
	if currentCCRRecaller() != nil {
		allowed[recallSubcmd] = true
	}
	// Granted session plugins (resolution canonicalizes both native and XML
	// calls to the def name — see resolveToolCalls).
	for _, grant := range config.PluginTools {
		allowed[pluginDefName(grant)] = true
	}

	// Worker-loop microcompact: long runs (up to maxTurns) accumulate tool
	// results with no other reduction mechanism. Same engine and knobs as
	// the orchestrator's, sharing the session CCR layer so every dropped
	// byte stays recoverable via the recall tool.
	mcCfg := agent.DefaultMicrocompactConfig()
	mcCfg.CCR = currentSquadCompressionLayer()
	// The orchestrator's window management (compactor with the tenant's
	// CCR, journal) and the same bounded overflow recovery the main loop
	// has: a worker no longer fails its task when the provider says the
	// context is too long.
	window := currentWorkerWindow()
	recovery := agent.NewContextRecovery(agent.DefaultContextRecoveryConfig(), logger)
	workerName := liveRun.Snapshot().Agent

	// --- Failure tracking for reflection ---
	consecutiveFailures := 0
	blockedCmds := make(map[string]int)
	// Empty completions (no text, no tool calls) tolerated before the
	// worker fails loudly — see the guard after resolveToolCalls.
	emptyTurns := 0

	for turn := 0; turn < maxTurns; turn++ {
		if ctx.Err() != nil {
			return &AgentResult{
				CallID:    callID,
				Output:    finalOutput.String(),
				Error:     ctx.Err(),
				Duration:  time.Since(startTime),
				ToolCalls: allToolCalls,
			}, ctx.Err()
		}

		liveRun.SetTurn(turn+1, maxTurns)
		liveRun.SetAction("")

		// Drain this agent's squad mailbox at the turn boundary — the only
		// point where injecting context cannot split a tool_use/tool_result
		// pair. Merged into a trailing user message when one exists so
		// strict-alternation providers never see two user turns in a row.
		history = drainWorkerInbox(liveRun, history)

		// Progressively compact old tool results (turn boundary only, so a
		// tool_use/tool_result pair is never split). CCR-backed when the
		// session layer is registered — otherwise same legacy behavior the
		// orchestrator had before CCR.
		history, _ = agent.ApplyMicrocompact(history, turn, mcCfg, logger)
		history = applyWorkerWindow(ctx, window, history, logger)

		// --- Call LLM (native or text mode) ---
		responseText, nativeToolCalls, stopReason, newHistory, err := callWorkerLLM(ctx, useNativeTools, toolAware, llmClient, history, toolDefs)
		if recovered, retry := recoverWorkerOverflow(err, recovery, history, turn, logger); retry {
			history = recovered
			continue
		}
		if err != nil {
			return &AgentResult{
				CallID:    callID,
				Output:    finalOutput.String(),
				Error:     fmt.Errorf("turn %d: LLM call failed: %w", turn+1, err),
				Duration:  time.Since(startTime),
				ToolCalls: allToolCalls,
			}, err
		}
		noteWorkerTurn(window, workerName, history, newHistory)
		history = newHistory

		// --- Resolve tool calls to unified format ---
		resolved, parseErrRecords := resolveToolCalls(useNativeTools, nativeToolCalls, responseText, turn, config.PluginTools)
		allToolCalls = append(allToolCalls, parseErrRecords...)

		if len(resolved) == 0 {
			if strings.TrimSpace(responseText) == "" {
				// An empty completion is NOT a final answer — see
				// handleEmptyWorkerTurn for the recovery policy.
				var retry bool
				var emptyErr error
				history, retry, emptyErr = handleEmptyWorkerTurn(history, emptyWorkerTurn{
					stopReason: stopReason,
					modelName:  llmClient.GetModelName(),
					turn:       turn,
					maxTurns:   maxTurns,
					attempts:   &emptyTurns,
					logger:     logger,
				})
				if emptyErr != nil {
					return emptyWorkerResult(callID, finalOutput.String(), emptyErr, startTime, allToolCalls), emptyErr
				}
				if retry {
					continue
				}
			}
			// No tool calls — worker is done
			finalOutput.WriteString(responseText)
			break
		}
		// A productive turn resets the empty-completion tolerance.
		emptyTurns = 0

		// --- Pre-validate and classify ---
		validated, classifyRecords := classifyToolCalls(resolved, allowed, blockedCmds, config)
		allToolCalls = append(allToolCalls, classifyRecords...)

		var runnable []validatedTC
		var runnableResolved []resolvedToolCall
		for _, v := range validated {
			if !v.blocked {
				runnable = append(runnable, v)
				runnableResolved = append(runnableResolved, v.rtc)
			}
		}

		// --- Execute tool calls ---
		results := make([]execResult, len(validated))

		// Use the concurrency classifier for smarter parallelization.
		// This allows file-scoped writes (write/patch) to different files
		// to run in parallel, not just read-only commands.
		canParallelize, _, _ := CanParallelizeToolCalls(runnableResolved)
		if canParallelize {
			logger.Info("Executing tool calls in parallel",
				zap.Int("count", len(runnable)),
				zap.String("callID", callID))
		}

		if canParallelize {
			maxParallel = max(maxParallel, len(runnable))
			liveRun.SetAction(batchActionLabel(runnableResolved))
			var wg sync.WaitGroup
			var mu sync.Mutex
			for i, v := range validated {
				if v.blocked {
					results[i] = execResult{index: v.index, output: v.msg + "\n", failed: true, toolID: v.rtc.ID}
					continue
				}
				wg.Add(1)
				go func(idx int, vtc validatedTC) {
					defer wg.Done()
					r := executeToolCall(ctx, vtc, lockMgr, policyChecker)
					mu.Lock()
					results[idx] = r
					mu.Unlock()
				}(i, v)
			}
			wg.Wait()
		} else {
			for i, v := range validated {
				if !v.blocked {
					liveRun.SetAction(toolActionLabel(v.rtc))
				}
				results[i] = executeToolCall(ctx, v, lockMgr, policyChecker)
			}
		}
		liveRun.AddToolCalls(len(runnable))

		// --- Aggregate results ---
		agg := aggregateTurnResults(results, validated, blockedCmds)
		allToolCalls = append(allToolCalls, agg.records...)
		turnOutput := agg.turnOutput
		turnFailures := agg.turnFailures
		turnBlocked := agg.turnBlocked
		failedCmds := agg.failedCmds

		// --- Build feedback and inject into history ---
		history = appendTurnFeedback(history, useNativeTools, results, turnOutput,
			turnFailures, turnBlocked, len(validated), consecutiveFailures, blockedCmds)

		// Reflection bookkeeping (and native-mode reflection prompt injection)
		if turnFailures > 0 {
			consecutiveFailures++
			if useNativeTools {
				reflectionMsg := buildReflectionPrompt(turnBlocked, len(validated), consecutiveFailures, blockedCmds)
				if reflectionMsg != "" {
					history = append(history, models.Message{Role: "user", Content: reflectionMsg})
				}
				logger.Debug("Reflection prompt injected (native)",
					zap.Int("consecutive_failures", consecutiveFailures),
					zap.Strings("failed_cmds", failedCmds),
					zap.Int("turn", turn+1),
				)
			} else {
				logger.Debug("Reflection prompt injected",
					zap.Int("consecutive_failures", consecutiveFailures),
					zap.Strings("failed_cmds", failedCmds),
					zap.Int("turn", turn+1),
				)
			}
		} else {
			consecutiveFailures = 0
		}

		finalOutput.WriteString(turnOutput)
	}

	output := finalOutput.String()
	if len(output) > MaxWorkerOutputBytes {
		// Persist the full output and keep (almost) the same inline budget as
		// before — the tail is now recoverable via the referenced overflow
		// file instead of silently lost. The margin keeps preview + reference
		// suffix within the historical MaxWorkerOutputBytes bound.
		const suffixMargin = 256
		output = overflowToDisk("worker", output, MaxWorkerOutputBytes-suffixMargin)
	}

	return &AgentResult{
		CallID:        callID,
		Output:        output,
		Duration:      time.Since(startTime),
		ToolCalls:     allToolCalls,
		ParallelCalls: maxParallel,
	}, nil
}

// drainWorkerInbox merges the agent's squad mailbox into the history at
// the turn boundary (nil-safe).
func drainWorkerInbox(liveRun *runs.Run, history []models.Message) []models.Message {
	snap := liveRun.Snapshot()
	if snap.Agent == "" {
		return history
	}
	inbox := mail.Default().Drain(snap.Agent)
	if len(inbox) == 0 {
		return history
	}
	return appendInboxMessage(history, mail.FormatInbox(inbox))
}

// applyWorkerWindow runs the shared compactor when the window says the
// history crossed the budget; a failure keeps the history as is.
func applyWorkerWindow(ctx context.Context, window WindowManager, history []models.Message, logger *zap.Logger) []models.Message {
	if window == nil || !window.NeedsCompaction(history) {
		return history
	}
	compacted, err := window.Compact(ctx, history)
	if err != nil {
		logger.Warn("worker compaction failed; continuing with the full history", zap.Error(err))
		return history
	}
	if len(compacted) == 0 {
		return history
	}
	return compacted
}

// recoverWorkerOverflow applies the bounded overflow recovery to a turn
// that failed with a context-too-long error; retry is true when the caller
// should resend with the recovered history.
func recoverWorkerOverflow(err error, recovery *agent.ContextRecovery, history []models.Message, turn int, logger *zap.Logger) ([]models.Message, bool) {
	if err == nil || !agent.IsContextTooLongError(err) || !recovery.CanRecoverContextOverflow() {
		return history, false
	}
	recovered, ok := recovery.RecoverContextOverflow(history)
	if !ok {
		return history, false
	}
	logger.Warn("worker context overflow; compacted and retrying the turn", zap.Int("turn", turn+1), zap.Int("history", len(recovered)))
	return recovered, true
}

// noteWorkerTurn journals what the turn appended (nil-safe).
func noteWorkerTurn(window WindowManager, worker string, before, after []models.Message) {
	if window == nil || len(after) <= len(before) {
		return
	}
	window.NoteTurn(worker, after[len(before):])
}

// resolveWorkerMaxTurns resolves the effective turn budget for a worker:
// config wins, then CHATCLI_AGENT_WORKER_MAX_TURNS, then the default.
func resolveWorkerMaxTurns(config WorkerReActConfig) int {
	if config.MaxTurns > 0 {
		return config.MaxTurns
	}
	if envVal := os.Getenv("CHATCLI_AGENT_WORKER_MAX_TURNS"); envVal != "" {
		if v, err := strconv.Atoi(envVal); err == nil && v > 0 {
			return v
		}
	}
	return DefaultWorkerMaxTurns
}

// buildWorkerToolDefs assembles the native tool definitions for a worker:
// its allowlisted engine subcommands, the read-only context tools granted
// through the same allowlist, universal squad mail, and (when a recaller is
// wired) universal CCR recall.
func buildWorkerToolDefs(config WorkerReActConfig) []models.ToolDefinition {
	toolDefs := CoderToolDefinitions(config.AllowedCommands)
	// Read-only context tools (memory/session/knowledge) granted via the
	// allowlist — CoderToolDefinitions only knows engine subcommands.
	toolDefs = append(toolDefs, ContextToolDefinitions(config.AllowedCommands)...)
	// Squad mail is a universal capability: every worker can message
	// other agents regardless of its command allowlist.
	toolDefs = append(toolDefs, MailToolDefinition())
	// CCR recall is universal too: compressed/truncated results carry
	// <<ccr:KEY>> markers every worker must be able to expand.
	if currentCCRRecaller() != nil {
		toolDefs = append(toolDefs, RecallToolDefinition())
	}
	// Per-task plugin grants (nil without a registered runner+definer).
	toolDefs = append(toolDefs, PluginToolDefinitionsFor(config.PluginTools)...)
	return toolDefs
}

// buildWorkerSystemPrompt composes the worker's effective system prompt:
// mode-appropriate charter + context-navigation guidance + the proactive
// recall block for this task (best-effort, "" when nothing matched).
func buildWorkerSystemPrompt(config WorkerReActConfig, useNativeTools bool, task string) string {
	systemPrompt := config.SystemPrompt
	if useNativeTools {
		systemPrompt = nativeToolSystemPrompt(config)
	} else if strings.TrimSpace(systemPrompt) != "" {
		// XML mode keeps the specialist prompt and gains the same
		// context-navigation guidance native mode embeds.
		systemPrompt += "\n\n" + workerContextGuidance
	}

	// Granted session plugins: name them so the model knows the surface
	// exists (definitions alone are easy to overlook mid-task).
	if len(config.PluginTools) > 0 {
		systemPrompt += "\n\n" + grantedPluginsPromptHeader + strings.Join(config.PluginTools, ", ") + grantedPluginsPromptUsage
	}

	// Proactive recall: give the worker the same [MEMORY AUTO-RECALL] /
	// [SESSION RECALL] surfaces the orchestrator gets, keyed off its task.
	if provider := currentWorkerContextProvider(); provider != nil {
		if block := provider(task); strings.TrimSpace(block) != "" {
			systemPrompt += "\n\n## SESSION CONTEXT (proactive recall)\n" + block
		}
	}
	return systemPrompt
}

// maxEmptyWorkerTurns bounds how many consecutive empty completions a worker
// nudges through before failing. Two covers the transient shapes (a
// thinking-only turn, a hiccup) without letting a model that has genuinely
// nothing to say burn the whole turn budget.
const maxEmptyWorkerTurns = 2

// workerEmptyTurnNudge is the model-facing recovery instruction folded into
// the trailing user message after an empty completion.
const workerEmptyTurnNudge = "[Your previous turn returned no text and no tool calls. " +
	"Either produce your final answer for the delegated task now, or call a tool. " +
	"An empty reply is not a valid completion.]"

// emptyWorkerTurn carries what handleEmptyWorkerTurn needs to decide.
type emptyWorkerTurn struct {
	stopReason string
	modelName  string
	turn       int
	maxTurns   int
	attempts   *int // running count of consecutive empty completions
	logger     *zap.Logger
}

// handleEmptyWorkerTurn applies the recovery policy for a completion with no
// text and no tool calls. Providers return that shape with err == nil in
// several legitimate cases (a thinking-only turn truncated at max_tokens,
// content null, a model that emits nothing under a tool schema it dislikes);
// treating it as "done" produced Status: OK with no output back at the
// orchestrator, which then looped or absorbed the task itself.
//
// Returns the (possibly rewritten) history, whether the loop should retry,
// and a terminal error when the worker must fail:
//   - a truncated empty turn fails immediately with the stop reason — a
//     retry on the same budget cannot help;
//   - otherwise up to maxEmptyWorkerTurns consecutive empties are nudged:
//     the empty assistant turn is dropped (strict providers reject empty
//     text blocks) and the nudge is folded into the trailing user message
//     so alternation is preserved;
//   - past that, the worker fails naming the model that served the call.
func handleEmptyWorkerTurn(history []models.Message, et emptyWorkerTurn) ([]models.Message, bool, error) {
	if et.stopReason == "max_tokens" || et.stopReason == "length" {
		return history, false, fmt.Errorf("turn %d: worker LLM (%s) returned no text and no tool calls: output truncated (stop_reason=%s)",
			et.turn+1, et.modelName, et.stopReason)
	}
	*et.attempts++
	if *et.attempts > maxEmptyWorkerTurns || et.turn >= et.maxTurns-1 {
		return history, false, fmt.Errorf("worker LLM (%s) returned an empty response (no text, no tool calls) after %d attempt(s)",
			et.modelName, *et.attempts)
	}
	history = appendInboxMessage(history[:len(history)-1], workerEmptyTurnNudge)
	if et.logger != nil {
		et.logger.Warn("worker returned an empty response — nudging",
			zap.Int("turn", et.turn+1),
			zap.Int("attempt", *et.attempts),
			zap.String("model", et.modelName))
	}
	return history, true, nil
}

// emptyWorkerResult builds the failure result for an empty-completion exit so
// both exit branches produce the same shape.
func emptyWorkerResult(callID, output string, err error, startTime time.Time, calls []ToolCallRecord) *AgentResult {
	return &AgentResult{
		CallID:    callID,
		Output:    output,
		Error:     err,
		Duration:  time.Since(startTime),
		ToolCalls: calls,
	}
}

// callWorkerLLM performs one LLM round trip in either native function-calling
// mode or text mode, appends the assistant turn to history, and returns the
// response text, any native tool calls, the provider's stop reason (when it
// reports one), the updated history and an error.
func callWorkerLLM(ctx context.Context, useNativeTools bool, toolAware client.ToolAwareClient, llmClient client.LLMClient, history []models.Message, toolDefs []models.ToolDefinition) (string, []models.ToolCall, string, []models.Message, error) {
	if useNativeTools {
		llmResp, err := toolAware.SendPromptWithTools(ctx, "", history, toolDefs, 0)
		if err != nil {
			return "", nil, "", history, err
		}
		if llmResp == nil {
			llmResp = &models.LLMResponse{}
		}
		responseText := llmResp.Content
		nativeToolCalls := llmResp.ToolCalls

		// Build assistant message with structured tool calls
		history = append(history, models.Message{
			Role:      "assistant",
			Content:   responseText,
			ToolCalls: nativeToolCalls,
		})
		return responseText, nativeToolCalls, llmResp.StopReason, history, nil
	}

	responseText, err := llmClient.SendPrompt(ctx, "", history, 0)
	if err != nil {
		return "", nil, "", history, err
	}
	stopReason := ""
	if src, ok := client.AsStopReasonAware(llmClient); ok {
		stopReason = src.LastStopReason()
	}
	history = append(history, models.Message{Role: "assistant", Content: responseText})
	return responseText, nil, stopReason, history, nil
}

// executeToolCall runs a single (possibly blocked) tool call: it enforces the
// security policy, special-cases delegate (recursive subagent), and otherwise
// dispatches to the coder engine, applying file locking and result truncation.
func executeToolCall(ctx context.Context, v validatedTC, lockMgr *FileLockManager, policyChecker PolicyChecker) execResult {
	if v.blocked {
		return execResult{index: v.index, output: v.msg + "\n", failed: true, toolID: v.rtc.ID}
	}

	// In-process tools (squad mail, CCR recall, read-only context views) have
	// no filesystem/shell/network side effects — the security policy exists
	// to gate those, so these skip the check instead of tripping an "ask"
	// prompt that no rule pattern can ever whitelist.
	if policyChecker != nil && !isPolicyExemptSubcmd(v.rtc.Subcmd) {
		policyName, policyArgs := policyCallSurface(v.rtc)
		policyAllowed, msg := policyChecker.CheckAndPrompt(ctx, policyName, policyArgs)
		if !policyAllowed {
			blockedMsg := fmt.Sprintf("[BLOCKED BY POLICY] %s", msg)
			record := ToolCallRecord{
				Name:  v.rtc.Subcmd,
				Args:  v.rtc.RawArgs,
				Error: fmt.Errorf("blocked by security policy"),
			}
			return execResult{index: v.index, record: record, output: blockedMsg + "\n", failed: true, toolID: v.rtc.ID}
		}
	}

	// Special-case: delegate is NOT an engine subcommand — it recursively
	// spawns a subagent with isolated context. Handle it here before we
	// build an engine.
	if v.rtc.Subcmd == "delegate" {
		return executeDelegate(ctx, v)
	}

	// Special-case: mail is the in-memory squad message bus, not an engine
	// subcommand.
	if v.rtc.Subcmd == "mail" {
		return executeMailSend(ctx, v)
	}

	// Special-case: recall expands CCR markers via the registered session
	// layer; context tools are served by the registered read-only runner.
	if v.rtc.Subcmd == recallSubcmd {
		return executeRecall(v)
	}
	if isContextToolSubcmd(v.rtc.Subcmd) {
		return executeContextTool(ctx, v)
	}
	// Granted session plugins (policy already checked above — plugin calls
	// are never in isPolicyExemptSubcmd).
	if v.rtc.pluginName != "" {
		return executePluginTool(ctx, v)
	}

	filePath := extractFilePathFromResolved(v.rtc)
	if isWriteCommand(v.rtc.Subcmd) && filePath != "" && lockMgr != nil {
		lockMgr.Lock(filePath)
	}

	var outBuf, errBuf strings.Builder
	outWriter := engine.NewStreamWriter(func(line string) {
		outBuf.WriteString(line)
		outBuf.WriteString("\n")
	})
	errWriter := engine.NewStreamWriter(func(line string) {
		errBuf.WriteString("ERR: ")
		errBuf.WriteString(line)
		errBuf.WriteString("\n")
	})

	eng := engine.NewEngine(outWriter, errWriter, "")
	execErr := eng.Execute(ctx, v.rtc.Subcmd, v.rtc.Args)
	outWriter.Flush()
	errWriter.Flush()

	if isWriteCommand(v.rtc.Subcmd) && filePath != "" && lockMgr != nil {
		lockMgr.Unlock(filePath)
	}

	output := outBuf.String() + errBuf.String()
	// Use smart truncation: large results saved to disk with inline preview
	output = TruncateToolResult(v.rtc.Subcmd, output)

	record := ToolCallRecord{Name: v.rtc.Subcmd, Args: v.rtc.RawArgs, Output: output}
	hasFailed := false
	if execErr != nil {
		record.Error = execErr
		hasFailed = true
	}

	out := fmt.Sprintf("[%s] %s\n", v.rtc.Subcmd, output)
	if execErr != nil {
		out += fmt.Sprintf("[ERROR] %v\n", execErr)
	}
	return execResult{index: v.index, record: record, output: out, failed: hasFailed, toolID: v.rtc.ID}
}

// appendInboxMessage injects drained squad mail into the history. When the
// last message is already a user turn, the mail is folded into it so
// strict-alternation providers never see consecutive user messages.
func appendInboxMessage(history []models.Message, inboxText string) []models.Message {
	if inboxText == "" {
		return history
	}
	if n := len(history); n > 0 && history[n-1].Role == "user" {
		history[n-1].Content += "\n\n" + inboxText
		return history
	}
	return append(history, models.Message{Role: "user", Content: inboxText})
}

// executeMailSend handles the mail tool call: it resolves the sender from
// the run registry handle on ctx and enqueues the message on the squad bus.
func executeMailSend(ctx context.Context, v validatedTC) execResult {
	fail := func(err error) execResult {
		record := ToolCallRecord{Name: "mail", Args: v.rtc.RawArgs, Error: err}
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[mail] %v\n", err), failed: true, toolID: v.rtc.ID}
	}

	args := v.rtc.NativeArgs
	if len(args) == 0 && v.rtc.RawArgs != "" {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(v.rtc.RawArgs), &parsed); err == nil {
			// Tolerate the {"cmd":"mail","args":{...}} envelope from XML mode.
			if inner, ok := parsed["args"].(map[string]interface{}); ok {
				parsed = inner
			}
			args = parsed
		}
	}
	getStr := func(keys ...string) string {
		for _, k := range keys {
			if s, ok := args[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	to := getStr("to", "recipient", "agent")
	text := getStr("text", "message", "body")
	if to == "" || text == "" {
		return fail(errors.New(`mail requires "to" and "text"`))
	}

	from := "worker"
	if snap := runs.FromContext(ctx).Snapshot(); snap.Agent != "" {
		from = snap.Agent
	}
	msg, err := mail.Default().Send(from, to, getStr("card_id", "cardId", "card"), text)
	if err != nil {
		return fail(err)
	}
	out := fmt.Sprintf("mail sent: %s -> %s (%s)", msg.From, msg.To, msg.ID)
	record := ToolCallRecord{Name: "mail", Args: v.rtc.RawArgs, Output: out}
	return execResult{index: v.index, record: record, output: "[mail] " + out + "\n", toolID: v.rtc.ID}
}

// executeDelegate handles the delegate tool call: it parses the delegation
// args and recursively spawns an isolated subagent, applying the same result
// truncation policy as normal tool calls.
func executeDelegate(ctx context.Context, v validatedTC) execResult {
	dargs, perr := parseDelegateArgs(v.rtc.NativeArgs, v.rtc.RawArgs)
	if perr != nil {
		record := ToolCallRecord{Name: v.rtc.Subcmd, Args: v.rtc.RawArgs, Error: perr}
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[delegate] %v\n", perr), failed: true, toolID: v.rtc.ID}
	}
	subOut, subErr := runSubagent(ctx, dargs)
	record := ToolCallRecord{Name: "delegate", Args: v.rtc.RawArgs, Output: subOut}
	if subErr != nil {
		record.Error = subErr
		return execResult{index: v.index, record: record, output: fmt.Sprintf("[delegate] %v\n%s", subErr, subOut), failed: true, toolID: v.rtc.ID}
	}
	// Apply the same truncation policy as normal tool results so
	// a chatty subagent doesn't blow up the parent context either.
	subOut = TruncateToolResult("delegate", subOut)
	record.Output = subOut
	return execResult{index: v.index, record: record, output: fmt.Sprintf("[delegate] %s\n", subOut), failed: false, toolID: v.rtc.ID}
}

// resolveToolCalls converts native tool calls or XML/JSON-parsed tool calls
// into the unified resolvedToolCall representation. It returns the resolved
// calls plus any ToolCallRecords for parse failures that the caller should
// append to its running log.
func resolveToolCalls(useNativeTools bool, nativeToolCalls []models.ToolCall, responseText string, turn int, pluginTools []string) ([]resolvedToolCall, []ToolCallRecord) {
	var resolved []resolvedToolCall
	var parseErrRecords []ToolCallRecord

	if useNativeTools && len(nativeToolCalls) > 0 {
		for _, ntc := range nativeToolCalls {
			subcmd, found := NativeToolNameToSubcmd(ntc.Name)
			if !found {
				subcmd = ntc.Name
			}
			pluginName := ""
			if grant, ok := pluginGrantForName(pluginTools, ntc.Name); ok {
				pluginName = grant
				subcmd = pluginDefName(grant)
			}
			flags := NativeToolArgsToFlags(subcmd, ntc.Arguments)
			argsJSON, _ := json.Marshal(ntc.Arguments)

			resolved = append(resolved, resolvedToolCall{
				ID:         ntc.ID,
				Name:       ntc.Name,
				Subcmd:     subcmd,
				Args:       flags,
				RawArgs:    string(argsJSON),
				Native:     true,
				NativeArgs: ntc.Arguments,
				pluginName: pluginName,
			})
		}
	} else {
		// XML/JSON parsing fallback — also taken in native mode when the
		// provider returned zero structured calls, mirroring the
		// orchestrator loop: a model that answers a native tool schema with
		// a textual <tool_call> block must not have that block silently
		// taken as its final answer (or, when the text is empty, as
		// "nothing to do").
		xmlToolCalls, _ := agent.ParseToolCalls(responseText)
		for _, tc := range xmlToolCalls {
			subcmd, args, parseErr := parseCoderToolCall(tc)
			if parseErr != nil {
				parseErrRecords = append(parseErrRecords, ToolCallRecord{Name: tc.Name, Args: tc.Args, Error: parseErr})
				continue
			}
			rtc := resolvedToolCall{
				ID:      fmt.Sprintf("xml_%d", turn),
				Name:    tc.Name,
				Subcmd:  subcmd,
				Args:    args,
				RawArgs: tc.Args,
			}
			// XML mode keeps the plugin identity in tc.Name ("@browser") —
			// the envelope's inner cmd landed in Subcmd and would otherwise
			// be blocked by the allowlist.
			if grant, ok := pluginGrantForName(pluginTools, tc.Name); ok {
				rtc.pluginName = grant
				rtc.Subcmd = pluginDefName(grant)
			}
			resolved = append(resolved, rtc)
		}
	}

	return resolved, parseErrRecords
}

// classifyToolCalls pre-validates resolved tool calls against the allowlist,
// the read-only policy and the per-command failure budget. Blocked calls are
// marked (and their blockedCmds counters bumped) so the executor can short-
// circuit them. It returns the classified calls plus the ToolCallRecords the
// caller should append to its running log.
func classifyToolCalls(resolved []resolvedToolCall, allowed map[string]bool, blockedCmds map[string]int, config WorkerReActConfig) ([]validatedTC, []ToolCallRecord) {
	validated := make([]validatedTC, 0, len(resolved))
	var records []ToolCallRecord

	for i, rtc := range resolved {
		if blockedCmds[rtc.Subcmd] >= maxBlockedRetries {
			records = append(records, ToolCallRecord{Name: rtc.Subcmd, Args: rtc.RawArgs, Error: fmt.Errorf("command %q permanently blocked after %d failed attempts", rtc.Subcmd, maxBlockedRetries)})
			validated = append(validated, validatedTC{index: i, rtc: rtc, blocked: true, msg: fmt.Sprintf("[PERMANENTLY BLOCKED] Command %q has failed %d times. You MUST use a completely different approach.", rtc.Subcmd, maxBlockedRetries)})
			continue
		}

		if !allowed[rtc.Subcmd] {
			records = append(records, ToolCallRecord{Name: rtc.Subcmd, Args: rtc.RawArgs, Error: fmt.Errorf("command %q not allowed for this agent", rtc.Subcmd)})
			validated = append(validated, validatedTC{index: i, rtc: rtc, blocked: true, msg: fmt.Sprintf("[BLOCKED] Command %q is not allowed. Allowed: %v", rtc.Subcmd, config.AllowedCommands)})
			blockedCmds[rtc.Subcmd]++
			continue
		}
		if config.ReadOnly && isWriteCommand(rtc.Subcmd) {
			records = append(records, ToolCallRecord{Name: rtc.Subcmd, Args: rtc.RawArgs, Error: fmt.Errorf("write command %q blocked for read-only agent", rtc.Subcmd)})
			validated = append(validated, validatedTC{index: i, rtc: rtc, blocked: true, msg: fmt.Sprintf("[BLOCKED] This agent is read-only and cannot execute %q", rtc.Subcmd)})
			blockedCmds[rtc.Subcmd]++
			continue
		}
		validated = append(validated, validatedTC{index: i, rtc: rtc})
	}

	return validated, records
}

// turnAggregate holds the per-turn outcome rollup produced from exec results.
type turnAggregate struct {
	turnOutput   string
	turnFailures int
	turnBlocked  int
	failedCmds   []string
	records      []ToolCallRecord
}

// aggregateTurnResults rolls up the per-call execution results into the
// counters and feedback string the loop needs, bumping blockedCmds for failed
// commands that carried an error.
func aggregateTurnResults(results []execResult, validated []validatedTC, blockedCmds map[string]int) turnAggregate {
	var turnOutput strings.Builder
	agg := turnAggregate{}

	for _, r := range results {
		if r.record.Name != "" {
			agg.records = append(agg.records, r.record)
		}
		turnOutput.WriteString(r.output)
		if r.failed {
			agg.turnFailures++
			if r.record.Name != "" {
				agg.failedCmds = append(agg.failedCmds, r.record.Name)
				if r.record.Error != nil {
					blockedCmds[r.record.Name]++
				}
			}
		}
	}

	for _, v := range validated {
		if v.blocked {
			agg.turnBlocked++
		}
	}

	agg.turnOutput = turnOutput.String()
	return agg
}

// appendTurnFeedback injects the turn's tool results back into the history. In
// native mode it emits structured tool_result messages; in text mode it folds
// the output (plus any reflection prompt) into a single user message.
func appendTurnFeedback(history []models.Message, useNativeTools bool, results []execResult, turnOutput string, turnFailures, turnBlocked, totalValidated, consecutiveFailures int, blockedCmds map[string]int) []models.Message {
	if useNativeTools {
		// Native mode: send proper tool_result messages
		for _, r := range results {
			toolContent := r.output
			if r.failed && r.record.Error != nil {
				toolContent = fmt.Sprintf("[ERROR] %v\n%s", r.record.Error, r.output)
			}
			history = append(history, models.Message{
				Role:       "tool",
				Content:    toolContent,
				ToolCallID: r.toolID,
			})
		}
		return history
	}

	// Text mode: append feedback as user message (legacy behavior)
	feedback := turnOutput
	if len(feedback) > MaxWorkerOutputBytes {
		const suffixMargin = 256
		feedback = overflowToDisk("feedback", feedback, MaxWorkerOutputBytes-suffixMargin)
	}

	// --- REFLECTION MECHANISM ---
	if turnFailures > 0 {
		feedback += buildReflectionPrompt(turnBlocked, totalValidated, consecutiveFailures, blockedCmds)
	}

	return append(history, models.Message{Role: "user", Content: feedback})
}

// isPolicyExemptSubcmd reports whether a subcommand runs entirely in-process
// with no filesystem/shell/network side effects, and therefore skips the
// security policy check (there is nothing for a rule to gate, and the "ask"
// fallback would block workers on prompts no pattern can whitelist).
func isPolicyExemptSubcmd(subcmd string) bool {
	return subcmd == "mail" || subcmd == recallSubcmd || isContextToolSubcmd(subcmd)
}

// policyCallSurface returns the (toolName, args) pair presented to the
// security policy for a resolved tool call. Native calls are canonicalized
// to the same "@coder" + {"cmd":subcmd,"args":{...}} envelope XML mode
// produces — without this, PolicyManager.Check saw toolName "run_command"
// and read the SHELL command out of args["cmd"] as if it were the
// subcommand, so rules like "@coder exec" never matched and the read-only
// exec auto-allow never fired (every worker exec degraded to "ask").
func policyCallSurface(rtc resolvedToolCall) (string, string) {
	// Granted plugin calls surface under their REAL name so user rules
	// ("@browser eval"), the read-only capability auto-allow and safety
	// immunity all match — canonicalizing them to "@coder" would show the
	// wrong prompt and consult the wrong rules.
	if rtc.pluginName != "" {
		return rtc.pluginName, pluginEnvelopeJSON(rtc)
	}
	if !rtc.Native {
		return rtc.Name, rtc.RawArgs
	}
	envelope := map[string]interface{}{"cmd": rtc.Subcmd}
	if len(rtc.NativeArgs) > 0 {
		envelope["args"] = rtc.NativeArgs
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return rtc.Name, rtc.RawArgs
	}
	return "@coder", string(data)
}

// workerContextGuidance teaches every worker how to navigate truncated and
// compressed context — the same affordances the orchestrator's prompt has.
// Model-facing: English on purpose.
const workerContextGuidance = `## TRUNCATED & COMPRESSED OUTPUT
Large tool results are reduced before you see them; nothing is lost:
- "[full output saved to /path/result_*.txt — N bytes total]": the COMPLETE output is in that file. Read it with the read tool (use start/end line ranges to page through big files).
- "<<ccr:KEY>>" markers: the original content is archived. Expand markers with the recall tool (pass the marker(s) in "keys").
- File reads truncated at a byte limit: re-read the same file with start/end (or head/tail) to get the remaining ranges.
Prefer recalling/reading the stored original over guessing about content you cannot see.`

// nativeToolCoreRules is the generic native-mode charter used when a worker
// has no specialist system prompt of its own.
const nativeToolCoreRules = `You are a specialized coding agent in ChatCLI.

## RULES
1. ALWAYS read a file before modifying it — never edit blind.
2. Keep changes minimal and focused on the task.
3. Preserve existing code style and conventions.
4. Do NOT narrate your actions. No "Let me...", "I will...", "Now I'll...".
5. Call tools directly — zero narration between tool calls.
6. Only output text AFTER all tool calls are done, for the final result or if blocked.

## WORKFLOW
- Read relevant files first to understand context
- Make targeted changes (prefer patch over full write)
- Verify critical changes by reading the result

Content is always plain text — no base64 encoding needed.`

// nativeModeOverride supersedes any XML tool-call syntax instructions that a
// specialist prompt (built-in agent or custom persona) carries — native
// function calling replaces that surface entirely, but the specialist
// identity, expertise, skills and review rules above it must be preserved.
const nativeModeOverride = `## NATIVE TOOL CALLING MODE (overrides any syntax above)
Native function calling is ACTIVE. Any earlier instructions describing <tool_call> XML syntax, <reasoning> tags, JSON envelopes or base64 content are OBSOLETE for this run:
1. Call the provided tools directly through the function-calling API. Content is plain text — no base64.
2. Do NOT narrate your actions between tool calls.
3. Only output text AFTER all tool calls are done, for the final result or if blocked.
Everything else above (your role, expertise, constraints, response structure) still applies.`

// nativeToolSystemPrompt builds the system prompt for native tool calling
// mode. The specialist prompt (built-in agent role or custom persona) is
// PRESERVED — discarding it made every worker a generic coder — with a
// trailing override that retires the XML syntax instructions it may carry.
// Workers without a specialist prompt get the generic charter.
func nativeToolSystemPrompt(config WorkerReActConfig) string {
	specialist := strings.TrimSpace(config.SystemPrompt)
	if specialist == "" {
		return nativeToolCoreRules + "\n\n" + workerContextGuidance
	}
	return specialist + "\n\n" + nativeModeOverride + "\n\n" + workerContextGuidance
}

// buildReflectionPrompt constructs reflection guidance based on failure severity.
func buildReflectionPrompt(turnBlocked, totalValidated, consecutiveFailures int, blockedCmds map[string]int) string {
	var reflection strings.Builder
	reflection.WriteString("\n\n")

	if turnBlocked == totalValidated {
		reflection.WriteString(reflectionAllBlockedPrompt)
	} else if consecutiveFailures >= 3 {
		reflection.WriteString(fmt.Sprintf(reflectionEscalatePrompt, consecutiveFailures))
	} else {
		reflection.WriteString(reflectionStandardPrompt)
	}

	var blacklisted []string
	for cmd, count := range blockedCmds {
		if count >= maxBlockedRetries {
			blacklisted = append(blacklisted, cmd)
		}
	}
	if len(blacklisted) > 0 {
		reflection.WriteString(fmt.Sprintf("\n\nBLACKLISTED COMMANDS (do NOT use): %s", strings.Join(blacklisted, ", ")))
	}

	return reflection.String()
}

// toolActionLabel builds the short, language-neutral live-progress label for
// one tool call: the subcommand plus its file argument when there is one
// (e.g. "read cli/foo.go"). Consumed by the run registry / live panel.
func toolActionLabel(rtc resolvedToolCall) string {
	if p := extractFilePathFromResolved(rtc); p != "" {
		return truncateStr(rtc.Subcmd+" "+p, 60)
	}
	return truncateStr(rtc.Subcmd, 60)
}

// batchActionLabel summarizes a parallel batch for the live-progress label:
// a single call keeps its full label, larger batches show "N× cmd1+cmd2".
func batchActionLabel(batch []resolvedToolCall) string {
	if len(batch) == 1 {
		return toolActionLabel(batch[0])
	}
	seen := make(map[string]bool, len(batch))
	var cmds []string
	for _, rtc := range batch {
		if !seen[rtc.Subcmd] {
			seen[rtc.Subcmd] = true
			cmds = append(cmds, rtc.Subcmd)
		}
	}
	return truncateStr(fmt.Sprintf("%d× %s", len(batch), strings.Join(cmds, "+")), 60)
}

// extractFilePathFromResolved extracts file path from a resolved tool call.
func extractFilePathFromResolved(rtc resolvedToolCall) string {
	if rtc.Native && rtc.NativeArgs != nil {
		if f, ok := rtc.NativeArgs["file"].(string); ok {
			return f
		}
	}
	return extractFilePathFromArgs(rtc.RawArgs)
}

// parseCoderToolCall extracts the subcommand and args from a tool call.
// Supports both JSON args ({"cmd":"read","args":{"file":"main.go"}}) and
// CLI-style args (read --file main.go).
func parseCoderToolCall(tc agent.ToolCall) (string, []string, error) {
	argsStr := tc.Args

	// Try JSON format first
	var jsonArgs struct {
		Cmd  string          `json:"cmd"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal([]byte(argsStr), &jsonArgs); err == nil && jsonArgs.Cmd != "" {
		var argsMap map[string]interface{}
		if err := json.Unmarshal(jsonArgs.Args, &argsMap); err == nil {
			// Normaliza aliases comuns que LLMs confundem
			argsMap = normalizeArgAliases(jsonArgs.Cmd, argsMap)
			var cliArgs []string
			for k, v := range argsMap {
				cliArgs = append(cliArgs, fmt.Sprintf("--%s", k), fmt.Sprintf("%v", v))
			}
			return jsonArgs.Cmd, cliArgs, nil
		}
		// Args might be a simple string
		return jsonArgs.Cmd, nil, nil
	}

	// CLI-style: "read --file main.go"
	parts := strings.Fields(argsStr)
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("empty tool call args")
	}

	// Normalize common LLM misspellings of subcommands
	subcmd := normalizeSubcommand(parts[0])
	return subcmd, parts[1:], nil
}

// normalizeSubcommand maps common LLM variations to the canonical subcommand name.
func normalizeSubcommand(cmd string) string {
	aliases := map[string]string{
		"read_file":      "read",
		"readfile":       "read",
		"read-file":      "read",
		"write_file":     "write",
		"writefile":      "write",
		"write-file":     "write",
		"patch_file":     "patch",
		"patchfile":      "patch",
		"patch-file":     "patch",
		"edit":           "patch",
		"edit_file":      "patch",
		"editfile":       "patch",
		"search_files":   "search",
		"searchfiles":    "search",
		"grep":           "search",
		"find":           "search",
		"run_command":    "exec",
		"run":            "exec",
		"shell":          "exec",
		"bash":           "exec",
		"execute":        "exec",
		"list_dir":       "tree",
		"listdir":        "tree",
		"ls":             "tree",
		"list":           "tree",
		"list_directory": "tree",
		"run_tests":      "test",
		"run_test":       "test",
		"git_status":     "git-status",
		"gitstatus":      "git-status",
		"git_diff":       "git-diff",
		"gitdiff":        "git-diff",
		"git_log":        "git-log",
		"gitlog":         "git-log",
		"git_changed":    "git-changed",
		"gitchanged":     "git-changed",
		"git_branch":     "git-branch",
		"gitbranch":      "git-branch",
		"rollback_file":  "rollback",
		"clean_backups":  "clean",
	}
	if canonical, ok := aliases[strings.ToLower(cmd)]; ok {
		return canonical
	}
	return cmd
}

// normalizeArgAliases maps common LLM arg mistakes to the correct flag names.
func normalizeArgAliases(cmd string, args map[string]interface{}) map[string]interface{} {
	// Alias table: wrong_key → correct_key (per command or global)
	type alias struct {
		from string
		to   string
		cmds []string // nil = all commands
	}
	aliases := []alias{
		{from: "path", to: "file", cmds: []string{"read", "write", "patch"}},
		{from: "filepath", to: "file", cmds: []string{"read", "write", "patch"}},
		{from: "filename", to: "file", cmds: []string{"read", "write", "patch"}},
		{from: "pattern", to: "term", cmds: []string{"search"}},
		{from: "query", to: "term", cmds: []string{"search"}},
		{from: "regex", to: "term", cmds: []string{"search"}},
		{from: "directory", to: "dir"},
		{from: "cwd", to: "dir"},
		{from: "workdir", to: "dir"},
		{from: "command", to: "cmd", cmds: []string{"exec"}},
		{from: "content_b64", to: "content", cmds: []string{"write", "patch"}},
		{from: "body", to: "content", cmds: []string{"write"}},
		{from: "data", to: "content", cmds: []string{"write"}},
		{from: "begin", to: "start", cmds: []string{"read"}},
		{from: "from", to: "start", cmds: []string{"read"}},
		{from: "to", to: "end", cmds: []string{"read"}},
		{from: "depth", to: "max-depth", cmds: []string{"tree"}},
		{from: "max_depth", to: "max-depth", cmds: []string{"tree"}},
		{from: "maxdepth", to: "max-depth", cmds: []string{"tree"}},
	}

	for _, a := range aliases {
		val, exists := args[a.from]
		if !exists {
			continue
		}
		if _, hasDest := args[a.to]; hasDest {
			continue // don't overwrite if correct key already present
		}
		match := a.cmds == nil
		if !match {
			for _, c := range a.cmds {
				if c == cmd {
					match = true
					break
				}
			}
		}
		if match {
			args[a.to] = val
			delete(args, a.from)
		}
	}

	// Se content_b64 foi mapeado para content, garantir encoding=base64
	if _, ok := args["content"]; ok {
		if enc, hasEnc := args["encoding"]; !hasEnc || enc == "" {
			// Se veio de content_b64, é base64
			if cmd == "write" || cmd == "patch" {
				// Detectar se o valor parece base64 (sem espaços/newlines e longo)
				if s, ok := args["content"].(string); ok && len(s) > 50 && !strings.ContainsAny(s, " \n\t{}<>") {
					args["encoding"] = "base64"
				}
			}
		}
	}

	return args
}

// maxBlockedRetries is the number of times a command can fail/be blocked
// before the reflection system permanently blacklists it for this worker.
const maxBlockedRetries = 3

// Reflection prompts injected after failures to force the LLM to replan.

const reflectionStandardPrompt = `[REFLECTION REQUIRED]
One or more actions in this turn FAILED. Before proceeding, you MUST:
1. Analyze WHY each action failed (permission denied? wrong arguments? file not found?)
2. Decide if retrying the same approach makes sense or if you need a different strategy
3. If a command was blocked by policy, do NOT retry the exact same command — try an alternative

Think step by step about what went wrong and what to do differently.`

const reflectionAllBlockedPrompt = `[CRITICAL — ALL ACTIONS BLOCKED]
EVERY action you attempted in this turn was blocked or failed. You are stuck in a loop.

You MUST change your approach entirely:
- If commands are blocked by policy, you cannot bypass this — find an alternative way to accomplish the task
- If commands are not allowed for this agent type, work within your allowed commands
- If you have exhausted all viable approaches, output your findings so far and finish (do NOT emit any more tool_calls)

Do NOT retry the same actions. Think about what you CAN do instead.`

const reflectionEscalatePrompt = `[CRITICAL — %d CONSECUTIVE TURNS WITH FAILURES]
You have had multiple consecutive turns with failures. You are likely stuck in a retry loop.

STOP and reconsider your entire approach:
1. List what you have tried so far and why it failed
2. Identify what constraints are blocking you (permissions, missing files, wrong commands)
3. Either try a fundamentally different approach OR finish with a partial result

If you cannot complete the task with your available tools, say so clearly — do NOT keep retrying the same failing actions.`

// isWriteCommand returns true if the subcommand modifies files.
func isWriteCommand(cmd string) bool {
	switch cmd {
	case "write", "patch", "multipatch", "exec", "test", "rollback", "clean":
		return true
	}
	return false
}

// extractFilePathFromArgs attempts to extract a file path from tool call args.
func extractFilePathFromArgs(args string) string {
	// Try JSON
	var jsonArgs struct {
		Cmd  string `json:"cmd"`
		Args struct {
			File string `json:"file"`
		} `json:"args"`
	}
	if err := json.Unmarshal([]byte(args), &jsonArgs); err == nil && jsonArgs.Args.File != "" {
		return jsonArgs.Args.File
	}

	// Try CLI-style: --file <path>
	parts := strings.Fields(args)
	for i, p := range parts {
		if (p == "--file" || p == "-f") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
