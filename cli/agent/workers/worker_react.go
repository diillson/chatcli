package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent"
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
}

// WorkerReActConfig controls the worker's internal ReAct loop.
type WorkerReActConfig struct {
	MaxTurns        int
	SystemPrompt    string
	AllowedCommands []string // which @coder subcommands this worker can use
	ReadOnly        bool     // if true, only read/search/tree/git-read allowed
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

	maxTurns := config.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultWorkerMaxTurns
		if envVal := os.Getenv("CHATCLI_AGENT_WORKER_MAX_TURNS"); envVal != "" {
			if v, err := strconv.Atoi(envVal); err == nil && v > 0 {
				maxTurns = v
			}
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
		toolDefs = CoderToolDefinitions(config.AllowedCommands)
		logger.Info("Using native function calling",
			zap.Int("tools", len(toolDefs)),
			zap.String("callID", callID))
	}

	// Adjust system prompt for native tool mode (no XML instructions needed)
	systemPrompt := config.SystemPrompt
	if useNativeTools {
		systemPrompt = nativeToolSystemPrompt(config)
	}

	history := []models.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: task},
	}

	var allToolCalls []ToolCallRecord
	var finalOutput strings.Builder
	maxParallel := 0

	allowed := make(map[string]bool, len(config.AllowedCommands))
	for _, cmd := range config.AllowedCommands {
		allowed[cmd] = true
	}

	// --- Failure tracking for reflection ---
	consecutiveFailures := 0
	blockedCmds := make(map[string]int)

	for turn := 0; turn < maxTurns; turn++ {
		select {
		case <-ctx.Done():
			return &AgentResult{
				CallID:    callID,
				Output:    finalOutput.String(),
				Error:     ctx.Err(),
				Duration:  time.Since(startTime),
				ToolCalls: allToolCalls,
			}, ctx.Err()
		default:
		}

		// --- Call LLM (native or text mode) ---
		var responseText string
		var nativeToolCalls []models.ToolCall

		if useNativeTools {
			llmResp, err := toolAware.SendPromptWithTools(ctx, "", history, toolDefs, 0)
			if err != nil {
				return &AgentResult{
					CallID:    callID,
					Output:    finalOutput.String(),
					Error:     fmt.Errorf("LLM call failed on turn %d: %w", turn+1, err),
					Duration:  time.Since(startTime),
					ToolCalls: allToolCalls,
				}, err
			}
			responseText = llmResp.Content
			nativeToolCalls = llmResp.ToolCalls

			// Build assistant message with structured tool calls
			history = append(history, models.Message{
				Role:      "assistant",
				Content:   responseText,
				ToolCalls: nativeToolCalls,
			})
		} else {
			var err error
			responseText, err = llmClient.SendPrompt(ctx, "", history, 0)
			if err != nil {
				return &AgentResult{
					CallID:    callID,
					Output:    finalOutput.String(),
					Error:     fmt.Errorf("LLM call failed on turn %d: %w", turn+1, err),
					Duration:  time.Since(startTime),
					ToolCalls: allToolCalls,
				}, err
			}
			history = append(history, models.Message{Role: "assistant", Content: responseText})
		}

		// --- Resolve tool calls to unified format ---
		var resolved []resolvedToolCall

		if useNativeTools && len(nativeToolCalls) > 0 {
			for _, ntc := range nativeToolCalls {
				subcmd, found := NativeToolNameToSubcmd(ntc.Name)
				if !found {
					subcmd = ntc.Name
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
				})
			}
		} else if !useNativeTools {
			// XML/JSON parsing fallback
			xmlToolCalls, _ := agent.ParseToolCalls(responseText)
			for _, tc := range xmlToolCalls {
				subcmd, args, parseErr := parseCoderToolCall(tc)
				if parseErr != nil {
					allToolCalls = append(allToolCalls, ToolCallRecord{Name: tc.Name, Args: tc.Args, Error: parseErr})
					continue
				}
				resolved = append(resolved, resolvedToolCall{
					ID:      fmt.Sprintf("xml_%d", turn),
					Name:    tc.Name,
					Subcmd:  subcmd,
					Args:    args,
					RawArgs: tc.Args,
				})
			}
		}

		if len(resolved) == 0 {
			// No tool calls — worker is done
			finalOutput.WriteString(responseText)
			break
		}

		// --- Pre-validate and classify ---
		type validatedTC struct {
			index   int
			rtc     resolvedToolCall
			blocked bool
			msg     string
		}
		validated := make([]validatedTC, 0, len(resolved))

		for i, rtc := range resolved {
			if blockedCmds[rtc.Subcmd] >= maxBlockedRetries {
				allToolCalls = append(allToolCalls, ToolCallRecord{Name: rtc.Subcmd, Args: rtc.RawArgs, Error: fmt.Errorf("command %q permanently blocked after %d failed attempts", rtc.Subcmd, maxBlockedRetries)})
				validated = append(validated, validatedTC{index: i, rtc: rtc, blocked: true, msg: fmt.Sprintf("[PERMANENTLY BLOCKED] Command %q has failed %d times. You MUST use a completely different approach.", rtc.Subcmd, maxBlockedRetries)})
				continue
			}

			if !allowed[rtc.Subcmd] {
				allToolCalls = append(allToolCalls, ToolCallRecord{Name: rtc.Subcmd, Args: rtc.RawArgs, Error: fmt.Errorf("command %q not allowed for this agent", rtc.Subcmd)})
				validated = append(validated, validatedTC{index: i, rtc: rtc, blocked: true, msg: fmt.Sprintf("[BLOCKED] Command %q is not allowed. Allowed: %v", rtc.Subcmd, config.AllowedCommands)})
				blockedCmds[rtc.Subcmd]++
				continue
			}
			if config.ReadOnly && isWriteCommand(rtc.Subcmd) {
				allToolCalls = append(allToolCalls, ToolCallRecord{Name: rtc.Subcmd, Args: rtc.RawArgs, Error: fmt.Errorf("write command %q blocked for read-only agent", rtc.Subcmd)})
				validated = append(validated, validatedTC{index: i, rtc: rtc, blocked: true, msg: fmt.Sprintf("[BLOCKED] This agent is read-only and cannot execute %q", rtc.Subcmd)})
				blockedCmds[rtc.Subcmd]++
				continue
			}
			validated = append(validated, validatedTC{index: i, rtc: rtc})
		}

		var runnable []validatedTC
		var runnableResolved []resolvedToolCall
		for _, v := range validated {
			if !v.blocked {
				runnable = append(runnable, v)
				runnableResolved = append(runnableResolved, v.rtc)
			}
		}

		// --- Execute tool calls ---
		type execResult struct {
			index  int
			record ToolCallRecord
			output string
			failed bool
			toolID string // for native tool result messages
		}
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

		executeOne := func(v validatedTC) execResult {
			if v.blocked {
				return execResult{index: v.index, output: v.msg + "\n", failed: true, toolID: v.rtc.ID}
			}

			if policyChecker != nil {
				policyAllowed, msg := policyChecker.CheckAndPrompt(ctx, v.rtc.Name, v.rtc.RawArgs)
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

		if canParallelize {
			maxParallel = max(maxParallel, len(runnable))
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
					r := executeOne(vtc)
					mu.Lock()
					results[idx] = r
					mu.Unlock()
				}(i, v)
			}
			wg.Wait()
		} else {
			for i, v := range validated {
				results[i] = executeOne(v)
			}
		}

		// --- Aggregate results ---
		var turnOutput strings.Builder
		turnFailures := 0
		turnBlocked := 0
		var failedCmds []string

		for _, r := range results {
			if r.record.Name != "" {
				allToolCalls = append(allToolCalls, r.record)
			}
			turnOutput.WriteString(r.output)
			if r.failed {
				turnFailures++
				if r.record.Name != "" {
					failedCmds = append(failedCmds, r.record.Name)
					if r.record.Error != nil {
						blockedCmds[r.record.Name]++
					}
				}
			}
		}

		for _, v := range validated {
			if v.blocked {
				turnBlocked++
			}
		}

		// --- Build feedback and inject into history ---
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
		} else {
			// Text mode: append feedback as user message (legacy behavior)
			feedback := turnOutput.String()
			if len(feedback) > MaxWorkerOutputBytes {
				feedback = feedback[:MaxWorkerOutputBytes] + "\n... [feedback truncated]"
			}

			// --- REFLECTION MECHANISM ---
			if turnFailures > 0 {
				feedback += buildReflectionPrompt(turnBlocked, len(validated), consecutiveFailures, blockedCmds)
			}

			history = append(history, models.Message{Role: "user", Content: feedback})
		}

		// Reflection for native mode too
		if useNativeTools && turnFailures > 0 {
			consecutiveFailures++
			reflectionMsg := buildReflectionPrompt(turnBlocked, len(validated), consecutiveFailures, blockedCmds)
			if reflectionMsg != "" {
				history = append(history, models.Message{Role: "user", Content: reflectionMsg})
			}
			logger.Debug("Reflection prompt injected (native)",
				zap.Int("consecutive_failures", consecutiveFailures),
				zap.Strings("failed_cmds", failedCmds),
				zap.Int("turn", turn+1),
			)
		} else if !useNativeTools && turnFailures > 0 {
			consecutiveFailures++
			logger.Debug("Reflection prompt injected",
				zap.Int("consecutive_failures", consecutiveFailures),
				zap.Strings("failed_cmds", failedCmds),
				zap.Int("turn", turn+1),
			)
		} else {
			consecutiveFailures = 0
		}

		finalOutput.WriteString(turnOutput.String())
	}

	output := finalOutput.String()
	if len(output) > MaxWorkerOutputBytes {
		output = output[:MaxWorkerOutputBytes] + "\n... [output truncated]"
	}

	return &AgentResult{
		CallID:        callID,
		Output:        output,
		Duration:      time.Since(startTime),
		ToolCalls:     allToolCalls,
		ParallelCalls: maxParallel,
	}, nil
}

// nativeToolSystemPrompt builds a cleaner system prompt for native tool calling mode.
// No XML syntax instructions needed — the LLM calls tools via the API directly.
func nativeToolSystemPrompt(config WorkerReActConfig) string {
	return `You are a specialized coding agent in ChatCLI.

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
	case "write", "patch", "exec", "test", "rollback", "clean":
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
