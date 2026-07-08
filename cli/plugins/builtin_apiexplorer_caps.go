/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinAPIExplorerPlugin.

// IsReadOnly reports true: every subcommand only observes the target. discover,
// spec and endpoint issue GET/HEAD/OPTIONS; graphql issues an introspection
// query, which is a pure read (it mutates nothing). Nothing here changes remote
// state, so the tool skips the confirmation policy and can fan out.
func (*BuiltinAPIExplorerPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: each call is an independent read; the shared
// client is safe for concurrent use.
func (*BuiltinAPIExplorerPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the subcommand and target in the spinner.
func (*BuiltinAPIExplorerPlugin) DescribeCall(args []string) string {
	in, err := parseAPIExplorerArgs(args)
	if err != nil {
		return i18n.T("plugins.apiexplorer.describe_generic")
	}
	switch in.Cmd {
	case "spec":
		return i18n.T("plugins.apiexplorer.describe_spec", describeTrim(in.URL))
	case "endpoint":
		target := in.Path
		if in.Method != "" {
			target = in.Method + " " + in.Path
		}
		return i18n.T("plugins.apiexplorer.describe_endpoint", target)
	case "graphql":
		return i18n.T("plugins.apiexplorer.describe_graphql", describeTrim(in.URL))
	case "security":
		return i18n.T("plugins.apiexplorer.describe_security", describeTrim(in.URL))
	default:
		return i18n.T("plugins.apiexplorer.describe_discover", describeTrim(in.URL))
	}
}
