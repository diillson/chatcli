/*
 * ChatCLI - Subagent (delegated-context worker)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A subagent is a recursive invocation of RunWorkerReAct with an isolated
 * history. The parent agent delegates a focused task — typically one that
 * would otherwise flood the parent's context with raw data (large metrics
 * endpoint, verbose log analysis, exhaustive code search). The subagent
 * runs its own ReAct loop, uses whatever tools it was granted, and returns
 * only a final text summary to the parent.
 *
 * Key properties:
 *   - Subagent history is NOT shared with the parent. Only Output comes back.
 *   - Subagent depth is capped (default 2) to prevent pathological recursion.
 *   - Subagent inherits the parent's LLM client, lock manager, and logger.
 *   - Tool allowlist is configurable per call (default: read-only toolset).
 */
package workers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// DefaultSubagentMaxDepth caps recursive delegation.
// Overridable via CHATCLI_AGENT_SUBAGENT_MAX_DEPTH.
const DefaultSubagentMaxDepth = 2

// DefaultSubagentMaxTurns caps ReAct iterations inside a single subagent.
// Overridable via CHATCLI_AGENT_SUBAGENT_MAX_TURNS.
const DefaultSubagentMaxTurns = 15

// defaultSubagentReadOnlyTools is the toolset granted when the parent does
// not specify one. Covers exploration/analysis without any write side-effects.
var defaultSubagentReadOnlyTools = []string{
	"read", "search", "tree",
	"git-status", "git-diff", "git-log", "git-changed", "git-branch",
}

// subagentContextKey carries depth + parent LLM client/lock manager/logger
// through recursive RunWorkerReAct calls.
type subagentContextKey struct{}

// subagentContext holds the values needed to spin up a nested worker.
type subagentContext struct {
	Depth         int
	LLMClient     client.LLMClient
	LockManager   *FileLockManager
	Skills        *SkillSet
	PolicyChecker PolicyChecker
	Logger        *zap.Logger
}

// withSubagentContext attaches a subagent context so nested ReAct loops can
// find the dependencies they need (LLM client, lock manager, etc.) without
// us threading them through public APIs.
func withSubagentContext(ctx context.Context, sc subagentContext) context.Context {
	return context.WithValue(ctx, subagentContextKey{}, sc)
}

// getSubagentContext retrieves the subagent context or a zero value if none.
func getSubagentContext(ctx context.Context) subagentContext {
	if v := ctx.Value(subagentContextKey{}); v != nil {
		if sc, ok := v.(subagentContext); ok {
			return sc
		}
	}
	return subagentContext{}
}

// delegateArgs are the JSON arguments accepted by the `delegate` tool.
type delegateArgs struct {
	Description   string   `json:"description"`     // one-line human label
	Prompt        string   `json:"prompt"`          // full task for the subagent
	Tools         []string `json:"tools,omitempty"` // allowlist; default = read-only
	MaxTurns      int      `json:"max_turns,omitempty"`
	ReadOnly      *bool    `json:"read_only,omitempty"` // default true
	SystemPreface string   `json:"system_preface,omitempty"`
}

// parseDelegateArgs parses a native tool call's arguments. Accepts either a
// pre-decoded map (native function calling) or a JSON string (XML fallback).
func parseDelegateArgs(native map[string]interface{}, raw string) (delegateArgs, error) {
	var args delegateArgs

	if len(native) > 0 {
		if v, ok := native["description"].(string); ok {
			args.Description = v
		}
		if v, ok := native["prompt"].(string); ok {
			args.Prompt = v
		}
		if v, ok := native["tools"].([]interface{}); ok {
			for _, t := range v {
				if s, ok := t.(string); ok && s != "" {
					args.Tools = append(args.Tools, s)
				}
			}
		} else if s, ok := native["tools"].(string); ok && s != "" {
			for _, t := range strings.Split(s, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					args.Tools = append(args.Tools, t)
				}
			}
		}
		if v, ok := native["max_turns"].(float64); ok {
			args.MaxTurns = int(v)
		} else if v, ok := native["max_turns"].(int); ok {
			args.MaxTurns = v
		}
		if v, ok := native["read_only"].(bool); ok {
			args.ReadOnly = &v
		}
		if v, ok := native["system_preface"].(string); ok {
			args.SystemPreface = v
		}
	} else if raw != "" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return args, fmt.Errorf("invalid delegate JSON: %w", err)
		}
	}

	if args.Prompt == "" {
		return args, errors.New("delegate requires a non-empty prompt")
	}
	return args, nil
}

