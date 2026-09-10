/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
)

// handleStorageCommand implements /storage:
//
//	/storage                      inventory: size, policy, what would go now
//	/storage prune [store]        dry run: list what the rules would remove
//	/storage prune [store] --apply  remove it
//
// Simulation is the default on purpose — the command exists so a user can
// see the curation before trusting it.
func (cli *ChatCLI) handleStorageCommand(ctx context.Context, args string) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		cli.runStorage(ctx, "", false, false)
		return
	}
	switch fields[0] {
	case "prune":
		only, apply, ok := parseStoragePruneArgs(fields[1:])
		if !ok {
			fmt.Println(colorize("  "+i18n.T("storage.usage"), ColorYellow))
			return
		}
		cli.runStorage(ctx, only, true, apply)
	case "help", "-h", "--help":
		fmt.Println(colorize("  "+i18n.T("storage.usage"), ColorGray))
	default:
		// A bare store name is the inventory of that store.
		if IsStorageStore(fields[0]) {
			cli.runStorage(ctx, fields[0], false, false)
			return
		}
		fmt.Println(colorize("  "+i18n.T("storage.unknown_store", fields[0], strings.Join(StorageStoreNames(), ", ")), ColorYellow))
	}
}

// parseStoragePruneArgs accepts "[store] [--apply|--dry-run]" in any order
// and reports ok=false on anything else.
func parseStoragePruneArgs(fields []string) (only string, apply, ok bool) {
	for _, f := range fields {
		switch strings.ToLower(f) {
		case "--apply", "apply", "--yes", "-y":
			apply = true
		case "--dry-run", "dry-run", "--dry", "-n":
			apply = false
		default:
			if only != "" || !IsStorageStore(f) {
				return "", false, false
			}
			only = f
		}
	}
	return only, apply, true
}

// storageOptions builds the run options from the live session: its state
// root (tenant-aware), the session TTL, the active task-graph run.
func (cli *ChatCLI) storageOptions(only string, prune, apply bool) StorageOptions {
	opts := StorageOptions{TTL: sessionTTLDuration(), Only: only, Apply: apply, Burst: prune}
	if cli != nil {
		opts.Root = cli.stateRoot
		opts.Logger = cli.logger
		if cli.taskGraphAdapter != nil {
			opts.SkipTaskGraphRun = cli.taskGraphAdapter.activeRunID()
		}
	}
	return opts
}

func (cli *ChatCLI) runStorage(ctx context.Context, only string, prune, apply bool) {
	res, err := RunStorage(ctx, cli.storageOptions(only, prune, apply))
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("storage.error", err.Error()), ColorRed))
		return
	}
	RenderStorage(os.Stdout, res, prune)
}

// RenderStorage prints the inventory (or the dry run / apply outcome when
// prune is set) as an aligned table, one store per line. Shared by the
// REPL command and the `chatcli storage` subcommand.
func RenderStorage(w io.Writer, res StorageResult, prune bool) {
	p := uiPrefix(ColorCyan)
	title := i18n.T("storage.title")
	if prune && res.Applied {
		title = i18n.T("storage.title_applied")
	} else if prune {
		title = i18n.T("storage.title_dry_run")
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, uiBox("🗄", title, ColorCyan))
	fmt.Fprintln(w, p+"  "+colorize(res.Root, ColorGray))

	hStore, hFiles, hSize, hPolicy, hNow := i18n.T("storage.col.store"), i18n.T("storage.col.files"), i18n.T("storage.col.size"), i18n.T("storage.col.policy"), i18n.T("storage.col.prunable")
	if prune && res.Applied {
		hNow = i18n.T("storage.col.removed")
	}
	wStore, wFiles, wSize, wPolicy := kit.VisibleLen(hStore), kit.VisibleLen(hFiles), kit.VisibleLen(hSize), kit.VisibleLen(hPolicy)
	type line struct{ store, files, size, policy, now string }
	lines := make([]line, 0, len(res.Stores))
	for _, st := range res.Stores {
		l := line{
			store:  st.Name,
			files:  fmt.Sprintf("%d", st.Files),
			size:   bytesLabel(st.Bytes),
			policy: i18n.T("storage.policy." + st.Policy),
			now:    storageNowCell(st, prune, res.Applied),
		}
		lines = append(lines, l)
		wStore, wFiles, wSize, wPolicy = maxInt(wStore, kit.VisibleLen(l.store)), maxInt(wFiles, kit.VisibleLen(l.files)), maxInt(wSize, kit.VisibleLen(l.size)), maxInt(wPolicy, kit.VisibleLen(l.policy))
	}
	fmt.Fprintln(w, p)
	fmt.Fprintln(w, p+"  "+colorize(kit.PadRight(hStore, wStore)+"  "+padLeft(hFiles, wFiles)+"  "+padLeft(hSize, wSize)+"  "+kit.PadRight(hPolicy, wPolicy)+"  "+hNow, ColorBold))
	for _, l := range lines {
		fmt.Fprintln(w, p+"  "+kit.PadRight(l.store, wStore)+"  "+padLeft(l.files, wFiles)+"  "+padLeft(l.size, wSize)+"  "+colorize(kit.PadRight(l.policy, wPolicy), ColorGray)+"  "+l.now)
	}
	fmt.Fprintln(w, p)
	fmt.Fprintln(w, p+"  "+i18n.T("storage.total", bytesLabel(res.Bytes)))
	switch {
	case prune && res.Applied:
		fmt.Fprintln(w, p+"  "+colorize(i18n.T("storage.applied", res.Removed, bytesLabel(res.BytesFreed)), ColorGreen))
	case res.Prunable > 0:
		fmt.Fprintln(w, p+"  "+colorize(i18n.T("storage.hint_apply", res.Prunable), ColorYellow))
	default:
		fmt.Fprintln(w, p+"  "+colorize(i18n.T("storage.nothing"), ColorGray))
	}
	fmt.Fprintln(w)
}

