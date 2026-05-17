/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// TestPluginCapabilityResolver_AutoAllowsReadOnly is the headline
// regression: a tool with no matching policy rule that the resolver
// flags as read-only auto-allows (Priority 5) instead of falling
// through to ActionAsk.
func TestPluginCapabilityResolver_AutoAllowsReadOnly(t *testing.T) {
	// Install a resolver that returns Known+ReadOnly for @testtool.
	SetPluginCapabilityResolver(func(toolName, args string) PluginCapabilityResult {
		if toolName == "@testtool" {
			return PluginCapabilityResult{Known: true, ReadOnly: true}
		}
		return PluginCapabilityResult{}
	})
	t.Cleanup(func() { SetPluginCapabilityResolver(nil) })

	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}

	action := pm.Check("@testtool", "")
	assert.Equal(t, ActionAllow, action,
		"read-only tool with no explicit rule must auto-allow via the capability gate")
}

// TestPluginCapabilityResolver_AskForMutatingTools confirms the
// opposite: a tool the resolver knows but flags as NOT read-only
// keeps the default ActionAsk.
func TestPluginCapabilityResolver_AskForMutatingTools(t *testing.T) {
	SetPluginCapabilityResolver(func(toolName, args string) PluginCapabilityResult {
		if toolName == "@mutatetool" {
			return PluginCapabilityResult{Known: true, ReadOnly: false}
		}
		return PluginCapabilityResult{}
	})
	t.Cleanup(func() { SetPluginCapabilityResolver(nil) })

	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}

	action := pm.Check("@mutatetool", "")
	assert.Equal(t, ActionAsk, action,
		"non-read-only tool must default to ActionAsk")
}

// TestPluginCapabilityResolver_UnknownToolFallsThrough confirms the
// fail-closed default: when the resolver returns Known=false (plugin
// not in the manager, e.g. an MCP tool or a typo), Priority 5 does
// nothing and the existing default applies.
func TestPluginCapabilityResolver_UnknownToolFallsThrough(t *testing.T) {
	called := 0
	SetPluginCapabilityResolver(func(toolName, args string) PluginCapabilityResult {
		called++
		return PluginCapabilityResult{} // Known: false
	})
	t.Cleanup(func() { SetPluginCapabilityResolver(nil) })

	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}

	action := pm.Check("@unknown", "")
	assert.Equal(t, ActionAsk, action)
	assert.Equal(t, 1, called, "the resolver must be consulted exactly once")
}

// TestPluginCapabilityResolver_NilResolverIsNoop documents the
// safe-default behavior: with no resolver wired (test envs that never
// call SetPluginCapabilityResolver), the policy_manager behaves
// exactly as it did before Item 4.
func TestPluginCapabilityResolver_NilResolverIsNoop(t *testing.T) {
	SetPluginCapabilityResolver(nil)
	t.Cleanup(func() { SetPluginCapabilityResolver(nil) })

	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}

	action := pm.Check("@anything", "")
	assert.Equal(t, ActionAsk, action,
		"without a resolver the legacy ActionAsk default applies")
}

// TestPluginCapabilityResolver_DoesNotOverrideDenyRules pins the
// priority order: an explicit deny rule beats the capability auto-
// allow. The user can write a deny pattern that blocks a tool even
// if it's read-only (e.g. block @websearch entirely in corporate
// environments).
func TestPluginCapabilityResolver_DoesNotOverrideDenyRules(t *testing.T) {
	SetPluginCapabilityResolver(func(toolName, args string) PluginCapabilityResult {
		return PluginCapabilityResult{Known: true, ReadOnly: true}
	})
	t.Cleanup(func() { SetPluginCapabilityResolver(nil) })

	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}
	// User says "always deny @websearch". The pattern is a prefix
	// (matchesWithBoundary uses HasPrefix + word boundary), not a glob.
	if err := pm.AddRule("@websearch", ActionDeny); err != nil {
		t.Fatalf("AddRule: %v", err)
	}

	action := pm.Check("@websearch", "search foo")
	assert.Equal(t, ActionDeny, action,
		"explicit user deny must beat capability auto-allow")
}

// TestPluginCapabilityResolver_DoesNotOverrideSafetyImmune covers the
// other priority guard: SafetyImmune (currently @coder exec) requires
// confirmation even when the plugin advertises read-only. The Priority
// 2 SafetyImmune check fires BEFORE Priority 5.
func TestPluginCapabilityResolver_DoesNotOverrideSafetyImmune(t *testing.T) {
	// Resolver claims everything is read-only.
	SetPluginCapabilityResolver(func(_, _ string) PluginCapabilityResult {
		return PluginCapabilityResult{Known: true, ReadOnly: true}
	})
	t.Cleanup(func() { SetPluginCapabilityResolver(nil) })

	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}

	// @coder exec is the canonical safety-immune operation.
	action := pm.Check("@coder", `{"cmd":"exec","args":{"cmd":"ls"}}`)
	assert.Equal(t, ActionAsk, action,
		"@coder exec must always ask, regardless of any read-only claim")
}
