package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	cliagent "github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Reproduces the corporate-middlebox incident end-to-end: a proxy/WAF in
// front of bedrock-runtime answers InvokeModel with 403 + an HTML block
// page. The AWS SDK surfaces this as "deserialization failed ... invalid
// character '<'". The error must (a) be classified as a proxy/WAF payload
// rejection and (b) carry the exact request size so agent-mode recovery can
// learn an adaptive payload cap instead of guessing.
func TestSendPromptAnthropic_WAF403HTMLBlockPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html><head><title>Access Denied</title></head><body>Request blocked by security policy</body></html>"))
	}))
	defer srv.Close()

	c := &BedrockClient{
		model:       "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		runtime:     newFakeEndpointRuntime(srv.URL),
		maxAttempts: 1,
		backoff:     time.Millisecond,
	}

	history := []models.Message{
		{Role: "system", Content: "agent charter"},
		{Role: "user", Content: "ping"},
	}

	_, err := c.sendPromptAnthropic(t.Context(), "ping", history, 512)
	require.Error(t, err)

	assert.True(t, cliagent.IsProxyWAFRejection(err),
		"HTML-body 403 must be classified as a proxy/WAF rejection, got: %v", err)

	size, ok := client.RequestSizeFromError(err)
	require.True(t, ok, "error must carry the rejected request size, got: %v", err)
	assert.Greater(t, size, len("agent charter")+len("ping"),
		"annotated size must reflect the serialized request body")
}

// Same middlebox scenario through the OpenAI-family InvokeModel path: the
// request-size annotation must be present on every Bedrock family, not just
// Anthropic — payload recovery is provider-family agnostic.
func TestSendPromptOpenAI_WAF403HTMLBlockPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html><body>Blocked by security policy</body></html>"))
	}))
	defer srv.Close()

	c := &BedrockClient{
		model:       "openai.gpt-oss-120b-1:0",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		runtime:     newFakeEndpointRuntime(srv.URL),
		maxAttempts: 1,
		backoff:     time.Millisecond,
	}

	_, err := c.sendPromptOpenAI(t.Context(), "ping", nil, 256)
	require.Error(t, err)
	assert.True(t, cliagent.IsProxyWAFRejection(err), "got: %v", err)
	size, ok := client.RequestSizeFromError(err)
	require.True(t, ok, "OpenAI-family errors must carry the request size, got: %v", err)
	assert.Positive(t, size)
}

// Converse cannot observe the SDK-marshaled body, so its annotation is a
// content-based estimate — it must still be present and plausible.
func TestSendPromptConverse_WAF403AnnotatesEstimatedSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html><body>Blocked by security policy</body></html>"))
	}))
	defer srv.Close()

	c := &BedrockClient{
		model:       "amazon.nova-pro-v1:0",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		runtime:     newFakeEndpointRuntime(srv.URL),
		maxAttempts: 1,
		backoff:     time.Millisecond,
	}

	history := []models.Message{{Role: "user", Content: "some prior context"}}
	_, err := c.sendPromptConverse(t.Context(), "ping", history, 256)
	require.Error(t, err)
	size, ok := client.RequestSizeFromError(err)
	require.True(t, ok, "Converse errors must carry an estimated request size, got: %v", err)
	assert.Greater(t, size, len("some prior context"))
}

func TestEstimateConversePayloadBytes(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: strings.Repeat("s", 1000)},
		{Role: "user", Content: strings.Repeat("u", 500)},
	}
	got := estimateConversePayloadBytes("prompt", history)
	// Content chars + prompt dominate; fixed overhead covers JSON framing.
	if got < 1500+len("prompt") {
		t.Errorf("estimate %d must cover all content bytes", got)
	}
	if got > 1500+len("prompt")+4096 {
		t.Errorf("estimate %d implausibly large for the given content", got)
	}
}

// newFakeEndpointRuntime builds a bedrockruntime client aimed at a local
// httptest server, with inline static credentials (no direct dependency on
// the aws credentials package — see Floor 4 note above).
func newFakeEndpointRuntime(baseURL string) *bedrockruntime.Client {
	cfg := aws.Config{
		Region: "us-east-1",
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
	}
	return bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}
