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
		return false
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

// IsConcurrencySafe mirrors IsReadOnly: provider calls are independent;
// a call that writes a vectors file stays serial, like every other write.
func (p *BuiltinEmbedPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

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
