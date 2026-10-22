/*
 * ChatCLI - @lsp implementation / workspace_symbol / rename over the pool.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements the optional plugins.LSPWorkspaceNavigator and plugins.LSPRenamer
 * capabilities on the same lspToolAdapter: implementation and workspace
 * symbol search are listings like the base verbs; rename previews the
 * server's WorkspaceEdit by default and, on dry_run:false, applies it through
 * lsp.ApplyWorkspaceEdit — bounded to the session's project root, atomic per
 * file, refusing files that moved under it.
 */
package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/agent/lsp"
	"github.com/diillson/chatcli/cli/plugins"
)

const (
	// lspDefaultWorkspaceSymbolLimit / lspMaxWorkspaceSymbolLimit bound a
	// workspace search the same way reference listings are bounded.
	lspDefaultWorkspaceSymbolLimit = 50
	lspMaxWorkspaceSymbolLimit     = 200
	// lspMaxRenamePreviewEdits bounds the per-range preview of a rename so a
	// symbol with thousands of uses still renders a readable plan.
	lspMaxRenamePreviewEdits = 200

	lspMsgNoImplementation   = "No implementation found at that position — check that line/column point at an interface or method name (1-based)."
	lspMsgNoWorkspaceSymbols = "No workspace symbols match %q."
	lspMsgNoServerForSearch  = `No language server is running yet — pass "file" (any source file of the language to search, e.g. a .go file) so one can be started.`
	lspMsgRenameNoEdits      = "The language server produced no edits for that rename — check that line/column point at the symbol name (1-based) and that the symbol is renameable."
)

// Implementation implements plugins.LSPWorkspaceNavigator. line/column are 1-based.
func (a *lspToolAdapter) Implementation(file string, line, column int) (string, error) {
	sess, _, err := a.acquire(file)
	if err != nil {
		return "", err
	}
	locs, err := sess.Client.Implementation(sess.URI, lspPos(line, column))
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return lspMsgNoImplementation, nil
	}
	return formatLocations("implementation", sess.Root, locs, len(locs)), nil
}

// WorkspaceSymbol implements plugins.LSPWorkspaceNavigator. file is optional:
// it selects (and if needed spawns) the language server; without it any
// server already running in the pool answers.
func (a *lspToolAdapter) WorkspaceSymbol(file, query string, limit int) (string, error) {
	var sess *lsp.Session
	if strings.TrimSpace(file) != "" {
		s, _, err := a.acquire(file)
		if err != nil {
			return "", err
		}
		sess = s
	} else {
		s, ok := a.pool().AnyReady()
		if !ok {
			return "", fmt.Errorf("%s", lspMsgNoServerForSearch)
		}
		sess = s
	}
	if limit <= 0 {
		limit = lspDefaultWorkspaceSymbolLimit
	}
	if limit > lspMaxWorkspaceSymbolLimit {
		limit = lspMaxWorkspaceSymbolLimit
	}
	syms, err := sess.Client.WorkspaceSymbols(query)
	if err != nil {
		return "", err
	}
	if len(syms) == 0 {
		return fmt.Sprintf(lspMsgNoWorkspaceSymbols, query), nil
	}
	total := len(syms)
	if len(syms) > limit {
		syms = syms[:limit]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d workspace symbol(s) matching %q:\n", total, query)
	for _, s := range syms {
		container := ""
		if s.Container != "" {
			container = " — " + s.Container
		}
		fmt.Fprintf(&b, "- %s:%d %s %s%s\n", relPath(sess.Root, s.URI), s.Line+1, s.KindLabel(), s.Name, container)
	}
	if len(syms) < total {
		fmt.Fprintf(&b, "… %d more not shown (raise \"limit\" to see them).\n", total-len(syms))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Rename implements plugins.LSPRenamer. line/column are 1-based. dryRun
// lists every file and range the server would change; otherwise the edit
// set is applied to disk inside the session's project root.
func (a *lspToolAdapter) Rename(file string, line, column int, newName string, dryRun bool) (string, error) {
	sess, _, err := a.acquire(file)
	if err != nil {
		return "", err
	}
	edit, err := sess.Client.Rename(sess.URI, lspPos(line, column), newName)
	if err != nil {
		return "", err
	}
	if len(edit.Changes) == 0 && len(edit.Unsupported) == 0 {
		return lspMsgRenameNoEdits, nil
	}
	if dryRun {
		return renderRenamePreview(sess.Root, newName, edit), nil
	}
	touched, err := lsp.ApplyWorkspaceEdit(sess.Root, edit)
	if err != nil {
		if len(touched) > 0 {
			return "", fmt.Errorf("rename applied to %d file(s) (%s) before failing: %w", len(touched), strings.Join(touched, ", "), err)
		}
		return "", fmt.Errorf("rename not applied: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "renamed to %q — %d file(s) written:\n", newName, len(touched))
	for _, f := range touched {
		fmt.Fprintf(&b, "- %s (%d edit(s))\n", f, countEditsFor(sess.Root, edit, f))
	}
	b.WriteString("Run diagnostics on the touched files to confirm the build is still clean.")
	return b.String(), nil
}

// countEditsFor returns how many edits the set holds for the root-relative
// file (0 when the key cannot be matched back).
func countEditsFor(root string, edit lsp.WorkspaceEdit, rel string) int {
	for uri, edits := range edit.Changes {
		if relPath(root, uri) == rel {
			return len(edits)
		}
	}
	return 0
}

// renderRenamePreview lists every document and range a rename would touch,
// bounded and root-relative, with 1-based positions.
func renderRenamePreview(root, newName string, edit lsp.WorkspaceEdit) string {
	files, total := 0, 0
	for _, edits := range edit.Changes {
		files++
		total += len(edits)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "rename preview (dry_run): %d edit(s) in %d file(s) would rename to %q — nothing written.\n", total, files, newName)
	shown := 0
	for _, uri := range sortedEditURIs(edit) {
		fmt.Fprintf(&b, "%s (%d edit(s))\n", relPath(root, uri), len(edit.Changes[uri]))
		for _, e := range edit.Changes[uri] {
			if shown >= lspMaxRenamePreviewEdits {
				break
			}
			fmt.Fprintf(&b, "  - L%d:%d-L%d:%d -> %q\n", e.Range.Start.Line+1, e.Range.Start.Character+1, e.Range.End.Line+1, e.Range.End.Character+1, e.NewText)
			shown++
		}
	}
	if shown < total {
		fmt.Fprintf(&b, "… %d more edit(s) not shown.\n", total-shown)
	}
	if len(edit.Unsupported) > 0 {
		fmt.Fprintf(&b, "Unsupported file operations (apply would be refused): %s\n", strings.Join(edit.Unsupported, "; "))
	}
	b.WriteString(`Apply with the same call plus "dry_run": false.`)
	return b.String()
}

// sortedEditURIs returns the edit set's document URIs in stable order.
func sortedEditURIs(edit lsp.WorkspaceEdit) []string {
	uris := make([]string, 0, len(edit.Changes))
	for uri := range edit.Changes {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	return uris
}

// compile-time assertions for the optional capabilities.
var (
	_ plugins.LSPWorkspaceNavigator = (*lspToolAdapter)(nil)
	_ plugins.LSPRenamer            = (*lspToolAdapter)(nil)
)
