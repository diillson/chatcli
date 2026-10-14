/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package manager

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

type stubClient struct{ model string }

func (s stubClient) GetModelName() string { return s.model }
func (s stubClient) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	return "ok", nil
}

// Runtime providers (plugins) register through the optional interface and
// survive RefreshProviders, which only re-adds the built-ins.
func TestProviderRegistrar(t *testing.T) {
	setupTestEnv(t, map[string]string{})
	mgr, err := NewLLMManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	reg, ok := mgr.(ProviderRegistrar)
	if !ok {
		t.Fatal("LLMManagerImpl must implement ProviderRegistrar")
	}
	reg.RegisterProvider("acme", func(model string) (client.LLMClient, error) { return stubClient{model: model}, nil })
	reg.RegisterProvider("", nil) // ignored

	mgr.RefreshProviders()
	c, err := mgr.GetClient("ACME", "m1")
	if err != nil || c.GetModelName() != "m1" {
		t.Fatalf("GetClient = %v, %v", c, err)
	}
	found := false
	for _, p := range mgr.GetAvailableProviders() {
		found = found || p == "ACME"
	}
	if !found {
		t.Fatal("registered provider missing from GetAvailableProviders")
	}

	reg.UnregisterProvider("acme")
	if _, err := mgr.GetClient("ACME", "m1"); err == nil {
		t.Fatal("unregistered provider still resolves")
	}
}
