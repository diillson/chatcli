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
// Executable skills are short-circuited (executed directly without LLM call).
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
		// Allow env override for worker max turns
		if envVal := os.Getenv("CHATCLI_AGENT_WORKER_MAX_TURNS"); envVal != "" {
			if v, err := strconv.Atoi(envVal); err == nil && v > 0 {
				maxTurns = v
			}
		}
	}

	history := []models.Message{
		{Role: "system", Content: config.SystemPrompt},
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
	blockedCmds := make(map[string]int) // cmd → count of times blocked/failed

	for turn := 0; turn < maxTurns; turn++ {
		// Check cancellation
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

		// Call LLM
		response, err := llmClient.SendPrompt(ctx, "", history, 0)
		if err != nil {
			return &AgentResult{
				CallID:    callID,
				Output:    finalOutput.String(),
				Error:     fmt.Errorf("LLM call failed on turn %d: %w", turn+1, err),
				Duration:  time.Since(startTime),
				ToolCalls: allToolCalls,
			}, err
		}

		history = append(history, models.Message{Role: "assistant", Content: response})

		// Parse tool_calls from response
		toolCalls, _ := agent.ParseToolCalls(response)

		if len(toolCalls) == 0 {
			// No tool calls — worker is done, capture final response
			finalOutput.WriteString(response)
			break
		}

		// Pre-validate and classify tool calls
		type validatedTC struct {
			index   int
			tc      agent.ToolCall
			subcmd  string
			args    []string
			blocked bool
			msg     string
		}
		validated := make([]validatedTC, 0, len(toolCalls))
		allReadOnly := true
		for i, tc := range toolCalls {
			subcmd, args, parseErr := parseCoderToolCall(tc)
			if parseErr != nil {
				allToolCalls = append(allToolCalls, ToolCallRecord{Name: tc.Name, Args: tc.Args, Error: parseErr})
				validated = append(validated, validatedTC{index: i, tc: tc, blocked: true, msg: fmt.Sprintf("[ERROR] Failed to parse tool call: %v", parseErr)})
				continue
			}

			// Check if this command has been blocked too many times
			if blockedCmds[subcmd] >= maxBlockedRetries {
				allToolCalls = append(allToolCalls, ToolCallRecord{Name: subcmd, Args: tc.Args, Error: fmt.Errorf("command %q permanently blocked after %d failed attempts", subcmd, maxBlockedRetries)})
				validated = append(validated, validatedTC{index: i, tc: tc, subcmd: subcmd, blocked: true, msg: fmt.Sprintf("[PERMANENTLY BLOCKED] Command %q has failed %d times. You MUST use a completely different approach. Do NOT retry this command.", subcmd, maxBlockedRetries)})
				continue
			}

			if !allowed[subcmd] {
				allToolCalls = append(allToolCalls, ToolCallRecord{Name: subcmd, Args: tc.Args, Error: fmt.Errorf("command %q not allowed for this agent", subcmd)})
				validated = append(validated, validatedTC{index: i, tc: tc, subcmd: subcmd, blocked: true, msg: fmt.Sprintf("[BLOCKED] Command %q is not allowed for this agent. Allowed: %v", subcmd, config.AllowedCommands)})
				blockedCmds[subcmd]++
				continue
			}
			if config.ReadOnly && isWriteCommand(subcmd) {
				allToolCalls = append(allToolCalls, ToolCallRecord{Name: subcmd, Args: tc.Args, Error: fmt.Errorf("write command %q blocked for read-only agent", subcmd)})
				validated = append(validated, validatedTC{index: i, tc: tc, subcmd: subcmd, blocked: true, msg: fmt.Sprintf("[BLOCKED] This agent is read-only and cannot execute %q", subcmd)})
				blockedCmds[subcmd]++
				continue
			}
			if isWriteCommand(subcmd) {
				allReadOnly = false
			}
			validated = append(validated, validatedTC{index: i, tc: tc, subcmd: subcmd, args: args})
		}

		// Count runnable (non-blocked) tool calls
		var runnable []validatedTC
		for _, v := range validated {
			if !v.blocked {
				runnable = append(runnable, v)
			}
		}

		// Execute tool calls: parallel for read-only batches, sequential otherwise
		type execResult struct {
			index  int
			record ToolCallRecord
			output string
			failed bool
		}
		results := make([]execResult, len(validated))

		canParallelize := allReadOnly && len(runnable) > 1
		if canParallelize {
			logger.Info("Executing tool calls in parallel",
				zap.Int("count", len(runnable)),
				zap.String("callID", callID))
		}

		executeOne := func(v validatedTC) execResult {
			if v.blocked {
				return execResult{index: v.index, output: v.msg + "\n", failed: true}
			}

			// --- POLICY CHECK ---
			if policyChecker != nil {
				allowed, msg := policyChecker.CheckAndPrompt(ctx, v.tc.Name, v.tc.Args)
				if !allowed {
					blockedMsg := fmt.Sprintf("[BLOCKED BY POLICY] %s", msg)
					record := ToolCallRecord{
						Name:  v.subcmd,
						Args:  v.tc.Args,
						Error: fmt.Errorf("blocked by security policy"),
					}
					logger.Warn("Tool call blocked by policy",
						zap.String("subcmd", v.subcmd),
						zap.String("message", msg),
					)
					return execResult{index: v.index, record: record, output: blockedMsg + "\n", failed: true}
				}
			}

			filePath := extractFilePathFromArgs(v.tc.Args)
			if isWriteCommand(v.subcmd) && filePath != "" && lockMgr != nil {
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

			eng := engine.NewEngine(outWriter, errWriter)
			execErr := eng.Execute(ctx, v.subcmd, v.args)
			outWriter.Flush()
			errWriter.Flush()

			if isWriteCommand(v.subcmd) && filePath != "" && lockMgr != nil {
				lockMgr.Unlock(filePath)
			}

			output := outBuf.String() + errBuf.String()
			if len(output) > MaxWorkerOutputBytes {
				output = output[:MaxWorkerOutputBytes] + "\n... [output truncated]"
			}

			record := ToolCallRecord{Name: v.subcmd, Args: v.tc.Args, Output: output}
			hasFailed := false
			if execErr != nil {
				record.Error = execErr
				hasFailed = true
			}

			out := fmt.Sprintf("[%s] %s\n", v.subcmd, output)
			if execErr != nil {
				out += fmt.Sprintf("[ERROR] %v\n", execErr)
			}
			return execResult{index: v.index, record: record, output: out, failed: hasFailed}
		}

		if canParallelize {
			maxParallel = max(maxParallel, len(runnable))
			var wg sync.WaitGroup
			var mu sync.Mutex
			for i, v := range validated {
				if v.blocked {
					results[i] = execResult{index: v.index, output: v.msg + "\n", failed: true}
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

		// Aggregate results in original order and count failures
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
					// Track blocked/failed commands for repeat detection
					if r.record.Error != nil {
						blockedCmds[r.record.Name]++
					}
				}
			}
		}

		// Count blocked-only results (no runnable succeeded)
		for _, v := range validated {
			if v.blocked {
				turnBlocked++
			}
		}

		// Build feedback with optional reflection prompt
		feedback := turnOutput.String()
		if len(feedback) > MaxWorkerOutputBytes {
			feedback = feedback[:MaxWorkerOutputBytes] + "\n... [feedback truncated]"
		}

		// --- REFLECTION MECHANISM ---
		if turnFailures > 0 {
			consecutiveFailures++

			// Build reflection prompt based on failure severity
			var reflection strings.Builder
			reflection.WriteString("\n\n")

			if turnBlocked == len(validated) {
				// ALL tool calls in this turn were blocked/failed
				reflection.WriteString(reflectionAllBlockedPrompt)
			} else if consecutiveFailures >= 3 {
				// Multiple consecutive turns with failures — escalate
				reflection.WriteString(fmt.Sprintf(reflectionEscalatePrompt, consecutiveFailures))
			} else {
				// Some failures — standard reflection
				reflection.WriteString(reflectionStandardPrompt)
			}

			// Add blacklist of commands that have failed too many times
			var blacklisted []string
			for cmd, count := range blockedCmds {
				if count >= maxBlockedRetries {
					blacklisted = append(blacklisted, cmd)
				}
			}
			if len(blacklisted) > 0 {
				reflection.WriteString(fmt.Sprintf("\n\nBLACKLISTED COMMANDS (do NOT use): %s", strings.Join(blacklisted, ", ")))
			}

			feedback += reflection.String()

			logger.Debug("Reflection prompt injected",
				zap.Int("consecutive_failures", consecutiveFailures),
				zap.Strings("failed_cmds", failedCmds),
				zap.Int("turn", turn+1),
			)
		} else {
			// Successful turn — reset consecutive failure counter
			consecutiveFailures = 0
		}

		history = append(history, models.Message{Role: "user", Content: feedback})
		finalOutput.WriteString(feedback)
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
	return parts[0], parts[1:], nil
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
