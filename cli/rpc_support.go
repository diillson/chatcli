/*
 * ChatCLI - rpc_support.go
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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
)

var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// captureRPCStdout runs fn with os.Stdout redirected to a buffer and returns the
// captured (ANSI-stripped) output. The pipe is always restored.
func captureRPCStdout(fn func() error) (string, error) {
	orig := os.Stdout
	r, w, perr := os.Pipe()
	if perr != nil {
		return "", fn() // fall back to running without capture
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	runErr := fn()

	_ = w.Close()
	os.Stdout = orig
	<-done
	_ = r.Close()

	return strings.TrimSpace(ansiSeq.ReplaceAllString(buf.String(), "")), runErr
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

// BuiltinTool describes a built-in tool exposed over MCP.
type BuiltinTool struct {
	Name        string
	Description string
}

// rpcExposedTools is the curated set of built-in tools surfaced over MCP.
// These are read-only/safe and return their result as a string.
var rpcExposedTools = []string{"@read", "@search", "@tree", "@websearch", "@webfetch"}

// ListBuiltinTools returns the curated built-in tools available over MCP.
func (cli *ChatCLI) ListBuiltinTools() []BuiltinTool {
	if cli.pluginManager == nil {
		return nil
	}
	var out []BuiltinTool
	for _, name := range rpcExposedTools {
		if p, ok := cli.pluginManager.GetPlugin(name); ok {
			out = append(out, BuiltinTool{Name: strings.TrimPrefix(name, "@"), Description: p.Description()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RunBuiltinTool invokes a curated built-in tool by name (without the '@')
// with a raw argument string, returning its output. Only tools in
// rpcExposedTools are allowed.
func (cli *ChatCLI) RunBuiltinTool(ctx context.Context, name, args string) (string, error) {
	if cli.pluginManager == nil {
		return "", fmt.Errorf("plugins not available")
	}
	full := "@" + strings.TrimPrefix(name, "@")
	allowed := false
	for _, t := range rpcExposedTools {
		if t == full {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("tool %q is not exposed over MCP", name)
	}
	p, ok := cli.pluginManager.GetPlugin(full)
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	var argv []string
	if strings.TrimSpace(args) != "" {
		argv = []string{args}
	}
	return execBuiltin(ctx, p, argv)
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
