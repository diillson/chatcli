/*
 * ChatCLI - Adapter binding the @lsp tool to the session language-server pool.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.LSPAdapter over cli/agent/lsp: a lazily created,
 * session-scoped Pool keeps one initialized server per (project root,
 * language), and every subcommand formats its answer as bounded,
 * model-facing text with workspace-relative paths and 1-based positions.
 * Wired via plugins.SetLSPAdapter at startup; the pool is shut down with
 * the session.
 */
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/agent/lsp"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/utils"
)

const (
	// lspOpTimeout bounds one tool operation, including a possible server
	// spawn + initialize on a cold pool.
	lspOpTimeout = 45 * time.Second

	// lspDiagnosticsWait bounds how long a diagnostics call waits for the
	// server's publish after (re)opening a document.
	lspDiagnosticsWait = 12 * time.Second

	// lspDefaultRefLimit / lspMaxRefLimit bound reference listings so one
	// popular symbol cannot flood the conversation.
	lspDefaultRefLimit = 50
	lspMaxRefLimit     = 200

	// lspMaxHoverBytes bounds hover payloads (some servers return whole doc
	// pages). Cut on a rune boundary — hover content is arbitrary UTF-8.
	lspMaxHoverBytes = 2000

	// lspMaxSymbols bounds an outline listing.
	lspMaxSymbols = 200
)

// lspToolAdapter is the concrete plugins.LSPAdapter.
type lspToolAdapter struct {
	cli *ChatCLI
}

// pool returns the session pool, creating it on first use.
func (a *lspToolAdapter) pool() *lsp.Pool {
	a.cli.lspPoolOnce.Do(func() {
		a.cli.lspPool = lsp.NewPool(a.cli.logger)
	})
	return a.cli.lspPool
}

// acquire resolves the file path (expansion + absolutization) and returns a
// ready session plus the absolute path.
func (a *lspToolAdapter) acquire(file string) (*lsp.Session, string, error) {
	expanded, err := utils.ExpandPath(file)
	if err != nil {
		expanded = file
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return nil, "", err
	}
	// @lsp calls have no caller deadline of their own; bound the possible
	// cold spawn + initialize here (same pattern as the @knowledge adapter).
	ctx, cancel := context.WithTimeout(context.Background(), lspOpTimeout)
	defer cancel()
	sess, err := a.pool().Acquire(ctx, abs)
	if err != nil {
		return nil, "", err
	}
	return sess, abs, nil
}

// relPath renders a location path relative to the session root when possible.
func relPath(root, uri string) string {
	p := strings.TrimPrefix(uri, "file://")
	if root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return p
}

// Diagnostics implements plugins.LSPAdapter.
func (a *lspToolAdapter) Diagnostics(file string) (string, error) {
	sess, abs, err := a.acquire(file)
	if err != nil {
		return "", err
	}
	diags, ok := sess.Client.Diagnostics(sess.URI, lspDiagnosticsWait)
	if !ok {
		return fmt.Sprintf("%s: the language server produced no diagnostics report within %s — treat as inconclusive, not clean.",
			relPath(sess.Root, "file://"+abs), lspDiagnosticsWait), nil
	}
	if len(diags) == 0 {
		return fmt.Sprintf("%s: no diagnostics — the file is clean.", relPath(sess.Root, "file://"+abs)), nil
	}
	return renderDiagnosticsList(sess.Root, abs, diags), nil
}

// renderDiagnosticsList formats a non-empty diagnostics slice as bounded,
// model-facing text. Shared by Diagnostics and QuickDiagnostics.
func renderDiagnosticsList(root, abs string, diags []lsp.Diagnostic) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d diagnostic(s) in %s:\n", len(diags), relPath(root, "file://"+abs))
	for _, d := range diags {
		src := d.Source
		if src != "" {
			src = " (" + src + ")"
		}
		fmt.Fprintf(&b, "- L%d:%d [%s] %s%s\n",
			d.Range.Start.Line+1, d.Range.Start.Character+1, d.SeverityLabel(), d.Message, src)
	}
	return strings.TrimRight(b.String(), "\n")
}

