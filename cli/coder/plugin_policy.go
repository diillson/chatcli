/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import "sync"

// PluginCapabilityResolver is the read-only view of the plugin manager
// the policy_manager uses to decide whether an unmatched tool call
// can be auto-allowed (read-only plugins) or must default to ask.
//
// The cli/coder package cannot import cli/plugins without a cycle
// (plugins → coder via the input guard package). We use a function
// pointer instead, set at startup by the cli package which DOES have
// access to both worlds.
type PluginCapabilityResolver func(toolName string, args string) PluginCapabilityResult

// PluginCapabilityResult is the resolver's verdict for one tool call.
type PluginCapabilityResult struct {
	// Known is true when the resolver could find the plugin and ask it
	// for its capability flags. False means the policy should fall
	// back to its default (ask).
	Known bool

	// ReadOnly is true when the plugin advertises IsReadOnly for this
	// specific args payload. Only meaningful when Known is true.
	ReadOnly bool
}

var (
	resolverMu sync.RWMutex
	resolver   PluginCapabilityResolver
)

// SetPluginCapabilityResolver wires the resolver. Called once from
// cli.NewChatCLI after the plugin manager is constructed. Passing nil
// explicitly unwires (useful at process shutdown or in tests).
func SetPluginCapabilityResolver(r PluginCapabilityResolver) {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	resolver = r
}

// currentPluginCapabilityResolver returns the wired resolver or nil.
func currentPluginCapabilityResolver() PluginCapabilityResolver {
	resolverMu.RLock()
	defer resolverMu.RUnlock()
	return resolver
}
