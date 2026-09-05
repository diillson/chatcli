/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The provider's rules, as assertions.
 *
 * Every test here states a rule that lives on the other side of the wire —
 * what the API accepts, rejects, or requires — rather than what our
 * encoder happens to produce. A test of the second kind passes forever
 * while the request is refused in production, which is exactly what
 * happened with the placement of turn-scoped system messages: the
 * documentation was read, the code was written, no request was ever made,
 * and the 400 arrived from a user's terminal.
 *
 * The rule that governs all of them: a beta flag opts a request into a
 * feature, and the API rejects the feature's fields without it. The
 * converse is what bites — a request that declares a beta it does not use
 * has two halves of one feature disagreeing about whether it is on, and
 * that disagreement is how a gated field ends up never traveling while
 * its header does.
 */
package claudeai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// betasOn returns the anthropic-beta flags a request ended up carrying.
func betasOn(req *http.Request) []string {
	raw := req.Header.Get("anthropic-beta")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func hasBeta(req *http.Request, beta string) bool {
	for _, b := range betasOn(req) {
		if strings.TrimSpace(b) == beta {
			return true
		}
	}
	return false
}

// TestBetaNeverTravelsWithoutItsFeature is the general form of a defect
// found in the task budget: the header was decided from the request
// context and the field from the request body, behind a gate the header
// did not know about, so a request could declare a beta whose field it
// never sent.
//
// A beta announced without its feature is harmless to the response and
// corrosive to the code: it is the visible symptom of two halves of one
// feature disagreeing about whether it is on.
func TestBetaNeverTravelsWithoutItsFeature(t *testing.T) {
	c := &ClaudeClient{model: "claude-opus-5", logger: zap.NewNop()}

	t.Run("task budget", func(t *testing.T) {
		// No ceiling on this turn: neither the field nor its beta.
		req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		applyTaskBudgetBeta(req)
		if hasBeta(req, client.TaskBudgetBeta) {
			t.Error("a turn with no ceiling must not declare the task-budget beta")
		}
		body := map[string]interface{}{"max_tokens": 4096}
		if applyTaskBudget(body, c.model, context.Background()) {
			t.Error("and must not send the field either")
		}

		// With a ceiling: both halves agree, and they agree without an
		// effort hint, which is the case that used to send the header
		// alone.
		ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudget(64000))
		req = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil).WithContext(ctx)
		applyTaskBudgetBeta(req)
		sent := applyTaskBudget(map[string]interface{}{"max_tokens": 4096}, c.model, ctx)
		if got := hasBeta(req, client.TaskBudgetBeta); got != sent {
			t.Errorf("header says %v, body says %v — the two halves must agree", got, sent)
		}
		if !sent {
			t.Error("a ceiling with no effort hint must still travel")
		}
	})

	t.Run("turn-scoped system", func(t *testing.T) {
		// A conversation with no per-turn block declares nothing.
		plain := []models.Message{{Role: "user", Content: "q1"}}
		_, _ = c.buildMessagesAndSystem("q1", plain)
		req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		c.applyTurnScopedSystemBeta(req)
		if hasBeta(req, client.TurnScopedSystemBeta) {
			t.Error("a request carrying no turn-scoped message must not declare its beta")
		}

		// One that carries a block declares it.
		withBlock := []models.Message{
			models.TurnContextMessage("[TURN CONTEXT] today"),
			{Role: "user", Content: "q1"},
		}
		_, _ = c.buildMessagesAndSystem("q1", withBlock)
		req = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		c.applyTurnScopedSystemBeta(req)
		if !hasBeta(req, client.TurnScopedSystemBeta) {
			t.Error("the field is rejected as unknown without its beta")
		}
	})

	t.Run("extended cache ttl", func(t *testing.T) {
		// The hour is what the beta gates; the five-minute default needs
		// no flag at all.
		t.Setenv(client.PromptCacheTTLEnv, "5m")
		req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		applyExtendedCacheTTLBeta(req)
		if hasBeta(req, client.ExtendedCacheTTLBeta) {
			t.Error("the default lifetime needs no beta")
		}
		t.Setenv(client.PromptCacheTTLEnv, "1h")
		req = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		applyExtendedCacheTTLBeta(req)
		if !hasBeta(req, client.ExtendedCacheTTLBeta) {
			t.Error("the hour is gated on the beta")
		}
	})

	t.Run("context management and compaction", func(t *testing.T) {
		// Off: neither flag.
		t.Setenv(client.ContextEngineEnv, "builtin")
		req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		applyExtendedCacheTTLBeta(req)
		if hasBeta(req, client.ContextManagementBeta) || hasBeta(req, client.CompactionBeta) {
			t.Error("the builtin engine declares neither server-side beta")
		}

		// Editing only: the editing flag, and no compaction flag, because
		// no compaction edit is in the body.
		t.Setenv(client.ContextEngineEnv, "provider")
		req = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		applyExtendedCacheTTLBeta(req)
		if !hasBeta(req, client.ContextManagementBeta) {
			t.Error("the provider engine is gated on the context-management beta")
		}
		if hasBeta(req, client.CompactionBeta) {
			t.Error("no compaction edit travels, so its beta must not either")
		}

		// Editing plus summarizing: both flags, and the edit is present.
		t.Setenv(client.ContextEngineEnv, "provider-compact")
		req = httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		applyExtendedCacheTTLBeta(req)
		if !hasBeta(req, client.CompactionBeta) {
			t.Error("the compaction edit is gated on its beta")
		}
		if !client.ProviderCompactionEngine() {
			t.Error("the beta is declared, so the edit must travel")
		}
	})
}

// TestTurnScopedSystem_HasAWayBack covers the gap that left a user with no
// option but to change model when this feature broke. Every other wire
// feature here has a switch; this one shipped without one.
func TestTurnScopedSystem_HasAWayBack(t *testing.T) {
	history := []models.Message{
		models.TurnContextMessage("[TURN CONTEXT] today"),
		{Role: "user", Content: "q1"},
	}
	c := &ClaudeClient{model: "claude-opus-5", logger: zap.NewNop()}

	t.Setenv(client.TurnScopedSystemEnv, "off")
	messages, _ := c.buildMessagesAndSystem("q1", history)
	for _, m := range messages {
		if roleAt(m) == "system" {
			t.Fatal("switched off, the block travels as the user message it always was")
		}
	}
	if c.turnScoped.Used() {
		t.Fatal("and no beta is declared")
	}

	// Only an explicit off disables it: a typo must not silently drop a
	// capability.
	t.Setenv(client.TurnScopedSystemEnv, "maybe")
	messages, _ = c.buildMessagesAndSystem("q1", history)
	var found bool
	for _, m := range messages {
		if roleAt(m) == "system" {
			found = true
		}
	}
	if !found {
		t.Fatal("an unrecognized value must leave the capability on")
	}
}