// runSubagent executes a delegated task as an isolated ReAct loop. Returns
// the subagent's final Output string (already bounded to MaxWorkerOutputBytes),
// along with metadata useful for observability.
func runSubagent(ctx context.Context, args delegateArgs) (string, error) {
	sc := getSubagentContext(ctx)
	if sc.LLMClient == nil {
		return "", errors.New("delegate tool used outside an active agent loop (no LLM client bound to context)")
	}

	maxDepth := DefaultSubagentMaxDepth
	if v := os.Getenv("CHATCLI_AGENT_SUBAGENT_MAX_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxDepth = n
		}
	}
	if sc.Depth >= maxDepth {
		return "", fmt.Errorf("subagent depth %d exceeds maximum %d — refusing to recurse further", sc.Depth+1, maxDepth)
	}

	// Resolve allowed tool list.
	readOnly := true
	if args.ReadOnly != nil {
		readOnly = *args.ReadOnly
	}
	tools := args.Tools
	if len(tools) == 0 {
		if readOnly {
			tools = append([]string(nil), defaultSubagentReadOnlyTools...)
		} else {
			// Full toolset minus delegate (prevent loops) and destructive rollback/clean.
			tools = []string{"read", "write", "patch", "tree", "search", "exec",
				"git-status", "git-diff", "git-log", "git-changed", "git-branch", "test"}
		}
	}

	// Resolve turn budget.
	maxTurns := args.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultSubagentMaxTurns
		if v := os.Getenv("CHATCLI_AGENT_SUBAGENT_MAX_TURNS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				maxTurns = n
			}
		}
	}

	// Compose the subagent system prompt. Keep it lean — the subagent is
	// focused on one task and returns a summary.
	sysPrompt := subagentSystemPrompt(args.SystemPreface, readOnly, tools)

	subConfig := WorkerReActConfig{
		MaxTurns:        maxTurns,
		SystemPrompt:    sysPrompt,
		AllowedCommands: tools,
		ReadOnly:        readOnly,
	}

	// Increment depth so nested delegates respect the cap.
	subCtx := withSubagentContext(ctx, subagentContext{
		Depth:         sc.Depth + 1,
		LLMClient:     sc.LLMClient,
		LockManager:   sc.LockManager,
		Skills:        sc.Skills,
		PolicyChecker: sc.PolicyChecker,
		Logger:        sc.Logger,
	})

	start := time.Now()
	if sc.Logger != nil {
		sc.Logger.Info("Subagent starting",
			zap.Int("depth", sc.Depth+1),
			zap.String("description", args.Description),
			zap.Int("max_turns", maxTurns),
			zap.Bool("read_only", readOnly),
			zap.Int("tools", len(tools)))
	}

	result, err := RunWorkerReAct(
		subCtx,
		subConfig,
		args.Prompt,
		sc.LLMClient,
		sc.LockManager,
		sc.Skills,
		sc.PolicyChecker,
		sc.Logger,
	)

	elapsed := time.Since(start)
	if sc.Logger != nil {
		sc.Logger.Info("Subagent finished",
			zap.Duration("elapsed", elapsed),
			zap.Int("tool_calls", func() int {
				if result != nil {
					return len(result.ToolCalls)
				}
				return 0
			}()),
			zap.Error(err))
	}

	if err != nil {
		if result != nil && result.Output != "" {
			return result.Output, err
		}
		return "", err
	}

	if result == nil || result.Output == "" {
		return "[subagent produced no output]", nil
	}

	// Prefix a small header so the parent model knows this came from a subagent.
	header := fmt.Sprintf("[subagent result — description=%q, tool_calls=%d, elapsed=%s]\n",
		args.Description, len(result.ToolCalls), elapsed.Round(time.Millisecond))
	return header + result.Output, nil
}

