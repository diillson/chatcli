/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * tool.go
 *
 * `chatcli tool` — run one built-in tool directly and print its output, with
 * no LLM turn at all (a plain `-p "@tool ..."` executes the tool but then
 * feeds the result to the model). Works keyless: like the MCP server's
 * direct-tool surface, no provider is required. The catalog and the exposure
 * policy (CHATCLI_MCP_TOOLS) are exactly the RPC tool surface.
 */
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/manager"
	"go.uber.org/zap"
)

// toolInvocation is the parsed form of the subcommand's arguments.
type toolInvocation struct {
	List bool
	Name string
	// JSON carries the raw envelope when the single argument is a JSON
	// object; Argv carries the arguments verbatim otherwise. Argv is NEVER
	// joined and re-split — values with whitespace must survive intact.
	JSON string
	Argv []string
}

// parseToolInvocation maps `chatcli tool [name [args…]]` to an invocation.
func parseToolInvocation(args []string) toolInvocation {
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		return toolInvocation{List: true}
	}
	inv := toolInvocation{Name: args[0]}
	rest := args[1:]
	if len(rest) == 1 && strings.HasPrefix(strings.TrimSpace(rest[0]), "{") {
		inv.JSON = rest[0]
		return inv
	}
	if len(rest) > 0 {
		inv.Argv = append([]string(nil), rest...)
	}
	return inv
}

// toolRunner is the slice of ChatCLI the subcommand needs; an interface so
// the dispatch/rendering logic is testable without a full boot.
type toolRunner interface {
	ListAllRPCTools() []cli.RPCToolInfo
	RunAnyRPCTool(ctx context.Context, name, args string) (string, error)
	RunAnyRPCToolArgv(ctx context.Context, name string, argv []string) (string, error)
}

// RunTool executes the `tool` subcommand: list the policy-admitted tool
// catalog or run one tool and print its output.
func RunTool(ctx context.Context, args []string, mgr manager.LLMManager, logger *zap.Logger) error {
	chatCLI, err := cli.NewChatCLI(ctx, mgr, logger)
	if err != nil {
		return fmt.Errorf("chatcli init failed: %w", err)
	}
	chatCLI.SetAuditSurface("tool")
	// One-shot, non-interactive: nothing may block reading stdin.
	chatCLI.SetUnattended(true)
	return runToolWith(ctx, chatCLI, args, os.Stdout)
}

// runToolWith is the boot-free body of RunTool.
func runToolWith(ctx context.Context, r toolRunner, args []string, w io.Writer) error {
	inv := parseToolInvocation(args)
	if inv.List {
		printToolCatalog(w, r.ListAllRPCTools())
		return nil
	}

	var out string
	var err error
	if inv.JSON != "" {
		out, err = r.RunAnyRPCTool(ctx, inv.Name, inv.JSON)
	} else {
		out, err = r.RunAnyRPCToolArgv(ctx, inv.Name, inv.Argv)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(w, out)
	return nil
}

// printToolCatalog lists every tool the exposure policy admits.
func printToolCatalog(w io.Writer, tools []cli.RPCToolInfo) {
	if len(tools) == 0 {
		fmt.Fprintln(w, i18n.T("tool.none"))
		return
	}
	fmt.Fprintln(w, i18n.T("tool.list_header"))
	for _, t := range tools {
		tag := ""
		if t.ReadOnly {
			tag = " [read-only]"
		}
		desc := strings.SplitN(t.Description, "\n", 2)[0]
		if r := []rune(desc); len(r) > 100 {
			desc = string(r[:100]) + "…"
		}
		fmt.Fprintf(w, "  @%s%s — %s\n", t.Name, tag, desc)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, i18n.T("tool.usage"))
}
