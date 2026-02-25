package workers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
const DefaultWorkerMaxTurns = 10

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
	logger *zap.Logger,
) (*AgentResult, error) {
	startTime := time.Now()
	callID := nextCallID()

	maxTurns := config.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultWorkerMaxTurns
	}

	history := []models.Message{
		{Role: "system", Content: config.SystemPrompt},
		{Role: "user", Content: task},
	}

	var allToolCalls []ToolCallRecord
	var finalOutput strings.Builder

	allowed := make(map[string]bool, len(config.AllowedCommands))
	for _, cmd := range config.AllowedCommands {
		allowed[cmd] = true
	}

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

		// Execute each tool call
		var turnOutput strings.Builder
		for _, tc := range toolCalls {
			subcmd, args, parseErr := parseCoderToolCall(tc)
			if parseErr != nil {
				record := ToolCallRecord{
					Name:  tc.Name,
					Args:  tc.Args,
					Error: parseErr,
				}
				allToolCalls = append(allToolCalls, record)
				fmt.Fprintf(&turnOutput, "[ERROR] Failed to parse tool call: %v\n", parseErr)
				continue
			}

			// Validate against allowed commands
			if !allowed[subcmd] {
				record := ToolCallRecord{
					Name:  subcmd,
					Args:  tc.Args,
					Error: fmt.Errorf("command %q not allowed for this agent", subcmd),
				}
				allToolCalls = append(allToolCalls, record)
				fmt.Fprintf(&turnOutput, "[BLOCKED] Command %q is not allowed for this agent. Allowed: %v\n",
					subcmd, config.AllowedCommands)
				continue
			}

			// Check write permission for read-only agents
			if config.ReadOnly && isWriteCommand(subcmd) {
				record := ToolCallRecord{
					Name:  subcmd,
					Args:  tc.Args,
					Error: fmt.Errorf("write command %q blocked for read-only agent", subcmd),
				}
				allToolCalls = append(allToolCalls, record)
				fmt.Fprintf(&turnOutput, "[BLOCKED] This agent is read-only and cannot execute %q\n", subcmd)
				continue
			}

			// Acquire file lock for write operations
			filePath := extractFilePathFromArgs(tc.Args)
			if isWriteCommand(subcmd) && filePath != "" && lockMgr != nil {
				lockMgr.Lock(filePath)
			}

			// Execute via fresh Engine
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
			execErr := eng.Execute(ctx, subcmd, args)
			outWriter.Flush()
			errWriter.Flush()

			// Release file lock
			if isWriteCommand(subcmd) && filePath != "" && lockMgr != nil {
				lockMgr.Unlock(filePath)
			}

			output := outBuf.String() + errBuf.String()
			// Truncate large output
			if len(output) > MaxWorkerOutputBytes {
				output = output[:MaxWorkerOutputBytes] + "\n... [output truncated]"
			}

			record := ToolCallRecord{
				Name:   subcmd,
				Args:   tc.Args,
				Output: output,
			}
			if execErr != nil {
				record.Error = execErr
			}
			allToolCalls = append(allToolCalls, record)

			fmt.Fprintf(&turnOutput, "[%s] %s\n", subcmd, output)
			if execErr != nil {
				fmt.Fprintf(&turnOutput, "[ERROR] %v\n", execErr)
			}
		}

		// Feed results back to LLM
		feedback := turnOutput.String()
		if len(feedback) > MaxWorkerOutputBytes {
			feedback = feedback[:MaxWorkerOutputBytes] + "\n... [feedback truncated]"
		}
		history = append(history, models.Message{Role: "user", Content: feedback})
		finalOutput.WriteString(feedback)
	}

	output := finalOutput.String()
	if len(output) > MaxWorkerOutputBytes {
		output = output[:MaxWorkerOutputBytes] + "\n... [output truncated]"
	}

	return &AgentResult{
		CallID:    callID,
		Output:    output,
		Duration:  time.Since(startTime),
		ToolCalls: allToolCalls,
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