// RunDelegate is the public entry point used by the main agent_mode.Run
// loop when it sees a delegate_subagent native tool call. It assembles the
// subagent context and invokes runSubagent.
//
// rawArgs should be the JSON object that the LLM produced (either wrapped
// as {"cmd":"delegate","args":{...}} or the inner args directly); native
// is the structured map from native function calling (preferred when
// available).
func RunDelegate(
	ctx context.Context,
	native map[string]interface{},
	rawArgs string,
	llmClient client.LLMClient,
	lockMgr *FileLockManager,
	skills *SkillSet,
	policyChecker PolicyChecker,
	logger *zap.Logger,
) (string, error) {
	// Unwrap {"cmd":"delegate","args":{...}} if that's what we got.
	if len(native) == 0 && rawArgs != "" {
		var outer struct {
			Cmd  string          `json:"cmd"`
			Args json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal([]byte(rawArgs), &outer); err == nil && outer.Cmd == "delegate" && len(outer.Args) > 0 {
			var inner map[string]interface{}
			if err := json.Unmarshal(outer.Args, &inner); err == nil {
				native = inner
			} else {
				rawArgs = string(outer.Args)
			}
		}
	}

	args, err := parseDelegateArgs(native, rawArgs)
	if err != nil {
		return "", err
	}

	// Inject the subagent context so runSubagent has what it needs.
	ctx = withSubagentContext(ctx, subagentContext{
		Depth:         0,
		LLMClient:     llmClient,
		LockManager:   lockMgr,
		Skills:        skills,
		PolicyChecker: policyChecker,
		Logger:        logger,
	})

	return runSubagent(ctx, args)
}

// subagentSystemPrompt composes the subagent's system prompt.
func subagentSystemPrompt(preface string, readOnly bool, tools []string) string {
	var sb strings.Builder
	if preface != "" {
		sb.WriteString(preface)
		sb.WriteString("\n\n")
	}
	sb.WriteString("You are a focused ChatCLI subagent with an ISOLATED context window.\n")
	sb.WriteString("You were delegated a specific task by a parent agent. Only your final text output is returned to the parent — your tool calls, intermediate reasoning, and raw tool outputs stay here.\n\n")
	sb.WriteString("## RULES\n")
	sb.WriteString("1. Answer ONLY the delegated task. Do not expand scope.\n")
	sb.WriteString("2. Use tools to gather what you need, then produce a concise, structured summary (markdown OK).\n")
	sb.WriteString("3. Do NOT narrate between tool calls. Final text answer only.\n")
	sb.WriteString("4. If you hit a dead-end, say so explicitly and return partial findings — do not loop.\n")
	if readOnly {
		sb.WriteString("5. This subagent is READ-ONLY. Any write/exec attempt will be blocked.\n")
	}
	sb.WriteString(fmt.Sprintf("\nAllowed tools: %s\n", strings.Join(tools, ", ")))

	// Knowledge: point the subagent at the session scratch dir in case it
	// needs to stage intermediate files.
	if tmp := os.Getenv("CHATCLI_AGENT_TMPDIR"); tmp != "" {
		sb.WriteString(fmt.Sprintf("\nScratch dir (read/write allowed): %s  (exposed as $CHATCLI_AGENT_TMPDIR to exec)\n", tmp))
	}

	return sb.String()
}
