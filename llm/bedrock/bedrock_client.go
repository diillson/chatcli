/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	bedrocksvc "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/internal/visionwire"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// modelFamily identifies which dispatch path a Bedrock model uses.
// Anthropic and OpenAI keep dedicated InvokeModel paths (preserving cache
// markers, extended thinking and the existing wire formats). Everything
// else routes through the unified Converse API, which supports Llama,
// Amazon Nova, Mistral, Cohere, AI21 Jamba, DeepSeek, Stability and any
// future provider Bedrock onboards without a per-provider body schema.
type modelFamily string

const (
	familyAnthropic modelFamily = "anthropic"
	familyOpenAI    modelFamily = "openai"
	familyConverse  modelFamily = "converse"
)

// converseOnlyVendors lists Bedrock model-id vendor segments whose models
// only speak their native (or the Converse) schema — the Anthropic Messages
// and OpenAI Chat Completions InvokeModel bodies are rejected outright
// (Nova: `extraneous key [max_tokens] is not permitted`). A BEDROCK_PROVIDER
// override pointing one of these at another family would guarantee a 400,
// so resolveFamily ignores the override for them.
var converseOnlyVendors = []string{
	"amazon.", "meta.", "mistral.", "cohere.", "ai21.",
	"stability.", "deepseek.", "writer.", "luma.", "twelvelabs.", "qwen.",
}

// isConverseOnlyVendor reports whether the model id positively identifies a
// vendor from converseOnlyVendors, either bare ("amazon.nova-pro-v1:0") or
// behind an inference-profile prefix ("us.amazon.nova-pro-v1:0"). Opaque ids
// (application inference profile ARNs) never match.
func isConverseOnlyVendor(model string) bool {
	m := strings.ToLower(model)
	for _, v := range converseOnlyVendors {
		if strings.HasPrefix(m, v) || strings.Contains(m, "."+v) {
			return true
		}
	}
	return false
}

// isConverseOnlyModel extends the vendor list with per-model catalog
// knowledge: some ids under an otherwise InvokeModel-capable vendor prefix
// (openai.gpt-5.6-*) only exist on Converse, and the catalog marks them
// with the bedrock_converse_only capability.
func isConverseOnlyModel(model string) bool {
	return isConverseOnlyVendor(model) ||
		catalog.HasCapability(catalog.ProviderBedrock, model, "bedrock_converse_only")
}

// resolveFamily picks the dispatch path. Precedence:
//  1. BEDROCK_PROVIDER env var ("anthropic"/"claude", "openai"/"gpt",
//     "converse"/"auto") — except for Converse-only vendor ids, where a
//     forced anthropic/openai schema cannot ever succeed.
//  2. Model ID content: "openai.*" → OpenAI; any Claude marker (an
//     "anthropic" segment — bare or behind a global./us./eu./apac.
//     profile prefix — or a bare "claude"/"fable" first-party id) →
//     Anthropic. Claude models always speak the Anthropic Messages
//     schema; routing them through Converse would silently drop
//     cache_control markers and extended-thinking knobs.
//  3. Default: Converse — covers Llama, Nova, Mistral, Cohere, AI21,
//     DeepSeek, Stability and any unknown provider through the unified
//     Bedrock Converse API.
func resolveFamily(model string) modelFamily {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BEDROCK_PROVIDER"))) {
	case "openai", "gpt":
		if isConverseOnlyModel(model) {
			return familyConverse
		}
		return familyOpenAI
	case "anthropic", "claude":
		if isConverseOnlyModel(model) {
			return familyConverse
		}
		return familyAnthropic
	case "converse", "auto":
		return familyConverse
	}
	// Catalog entries flagged bedrock_converse_only (GPT-5.6 Sol/Terra/
	// Luna: Converse/Responses/Chat Completions on AWS, InvokeModel
	// unsupported) must never reach the vendor sniff below, which would
	// route the "openai." prefix down the gpt-oss InvokeModel path.
	if catalog.HasCapability(catalog.ProviderBedrock, model, "bedrock_converse_only") {
		return familyConverse
	}
	// Um application inference profile carrega um ARN opaco sem nenhuma
	// pista do provedor na string; o catálogo (alimentado pelo discovery ou
	// pelo lookup lazy de GetInferenceProfile) sabe qual modelo está por
	// trás. Só o roteamento Anthropic é decidido aqui — entradas que
	// preferem ChatCompletions seguem para os sniffs de string, que
	// separam a família OpenAI da Converse.
	if meta, ok := catalog.Resolve(catalog.ProviderBedrock, model); ok &&
		meta.PreferredAPI == catalog.APIAnthropicMessages {
		return familyAnthropic
	}
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "openai.") || strings.Contains(m, ".openai.") {
		return familyOpenAI
	}
	if isClaudeModelID(m) {
		return familyAnthropic
	}
	return familyConverse
}

// isClaudeModelID reports whether a Bedrock model id refers to an Anthropic
// Claude model, whatever the surface spelling: "anthropic."-prefixed base
// IDs, inference-profile IDs (global./us./eu./apac. + ".anthropic."), or a
// bare first-party id ("claude-fable-5", "fable-5", "claude-sonnet-5") the
// user picked out of habit.
func isClaudeModelID(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "anthropic.") ||
		strings.Contains(m, ".anthropic.") ||
		strings.Contains(m, "claude") ||
		strings.Contains(m, "fable")
}