// storageNowCell renders the last column: what would go (with reasons),
// what went, or why nothing can.
func storageNowCell(st StorageStore, prune, applied bool) string {
	switch {
	case st.Protected:
		return colorize(i18n.T("storage.protected"), ColorGray)
	case applied:
		if st.Removed == 0 {
			return colorize("0", ColorGray)
		}
		return colorize(fmt.Sprintf("%d (%s)", st.Removed, bytesLabel(st.BytesFreed)), ColorGreen)
	case st.OnApplyOnly:
		return colorize(i18n.T("storage.on_apply"), ColorGray)
	case st.Prunable == 0:
		return colorize("0", ColorGray)
	}
	parts := make([]string, 0, len(st.Reasons))
	for _, r := range sortedReasons(st.Reasons) {
		parts = append(parts, fmt.Sprintf("%d %s", st.Reasons[r], i18n.T("storage.reason."+r)))
	}
	cell := fmt.Sprintf("%d (%s)", st.Prunable, bytesLabel(st.PrunableBytes))
	if len(parts) > 0 {
		cell += " — " + strings.Join(parts, ", ")
	}
	if prune {
		return colorize(cell, ColorYellow)
	}
	return cell
}

// padLeft right-aligns s in cols visible columns.
func padLeft(s string, cols int) string {
	if n := cols - kit.VisibleLen(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// bytesLabel renders a byte count; sizes are never negative, but the
// conversion below is what the static scanner watches, so clamp first.
func bytesLabel(n int64) string {
	if n < 0 {
		n = 0
	}
	return formatBytes(uint64(n)) // #nosec G115 -- clamped to >= 0 above
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// getStorageSuggestions completes the /storage subcommands, store names
// and flags — only what the command still accepts at the cursor: one
// store at most, each flag once, and after prune only the stores that
// have a pruning rule (the protected ones are inventory-only).
func (cli *ChatCLI) getStorageSuggestions(d prompt.Document) []prompt.Suggest {
	line := d.TextBeforeCursor()
	args := strings.Fields(line)
	word := d.GetWordBeforeCursor()
	typing := !strings.HasSuffix(line, " ") // the last field is still being typed
	stores := func(prunableOnly bool) []prompt.Suggest {
		out := make([]prompt.Suggest, 0, len(StorageStoreNames()))
		for _, n := range StorageStoreNames() {
			policy := storePolicyName(n)
			if prunableOnly && policy == PolicyReadOnly {
				continue
			}
			out = append(out, prompt.Suggest{Text: n, Description: i18n.T("storage.policy." + policy)})
		}
		return out
	}
	switch {
	case len(args) == 1 || (len(args) == 2 && typing):
		return prompt.FilterHasPrefix(append([]prompt.Suggest{
			{Text: "prune", Description: i18n.T("complete.storage.prune")},
		}, stores(false)...), word, true)
	case args[1] != "prune":
		return nil // a bare store name takes nothing after it
	}
	// After prune: what is already on the line is not offered again.
	settled := args[2:]
	if typing && len(settled) > 0 {
		settled = settled[:len(settled)-1]
	}
	hasStore, hasMode := false, false
	for _, a := range settled {
		switch strings.ToLower(a) {
		case "--apply", "apply", "--yes", "-y", "--dry-run", "dry-run", "--dry", "-n":
			hasMode = true
		default:
			hasStore = true
		}
	}
	var out []prompt.Suggest
	if !hasStore {
		out = append(out, stores(true)...)
	}
	if !hasMode {
		out = append(out,
			prompt.Suggest{Text: "--apply", Description: i18n.T("complete.storage.apply")},
			prompt.Suggest{Text: "--dry-run", Description: i18n.T("complete.storage.dry_run")},
		)
	}
	return prompt.FilterHasPrefix(out, word, true)
}

// storePolicyName is the policy a store is governed by, for completions.
func storePolicyName(name string) string {
	return inventoryStore(StorageOptions{Root: string(os.PathSeparator)}, name).Policy
}
