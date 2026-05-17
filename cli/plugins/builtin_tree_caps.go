/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "github.com/diillson/chatcli/i18n"

// Capability advertisements for BuiltinTreePlugin.

// IsReadOnly reports true: directory listing never mutates the tree.
func (p *BuiltinTreePlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: multiple tree walks don't conflict.
func (p *BuiltinTreePlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the directory being listed.
func (p *BuiltinTreePlugin) DescribeCall(args []string) string {
	parsed, err := parseTreeArgs(args)
	if err != nil {
		return p.Description()
	}
	dir := parsed.Dir
	if dir == "" {
		dir = "."
	}
	return i18n.T("plugins.tree.describe", dir)
}