// normalizeBedrockModelID upgrades a bare first-party Claude id to the
// invokable Bedrock id from the catalog (e.g. "claude-fable-5" →
// "anthropic.claude-fable-5"). IDs that already carry an "anthropic."
// segment — including account-specific inference profiles — and non-Claude
// IDs pass through untouched.
func normalizeBedrockModelID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" || strings.Contains(m, "anthropic.") || !isClaudeModelID(m) {
		return model
	}
	if meta, ok := catalog.Resolve(catalog.ProviderBedrock, model); ok &&
		strings.Contains(strings.ToLower(meta.ID), "anthropic.") {
		return meta.ID
	}
	return model
}

// filterBedrockCapabilities strips capability flags that exist on the
// first-party Claude API but are not served by Bedrock, per Anthropic's
// platform-availability matrix: fast_mode (research preview, first-party
// only) and mid_conversation_system (unsupported on Bedrock). Advertising
// either on a Bedrock entry would make the request builders emit
// parameters AWS rejects.
func filterBedrockCapabilities(caps []string) []string {
	if caps == nil {
		return nil
	}
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		if c == "fast_mode" || c == "mid_conversation_system" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// BedrockClient invokes foundation models hosted on AWS Bedrock.
// Three dispatch paths via auto-detection (model-id prefix) or explicit
// selection through BEDROCK_PROVIDER:
//   - Anthropic Messages schema (Claude 3/3.5/3.7/4/4.5/4.6/4.7) — preserves
//     cache_control markers and extended-thinking knobs.
//   - OpenAI Chat Completions schema (openai.gpt-oss-*).
//   - Converse API (default for everything else: Llama, Nova, Mistral,
//     Cohere, AI21 Jamba, DeepSeek, Stability) — one schema for all.
//
// Authentication uses the default AWS credentials chain:
//   - Environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN)
//   - Shared credentials file (~/.aws/credentials) — selected by AWS_PROFILE
//   - EC2/ECS/EKS IAM roles
type BedrockClient struct {
	model       string
	region      string
	profile     string
	logger      *zap.Logger
	maxAttempts int
	backoff     time.Duration
	runtime     *bedrockruntime.Client
	control     *bedrocksvc.Client
	// credentials carries the resolved credential chain for endpoints
	// outside the AWS SDK service clients — the bedrock-mantle Messages
	// endpoint signs raw HTTP requests with SigV4 using it. Kept as the
	// provider interface (not aws.Config) so BedrockClient stays a
	// comparable type: aws.Config embeds slices and would break any
	// downstream == comparison of client values.
	credentials aws.CredentialsProvider
	// profileLookupDone gates the one-shot GetInferenceProfile call that
	// resolves an application-inference-profile ARN configured directly
	// (env/config) without going through /model discovery.
	profileLookupDone atomic.Bool
	// usage holds the most recent call's real token usage (see usage.go).
	// client.UsageState is a comparable struct, preserving the comparable
	// contract documented above.
	usage client.UsageState
}

// NewBedrockClient creates a client bound to a model id and region.
// The AWS SDK is initialized lazily on the first call so that a missing
// credentials chain does not break provider discovery.
func NewBedrockClient(model, region, profile string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *BedrockClient {
	if normalized := normalizeBedrockModelID(model); normalized != model {
		logger.Info("bedrock: normalized bare Claude model id to invokable Bedrock id",
			zap.String("from", model), zap.String("to", normalized))
		model = normalized
	}
	return &BedrockClient{
		model:       model,
		region:      region,
		profile:     profile,
		logger:      logger,
		maxAttempts: maxAttempts,
		backoff:     backoff,
	}
}

func (c *BedrockClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderBedrock, c.model)
}

func (c *BedrockClient) getMaxTokens() int {
	if tokenStr := os.Getenv("BEDROCK_MAX_TOKENS"); tokenStr != "" {
		if parsed, err := strconv.Atoi(tokenStr); err == nil && parsed > 0 {
			return parsed
		}
	}
	if tokenStr := os.Getenv("ANTHROPIC_MAX_TOKENS"); tokenStr != "" {
		if parsed, err := strconv.Atoi(tokenStr); err == nil && parsed > 0 {
			return parsed
		}
	}
	// Sem override: o terceiro argumento tem prioridade máxima em
	// GetMaxTokens, então passar 4096 aqui ignorava o catálogo inteiro e
	// estrangulava modelos com teto de 64K-128K (Fable/Sonnet/Opus).
	return catalog.GetMaxTokens(catalog.ProviderBedrock, c.model, 0)
}

// shouldDisableIMDS returns true when the process is clearly NOT running on
// EC2/ECS/EKS and thus should not waste time (and noisy errors) trying to
// reach http://169.254.169.254. The user can force-enable the probe with
// CHATCLI_BEDROCK_ENABLE_IMDS=1, or force-disable with
// AWS_EC2_METADATA_DISABLED=true (standard AWS SDK env var).
func shouldDisableIMDS() bool {
	if v := strings.ToLower(os.Getenv("AWS_EC2_METADATA_DISABLED")); v == "true" || v == "1" {
		return true
	}
	if v := os.Getenv("CHATCLI_BEDROCK_ENABLE_IMDS"); v == "1" || strings.EqualFold(v, "true") {
		return false
	}
	// Running inside ECS/EKS — IMDS (or its container equivalent) is legit.
	if os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI") != "" ||
		os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI") != "" ||
		os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE") != "" ||
		os.Getenv("ECS_CONTAINER_METADATA_URI") != "" ||
		os.Getenv("ECS_CONTAINER_METADATA_URI_V4") != "" {
		return false
	}
	// If static creds or a profile are set, IMDS won't be reached anyway,
	// but disabling it costs nothing and guarantees no hang on misconfig.
	return true
}

func (c *BedrockClient) ensureRuntime(ctx context.Context) error {
	if c.runtime != nil {
		return nil
	}
	runtime, resolvedRegion, err := LoadBedrockRuntime(ctx, c.region, c.profile, c.logger)
	if err != nil {
		return err
	}
	// The control-plane client is only used by the chat client (ListModels);
	// the embedding provider doesn't need it, which is why LoadBedrockRuntime
	// returns just the runtime. We rebuild the AWS config locally here only
	// to construct the bedrock control client with the same credential chain.
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsLoadOptions(c.region, c.profile, c.logger)...)
	if err != nil {
		return fmt.Errorf("bedrock: failed to load control-plane AWS config: %w", err)
	}
	// Control plane (ListModels/ListInferenceProfiles): BEDROCK_BASE_URL
	// covers it too, so one variable is enough for corporate gateways and
	// custom DNS that front the whole Bedrock surface on a single host.
	// BEDROCK_CONTROL_BASE_URL is the optional per-plane override for
	// setups where the control plane genuinely lives on another host —
	// AWS VPC interface endpoints are created per service (bedrock vs
	// bedrock-runtime), each with its own DNS name. The AWS-standard
	// AWS_ENDPOINT_URL_BEDROCK is honored by the SDK inside NewFromConfig
	// when neither chatcli-native variable is set.
	controlBase, err := envBaseURL("BEDROCK_CONTROL_BASE_URL")
	if err != nil {
		return err
	}
	controlSource := "BEDROCK_CONTROL_BASE_URL"
	if controlBase == "" {
		if controlBase, err = envBaseURL("BEDROCK_BASE_URL"); err != nil {
			return err
		}
		controlSource = "BEDROCK_BASE_URL"
	}
	var controlOpts []func(*bedrocksvc.Options)
	if controlBase != "" {
		controlOpts = append(controlOpts, func(o *bedrocksvc.Options) {
			o.BaseEndpoint = aws.String(controlBase)
		})
		c.logger.Info("bedrock: custom control-plane endpoint configured",
			zap.String("endpoint", controlBase),
			zap.String("source", controlSource))
	}
	c.region = resolvedRegion
	c.runtime = runtime
	c.control = bedrocksvc.NewFromConfig(cfg, controlOpts...)
	c.credentials = cfg.Credentials
	c.logger.Info(i18n.T("llm.info.configuring_provider", "Bedrock"),
		zap.String("region", c.region),
		zap.String("endpoint", RuntimeEndpointURL(c.region)),
		zap.String("model", c.model))
	return nil
}

