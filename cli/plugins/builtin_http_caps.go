/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinHTTPPlugin.

// IsReadOnly is PER-METHOD: GET/HEAD/OPTIONS only observe the target;
// POST/PUT/PATCH/DELETE mutate remote state and go through the
// orchestrator's confirmation policy. Unparseable args fail closed.
func (p *BuiltinHTTPPlugin) IsReadOnly(args []string) bool {
	return httpReadOnlyMethods[httpInvocationMethod(args)]
}

// IsConcurrencySafe reports true: requests are independent; the shared client
// is safe for concurrent use.
func (p *BuiltinHTTPPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the request line in the spinner.
func (p *BuiltinHTTPPlugin) DescribeCall(args []string) string {
	in, err := parseHTTPInvocation(args)
	if err != nil {
		return i18n.T("plugins.http.describe_generic")
	}
	return i18n.T("plugins.http.describe_request", in.Method, describeTrim(in.URL))
}
