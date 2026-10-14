/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/providersdk"
)

// The test binary doubles as plugin fixtures: copied under a name starting
// with "ph-", it serves the plugin named after the rest of the file name
// instead of running the tests. Plugins inherit a filtered environment, so
// the mode travels in the file name rather than an env var.
func TestMain(m *testing.M) {
	base := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	switch {
	case strings.HasPrefix(base, "ph-dialect"):
		api := providerproto.DialectOpenAIChat
		switch {
		case strings.HasSuffix(base, "-anthropic"):
			api = providerproto.DialectAnthropicMessages
		case strings.HasSuffix(base, "-gemini"):
			api = providerproto.DialectGemini
		case strings.HasSuffix(base, "-responses"):
			api = providerproto.DialectOpenAIResponses
		}
		serve(dialectFixture{api: api})
	case strings.HasPrefix(base, "ph-crash"):
		serve(crashFixture{})
	case strings.HasPrefix(base, "ph-impostor"):
		serve(impostorFixture{})
	default:
		os.Exit(m.Run())
	}
}

func serve(p providersdk.Provider) {
	if err := providersdk.Serve(context.Background(), p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// fixtureURLFile carries the fake backend URL to dialect fixtures (they run
// with a filtered environment and no arguments).
func fixtureURLFile() string {
	return filepath.Join(filepath.Dir(os.Args[0]), "backend-url")
}

// dialectFixture is an OpenAI-compatible gateway plugin: prompt login,
// rotating bearer tokens, a per-model routing header, and an authEnv
// variable echoed back so the test can see what reached the process.
type dialectFixture struct{ api providerproto.DialectAPI }

var tokenSerial atomic.Int64

func (dialectFixture) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{Name: "PH_GATEWAY", DisplayName: map[string]string{"en": "PH Gateway", "pt-BR": "Gateway PH"}, Version: "1.0.0"}
}
func (dialectFixture) Mode() providerproto.Mode { return providerproto.ModeDialect }
func (d dialectFixture) Dialect() providerproto.Dialect {
	raw, _ := os.ReadFile(fixtureURLFile())
	return providerproto.Dialect{API: d.api, BaseURL: strings.TrimSpace(string(raw)) + "/v1"}
}
func (dialectFixture) AuthEnv() []string { return []string{"PH_TEST_SECRET"} }
func (dialectFixture) AuthStatus(_ context.Context, c providerproto.Credential) (providerproto.AuthStatusResult, error) {
	if c["token"] == "" {
		return providerproto.AuthStatusResult{State: providerproto.AuthLoggedOut}, nil
	}
	return providerproto.AuthStatusResult{State: providerproto.AuthLoggedIn, Account: "acct"}, nil
}
func (dialectFixture) Login(context.Context, providerproto.AuthLoginParams) (providerproto.AuthStep, error) {
	return providerproto.AuthStep{Kind: providerproto.StepPrompt, Session: "s", Fields: []providerproto.PromptField{
		{ID: "token", Label: map[string]string{"en": "Token"}, Secret: true},
	}}, nil
}
func (dialectFixture) Continue(_ context.Context, p providerproto.AuthContinueParams) (providerproto.AuthStep, error) {
	return providerproto.AuthStep{Kind: providerproto.StepDone, Account: "acct", Credential: providerproto.Credential{"token": p.Inputs["token"]}}, nil
}
func (dialectFixture) Token(_ context.Context, c providerproto.Credential) (providerproto.AuthTokenResult, error) {
	n := tokenSerial.Add(1)
	return providerproto.AuthTokenResult{Headers: map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s-%d", c["token"], n),
		"X-Env-Seen":    os.Getenv("PH_TEST_SECRET"),
		"X-Env-Leak":    os.Getenv("PH_TEST_LEAK"),
	}}, nil
}
func (dialectFixture) ListModels(context.Context, providerproto.Credential) ([]providerproto.Model, error) {
	return []providerproto.Model{{
		ID:       "gw-sonnet",
		Upstream: &providerproto.Upstream{Provider: "CLAUDEAI", Model: "claude-sonnet-4-6"},
		Headers:  map[string]string{"X-Route": "anthropic"},
	}}, nil
}

// crashFixture dies in the middle of chat/send.
type crashFixture struct{}

func (crashFixture) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{Name: "PH_CRASH", DisplayName: map[string]string{"en": "PH Crash"}, Version: "0"}
}
func (crashFixture) Mode() providerproto.Mode { return providerproto.ModeFull }
func (crashFixture) Chat(context.Context, providerproto.ChatParams, func(providerproto.ChatDelta) error) (providerproto.ChatResult, error) {
	fmt.Fprintln(os.Stderr, "fatal: backend exploded")
	os.Exit(3)
	return providerproto.ChatResult{}, nil
}

// impostorFixture claims a built-in provider's name.
type impostorFixture struct{}

func (impostorFixture) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{Name: "OPENAI", DisplayName: map[string]string{"en": "Not OpenAI"}, Version: "0"}
}
func (impostorFixture) Mode() providerproto.Mode { return providerproto.ModeFull }
func (impostorFixture) Chat(context.Context, providerproto.ChatParams, func(providerproto.ChatDelta) error) (providerproto.ChatResult, error) {
	return providerproto.ChatResult{}, nil
}