// maybeResolveProfileModel resolves the foundation model behind an
// application inference profile the user configured directly (env/config)
// without going through /model discovery. One control-plane
// GetInferenceProfile call, attempted once per client; the result lands in
// the catalog so max-tokens sizing, context-window sizing and family
// dispatch see the real model instead of the worst-case fallbacks. A
// lookup failure (missing bedrock:GetInferenceProfile permission, network)
// is logged and the conservative fallbacks stay in effect.
func (c *BedrockClient) maybeResolveProfileModel(ctx context.Context) {
	if !strings.Contains(strings.ToLower(c.model), "application-inference-profile") {
		return
	}
	if _, ok := catalog.Resolve(catalog.ProviderBedrock, c.model); ok {
		return
	}
	if c.profileLookupDone.Swap(true) {
		return
	}
	out, err := c.control.GetInferenceProfile(ctx, &bedrocksvc.GetInferenceProfileInput{
		InferenceProfileIdentifier: aws.String(c.model),
	})
	if err != nil {
		c.logger.Warn("bedrock: could not resolve application inference profile; conservative catalog fallbacks stay in effect",
			zap.String("model", c.model), zap.Error(err))
		return
	}
	displayName := c.model
	if out.InferenceProfileName != nil && *out.InferenceProfileName != "" {
		displayName = i18n.T("llm.bedrock.app_profile_display", *out.InferenceProfileName)
	}
	underlying := firstProfileModelID(out.Models)
	registerBedrockModel(c.model, displayName, underlying)
	c.logger.Info("bedrock: application inference profile resolved",
		zap.String("profile", c.model),
		zap.String("underlying_model", underlying))
}

