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
	"sync/atomic"

	"github.com/diillson/chatcli/cli/agent/proc"
	"github.com/diillson/chatcli/cli/scheduler"
)

// procOwnerJobTag records, on a scheduled job, the process table of the
// session that created it. It is in the scheduler's reserved namespace:
// set from the creating context (withProcOwnerJobTag), refused in input.
const procOwnerJobTag = "chatcli.proc_owner"

// withProcOwnerJobTag marks ctx so the job created under it records the
// process table of the creating session. The scheduler takes reserved tags
// from the context only and refuses them in any input, so neither a model
// nor a /schedule --tag can point a job at another session's table.
func withProcOwnerJobTag(ctx context.Context) context.Context {
	return scheduler.WithTrustedJobTags(ctx, map[string]string{procOwnerJobTag: proc.OwnerFrom(ctx)})
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

// operatorMark is the mark itself. It is live only while the handler of
// the typed command runs: a context outlives the call it was made for — a
// background run started by the command keeps its values — and the mark
// must not.
type operatorMark struct{ live atomic.Bool }

// withLocalOperator marks ctx as a command the person at the terminal
// typed at the REPL prompt; release ends the mark and is called when the
// command's handler returns. Nothing else sets it: not a headless surface,
// not a scheduled job, not a command issued while a turn runs.
func withLocalOperator(ctx context.Context) (marked context.Context, release func()) {
	mark := &operatorMark{}
	mark.live.Store(true)
	return context.WithValue(ctx, localOperatorKey{}, mark), func() { mark.live.Store(false) }
}

// withoutLocalOperator returns ctx with the operator mark dropped. Every
// agent turn starts under it: what the model does in a turn — the side
// commands it is fed, the background runs it starts — is not the operator,
// even when a typed command (/resume, a routed custom command) is what
// started the turn.
func withoutLocalOperator(ctx context.Context) context.Context {
	if ctx.Value(localOperatorKey{}) == nil {
		return ctx
	}
	return context.WithValue(ctx, localOperatorKey{}, (*operatorMark)(nil))
}

// isLocalOperator reports whether ctx is a prompt command of the terminal
// session, still running — the only caller allowed to look across
// process-table owners.
func isLocalOperator(ctx context.Context) bool {
	mark, _ := ctx.Value(localOperatorKey{}).(*operatorMark)
	return mark != nil && mark.live.Load() && !proc.HasOwner(ctx)
}
