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

// anthropicEndpointMode is the operator's routing choice for Anthropic
// requests on Bedrock, read from BEDROCK_ANTHROPIC_ENDPOINT.
type anthropicEndpointMode int

const (
	// endpointModeAuto (default / "auto"): catalog decides — models
	// flagged bedrock_mantle_only go to Mantle with an automatic
	// fallback to InvokeModel when the Mantle call fails; everything
	// else stays on InvokeModel.
	endpointModeAuto anthropicEndpointMode = iota
	// endpointModeMantle ("mantle"): every Anthropic request pinned to
	// the Mantle Messages endpoint — no fallback (the operator asked
	// for this surface explicitly).
	endpointModeMantle
	// endpointModeInvoke ("invoke"/"invokemodel"/"legacy"): every
	// Anthropic request pinned to InvokeModel.
	endpointModeInvoke
)

// resolveAnthropicEndpointMode parses BEDROCK_ANTHROPIC_ENDPOINT.
// Unknown values fall back to auto so a typo degrades to the default
// routing instead of silently pinning a surface.
func resolveAnthropicEndpointMode() anthropicEndpointMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BEDROCK_ANTHROPIC_ENDPOINT"))) {
	case "mantle":
		return endpointModeMantle
	case "invoke", "invokemodel", "legacy":
		return endpointModeInvoke
	}
	return endpointModeAuto
}

// usesMantleEndpoint decides whether an Anthropic-family request must go
// through the Mantle Messages endpoint instead of legacy InvokeModel.
//
// Default: catalog-driven — models flagged bedrock_mantle_only route to
// Mantle; everything else stays on the proven InvokeModel path. Sonnet 5
// has no InvokeModel surface at all, and Fable 5 rejects InvokeModel with
// 400 "data retention mode 'default' is not available for this model"
// (it requires 30-day retention, provided only under the
// Claude-in-Amazon-Bedrock agreement the Mantle endpoint serves).
// Catalog resolution matches inference-profile spellings too
// (us./eu./apac./global. + the embedded claude segment), so a profile id
// picked from ListInferenceProfiles routes the same as the canonical id.
// BEDROCK_ANTHROPIC_ENDPOINT overrides for operators: "mantle" forces
// every Anthropic request onto the Messages endpoint, "invoke"/"legacy"
// pins them all to InvokeModel. In the default "auto" mode, a Mantle
// failure additionally falls back to InvokeModel (see SendPrompt).
func usesMantleEndpoint(model string) bool {
	switch resolveAnthropicEndpointMode() {
	case endpointModeMantle:
		return true
	case endpointModeInvoke:
		return false
	}
	return catalog.HasCapability(catalog.ProviderBedrock, model, "bedrock_mantle_only")
}

// mantleProfilePrefixes are the cross-region inference-profile prefixes
// AWS prepends to Bedrock model IDs (ListInferenceProfiles spelling).
// The Mantle Messages endpoint does not know these — it serves the
// canonical "anthropic."-prefixed dateless IDs only.
var mantleProfilePrefixes = []string{"us.", "eu.", "apac.", "jp.", "au.", "global."}

// stripInferenceProfilePrefix removes a cross-region profile prefix from a
// Claude Bedrock id ("global.anthropic.claude-x" → "anthropic.claude-x").
// Non-Claude and unprefixed ids come back unchanged.
func stripInferenceProfilePrefix(id string) string {
	trimmed := strings.TrimSpace(id)
	m := strings.ToLower(trimmed)
	for _, p := range mantleProfilePrefixes {
		if strings.HasPrefix(m, p) && strings.HasPrefix(m[len(p):], "anthropic.") {
			return trimmed[len(p):]
		}
	}
	return trimmed
}

// mantleModelID canonicalizes whatever Bedrock-side spelling the user
// selected — an inference-profile ID discovered via ListInferenceProfiles
// ("us.anthropic.claude-sonnet-5", "global.anthropic.claude-sonnet-5-v1:0"),
// a bare first-party id or a catalog alias — into the id the Mantle
// Messages endpoint actually serves. Sending a profile id verbatim
// returns 404 not_found_error ("model does not exist").
//
// Resolution order: the Bedrock catalog first (it owns the invokable
// Mantle id; the catalog canonical may itself be a global. profile — for
// InvokeModel-era models like Opus 4.8 — so its prefix is stripped too);
// a mechanical profile-prefix strip as fallback for real-but-uncataloged
// Claude IDs; non-Claude ids pass through.
func mantleModelID(model string) string {
	if meta, ok := catalog.Resolve(catalog.ProviderBedrock, model); ok {
		if id := stripInferenceProfilePrefix(meta.ID); strings.HasPrefix(strings.ToLower(id), "anthropic.") {
			return id
		}
	}
	if id := stripInferenceProfilePrefix(model); strings.HasPrefix(strings.ToLower(id), "anthropic.") {
		return id
	}
	return model
}

// invokeFallbackModelID maps the model id the Mantle path was serving
// onto an id the legacy InvokeModel surface accepts on-demand. Recent
// Claude generations have no bare-ID on-demand surface — InvokeModel
// requires an inference-profile id — so a canonical "anthropic.claude-*"
// id gets the global. profile prefix (the profile AWS publishes for every
// family-5 model). Ids that already carry a profile prefix, application-
// profile ARNs and non-Claude ids pass through unchanged.
func invokeFallbackModelID(model string) string {
	trimmed := strings.TrimSpace(model)
	m := strings.ToLower(trimmed)
	for _, p := range mantleProfilePrefixes {
		if strings.HasPrefix(m, p) && strings.HasPrefix(m[len(p):], "anthropic.") {
			return trimmed
		}
	}
	if strings.HasPrefix(m, "anthropic.") {
		return "global." + trimmed
	}
	return trimmed
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

	wireModel := mantleModelID(c.model)
	if wireModel != c.model {
		c.logger.Info("bedrock-mantle: canonicalized inference-profile id for the Messages wire",
			zap.String("from", c.model), zap.String("to", wireModel))
	}

	reqBody := map[string]interface{}{
		"model":      wireModel,
		"max_tokens": effectiveMaxTokens,
		"messages":   messages,
	}
	if systemObj != nil {
		reqBody["system"] = systemObj
	}

	applyAnthropicThinkingForEffort(reqBody, c.model, ctx)
	// Rolling conversation breakpoint; the configured TTL applies on Claude
	// 4.5+ (Bedrock accepts "ttl":"1h" in cache_control).
	client.MarkAnthropicHistoryBreakpoint(client.AnthropicMessages{Maps: messages}, client.AnthropicCacheMarkerWithTTL(extendedCacheTTLModel(c.model)))
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
		// Annotate with the request size so agent-mode payload recovery can
		// learn the proxy/WAF cap from the exact size that was rejected.
		return "", client.WithRequestSize(err, len(payload))
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
		if c.credentials == nil {
			return "", fmt.Errorf("bedrock-mantle: AWS credential chain not initialized (or set AWS_BEARER_TOKEN_BEDROCK)")
		}
		creds, err := c.credentials.Retrieve(ctx)
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

	if resp.StatusCode < 300 {
		if text, perr := parseAnthropicBody(body); perr == nil {
			c.captureAnthropicUsage(body)
			return text, nil
		}
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