// SendPrompt dispatches to the correct body schema based on the resolved
// model family (Anthropic Messages vs. OpenAI Chat Completions).
// Retries are delegated to utils.Retry inside each family-specific path.
func (c *BedrockClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	// Clear per-call usage so an errored or usage-less response falls back
	// to estimation instead of re-counting the previous call (see usage.go).
	c.resetUsage()

	if err := c.ensureRuntime(ctx); err != nil {
		return "", err
	}
	c.maybeResolveProfileModel(ctx)

	family := resolveFamily(c.model)
	if override := strings.ToLower(strings.TrimSpace(os.Getenv("BEDROCK_PROVIDER"))); family == familyConverse && isConverseOnlyVendor(c.model) &&
		(override == "anthropic" || override == "claude" || override == "openai" || override == "gpt") {
		c.logger.Warn(i18n.T("llm.bedrock.provider_override_conflict", override, c.model))
	}
	c.logger.Debug("bedrock: dispatching request", zap.String("model", c.model), zap.String("family", string(family)))

	switch family {
	case familyOpenAI:
		return c.sendPromptOpenAI(ctx, prompt, history, maxTokens)
	case familyConverse:
		return c.sendPromptConverse(ctx, prompt, history, maxTokens)
	default:
		if usesMantleEndpoint(c.model) {
			resp, err := c.sendPromptAnthropicMantle(ctx, prompt, history, maxTokens)
			if err == nil || resolveAnthropicEndpointMode() != endpointModeAuto || ctx.Err() != nil {
				// Success, operator-pinned "mantle", or the caller is gone:
				// nothing to fall back to.
				return resp, err
			}
			// Auto mode: the Mantle surface failed after its own retries —
			// re-route the same request through the legacy InvokeModel
			// runtime under an inference-profile id. Same Messages payload,
			// different endpoint; regional outages, VPC gaps and account-
			// level Mantle restrictions recover here. Genuinely invalid
			// requests fail again on the runtime side and surface that
			// error (annotated with the original Mantle failure).
			fallbackModel := invokeFallbackModelID(c.model)
			c.logger.Warn(i18n.T("llm.bedrock.mantle_fallback_invoke", c.model, fallbackModel), zap.Error(err))
			resp, invokeErr := c.sendPromptAnthropicModel(ctx, fallbackModel, prompt, history, maxTokens)
			if invokeErr != nil {
				// Both chains stay inspectable (%w twice): errors.As walks
				// invokeErr first, so payload-recovery reads the InvokeModel
				// request size, while the Mantle failure stays visible for
				// WAF/proxy classification.
				return "", fmt.Errorf("%w (bedrock-mantle previously failed: %w)", invokeErr, err)
			}
			return resp, nil
		}
		return c.sendPromptAnthropic(ctx, prompt, history, maxTokens)
	}
}

// sendPromptAnthropic uses the Anthropic Messages body schema on Bedrock.
func (c *BedrockClient) sendPromptAnthropic(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	return c.sendPromptAnthropicModel(ctx, c.model, prompt, history, maxTokens)
}

// sendPromptAnthropicModel is sendPromptAnthropic with an explicit wire
// model id — the Mantle→InvokeModel fallback re-routes the configured
// model under its inference-profile spelling without mutating c.model
// (the next call still tries Mantle first; a healthy Mantle wins again).
func (c *BedrockClient) sendPromptAnthropicModel(ctx context.Context, wireModel, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	messages, systemObj := c.buildMessagesAndSystem(prompt, history)

	reqBody := map[string]interface{}{
		"anthropic_version": anthropicBedrockVersion,
		"max_tokens":        effectiveMaxTokens,
		"messages":          messages,
	}
	if systemObj != nil {
		reqBody["system"] = systemObj
	}

	applyAnthropicThinkingForEffort(reqBody, wireModel, ctx)

	// Rolling conversation breakpoint. Bedrock accepts "ttl":"1h" in the
	// cache_control object for Claude 4.5+ (docs.aws.amazon.com/bedrock/
	// latest/userguide/prompt-caching.html), so the configured TTL applies
	// there; older Claude keeps the 5-minute wire default.
	client.MarkAnthropicHistoryBreakpoint(client.AnthropicMessages{Maps: messages}, client.AnthropicCacheMarkerWithTTL(extendedCacheTTLModel(wireModel)))
	enforceCacheControlBudget(reqBody, anthropicMaxCacheBreakpoints)

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	start := time.Now()
	client.LogRequestStart(c.logger, "BEDROCK", wireModel,
		zap.String("family", string(familyAnthropic)),
		zap.String("region", c.region),
		zap.String("endpoint", RuntimeEndpointURL(c.region)),
		zap.Int("payload_bytes", len(payload)),
		zap.Int("history_len", len(history)),
		zap.Int("max_tokens", effectiveMaxTokens),
		zap.Int("cache_markers", client.CountAnthropicCacheMarkers(reqBody)),
	)

	responseText, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		out, err := c.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
			ModelId:     stringPtr(wireModel),
			ContentType: stringPtr("application/json"),
			Accept:      stringPtr("application/json"),
			Body:        payload,
		})
		if err != nil {
			return "", wrapBedrockInferenceProfileError(wireModel, err)
		}
		text, perr := parseAnthropicBody(out.Body)
		if perr == nil {
			c.captureAnthropicUsage(out.Body)
		}
		return text, perr
	})

	if err != nil {
		client.LogRequestFinish(c.logger, "BEDROCK", wireModel, "error", time.Since(start),
			zap.String("family", string(familyAnthropic)),
		)
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "Bedrock"), zap.Error(err))
		// Annotate with the request size so agent-mode payload recovery can
		// learn the proxy/WAF cap from the exact size that was rejected.
		return "", client.WithRequestSize(err, len(payload))
	}
	client.LogRequestFinish(c.logger, "BEDROCK", wireModel, "success", time.Since(start),
		zap.String("family", string(familyAnthropic)),
		zap.Int("response_chars", len(responseText)),
	)
	return responseText, nil
}

