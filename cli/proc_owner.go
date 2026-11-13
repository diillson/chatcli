/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * proc_owner.go — how the owner of a process table travels where a context
 * alone does not reach: on a scheduled job, which is created under the
 * session's context and fires under the dispatcher's; and to the commands
 * only the person at this terminal may run over every owner.
 */
package cli

import (
	"context"
	"encoding/json"

	"github.com/diillson/chatcli/cli/agent/proc"
	"github.com/diillson/chatcli/cli/scheduler"
)

// procOwnerJobTag records, on a scheduled job, the process table of the
// session that created it. Written only by stampProcOwner — whatever a
// caller put under this key is overwritten or removed, so neither a model
// nor a /schedule --tag can point a job at another session's table.
const procOwnerJobTag = "chatcli.proc_owner"

// stampProcOwner records ctx's process-table owner in a job's tags, and
// removes any value the caller supplied when ctx has none.
func stampProcOwner(ctx context.Context, tags map[string]string) map[string]string {
	owner := proc.OwnerFrom(ctx)
	if owner == "" {
		delete(tags, procOwnerJobTag)
		return tags
	}
	if tags == nil {
		tags = map[string]string{}
	}
	tags[procOwnerJobTag] = owner
	return tags
}

// stampProcOwnerJSON is stampProcOwner over a schedule/wait tool input.
// Input that is not a JSON object passes through: the scheduler reports it.
func stampProcOwnerJSON(ctx context.Context, inputJSON string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(inputJSON), &m); err != nil {
		return inputJSON
	}
	var tags map[string]string
	if raw, ok := m["tags"]; ok && json.Unmarshal(raw, &tags) != nil {
		return inputJSON // tags of the wrong shape: the scheduler reports it
	}
	_, had := tags[procOwnerJobTag]
	tags = stampProcOwner(ctx, tags)
	if _, has := tags[procOwnerJobTag]; !had && !has {
		return inputJSON
	}
	if len(tags) == 0 {
		delete(m, "tags")
	} else {
		raw, err := json.Marshal(tags)
		if err != nil {
			return inputJSON
		}
		m["tags"] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return inputJSON
	}
	return string(out)
}

// procCtxOfJob marks the context of a firing job with the process table
// its creator recorded, so a scheduled /proc or agent run acts on the
// session that scheduled it and not on the terminal's.
func procCtxOfJob(ctx context.Context) context.Context {
	if owner := scheduler.JobTagsFrom(ctx)[procOwnerJobTag]; owner != "" {
		return proc.WithOwner(ctx, owner)
	}
	return ctx
}

// localOperatorKey marks a command typed at this terminal's prompt.
type localOperatorKey struct{}

// withLocalOperator marks ctx as a command the person at the terminal
// typed at the REPL prompt. Nothing else sets it: not a headless surface,
// not a scheduled job, not a command issued while a turn runs.
func withLocalOperator(ctx context.Context) context.Context {
	return context.WithValue(ctx, localOperatorKey{}, true)
}

// isLocalOperator reports whether ctx is a prompt command of the terminal
// session — the only caller allowed to look across process-table owners.
func isLocalOperator(ctx context.Context) bool {
	local, _ := ctx.Value(localOperatorKey{}).(bool)
	return local && !proc.HasOwner(ctx)
}
