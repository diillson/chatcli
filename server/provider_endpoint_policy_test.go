/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/llm/ollama"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// countingServer records every request that reaches it.
func countingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("internal service body"))
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

// A client-chosen base_url is refused by default and never reaches the
// manager that would build a client dialing it.
func TestProviderConfig_ClientBaseURLRefusedByDefault(t *testing.T) {
	t.Setenv(providerEndpointsEnv, "")
	mgr := &mockLLMManager{}
	h := &Handler{llmManager: mgr, logger: zap.NewNop(), ssrfValidator: NewSSRFValidator(zap.NewNop())}

	_, err := h.SendPrompt(context.Background(), &pb.SendPromptRequest{
		Prompt:         "hello",
		Provider:       "OLLAMA",
		Model:          "llama3",
		ProviderConfig: map[string]string{"base_url": "https://ollama.attacker.invalid:11434"},
	})
	if err == nil {
		t.Fatal("client base_url was accepted")
	}
	if !strings.Contains(err.Error(), providerEndpointsEnv) {
		t.Fatalf("error does not say how to allow an endpoint: %v", err)
	}
	if code := status.Code(err); code != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument: a refused client configuration is a client error", code)
	}
	mgr.AssertNotCalled(t, "CreateClientWithConfig")
}

// Even an operator-allowlisted origin is never dialed when it resolves to
// a loopback address: the dial guard runs after resolution.
func TestProviderConfig_AllowlistedLoopbackNeverDialed(t *testing.T) {
	ts, hits := countingServer(t)
	t.Setenv(providerEndpointsEnv, ts.URL)
	mgr := &mockLLMManager{}
	// No upfront validator: the dial guard alone must hold, as it must
	// against a name that resolves differently at dial time.
	h := &Handler{llmManager: mgr, logger: zap.NewNop()}

	c, err := h.getClient("OLLAMA", "llama3", "", map[string]string{"base_url": ts.URL})
	if err != nil {
		t.Fatalf("allowlisted endpoint refused before dialing: %v", err)
	}
	if _, ok := c.(*ollama.Client); !ok {
		t.Fatalf("client = %T, want the guarded Ollama client", c)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = c.SendPrompt(ctx, "hello", nil, 16)
	if err == nil {
		t.Fatal("request to a loopback endpoint succeeded")
	}
	if strings.Contains(err.Error(), "internal service body") {
		t.Fatalf("internal response body leaked: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server dialed the loopback endpoint %d time(s)", n)
	}
	mgr.AssertNotCalled(t, "CreateClientWithConfig")
}

func TestGuardedProviderHTTPClient_BlocksLoopbackAtDial(t *testing.T) {
	ts, hits := countingServer(t)
	c := guardedProviderHTTPClient(zap.NewNop(), 5*time.Second)
	resp, err := c.Get(ts.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("guarded client reached a loopback server")
	}
	if !errors.Is(err, errProviderDialBlocked) {
		t.Fatalf("error = %v, want errProviderDialBlocked", err)
	}
	if hits.Load() != 0 {
		t.Fatal("loopback server was reached")
	}
}

func TestProviderDialBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "fd00:ec2::254", "fe80::1", "100.64.0.1",
		"0.0.0.0", "::", "224.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1", "198.18.0.1",
	}
	for _, s := range blocked {
		if !providerDialBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if providerDialBlockedIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestVetProviderConfig(t *testing.T) {
	h := &Handler{logger: zap.NewNop()}

	t.Run("unknown endpoint-like keys are refused", func(t *testing.T) {
		t.Setenv(providerEndpointsEnv, "")
		for _, k := range []string{"endpoint", "api_url", "api_base", "url", "host", "region", "server_url", "realm_url"} {
			if _, err := h.vetProviderConfig("OLLAMA", map[string]string{k: "https://example.test"}); err == nil {
				t.Errorf("key %q accepted", k)
			}
		}
	})

	t.Run("path segments stay single segments", func(t *testing.T) {
		for _, v := range []string{"../admin", "a/b", "a?b", "a#b", "%2e%2e", "..", "a@b"} {
			if _, err := h.vetProviderConfig("STACKSPOT", map[string]string{"realm": v}); err == nil {
				t.Errorf("realm %q accepted", v)
			}
			if _, err := h.vetProviderConfig("STACKSPOT", map[string]string{"agent_id": v}); err == nil {
				t.Errorf("agent_id %q accepted", v)
			}
		}
		got, err := h.vetProviderConfig("STACKSPOT", map[string]string{"client_id": "id", "client_key": "k", "realm": "acme", "agent_id": "01J0-agent.v2"})
		if err != nil || got.endpoint != nil || len(got.config) != 4 {
			t.Fatalf("valid StackSpot config: %v %+v", err, got)
		}
	})

	t.Run("base_url applies to Ollama only", func(t *testing.T) {
		t.Setenv(providerEndpointsEnv, "https://ollama.example.test")
		if _, err := h.vetProviderConfig("OPENAI", map[string]string{"base_url": "https://ollama.example.test"}); err == nil {
			t.Fatal("base_url accepted for OPENAI")
		}
	})

	t.Run("allowlist matches the exact origin", func(t *testing.T) {
		t.Setenv(providerEndpointsEnv, " https://Ollama.Example.test , not a url ")
		got, err := h.vetProviderConfig("ollama", map[string]string{"base_url": "https://ollama.example.test:443/"})
		if err != nil || got.endpoint == nil {
			t.Fatalf("allowlisted origin refused: %v", err)
		}
		if _, ok := got.config["base_url"]; ok {
			t.Fatal("base_url must not be forwarded to the manager")
		}
		for _, raw := range []string{
			"https://ollama.example.test:8443",
			"http://ollama.example.test",
			"https://ollama.example.test.attacker.test",
			"https://user:pw@ollama.example.test",
			"https://ollama.example.test/?x=1",
			"file:///etc/passwd",
		} {
			if _, err := h.vetProviderConfig("OLLAMA", map[string]string{"base_url": raw}); err == nil {
				t.Errorf("%q accepted", raw)
			}
		}
	})
}