func parseAnthropicBody(body []byte) (string, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("bedrock: decode response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("bedrock: %s: %s", result.Error.Type, result.Error.Message)
	}
	var out strings.Builder
	for _, blk := range result.Content {
		if blk.Type == "text" {
			out.WriteString(blk.Text)
		}
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("%s", i18n.T("llm.error.no_response", "Bedrock"))
	}
	return out.String(), nil
}

// buildMessagesAndSystem converts internal history into the Anthropic
// messages payload. Consecutive same-role messages are accepted by Bedrock.
func (c *BedrockClient) buildMessagesAndSystem(prompt string, history []models.Message) ([]map[string]interface{}, interface{}) {
	var messages []map[string]interface{}
	var systemBlocks []map[string]interface{}
	var plainSystemParts []string

	for _, msg := range history {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			messages = append(messages, map[string]interface{}{"role": "assistant", "content": visionwire.AnthropicContent(msg.Content, msg.Images)})
		case "system":
			if len(msg.SystemParts) > 0 {
				for _, part := range msg.SystemParts {
					block := map[string]interface{}{
						"type": "text",
						"text": part.Text,
					}
					if part.CacheControl != nil {
						// Same marker (and ttl) as the history breakpoint:
						// Anthropic requires longer-lived breakpoints to
						// precede shorter ones, so system and history must
						// carry one lifetime.
						block["cache_control"] = client.AnthropicCacheMarkerWithTTL(extendedCacheTTLModel(c.model))
					}
					systemBlocks = append(systemBlocks, block)
				}
			} else if msg.Content != "" {
				plainSystemParts = append(plainSystemParts, msg.Content)
			}
		default:
			messages = append(messages, map[string]interface{}{"role": "user", "content": visionwire.AnthropicContent(msg.Content, msg.Images)})
		}
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]interface{}{"role": "user", "content": prompt})
		}
	}

	if len(systemBlocks) > 0 {
		for _, part := range plainSystemParts {
			systemBlocks = append(systemBlocks, map[string]interface{}{"type": "text", "text": part})
		}
		return messages, systemBlocks
	}
	if len(plainSystemParts) > 0 {
		return messages, strings.Join(plainSystemParts, "\n\n")
	}
	return messages, nil
}

// extendedCacheTTLModel reports whether Bedrock honors the 1-hour
// cache TTL for the model: every Claude from the 4.5 generation on
// (Sonnet/Opus 4.5+, Haiku 4.5, the 5.x line, Fable/Mythos). Claude 3.x
// and non-Anthropic vendors keep the 5-minute default.
// SupportsExtendedCacheTTL reports whether the Bedrock Claude model accepts
// the 1h cache ttl (Claude 4.5+); telemetry reads it to report the TTL the
// request actually carried.
func SupportsExtendedCacheTTL(model string) bool { return extendedCacheTTLModel(model) }

func extendedCacheTTLModel(model string) bool {
	m := strings.ToLower(model)
	if !strings.Contains(m, "claude") {
		return false
	}
	for _, old := range []string{"claude-3", "claude-v2", "claude-instant", "sonnet-4-20", "opus-4-20", "opus-4-1", "claude-sonnet-4-v", "claude-opus-4-v"} {
		if strings.Contains(m, old) {
			return false
		}
	}
	return true
}

func supportsExtendedThinking(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "opus-4") ||
		strings.Contains(m, "sonnet-4") ||
		strings.Contains(m, "3-7-sonnet") ||
		strings.Contains(m, "claude-3-7")
}

// applyAnthropicThinkingForEffort routes the per-turn skill effort hint
// onto the Anthropic Messages body sent through Bedrock.
//
// Dispatch matches the Claude API client by design — both paths converge
// on the same schema and same models:
//   - effort unset → no thinking block (caller respects user intent)
//   - model has the catalog "adaptive_thinking" capability (Opus 4.7+)
//     → `thinking:{type:"adaptive"}`. Budgeted thinking returns 400 on
//     these models per Anthropic's 4.8 migration guide.
//   - otherwise, if the model supports budgeted extended thinking →
//     `thinking:{type:"enabled", budget_tokens:N}` with max_tokens raised
//     to budget+1024 when necessary (max_tokens must strictly exceed
//     budget_tokens or the API rejects the request).
//   - non-thinking model with effort set → silently no-op.
//
// Returns true when a thinking block was attached, false otherwise.
// Mutates reqBody in place.
func applyAnthropicThinkingForEffort(reqBody map[string]interface{}, model string, ctx context.Context) bool {
	effort := client.EffortFromContext(ctx)
	if effort == client.EffortUnset {
		return false
	}
	if catalog.HasCapability(catalog.ProviderBedrock, model, "adaptive_thinking") {
		reqBody["thinking"] = map[string]interface{}{"type": "adaptive"}
		return true
	}
	budget := client.ThinkingBudgetForEffort(effort)
	if budget <= 0 || !supportsExtendedThinking(model) {
		return false
	}
	required := budget + 1024
	if v, ok := reqBody["max_tokens"].(int); ok && v < required {
		reqBody["max_tokens"] = required
	}
	reqBody["thinking"] = map[string]interface{}{
		"type":          "enabled",
		"budget_tokens": budget,
	}
	return true
}

