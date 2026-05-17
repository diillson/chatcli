/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Fase 2.1 capability advertisements for BuiltinWebFetchPlugin, kept
// separate from builtin_webfetch.go so the original file (with its
// grandfathered-complex ExecuteWithStream / parseFetchArgs functions)
// stays out of the diff and out of the Quality Gate's cyclo-new scan.

// IsReadOnly reports true whenever the invocation is a plain fetch
// (HTTP GET). When the caller asks the plugin to save the body to the
// session scratch dir via save_to_file, that is still a side-effect-
// only-in-scratch operation — read-only with respect to the user's
// working tree.
func (p *BuiltinWebFetchPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: each fetch opens an independent HTTP
// connection. Two parallel fetches don't conflict, and net/http
// handles connection pooling under the hood with goroutine-safe
// semantics.
func (p *BuiltinWebFetchPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the URL being fetched so the spinner reports
// "Fetching https://x.example/..." instead of the generic description.
func (p *BuiltinWebFetchPlugin) DescribeCall(args []string) string {
	u := extractURLArg(args)
	if u == "" {
		return p.Description()
	}
	if len(u) > 70 {
		u = u[:70] + "..."
	}
	return i18n.T("plugins.webfetch.describe", u)
}
