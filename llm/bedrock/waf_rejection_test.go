package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	cfg := aws.Config{
		Region: "us-east-1",
		// Inline provider instead of the aws-sdk-go-v2/credentials package:
		// pulling that package in would promote it to a direct go.mod
		// dependency for a test-only static credential.
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
		}),
	}
	rt := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
	})

	c := &BedrockClient{
		model:       "global.anthropic.claude-sonnet-4-5-20250929-v1:0",
		region:      "us-east-1",
		logger:      zap.NewNop(),
		runtime:     rt,
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
