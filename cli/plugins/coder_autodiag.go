/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * coder_autodiag.go — post-edit diagnostics for @coder.
 *
 * Every mutating @coder subcommand (write, patch, multipatch) leaves a window
 * where the model believes the edit is fine but the file no longer compiles —
 * the error only surfaces turns later, when a test or exec fails. This hook
 * closes that window: right after a successful mutation, the touched files
 * are checked through the same session-scoped LSP pool the @lsp tool uses,
 * and any findings are appended to the tool result as a compact
 * [DIAGNOSTICS] block the model sees IMMEDIATELY.
 *
 * Degrades to a no-op when: the env kill switch is set, no LSP adapter is
 * wired (one-shot surfaces without a pool), the adapter lacks the quick
 * capability, the language has no server, or the session pool is still cold
 * (the adapter then warms it in the background and the NEXT edit gets real
 * findings). A clean file appends nothing — silence means clean, keeping
 * the happy path at zero token cost. The whole pass also runs under a
 * wall-clock budget: this hook sits between the edit and its tool result,
 * so a slow language server must degrade the check, never the edit.
 */
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// coderAutoDiagEnv is the kill switch for post-edit diagnostics. Unset or
// any value other than the off-forms keeps the feature on — the block is
// advisory, capped, and free when the file is clean.
const coderAutoDiagEnv = "CHATCLI_CODER_AUTODIAG"

// coderAutoDiagEnabled honors CHATCLI_CODER_AUTODIAG=0|false|off|no.
func coderAutoDiagEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(coderAutoDiagEnv))) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// LSPQuickDiagnoser is an OPTIONAL capability of the wired LSPAdapter:
// diagnostics that report whether anything was actually found, so callers
// can stay silent on clean files. A separate interface (not a new LSPAdapter
// method) because LSPAdapter is exported API — extending it would break
// existing implementers.
type LSPQuickDiagnoser interface {
	// QuickDiagnostics returns the rendered findings for file and whether
	// any finding exists. hasIssues=false means clean or inconclusive —
	// either way there is nothing worth injecting.
	QuickDiagnostics(file string) (text string, hasIssues bool, err error)
}

const (
	// maxAutoDiagFiles bounds how many touched files are checked per call —
	// a runaway multipatch must not turn one edit into a diagnostics storm.
	maxAutoDiagFiles = 5
	// maxAutoDiagBytes caps the appended block so a pathological file (a
	// thousand parse errors) cannot flood the tool result.
	maxAutoDiagBytes = 3000
)

// autoDiagPassBudget caps the WHOLE post-edit pass in wall-clock time. Each
// file's check already has a per-file publish wait in the adapter; this is
// the defense in depth for a multipatch over several slow files. A var, not
// a const, purely as a test seam.
var autoDiagPassBudget = 8 * time.Second

// mutatingCoderSubcommands are the @coder subcommands whose success means
// file content changed on disk.
var mutatingCoderSubcommands = map[string]bool{
	"write":      true,
	"patch":      true,
	"multipatch": true,
}

// autoDiagTargets extracts the file paths a mutating subcommand touched from
// its argv: the --file flag for write/patch, the edits JSON for multipatch.
// Lenient by design — a shape it does not recognize yields no targets, never
// an error (the edit itself already succeeded).
func autoDiagTargets(subcmd string, args []string) []string {
	switch subcmd {
	case "write", "patch":
		if f := flagValue(args, "--file"); f != "" {
			return []string{f}
		}
	case "multipatch":
		raw := flagValue(args, "--edits")
		if raw == "" {
			return nil
		}
		var edits []struct {
			File string `json:"file"`
		}
		if err := json.Unmarshal([]byte(raw), &edits); err != nil {
			return nil
		}
		seen := make(map[string]bool, len(edits))
		out := make([]string, 0, len(edits))
		for _, e := range edits {
			f := strings.TrimSpace(e.File)
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
		return out
	}
	return nil
}

// flagValue returns the value of a --flag in argv, supporting both the
// two-token form (--flag value) and the inline form (--flag=value).
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return strings.TrimSpace(args[i+1])
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// appendAutoDiagnostics runs post-edit diagnostics for a successful mutating
// subcommand and returns output with the findings block appended. Model-facing
// English, like every other injected block.
func appendAutoDiagnostics(subcmd string, args []string, output string) string {
	if !mutatingCoderSubcommands[subcmd] || !coderAutoDiagEnabled() {
		return output
	}
	qd, ok := currentLSPAdapter().(LSPQuickDiagnoser)
	if !ok {
		return output
	}
	targets := autoDiagTargets(subcmd, args)
	if len(targets) == 0 {
		return output
	}
	if len(targets) > maxAutoDiagFiles {
		targets = targets[:maxAutoDiagFiles]
	}

	var b strings.Builder
	started := time.Now()
	for i, file := range targets {
		if time.Since(started) > autoDiagPassBudget {
			b.WriteString(fmt.Sprintf("- %d file(s) not checked (diagnostics time budget) — run @lsp diagnostics on them if needed.\n", len(targets)-i))
			break
		}
		text, hasIssues, err := qd.QuickDiagnostics(file)
		if err != nil || !hasIssues || strings.TrimSpace(text) == "" {
			continue
		}
		if b.Len()+len(text) > maxAutoDiagBytes {
			b.WriteString(fmt.Sprintf("- %s: findings omitted (diagnostics budget) — run @lsp diagnostics on it.\n", file))
			continue
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return output
	}
	return output + "\n[DIAGNOSTICS] The edit succeeded but the language server reports issues in the touched file(s). " +
		"Fix them before moving on:\n" + strings.TrimRight(b.String(), "\n")
}
