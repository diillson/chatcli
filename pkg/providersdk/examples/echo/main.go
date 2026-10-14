/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Command echo is a complete reference provider plugin. It "answers" by
// echoing the last user message back, which makes it deterministic enough
// to drive the conformance kit, while exercising every part of the
// protocol a real plugin uses: a prompt-based login whose credential the
// host stores, token minting, a model list, streaming with cancellation,
// token counting and logout.
//
// Build it and run the conformance kit against it:
//
//	go build -o echo-provider ./pkg/providersdk/examples/echo
//	chatcli provider test ./echo-provider --input token=demo
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/providersdk"
)

type echo struct{}

func (echo) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{
		Name:        "ECHO",
		DisplayName: map[string]string{"en": "Echo (example)", "pt-BR": "Echo (exemplo)"},
		Version:     "1.0.0",
		Description: map[string]string{"en": "Reference provider plugin that echoes the prompt."},
	}
}

func (echo) Mode() providerproto.Mode { return providerproto.ModeFull }

func (echo) AuthEnv() []string { return []string{"ECHO_TOKEN"} }

func (echo) ChatCapabilities() providerproto.ChatCapabilities {
	return providerproto.ChatCapabilities{Stream: true, Tools: false}
}

// ------------------------------------------------------------------ auth

func (echo) AuthStatus(_ context.Context, cred providerproto.Credential) (providerproto.AuthStatusResult, error) {
	if cred["token"] == "" {
		return providerproto.AuthStatusResult{State: providerproto.AuthLoggedOut}, nil
	}
	return providerproto.AuthStatusResult{State: providerproto.AuthLoggedIn, Account: "demo"}, nil
}

var tokenField = providerproto.PromptField{
	ID:     "token",
	Label:  map[string]string{"en": "Access token", "pt-BR": "Token de acesso"},
	Secret: true,
}

func (e echo) Login(ctx context.Context, p providerproto.AuthLoginParams) (providerproto.AuthStep, error) {
	if tok := firstNonEmpty(p.Inputs["token"], os.Getenv("ECHO_TOKEN")); tok != "" {
		return e.done(tok), nil
	}
	return providerproto.AuthStep{
		Kind:    providerproto.StepPrompt,
		Session: "login",
		Message: map[string]string{"en": "Paste any token to sign in to the example provider."},
		Fields:  []providerproto.PromptField{tokenField},
	}, nil
}

func (e echo) Continue(_ context.Context, p providerproto.AuthContinueParams) (providerproto.AuthStep, error) {
	if p.Session != "login" {
		return providerproto.AuthStep{}, providerproto.NewError(providerproto.ErrCodeInvalidParams, "unknown login session")
	}
	tok := strings.TrimSpace(p.Inputs["token"])
	if tok == "" {
		return providerproto.AuthStep{}, providerproto.NewError(providerproto.ErrCodeAuthRequired, "a token is required")
	}
	return e.done(tok), nil
}

func (echo) done(tok string) providerproto.AuthStep {
	return providerproto.AuthStep{
		Kind:       providerproto.StepDone,
		Account:    "demo",
		Credential: providerproto.Credential{"token": tok},
	}
}

func (echo) Token(_ context.Context, cred providerproto.Credential) (providerproto.AuthTokenResult, error) {
	if cred["token"] == "" {
		return providerproto.AuthTokenResult{}, providerproto.NewError(providerproto.ErrCodeAuthRequired, "not logged in")
	}
	return providerproto.AuthTokenResult{
		Headers:   map[string]string{"Authorization": "Bearer " + cred["token"]},
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}, nil
}

func (echo) Logout(context.Context, providerproto.Credential) error { return nil }

// ---------------------------------------------------------------- models

func (echo) ListModels(context.Context, providerproto.Credential) ([]providerproto.Model, error) {
	return []providerproto.Model{
		{
			ID:              "echo-1",
			DisplayName:     "Echo 1",
			ContextWindow:   32000,
			MaxOutputTokens: 4096,
			Pricing:         &providerproto.Pricing{},
		},
		{
			ID:              "echo-slow",
			DisplayName:     "Echo (slow stream)",
			ContextWindow:   32000,
			MaxOutputTokens: 4096,
			Pricing:         &providerproto.Pricing{},
		},
	}, nil
}

// ------------------------------------------------------------------ chat

func (echo) Chat(ctx context.Context, p providerproto.ChatParams, emit func(providerproto.ChatDelta) error) (providerproto.ChatResult, error) {
	if p.Credential["token"] == "" {
		return providerproto.ChatResult{}, providerproto.NewError(providerproto.ErrCodeAuthRequired, "not logged in")
	}
	if p.Model != "echo-1" && p.Model != "echo-slow" {
		return providerproto.ChatResult{}, providerproto.NewError(providerproto.ErrCodeModelNotFound, fmt.Sprintf("unknown model %q", p.Model))
	}
	prompt := lastUserText(p.Messages)
	reply := "echo: " + prompt

	if emit != nil {
		pause := time.Millisecond
		if p.Model == "echo-slow" {
			pause = 200 * time.Millisecond
		}
		for i, word := range strings.Fields(reply) {
			if i > 0 {
				word = " " + word
			}
			select {
			case <-ctx.Done():
				return providerproto.ChatResult{}, providerproto.NewError(providerproto.ErrCodeCancelled, "cancelled")
			case <-time.After(pause):
			}
			if err := emit(providerproto.ChatDelta{Text: word}); err != nil {
				return providerproto.ChatResult{}, err
			}
		}
	}

	return providerproto.ChatResult{
		Message: providerproto.Message{
			Role:    providerproto.RoleAssistant,
			Content: []providerproto.Part{{Type: providerproto.PartText, Text: reply}},
		},
		StopReason: providerproto.StopEnd,
		Usage: &providerproto.Usage{
			InputTokens:  countWords(p.Messages),
			OutputTokens: len(strings.Fields(reply)),
		},
	}, nil
}

func (echo) CountTokens(_ context.Context, p providerproto.TokensCountParams) (int, error) {
	return countWords(p.Messages), nil
}

func lastUserText(msgs []providerproto.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != providerproto.RoleUser {
			continue
		}
		var b strings.Builder
		for _, part := range msgs[i].Content {
			if part.Type == providerproto.PartText {
				b.WriteString(part.Text)
			}
		}
		return b.String()
	}
	return ""
}

func countWords(msgs []providerproto.Message) int {
	n := 0
	for _, m := range msgs {
		for _, part := range m.Content {
			n += len(strings.Fields(part.Text))
		}
	}
	return n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func main() {
	if err := providersdk.Serve(context.Background(), echo{}); err != nil {
		fmt.Fprintln(os.Stderr, "echo provider:", err)
		os.Exit(1)
	}
}
