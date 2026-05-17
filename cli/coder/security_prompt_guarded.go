/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"context"

	"go.uber.org/zap"
)

// This file wires the InputGuard around the existing
// PromptSecurityCheck / PromptSecurityCheckWithContext entry points so
// the typeahead-defense logic does not have to live inside security_ui.go
// (whose formatActionDetails function is grandfathered above the cyclo
// threshold for new files). All guard-aware call sites use the wrappers
// defined here; security_ui.go itself is untouched.

var (
	promptInputGuard       *InputGuard
	promptInputGuardLogger *zap.Logger
)

// SetSecurityPromptLogger installs a zap logger into the package-level
// input guard. Callers that have a logger (the CLI initializer) should
// set this at startup; otherwise the guard falls back to a no-op logger.
//
// Idempotent: subsequent calls overwrite the active logger and guard.
func SetSecurityPromptLogger(logger *zap.Logger) {
	promptInputGuardLogger = logger
	promptInputGuard = NewInputGuard(logger)
}

// activeInputGuard returns the package-level guard, lazily instantiated
// with the most recently configured logger (or a no-op when nothing was
// wired). Used by the Guarded* wrappers below.
func activeInputGuard() *InputGuard {
	if promptInputGuard == nil {
		promptInputGuard = NewInputGuard(promptInputGuardLogger)
	}
	return promptInputGuard
}

// PromptSecurityCheckGuarded wraps PromptSecurityCheck with the
// typeahead defense layers from InputGuard: kernel TTY flush, channel
// drain, and post-render intent debounce. Use this from every agent or
// coder call site that prompts the user for a security decision —
// without it, keystrokes the user typed while the LLM was streaming
// would be consumed by the very next <-inputCh as the y/n answer.
func PromptSecurityCheckGuarded(ctx context.Context, toolName, args string, inputCh <-chan string) SecurityDecision {
	guard := activeInputGuard()
	guard.Guard(inputCh)
	decision := PromptSecurityCheck(ctx, toolName, args, inputCh)
	// The wrapped prompt already consumed the user's deliberate answer
	// from the channel. The debounce here is for any *trailing* input
	// that arrived during the brief render-to-answer window; it must
	// run AFTER the answer was read so we don't eat the answer itself.
	guard.IntentDebounce(ctx, inputCh)
	return decision
}

// PromptSecurityCheckWithContextGuarded mirrors PromptSecurityCheckGuarded
// for the richer prompt variant that carries SecurityContext (agent name +
// task description shown to the user). Same guard semantics: flush + drain
// before the prompt renders, debounce after the answer is read.
func PromptSecurityCheckWithContextGuarded(ctx context.Context, toolName, args string, secCtx *SecurityContext, inputCh <-chan string) SecurityDecision {
	guard := activeInputGuard()
	guard.Guard(inputCh)
	decision := PromptSecurityCheckWithContext(ctx, toolName, args, secCtx, inputCh)
	guard.IntentDebounce(ctx, inputCh)
	return decision
}
