/*
 * ChatCLI - Claude in Amazon Bedrock (bedrock-mantle Messages endpoint)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"go.uber.org/zap"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
)

// mantleSigningService is the SigV4 service name for the Claude-in-Amazon-
// Bedrock Messages endpoint (distinct from "bedrock" / "bedrock-runtime").
const mantleSigningService = "bedrock-mantle"

// mantleAnthropicVersion travels in the anthropic-version HTTP header —
// the Mantle endpoint speaks the first-party Messages wire shape, so the
// body carries NO anthropic_version field (that one is InvokeModel-only).
const mantleAnthropicVersion = "2023-06-01"

// mantleMessagesURL resolves the Messages endpoint for a region.
// BEDROCK_MANTLE_BASE_URL overrides scheme+host for VPC endpoints,
// corporate proxies and tests.
func mantleMessagesURL(region string) string {
	base := strings.TrimSpace(os.Getenv("BEDROCK_MANTLE_BASE_URL"))
	if base == "" {
		base = "https://bedrock-mantle." + region + ".api.aws"
	}
	return strings.TrimSuffix(base, "/") + "/anthropic/v1/messages"
}

// usesMantleEndpoint decides whether an Anthropic-family request must go
// through the Mantle Messages endpoint instead of legacy InvokeModel.
//
// Default: catalog-driven — models flagged bedrock_mantle_only (Sonnet 5
// has no InvokeModel surface at all) route to Mantle; everything else
// stays on the proven InvokeModel path. BEDROCK_ANTHROPIC_ENDPOINT
// overrides for operators: "mantle" forces every Anthropic request onto
// the Messages endpoint, "invoke"/"legacy" pins them all to InvokeModel.
func usesMantleEndpoint(model string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BEDROCK_ANTHROPIC_ENDPOINT"))) {
	case "mantle":
		return true
	case "invoke", "invokemodel", "legacy":
		return false
	}
	return catalog.HasCapability(catalog.ProviderBedrock, model, "bedrock_mantle_only")
}

// sendPromptAnthropicMantle sends a Messages API request to the
// Claude-in-Amazon-Bedrock endpoint. The body builder, thinking dispatch
// and cache_control budget are shared with the InvokeModel path — the
// only wire differences are the endpoint, the auth (SigV4 with the
// bedrock-mantle service, or AWS_BEARER_TOKEN_BEDROCK via x-api-key) and
// the anthropic-version header replacing the anthropic_version body field.
func (c *BedrockClient) sendPromptAnthropicMantle(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages, systemObj := c.buildMessagesAndSystem(prompt, history)

	reqBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": effectiveMaxTokens,
		"messages":   messages,
	}
	if systemObj != nil {
		reqBody["system"] = systemObj
	}

	applyAnthropicThinkingForEffort(reqBody, c.model, ctx)
	enforceCacheControlBudget(reqBody, anthropicMaxCacheBreakpoints)

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	endpoint := mantleMessagesURL(c.region)
	start := time.Now()
	client.LogRequestStart(c.logger, "BEDROCK", c.model,
		zap.String("family", "anthropic-mantle"),
		zap.String("region", c.region),
		zap.String("endpoint", endpoint),
		zap.Int("payload_bytes", len(payload)),
		zap.Int("history_len", len(history)),
		zap.Int("max_tokens", effectiveMaxTokens),
		zap.Int("cache_markers", client.CountAnthropicCacheMarkers(reqBody)),
	)

	responseText, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		return c.doMantleRequest(ctx, endpoint, payload)
	})

	if err != nil {
		client.LogRequestFinish(c.logger, "BEDROCK", c.model, "error", time.Since(start),
			zap.String("family", "anthropic-mantle"),
		)
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "Bedrock"), zap.Error(err))
		return "", err
	}
	client.LogRequestFinish(c.logger, "BEDROCK", c.model, "success", time.Since(start),
		zap.String("family", "anthropic-mantle"),
		zap.Int("response_chars", len(responseText)),
	)
	return responseText, nil
}

// doMantleRequest performs one signed HTTP round-trip against the Mantle
// Messages endpoint and parses the standard Anthropic response envelope.
func (c *BedrockClient) doMantleRequest(ctx context.Context, endpoint string, payload []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("bedrock-mantle: build request: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", mantleAnthropicVersion)

	if bearer := strings.TrimSpace(os.Getenv("AWS_BEARER_TOKEN_BEDROCK")); bearer != "" {
		// Short-term bearer tokens (aws-bedrock-token-generator) travel in
		// x-api-key — no SigV4 signature on this path.
		req.Header.Set("x-api-key", bearer)
	} else {
		creds, err := c.awsCfg.Credentials.Retrieve(ctx)
		if err != nil {
			return "", fmt.Errorf("bedrock-mantle: resolve AWS credentials (or set AWS_BEARER_TOKEN_BEDROCK): %w", err)
		}
		sum := sha256.Sum256(payload)
		if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]),
			mantleSigningService, c.region, time.Now()); err != nil {
			return "", fmt.Errorf("bedrock-mantle: sign request: %w", err)
		}
	}

	resp, err := c.mantleHTTPClient().Do(req)
	if resp != nil {
		// Close unconditionally — a redirect error can hand back a non-nil
		// response alongside a non-nil error.
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil {
		return "", fmt.Errorf("bedrock-mantle: %w", err)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return "", fmt.Errorf("bedrock-mantle: read response: %w", err)
	}

	if resp.StatusCode >= 300 {
		// The endpoint answers with the standard Anthropic error envelope;
		// parseAnthropicBody surfaces type+message when present.
		if _, perr := parseAnthropicBody(body); perr != nil {
			return "", fmt.Errorf("bedrock-mantle: HTTP %d: %w", resp.StatusCode, perr)
		}
		return "", fmt.Errorf("bedrock-mantle: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseAnthropicBody(body)
}

// mantleHTTPClient reuses the corporate-TLS-aware transport when the
// operator configured one; otherwise the default transport applies, which
// already honors the process-global trust overrides from
// utils.ApplyGlobalTLSTrust. Returns a concrete *http.Client so response
// bodies stay statically trackable by callers.
func (c *BedrockClient) mantleHTTPClient() *http.Client {
	if httpClient, note := buildCorporateHTTPClient(c.logger); httpClient != nil {
		if note != "" {
			c.logger.Warn(note)
		}
		if bc, ok := httpClient.(*awshttp.BuildableClient); ok {
			return &http.Client{Transport: bc.GetTransport(), Timeout: bc.GetTimeout()}
		}
	}
	return &http.Client{Timeout: 10 * time.Minute}
}
