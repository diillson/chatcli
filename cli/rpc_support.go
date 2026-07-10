/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * rpc_support.go
 *
 * Exposes ChatCLI capabilities to the MCP/ACP servers (cmd/rpcserve.go) so an
 * MCP client can drive the real agent/coder loops and the built-in tools — not
 * just a chat passthrough.
 *
 * The agent and coder render to stdout; these helpers redirect os.Stdout to a
 * buffer for the duration of the run and return the captured transcript. The
 * JSON-RPC server holds its own copy of the original stdout (captured at
 * construction), so the protocol channel is unaffected by the redirect.
 */
package cli

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/plugins"
)

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// rpcStdoutMu serializes os.Stdout redirection. os.Stdout is process-global and
// the agent/coder loops mutate shared ChatCLI state (e.g. cli.history), so only
// one captured run may be in flight at a time. Concurrent callers (e.g. the
// gateway fanning out messages) block here rather than corrupting each other.
var rpcStdoutMu sync.Mutex

// captureRPCStdout runs fn with os.Stdout redirected and returns the captured
// (ANSI-stripped) output. The pipe is always restored.
func captureRPCStdout(fn func() error) (string, error) {
	return captureStreaming(nil, fn)
}

// captureStreaming runs fn with os.Stdout redirected to a pipe. As fn writes
// lines, each is ANSI-stripped, appended to the returned transcript, and — when
// emit is non-nil — forwarded to emit so callers can stream progress live. The
// original stdout is always restored. Runs are serialized via rpcStdoutMu.
func captureStreaming(emit func(string), fn func() error) (string, error) {
	rpcStdoutMu.Lock()
	defer rpcStdoutMu.Unlock()

	orig := os.Stdout
	r, w, perr := os.Pipe()
	if perr != nil {
		return "", fn() // fall back to running without capture
	}
	os.Stdout = w

	var buf strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				clean := ansiSeq.ReplaceAllString(line, "")
				buf.WriteString(clean)
				if emit != nil {
					if s := strings.TrimSpace(clean); s != "" {
						emit(s)
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	runErr := fn()

	_ = w.Close()
	os.Stdout = orig
	<-done
	_ = r.Close()

	return strings.TrimSpace(buf.String()), runErr
}

// RunAgentCaptured runs the full agent (ReAct) loop one-shot on task with
// auto-execute, capturing its transcript. Used by the MCP agent_task tool.
func (cli *ChatCLI) RunAgentCaptured(ctx context.Context, task string) (string, error) {
	out, err := captureRPCStdout(func() error {
		return cli.RunAgentOnce(ctx, "/agent "+task, true)
	})
	if err != nil {
		return out, err
	}
	if out == "" {
		out = "(agent produced no textual output)"
	}
	return out, nil
}

// RunAgentStreaming runs the full agent (ReAct) loop one-shot on task with
// auto-execute, forwarding the agent's rendered progress to emit line by line
// as it works, and returning the full transcript. Used by the messaging
// gateway to narrate task execution back to the chat platform.
func (cli *ChatCLI) RunAgentStreaming(ctx context.Context, task string, emit func(string)) (string, error) {
	out, err := captureStreaming(emit, func() error {
		return cli.RunAgentOnce(ctx, "/agent "+task, true)
	})
	if err != nil {
		return out, err
	}
	if out == "" {
		out = "(agent produced no textual output)"
	}
	return out, nil
}

// RunGatewayCoderStreaming runs the coder ReAct loop one-shot on task with the
// gateway persona, forwarding the rendered progress to emit line by line and
// returning the full transcript. Used by the messaging gateway: it keeps the
// coder engine's full capability (create/edit files, run commands, iterate)
// while answering as concise chat prose. The clean final answer is captured
// into cli.lastAgentReply during the run.
func (cli *ChatCLI) RunGatewayCoderStreaming(ctx context.Context, task string, emit func(string)) (string, error) {
	out, err := captureStreaming(emit, func() error {
		return cli.RunGatewayCoderOnce(ctx, task)
	})
	if err != nil {
		return out, err
	}
	if out == "" {
		out = "(coder produced no textual output)"
	}
	return out, nil
}

// RunCoderCaptured runs the coder loop one-shot on task, capturing output.
func (cli *ChatCLI) RunCoderCaptured(ctx context.Context, task string) (string, error) {
	out, err := captureRPCStdout(func() error {
		return cli.RunCoderOnce(ctx, "/coder "+task)
	})
	if err != nil {
		return out, err
	}
	if out == "" {
		out = "(coder produced no textual output)"
	}
	return out, nil
}

// execBuiltin runs a plugin, capturing any streamed output into the result.
func execBuiltin(ctx context.Context, p plugins.Plugin, argv []string) (string, error) {
	var sb strings.Builder
	out, err := p.ExecuteWithStream(ctx, argv, func(s string) { sb.WriteString(s) })
	if out == "" {
		out = sb.String()
	}
	return out, err
}
