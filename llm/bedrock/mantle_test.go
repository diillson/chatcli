/*
 * ChatCLI - Tests for the Claude-in-Amazon-Bedrock (bedrock-mantle) path
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/diillson/chatcli/models"
)

func setEnvForTest(t *testing.T, key, value string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
	if value == "" {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, value)
	}
}

func TestMantleMessagesURL(t *testing.T) {
	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", "")
	assert.Equal(t,
		"https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages",
		mantleMessagesURL("us-east-1"))

	// Operator override (VPC endpoints, proxies, tests). Trailing slash
	// must not produce a double slash.
	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", "https://proxy.corp.example/")
	assert.Equal(t,
		"https://proxy.corp.example/anthropic/v1/messages",
		mantleMessagesURL("us-east-1"))
}

// Sonnet 5 exists ONLY on the bedrock-mantle Messages endpoint (no
// InvokeModel surface) and Fable 5 rejects legacy InvokeModel outright
// (400 "data retention mode 'default' is not available for this model" —
// it requires 30-day retention, which only the Claude-in-Amazon-Bedrock
// agreement provides), so both must route to Mantle by default — whatever
// spelling the user selected, including inference-profile IDs discovered
// via ListInferenceProfiles. Opus 4.8 stays on InvokeModel unless the
// operator forces the endpoint.
func TestUsesMantleEndpoint(t *testing.T) {
	setEnvForTest(t, "BEDROCK_ANTHROPIC_ENDPOINT", "")

	assert.True(t, usesMantleEndpoint("anthropic.claude-sonnet-5"))
	assert.True(t, usesMantleEndpoint("claude-sonnet-5"))
	assert.True(t, usesMantleEndpoint("us.anthropic.claude-sonnet-5"))
	assert.True(t, usesMantleEndpoint("global.anthropic.claude-sonnet-5"))
	assert.True(t, usesMantleEndpoint("anthropic.claude-fable-5"))
	assert.True(t, usesMantleEndpoint("us.anthropic.claude-fable-5"))
	assert.True(t, usesMantleEndpoint("global.anthropic.claude-fable-5"))
	assert.False(t, usesMantleEndpoint("anthropic.claude-opus-4-8"))
	assert.False(t, usesMantleEndpoint("global.anthropic.claude-opus-4-6-v1"))

	// Operator override: force every Anthropic request through Mantle…
	setEnvForTest(t, "BEDROCK_ANTHROPIC_ENDPOINT", "mantle")
	assert.True(t, usesMantleEndpoint("anthropic.claude-opus-4-8"))

	// …or pin everything to the legacy InvokeModel path.
	setEnvForTest(t, "BEDROCK_ANTHROPIC_ENDPOINT", "invoke")
	assert.False(t, usesMantleEndpoint("anthropic.claude-sonnet-5"))
	assert.False(t, usesMantleEndpoint("anthropic.claude-fable-5"))
}

// The Mantle Messages endpoint serves the canonical "anthropic."-prefixed
// dateless IDs only. Regional/global inference-profile IDs — exactly what
// ListInferenceProfiles hands the user in /switch — return 404
// not_found_error if sent verbatim, so the wire model must be
// canonicalized before the request is built.
func TestMantleModelID(t *testing.T) {
	cases := map[string]string{
		// Already canonical: untouched.
		"anthropic.claude-sonnet-5": "anthropic.claude-sonnet-5",
		"anthropic.claude-fable-5":  "anthropic.claude-fable-5",
		// Inference-profile spellings resolve through the catalog to the
		// invokable Mantle id.
		"us.anthropic.claude-sonnet-5":      "anthropic.claude-sonnet-5",
		"global.anthropic.claude-sonnet-5":  "anthropic.claude-sonnet-5",
		"us.anthropic.claude-sonnet-5-v1:0": "anthropic.claude-sonnet-5",
		"us.anthropic.claude-fable-5":       "anthropic.claude-fable-5",
		"global.anthropic.claude-fable-5":   "anthropic.claude-fable-5",
		// Bare first-party id (users type it out of habit).
		"claude-sonnet-5": "anthropic.claude-sonnet-5",
		// Unknown-but-real Claude profile id the catalog can't resolve:
		// the mechanical prefix strip is the fallback.
		"us.anthropic.claude-zeta-9-v1:0": "anthropic.claude-zeta-9-v1:0",
		// Non-Claude ids pass through untouched.
		"meta.llama3-70b-instruct-v1:0": "meta.llama3-70b-instruct-v1:0",
	}
	for in, want := range cases {
		assert.Equal(t, want, mantleModelID(in), "mantleModelID(%q)", in)
	}
}

// A client bound to a discovered inference-profile ID must put the
// canonical Mantle id on the wire — this is the exact scenario behind the
// production 404: /switch lists "us.anthropic.claude-sonnet-5", the user
// selects it, and the Messages endpoint doesn't know that id.
func TestSendPromptAnthropicMantleNormalizesProfileID(t *testing.T) {
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"pong"}]}`))
	}))
	defer srv.Close()

	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", srv.URL)
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	c := &BedrockClient{
		model:       "us.anthropic.claude-sonnet-5",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		maxAttempts: 1,
	}

	out, err := c.sendPromptAnthropicMantle(t.Context(), "ping", nil, 512)
	require.NoError(t, err)
	assert.Equal(t, "pong", out)
	assert.Equal(t, "anthropic.claude-sonnet-5", gotBody["model"],
		"inference-profile id must be canonicalized for the Mantle wire")
}

// End-to-end request assembly against a fake Mantle endpoint: the body is
// the first-party Messages shape (NO anthropic_version body field — the
// version travels in the anthropic-version header), cache_control markers
// survive, and bearer-token auth lands in x-api-key.
func TestSendPromptAnthropicMantleRoundTrip(t *testing.T) {
	var (
		gotPath    string
		gotHeaders http.Header
		gotBody    map[string]interface{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &gotBody))
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"pong"}]}`))
	}))
	defer srv.Close()

	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", srv.URL)
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	c := &BedrockClient{
		model:       "anthropic.claude-sonnet-5",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		maxAttempts: 1,
	}

	history := []models.Message{
		{
			Role: "system",
			SystemParts: []models.ContentBlock{
				{Type: "text", Text: "stable system prompt"},
				{Type: "text", Text: "attached context", CacheControl: &models.CacheControl{Type: "ephemeral"}},
			},
		},
		{Role: "user", Content: "ping"},
	}

	out, err := c.sendPromptAnthropicMantle(t.Context(), "ping", history, 2048)
	require.NoError(t, err)
	assert.Equal(t, "pong", out)

	assert.Equal(t, "/anthropic/v1/messages", gotPath)
	assert.Equal(t, "test-bearer-token", gotHeaders.Get("x-api-key"))
	assert.Equal(t, "2023-06-01", gotHeaders.Get("anthropic-version"))
	assert.Empty(t, gotHeaders.Get("Authorization"), "bearer path must not SigV4-sign")

	assert.Equal(t, "anthropic.claude-sonnet-5", gotBody["model"])
	assert.NotContains(t, gotBody, "anthropic_version",
		"Mantle speaks the first-party Messages shape; anthropic_version is InvokeModel-only")
	assert.EqualValues(t, 2048, gotBody["max_tokens"])

	system, ok := gotBody["system"].([]interface{})
	require.True(t, ok, "system must be a structured block list")
	markers := 0
	for _, b := range system {
		if blk, ok := b.(map[string]interface{}); ok {
			if _, has := blk["cache_control"]; has {
				markers++
			}
		}
	}
	assert.Equal(t, 1, markers, "cache_control marker must reach the Mantle wire")
}

// Without AWS_BEARER_TOKEN_BEDROCK the request is SigV4-signed with the
// bedrock-mantle service using the resolved credential chain.
func TestSendPromptAnthropicMantleSigV4(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"signed"}]}`))
	}))
	defer srv.Close()

	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", srv.URL)
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "")

	c := &BedrockClient{
		model:       "anthropic.claude-sonnet-5",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		maxAttempts: 1,
		credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDTEST", SecretAccessKey: "secret"}, nil
		}),
	}
	out, err := c.sendPromptAnthropicMantle(t.Context(), "ping", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "signed", out)
	assert.Contains(t, gotAuth, "AWS4-HMAC-SHA256")
	assert.Contains(t, gotAuth, "/bedrock-mantle/aws4_request",
		"signature must use the bedrock-mantle service name")
}

// No bearer token and no initialized credential chain must produce an
// actionable error, not a nil-pointer panic.
func TestSendPromptAnthropicMantleNoCredentials(t *testing.T) {
	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", "http://127.0.0.1:1")
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "")

	c := &BedrockClient{
		model:       "anthropic.claude-sonnet-5",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		maxAttempts: 1,
	}
	_, err := c.sendPromptAnthropicMantle(t.Context(), "ping", nil, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AWS_BEARER_TOKEN_BEDROCK")
}

// A transport-level failure (connection refused) must surface through the
// retry wrapper as a bedrock-mantle error.
func TestSendPromptAnthropicMantleTransportError(t *testing.T) {
	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", "http://127.0.0.1:1")
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	c := &BedrockClient{
		model:       "anthropic.claude-sonnet-5",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		maxAttempts: 1,
	}
	_, err := c.sendPromptAnthropicMantle(t.Context(), "ping", nil, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bedrock-mantle")
}

// Non-JSON error bodies (load balancers, proxies) fall back to the raw
// status + body instead of getting swallowed.
func TestSendPromptAnthropicMantleHTTPErrorNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream exploded"))
	}))
	defer srv.Close()

	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", srv.URL)
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	c := &BedrockClient{
		model:       "anthropic.claude-sonnet-5",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		maxAttempts: 1,
	}
	_, err := c.sendPromptAnthropicMantle(t.Context(), "ping", nil, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 502")
}

// The corporate-TLS override must reach the Mantle transport the same way
// it reaches the AWS SDK clients.
func TestMantleHTTPClientCorporateTLS(t *testing.T) {
	setEnvForTest(t, "CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "true")
	setEnvForTest(t, "CHATCLI_BEDROCK_CA_BUNDLE", "")

	c := &BedrockClient{logger: zap.NewNop()}
	hc := c.mantleHTTPClient()
	require.NotNil(t, hc)
	assert.NotNil(t, hc.Transport, "corporate override must install a custom transport")

	// Without overrides, the default client (process-global trust) is used.
	setEnvForTest(t, "CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "")
	setEnvForTest(t, "CHATCLI_TLS_INSECURE_SKIP_VERIFY", "")
	hc = c.mantleHTTPClient()
	require.NotNil(t, hc)
	assert.Nil(t, hc.Transport)
}

// SendPrompt must dispatch mantle-only models through the Messages
// endpoint end to end (ensureRuntime included).
func TestSendPromptDispatchesSonnet5ToMantle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"via-mantle"}]}`))
	}))
	defer srv.Close()

	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", srv.URL)
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")
	setEnvForTest(t, "AWS_REGION", "us-east-1")
	setEnvForTest(t, "BEDROCK_PROVIDER", "")
	setEnvForTest(t, "BEDROCK_ANTHROPIC_ENDPOINT", "")

	c := NewBedrockClient("anthropic.claude-sonnet-5", "us-east-1", "", zap.NewNop(), 1, 0)
	out, err := c.SendPrompt(t.Context(), "ping", nil, 128)
	require.NoError(t, err)
	assert.Equal(t, "via-mantle", out)
}

// Mantle errors come back in the standard Anthropic error envelope; the
// client must surface type+message instead of a raw HTTP status.
func TestSendPromptAnthropicMantleAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"boom"}}`))
	}))
	defer srv.Close()

	setEnvForTest(t, "BEDROCK_MANTLE_BASE_URL", srv.URL)
	setEnvForTest(t, "AWS_BEARER_TOKEN_BEDROCK", "test-bearer-token")

	c := &BedrockClient{
		model:       "anthropic.claude-sonnet-5",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		maxAttempts: 1,
	}
	_, err := c.sendPromptAnthropicMantle(t.Context(), "ping", nil, 128)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid_request_error")
	assert.Contains(t, err.Error(), "boom")
}
