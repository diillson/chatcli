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

// Capability advertisements for BuiltinToolsPlugin.

// IsReadOnly reports true: @tools only reads the plugin registry.
func (p *BuiltinToolsPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: the registry is read-only at query time.
func (p *BuiltinToolsPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces which tool is being looked up in the spinner.
func (p *BuiltinToolsPlugin) DescribeCall(args []string) string {
	cmd, inner, err := parseToolsInvocation(args)
	if err != nil {
		return i18n.T("plugins.tools.describe_generic")
	}
	if cmd == "list" {
		return i18n.T("plugins.tools.describe_list")
	}
	var in struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(inner), &in)
	if in.Name == "" {
		return i18n.T("plugins.tools.describe_generic")
	}
	return i18n.T("plugins.tools.describe_describe", describeTrim(in.Name))
}