// QuickDiagnostics implements plugins.LSPQuickDiagnoser: same findings as
// Diagnostics, plus an explicit hasIssues flag so the post-edit auto-check
// can stay silent on clean (or inconclusive) files instead of spending
// tokens announcing cleanliness.
func (a *lspToolAdapter) QuickDiagnostics(file string) (string, bool, error) {
	sess, abs, err := a.acquire(file)
	if err != nil {
		return "", false, err
	}
	diags, ok := sess.Client.Diagnostics(sess.URI, lspDiagnosticsWait)
	if !ok || len(diags) == 0 {
		return "", false, nil
	}
	return renderDiagnosticsList(sess.Root, abs, diags), true, nil
}

// Definition implements plugins.LSPAdapter. line/column are 1-based.
func (a *lspToolAdapter) Definition(file string, line, column int) (string, error) {
	sess, _, err := a.acquire(file)
	if err != nil {
		return "", err
	}
	locs, err := sess.Client.Definition(sess.URI, lspPos(line, column))
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "No definition found at that position — check that line/column point at the symbol name (1-based).", nil
	}
	return formatLocations("definition", sess.Root, locs, len(locs)), nil
}

// References implements plugins.LSPAdapter. line/column are 1-based.
func (a *lspToolAdapter) References(file string, line, column int, includeDeclaration bool, limit int) (string, error) {
	sess, _, err := a.acquire(file)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = lspDefaultRefLimit
	}
	if limit > lspMaxRefLimit {
		limit = lspMaxRefLimit
	}
	locs, err := sess.Client.References(sess.URI, lspPos(line, column), includeDeclaration)
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "No references found at that position — check that line/column point at the symbol name (1-based).", nil
	}
	total := len(locs)
	if len(locs) > limit {
		locs = locs[:limit]
	}
	return formatLocations("reference", sess.Root, locs, total), nil
}

// Symbols implements plugins.LSPAdapter.
func (a *lspToolAdapter) Symbols(file string) (string, error) {
	sess, abs, err := a.acquire(file)
	if err != nil {
		return "", err
	}
	syms, err := sess.Client.DocumentSymbols(sess.URI)
	if err != nil {
		return "", err
	}
	if len(syms) == 0 {
		return fmt.Sprintf("%s: the language server reported no symbols.", relPath(sess.Root, "file://"+abs)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d symbol(s) in %s:\n", len(syms), relPath(sess.Root, "file://"+abs))
	for i, s := range syms {
		if i >= lspMaxSymbols {
			fmt.Fprintf(&b, "… and %d more.\n", len(syms)-i)
			break
		}
		container := ""
		if s.Container != "" {
			container = " — " + s.Container
		}
		fmt.Fprintf(&b, "- L%d %s %s%s\n", s.Line+1, s.KindLabel(), s.Name, container)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Hover implements plugins.LSPAdapter. line/column are 1-based.
func (a *lspToolAdapter) Hover(file string, line, column int) (string, error) {
	sess, _, err := a.acquire(file)
	if err != nil {
		return "", err
	}
	text, err := sess.Client.Hover(sess.URI, lspPos(line, column))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "No hover information at that position — check that line/column point at the symbol name (1-based).", nil
	}
	return truncateHoverRuneSafe(text, lspMaxHoverBytes), nil
}

// truncateHoverRuneSafe bounds hover payloads on a rune boundary — servers
// sometimes return whole documentation pages, and hover content is arbitrary
// UTF-8.
func truncateHoverRuneSafe(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "\n… (hover truncated)"
}

// lspPos converts 1-based tool positions to the 0-based LSP wire format.
func lspPos(line, column int) lsp.Position {
	return lsp.Position{Line: line - 1, Character: column - 1}
}

// formatLocations renders a bounded location list with 1-based positions and
// root-relative paths, flagging any elision explicitly.
func formatLocations(noun, root string, locs []lsp.Location, total int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s(s):\n", total, noun)
	for _, l := range locs {
		fmt.Fprintf(&b, "- %s:%d:%d\n", relPath(root, l.URI), l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	if len(locs) < total {
		fmt.Fprintf(&b, "… %d more not shown (raise \"limit\" to see them).\n", total-len(locs))
	}
	return strings.TrimRight(b.String(), "\n")
}

// compile-time assertion that the adapter satisfies the plugin interface.
var _ plugins.LSPAdapter = (*lspToolAdapter)(nil)

// shutdownLSPPool tears down the session language-server pool, if one was
// ever created. Called from the session cleanup path.
func (cli *ChatCLI) shutdownLSPPool() {
	if cli.lspPool != nil {
		cli.lspPool.Close()
	}
}
