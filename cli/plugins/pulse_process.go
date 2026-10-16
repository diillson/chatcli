/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for external executable plugins. The tool call that runs a
 * plugin is already a node; the process it spawns is a child of it, so the
 * dashboard tells a slow plugin apart from a slow dispatch and shows how the
 * process ended: exit code, the signal that killed it, whether the deadline
 * did, and whether the binary that ran was the signature-verified copy.
 * Arguments, output and paths never travel.
 */
package plugins

import (
	"context"
	"errors"
	"os"
	"strconv"
	"syscall"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulsePluginProcessNode is the dashboard node every plugin process
// aggregates under; the plugin is an attribute.
const pulsePluginProcessNode = "plugin-process"

// pulseProcessBegin opens the node of one plugin process under the
// innermost node of ctx (the tool call that runs it).
func (p *ExecutablePlugin) pulseProcessBegin(ctx context.Context) *pulse.Span {
	if !pulse.Enabled() {
		return nil
	}
	_, span := pulse.BeginCtx(ctx, pulse.KindTool, pulsePluginProcessNode, "")
	return span.With("plugin", p.Name()).With("verified", strconv.FormatBool(p.execPath != ""))
}

// signalStatus is the part of a platform wait status that says whether a
// signal ended the process.
type signalStatus interface {
	Signaled() bool
	Signal() syscall.Signal
}

// pulseProcessEnd closes the node with how the process ended. execCtx is the
// deadline-bound context the process ran under, ctx the caller's.
func pulseProcessEnd(span *pulse.Span, ctx, execCtx context.Context, state *os.ProcessState, waitErr error) {
	if span == nil {
		return
	}
	timedOut := errors.Is(execCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	span = span.With("timeout", strconv.FormatBool(timedOut))
	if state != nil {
		span = span.With("exit_code", strconv.Itoa(state.ExitCode()))
		if ws, ok := state.Sys().(signalStatus); ok && ws.Signaled() {
			span = span.With("signal", ws.Signal().String())
		}
	}
	switch {
	case ctx.Err() != nil || timedOut:
		span.End(pulse.StatusCancelled)
	default:
		span.EndErr(waitErr)
	}
}
