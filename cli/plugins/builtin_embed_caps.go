/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"encoding/json"

	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinEmbedPlugin.

// IsReadOnly is true except for "text" with an "out" path, which writes
// a vectors file to disk.
func (p *BuiltinEmbedPlugin) IsReadOnly(args []string) bool {
	cmd, inner, err := parseEmbedInvocation(args)
	if err != nil {
		return true
	}
	if cmd != "text" {
		return true
	}
	var in struct {
		Out string `json:"out"`
	}
	_ = json.Unmarshal([]byte(inner), &in)
	return in.Out == ""
}

// IsConcurrencySafe is true: @embed only makes provider HTTP calls (and
// at most writes a caller-named file), so parallel tool fan-out is fine.
func (p *BuiltinEmbedPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall gives the spinner a concise, contextual one-liner.
func (p *BuiltinEmbedPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseEmbedInvocation(args)
	if err != nil {
		return i18n.T("plugins.embed.describe.default")
	}
	switch cmd {
	case "status":
		return i18n.T("plugins.embed.describe.status")
	case "similarity":
		return i18n.T("plugins.embed.describe.similarity")
	case "rank":
		return i18n.T("plugins.embed.describe.rank", describeTrunc(describeStr(inner, "query"), 50))
	}
	return i18n.T("plugins.embed.describe.text")
}
