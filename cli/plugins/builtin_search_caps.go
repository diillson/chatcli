/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "github.com/diillson/chatcli/i18n"

// Capability advertisements for BuiltinSearchPlugin.

// IsReadOnly reports true: @search only reads files, never modifies them.
func (p *BuiltinSearchPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: multiple searches don't conflict.
// File system reads at the syscall layer are safe to interleave.
func (p *BuiltinSearchPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the regex pattern being searched.
func (p *BuiltinSearchPlugin) DescribeCall(args []string) string {
	parsed, err := parseSearchArgs(args)
	if err != nil || parsed.Term == "" {
		return p.Description()
	}
	t := parsed.Term
	if len(t) > 60 {
		t = t[:60] + "..."
	}
	return i18n.T("plugins.search.describe", t)
}
