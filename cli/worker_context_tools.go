/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/plugins"
)

// runWorkerContextTool executes one read-only context tool call on behalf of
// a squad worker/subagent. It reuses the same builtin plugins the
// orchestrator's @memory/@session/@knowledge tools run on (adapters are
// process-global), but exposes ONLY their read verbs: the envelope is built
// here from a fixed cmd per tool, so a worker can never reach save/fork/
// attach/remember/forget through this surface.
func (cli *ChatCLI) runWorkerContextTool(ctx context.Context, tool string, args map[string]interface{}) (string, error) {
	envelope := func(cmd string, inner map[string]interface{}) (string, error) {
		payload := map[string]interface{}{"cmd": cmd}
		if len(inner) > 0 {
			payload["args"] = inner
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	// pick copies only the whitelisted keys — lenient on missing/mistyped
	// values (the plugin's own parser reports what is actually required).
	pick := func(keys ...string) map[string]interface{} {
		inner := make(map[string]interface{}, len(keys))
		for _, k := range keys {
			if v, ok := args[k]; ok && v != nil {
				inner[k] = v
			}
		}
		return inner
	}

	switch tool {
	case workers.ContextToolMemoryRecall:
		env, err := envelope("recall", pick("query"))
		if err != nil {
			return "", err
		}
		return plugins.NewBuiltinMemoryPlugin().Execute(ctx, []string{env})

	case workers.ContextToolSessionSearch:
		env, err := envelope("search", pick("query", "limit"))
		if err != nil {
			return "", err
		}
		return plugins.NewBuiltinSessionPlugin().Execute(ctx, []string{env})

	case workers.ContextToolSessionGet:
		env, err := envelope("get", pick("name", "offset", "limit", "query", "message"))
		if err != nil {
			return "", err
		}
		return plugins.NewBuiltinSessionPlugin().Execute(ctx, []string{env})

	case workers.ContextToolKnowledgeSearch:
		env, err := envelope("search", pick("query", "top_k", "kb"))
		if err != nil {
			return "", err
		}
		return plugins.NewBuiltinKnowledgePlugin().Execute(ctx, []string{env})

	case workers.ContextToolKnowledgeGet:
		env, err := envelope("get", pick("source", "offset", "kb"))
		if err != nil {
			return "", err
		}
		return plugins.NewBuiltinKnowledgePlugin().Execute(ctx, []string{env})
	}

	return "", fmt.Errorf("unknown worker context tool %q", tool)
}
