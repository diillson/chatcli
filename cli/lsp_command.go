/*
 * ChatCLI - lsp_command.go
 *
 * /lsp <file> runs the matching language server (gopls, pyright, ...) against
 * a file and prints its diagnostics — giving the user (and, via the agent,
 * the model) real compiler/linter feedback without a full build.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/lsp"
	"github.com/diillson/chatcli/i18n"
)

func (cli *ChatCLI) handleLSPCommand(input string) {
	arg := strings.TrimSpace(strings.TrimPrefix(input, "/lsp"))
	if arg == "" {
		fmt.Println(colorize("  "+i18n.T("lsp.usage"), ColorGray))
		return
	}

	abs, err := filepath.Abs(arg)
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	data, err := os.ReadFile(abs) //#nosec G304 -- user-specified file to diagnose
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}

	spec, ok := lsp.ServerForFile(abs)
	if !ok {
		fmt.Println(colorize("  "+i18n.T("lsp.unsupported", filepath.Ext(abs)), ColorYellow))
		return
	}

	fmt.Printf("  %s\n", colorize(i18n.T("lsp.starting", spec.Command[0]), ColorCyan))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := lsp.Spawn(ctx, spec, cli.logger)
	if err != nil {
		fmt.Printf("  %s %s\n", colorize("ERR", ColorRed), i18n.T("lsp.spawn_failed", spec.Command[0], err))
		return
	}
	defer client.Shutdown()

	rootURI := "file://" + filepath.Dir(abs)
	if err := client.Initialize(rootURI); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}

	uri := "file://" + abs
	if err := client.DidOpen(uri, spec.LanguageID, string(data)); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}

	diags, ok := client.Diagnostics(uri, 12*time.Second)
	if !ok {
		fmt.Println(colorize("  "+i18n.T("lsp.no_response"), ColorGray))
		return
	}

	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("lsp.header", filepath.Base(abs)), ColorCyan+ColorBold))
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))
	if len(diags) == 0 {
		fmt.Println(colorize("  "+i18n.T("lsp.clean"), ColorGreen))
		fmt.Println()
		return
	}
	for _, d := range diags {
		sevColor := ColorGray
		switch d.Severity {
		case 1:
			sevColor = ColorRed
		case 2:
			sevColor = ColorYellow
		}
		loc := fmt.Sprintf("%d:%d", d.Range.Start.Line+1, d.Range.Start.Character+1)
		src := d.Source
		if src != "" {
			src = " (" + src + ")"
		}
		fmt.Printf("  %s %s %s%s\n",
			colorize(loc, ColorCyan),
			colorize("["+d.SeverityLabel()+"]", sevColor),
			d.Message, colorize(src, ColorGray))
	}
	fmt.Println()
}
