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

// IsConcurrencySafe mirrors IsReadOnly: GET/HEAD/OPTIONS are independent
// reads; a POST, PUT, PATCH or DELETE may depend on the one before it (create
// then update), so mutating requests keep their order.
func (p *BuiltinHTTPPlugin) IsConcurrencySafe(args []string) bool { return p.IsReadOnly(args) }

// DescribeCall surfaces the request line in the spinner.
func (p *BuiltinHTTPPlugin) DescribeCall(args []string) string {
	in, err := parseHTTPInvocation(args)
	if err != nil {
		return i18n.T("plugins.http.describe_generic")
	}
	return i18n.T("plugins.http.describe_request", in.Method, describeTrim(in.URL))
}
