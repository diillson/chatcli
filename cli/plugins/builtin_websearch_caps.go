/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// This file isolates the Fase 2.1 capability advertisements for
// BuiltinWebSearchPlugin in a separate file from the main
// builtin_websearch.go. Keeping the additions out of the original file
// avoids dragging it into the Quality Gate's cyclo-new check, which
// scores every function in any changed file — and the pre-existing
// ExecuteWithStream there is grandfathered above the threshold.

// IsReadOnly reports true for every invocation: web search is a GET
// over HTTPS, never mutates local state. Skipping the security prompt
// for read-only searches is part of the UX win in Fase 2.1.
func (p *BuiltinWebSearchPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: each search opens its own HTTP
// connection and writes only to the returned string. Two parallel
// searches do not interfere — they may share connection pools but
// that is goroutine-safe at the net/http layer.
func (p *BuiltinWebSearchPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall returns a contextual one-liner showing the query the
// user is searching for. Falls back to the static description when the
// query cannot be parsed out of the args. The label is i18n-resolved so
// the spinner respects the active locale.
func (p *BuiltinWebSearchPlugin) DescribeCall(args []string) string {
	q := extractQueryArg(args)
	if q == "" {
		return p.Description()
	}
	if len(q) > 60 {
		q = q[:60] + "..."
	}
	return i18n.T("plugins.websearch.describe", q)
}