func stringPtr(s string) *string { return &s }

// buildCorporateHTTPClient returns a custom *http.Client for AWS SDK when the
// user has set chatcli-specific TLS overrides, meant for corporate proxies
// performing TLS interception with a private CA.
//
//   - CHATCLI_BEDROCK_CA_BUNDLE=/path/to/pem
//     Merges the PEM into the system cert pool and uses it as RootCAs.
//     Takes precedence over AWS_CA_BUNDLE when set.
//
//   - CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY=true
//     Disables TLS verification entirely (equivalent to NODE_TLS_REJECT_UNAUTHORIZED=0).
//     INSECURE — use only to confirm a corporate-proxy issue, then fix the CA bundle.
//
// Returns (nil, "") when no override is set, so the SDK falls back to its
// default HTTP client (which honors AWS_CA_BUNDLE, HTTPS_PROXY, etc.).
func buildCorporateHTTPClient(logger *zap.Logger) (aws.HTTPClient, string) {
	insecure := strings.EqualFold(strings.TrimSpace(os.Getenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY")), "true")
	bundlePath := strings.TrimSpace(os.Getenv("CHATCLI_BEDROCK_CA_BUNDLE"))

	// Bedrock goes through the AWS SDK's own HTTP client, so the global
	// corporate-trust overrides applied by utils.ApplyGlobalTLSTrust don't
	// reach it. Fall back to them per variable; the Bedrock-specific ones
	// keep precedence.
	if !insecure {
		insecure = strings.EqualFold(strings.TrimSpace(os.Getenv("CHATCLI_TLS_INSECURE_SKIP_VERIFY")), "true")
	}
	if bundlePath == "" {
		bundlePath = strings.TrimSpace(os.Getenv("CHATCLI_CA_BUNDLE"))
	}

	if !insecure && bundlePath == "" {
		return nil, ""
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	var note string

	if insecure {
		tlsCfg.InsecureSkipVerify = true
		note = "Bedrock: TLS verification is DISABLED (CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY or CHATCLI_TLS_INSECURE_SKIP_VERIFY). Do NOT use in production; configure CHATCLI_BEDROCK_CA_BUNDLE or CHATCLI_CA_BUNDLE with your corporate CA instead."
	} else {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		// The CA bundle path is intentionally operator-supplied via env var —
		// this is the documented way to trust a corporate proxy's CA. The
		// file is read as-is; we don't mount it or open relative to a root.
		// #nosec G304 G703 -- user-controlled path by design (CHATCLI_BEDROCK_CA_BUNDLE)
		pem, err := os.ReadFile(bundlePath)
		if err != nil {
			logger.Warn("Bedrock: failed to read CHATCLI_BEDROCK_CA_BUNDLE", zap.String("path", bundlePath), zap.Error(err))
			return nil, ""
		}
		if !pool.AppendCertsFromPEM(pem) {
			logger.Warn("Bedrock: no valid certificates found in CHATCLI_BEDROCK_CA_BUNDLE", zap.String("path", bundlePath))
			return nil, ""
		}
		tlsCfg.RootCAs = pool
		logger.Info("Bedrock: using CHATCLI_BEDROCK_CA_BUNDLE for TLS trust", zap.String("path", bundlePath))
	}

	// Use awshttp.BuildableClient so the AWS SDK can still layer in its own
	// transport options (e.g. AWS_CA_BUNDLE merging) on top of ours.
	client := awshttp.NewBuildableClient().
		WithTimeout(10 * time.Minute).
		WithTransportOptions(func(t *http.Transport) {
			t.Proxy = http.ProxyFromEnvironment
			t.TLSClientConfig = tlsCfg
			t.TLSHandshakeTimeout = 10 * time.Second
			t.IdleConnTimeout = 90 * time.Second
			t.MaxIdleConns = 100
			t.ForceAttemptHTTP2 = true
		})
	return client, note
}

// anthropicBedrockVersion is the required body field for Claude on Bedrock.
// See: https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html
const anthropicBedrockVersion = "bedrock-2023-05-31"

// bedrockListModelsTimeout bounds the whole control-plane listing when the
// caller supplied no deadline; the /switch catalog fallback makes a slow
// answer worthless anyway.
const bedrockListModelsTimeout = 30 * time.Second

// bedrockListEndpointHint travels in the structured warn when control-plane
// listing fails — the most common corporate cause is the default AWS host
// being unreachable or TLS-intercepted, both solved by endpoint/CA config.
const bedrockListEndpointHint = "if your network requires a custom Bedrock endpoint or CA, " +
	"set BEDROCK_BASE_URL or BEDROCK_CONTROL_BASE_URL, and CHATCLI_BEDROCK_CA_BUNDLE for a private CA; " +
	"chat keeps working via the model catalog meanwhile"

// ListModels queries Bedrock's control plane for Anthropic models the account
// has access to. Returns both foundation models (direct on-demand, e.g. Claude
// 3.x) AND inference profiles (global./us./eu./apac., required for Claude 3.7
// and newer). Implements client.ModelLister so `/switch --model` suggests IDs
// that are actually invokable.
func (c *BedrockClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	if err := c.ensureRuntime(ctx); err != nil {
		return nil, err
	}

	// Listing is best-effort UX — the catalog is the fallback — so it must
	// never freeze the REPL. A corporate firewall that blackholes the
	// control-plane host would otherwise hang through minutes of SDK
	// connect retries; bound the whole listing when the caller didn't.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, bedrockListModelsTimeout)
		defer cancel()
	}

	seen := make(map[string]bool)
	var result []client.ModelInfo

	// 1) Foundation models — text-output, on-demand-invokable.
	//
	// We deliberately do NOT maintain a hardcoded provider allowlist here.
	// AWS continuously onboards new providers (Moonshot, MiniMax, Qwen,
	// Z.AI, Google Gemma, NVIDIA Nemotron, TwelveLabs, …) and any
	// provider-name allowlist would silently hide them until we ship a
	// release. Two AWS-side signals do the gating instead:
	//
	//   - ByOutputModality: ModelModalityText excludes embedding-only and
	//     image-only models server-side.
	//   - supportsOnDemand(InferenceTypesSupported) drops models whose
	//     bare ID isn't invokable directly — those are exposed via the
	//     matching inference profile in the next loop, which avoids the
	//     "user picks anthropic.claude-3-7-... and gets ValidationException"
	//     trap.
	//
	// If a listed ID happens to be incompatible with our dispatch (rare),
	// wrapBedrockInferenceProfileError surfaces a helpful message instead
	// of a cryptic AWS error.
	fm, err := c.control.ListFoundationModels(ctx, &bedrocksvc.ListFoundationModelsInput{
		ByOutputModality: bedrocktypes.ModelModalityText,
	})
	if err != nil {
		c.logger.Warn("bedrock: ListFoundationModels failed", zap.Error(err),
			zap.String("hint", bedrockListEndpointHint))
	} else {
		for _, s := range fm.ModelSummaries {
			id, displayName := derefModelSummary(s.ModelId, s.ModelName, s.ProviderName)
			if id == "" || seen[id] {
				continue
			}
			if !supportsOnDemand(s.InferenceTypesSupported) {
				c.logger.Debug("bedrock: skipping foundation model without ON_DEMAND inference",
					zap.String("model_id", id),
					zap.Any("inference_types", s.InferenceTypesSupported))
				continue
			}
			seen[id] = true
			result = append(result, client.ModelInfo{
				ID:          id,
				DisplayName: displayName,
				Source:      client.ModelSourceAPI,
			})
			registerBedrockModel(id, displayName, "")
		}
	}

	// 2) System-defined inference profiles — needed for Claude 3.7, 4.x,
	//    4.5, 4.6, 4.7 and any cross-region-only profile across other
	//    providers.
	result = c.appendInferenceProfiles(ctx, bedrocktypes.InferenceProfileTypeSystemDefined, seen, result)

	// 3) Application inference profiles — user-created wrappers for cost
	//    tracking and routing. Their id is an opaque ARN, so the underlying
	//    foundation model (from the profile's model list) feeds the catalog
	//    specs: without it the profile would degrade to worst-case
	//    fallbacks for output cap, context window and family dispatch.
	result = c.appendInferenceProfiles(ctx, bedrocktypes.InferenceProfileTypeApplication, seen, result)

	c.logger.Info(i18n.T("llm.info.fetched_models", "Bedrock"), zap.Int("count", len(result)))
	return result, nil
}

// appendInferenceProfiles lists the inference profiles of one type and
// appends the unseen ones to result, registering each in the catalog.
// System-defined profiles are selected by their id ("us.anthropic.…");
// application profiles must be selected by ARN — that is the id AWS
// accepts at invocation time.
func (c *BedrockClient) appendInferenceProfiles(ctx context.Context, profileType bedrocktypes.InferenceProfileType, seen map[string]bool, result []client.ModelInfo) []client.ModelInfo {
	paginator := bedrocksvc.NewListInferenceProfilesPaginator(c.control, &bedrocksvc.ListInferenceProfilesInput{
		TypeEquals: profileType,
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			c.logger.Warn("bedrock: ListInferenceProfiles failed", zap.Error(err),
				zap.String("type", string(profileType)),
				zap.String("hint", bedrockListEndpointHint))
			break
		}
		for _, p := range page.InferenceProfileSummaries {
			id, displayName, underlying := profileEntry(p, profileType)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			result = append(result, client.ModelInfo{
				ID:          id,
				DisplayName: displayName,
				Source:      client.ModelSourceAPI,
			})
			registerBedrockModel(id, displayName, underlying)
		}
	}
	return result
}

// profileEntry projects an InferenceProfileSummary onto the (id,
// displayName, underlyingModelID) triple the listing needs.
func profileEntry(p bedrocktypes.InferenceProfileSummary, profileType bedrocktypes.InferenceProfileType) (string, string, string) {
	id := ""
	underlying := ""
	nameKey := "llm.bedrock.profile_display"
	if profileType == bedrocktypes.InferenceProfileTypeApplication {
		nameKey = "llm.bedrock.app_profile_display"
		if p.InferenceProfileArn != nil {
			id = *p.InferenceProfileArn
		}
		underlying = firstProfileModelID(p.Models)
	}
	if id == "" && p.InferenceProfileId != nil {
		id = *p.InferenceProfileId
	}
	displayName := id
	if p.InferenceProfileName != nil && *p.InferenceProfileName != "" {
		displayName = i18n.T(nameKey, *p.InferenceProfileName)
	}
	return id, displayName, underlying
}

// supportsOnDemand returns true when ON_DEMAND is one of the inference
// types declared by the foundation model. Models without ON_DEMAND require
// an inference profile to be invoked and would fail with ValidationException
// if selected by the bare model ID.
func supportsOnDemand(types []bedrocktypes.InferenceType) bool {
	for _, t := range types {
		if t == bedrocktypes.InferenceTypeOnDemand {
			return true
		}
	}
	return false
}

func derefModelSummary(id, name, provider *string) (string, string) {
	idStr := ""
	if id != nil {
		idStr = *id
	}
	display := idStr
	switch {
	case name != nil && *name != "" && provider != nil && *provider != "":
		display = *provider + " " + *name + " (Bedrock)"
	case name != nil && *name != "":
		display = *name + " (Bedrock)"
	}
	return idStr, display
}

// registerBedrockModel adds a discovered model to the catalog. underlyingID
// names the foundation model behind an application inference profile (empty
// when the id IS the model). The registered meta inherits context window,
// output ceiling and capabilities from the underlying model's entry so an
// opaque profile ARN does not degrade to the conservative fallbacks (tiny
// output cap, 128K window, Converse routing for Claude — which would drop
// cache_control markers on the wire).
func registerBedrockModel(id, displayName, underlyingID string) {
	if _, ok := catalog.Resolve(catalog.ProviderBedrock, id); ok {
		return
	}
	meta := catalog.ModelMeta{
		ID:           id,
		Aliases:      []string{id},
		DisplayName:  displayName,
		Provider:     catalog.ProviderBedrock,
		PreferredAPI: preferredAPIFor(id),
	}
	specSource := id
	if underlyingID != "" {
		specSource = underlyingID
		meta.PreferredAPI = preferredAPIFor(underlyingID)
	}
	// Discovered IDs inherit context window, output ceiling and
	// capabilities from the best spec donor (Bedrock catalog, then the
	// first-party Claude catalog — Resolve matches the embedded "claude-…"
	// segment inside profile-prefixed IDs). Without this, an
	// unknown-but-real Claude id would fall back to the generic 50K
	// context default and trip auto-compaction on almost every turn.
	if base, ok := underlyingBedrockMeta(specSource); ok {
		meta.ContextWindow = base.ContextWindow
		meta.MaxOutputTokens = base.MaxOutputTokens
		caps := filterBedrockCapabilities(base.Capabilities)
		if underlyingID != "" {
			// Profile ARNs are invoked via InvokeModel/Converse; the Mantle
			// Messages endpoint serves canonical "anthropic." ids only and
			// would 404 on an ARN.
			caps = stripCapability(caps, "bedrock_mantle_only")
		}
		meta.Capabilities = caps
	}
	catalog.Register(meta)
}

// underlyingBedrockMeta finds the best spec donor for a discovered id: the
// static Bedrock catalog first, then the first-party Claude catalog (covers
// brand-new Claude ids Bedrock lists before this catalog ships an entry).
func underlyingBedrockMeta(id string) (catalog.ModelMeta, bool) {
	if meta, ok := catalog.Resolve(catalog.ProviderBedrock, id); ok {
		return meta, true
	}
	if isClaudeModelID(id) {
		if meta, ok := catalog.Resolve(catalog.ProviderClaudeAI, id); ok {
			return meta, true
		}
	}
	return catalog.ModelMeta{}, false
}

// stripCapability returns caps without the named capability, preserving order.
func stripCapability(caps []string, name string) []string {
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		if c != name {
			out = append(out, c)
		}
	}
	return out
}

// firstProfileModelID extracts the foundation-model id from the model ARNs
// an inference profile routes to ("arn:aws:bedrock:us-east-1::foundation-
// model/anthropic.claude-sonnet-5" → "anthropic.claude-sonnet-5"). Profiles
// can fan out to several regional copies of the same model — the first
// entry is representative.
func firstProfileModelID(profileModels []bedrocktypes.InferenceProfileModel) string {
	for _, pm := range profileModels {
		if pm.ModelArn == nil {
			continue
		}
		arn := *pm.ModelArn
		if idx := strings.LastIndex(arn, "/"); idx >= 0 && idx+1 < len(arn) {
			return arn[idx+1:]
		}
	}
	return ""
}

// preferredAPIFor matches the dispatch family to a catalog PreferredAPI so
// downstream code (token sizing, capability lookups) does the right thing
// for the provider we just discovered. Anthropic keeps its messages API
// hint; OpenAI gets chat-completions; everything else marks itself as
// chat-completions-compatible too — Converse normalizes onto that shape.
func preferredAPIFor(id string) catalog.PreferredAPI {
	switch resolveFamily(id) {
	case familyAnthropic:
		return catalog.APIAnthropicMessages
	default:
		return catalog.APIChatCompletions
	}
}

// Ensure BedrockClient satisfies the LLMClient and ModelLister interfaces.
var _ client.LLMClient = (*BedrockClient)(nil)
var _ client.ModelLister = (*BedrockClient)(nil)

// Keep config import used for defaults.
var _ = config.DefaultMaxRetries
