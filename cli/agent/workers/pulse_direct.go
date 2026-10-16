/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the engine calls the executable skills make directly
 * (git status before a commit, the formatter run, a rename's patches): they
 * never pass through the ReAct dispatch, so they report themselves here, on
 * the same @coder node with the same subcmd attribute executeToolCall uses.
 * Arguments and output stay off the bus.
 */
package workers

import (
	"context"

	"github.com/diillson/chatcli/cli/agent/runs"
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseDirectOrigin marks a tool node started by a skill script rather than
// by the model.
const pulseDirectOrigin = "skill-script"

// engineExecutor is the slice of the coder engine a skill script drives.
type engineExecutor interface {
	Execute(ctx context.Context, cmd string, args []string) error
}

// directExecute runs one engine subcommand as a tool node hung from the
// innermost node of ctx (the worker's run).
func directExecute(ctx context.Context, eng engineExecutor, subcmd string, args []string) error {
	if !pulse.Enabled() {
		return eng.Execute(ctx, subcmd, args)
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindTool, "@coder", runs.FromContext(ctx).ID())
	span.With("subcmd", subcmd).With("origin", pulseDirectOrigin)
	err := eng.Execute(ctx, subcmd, args)
	span.EndErr(err)
	return err
}
