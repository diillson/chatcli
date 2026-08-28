/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinViewPlugin.

// IsReadOnly reports true: @view only reads a local file and stages it into
// the conversation — no file, process or remote state changes.
func (p *BuiltinViewPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: staging is mutex-guarded on the agent side
// and loads are independent.
func (p *BuiltinViewPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces which image is being viewed.
func (p *BuiltinViewPlugin) DescribeCall(args []string) string {
	path, err := parseViewInvocation(args)
	if err != nil || path == "" {
		return i18n.T("plugins.view.describe_generic")
	}
	return i18n.T("plugins.view.describe_file", describeTrim(path))
}
