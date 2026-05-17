/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "github.com/diillson/chatcli/i18n"

// Capability advertisements for BuiltinReadPlugin. Kept in a separate
// file so future cyclo-new scans don't sweep up unrelated helpers
// alongside any future expansion of the executor.

// IsReadOnly reports true for every invocation: @read never mutates
// the file system. The orchestrator uses this to skip the security
// prompt and to participate in concurrent batches.
func (p *BuiltinReadPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: each @read opens its own file
// descriptor and streams to its own output buffer. Two reads of the
// same file don't conflict; two reads of different files don't either.
func (p *BuiltinReadPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the file being read so the spinner reads
// "Reading: main.go" instead of the static description. Falls back to
// Description() when the file argument is missing.
func (p *BuiltinReadPlugin) DescribeCall(args []string) string {
	parsed, err := parseReadArgs(args)
	if err != nil || parsed.File == "" {
		return p.Description()
	}
	return i18n.T("plugins.read.describe", parsed.File)
}
