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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	bedrocksvc "github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// BedrockClient invokes Anthropic Claude models hosted on AWS Bedrock.
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
}

// NewBedrockClient creates a client bound to a model id and region.
// The AWS SDK is initialised lazily on the first call so that a missing
// credentials chain does not break provider discovery.
func NewBedrockClient(model, region, profile string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *BedrockClient {
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
	return catalog.GetMaxTokens(catalog.ProviderBedrock, c.model, 4096)
}

func (c *BedrockClient) ensureRuntime(ctx context.Context) error {
	if c.runtime != nil {
		return nil
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if c.region != "" {
		opts = append(opts, awsconfig.WithRegion(c.region))
	}
	if c.profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(c.profile))
	}
	if httpClient, note := buildCorporateHTTPClient(c.logger); httpClient != nil {
		opts = append(opts, awsconfig.WithHTTPClient(httpClient))
		if note != "" {
			c.logger.Warn(note)
		}
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("bedrock: failed to load AWS config: %w", err)
	}
	if cfg.Region == "" {
		return fmt.Errorf("bedrock: AWS region not configured (set AWS_REGION, BEDROCK_REGION, or configure ~/.aws/config)")
	}
	c.region = cfg.Region
	c.runtime = bedrockruntime.NewFromConfig(cfg)
	c.control = bedrocksvc.NewFromConfig(cfg)
	c.logger.Info(i18n.T("llm.info.configuring_provider", "Bedrock"),
		zap.String("region", c.region),
		zap.String("model", c.model))
	return nil
}

// SendPrompt sends the prompt using Bedrock's InvokeModel API with the
// Anthropic Messages body schema. Retries are delegated to utils.Retry.
func (c *BedrockClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	if err := c.ensureRuntime(ctx); err != nil {
		return "", err
	}

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

	if budget := client.ThinkingBudgetForEffort(client.EffortFromContext(ctx)); budget > 0 && supportsExtendedThinking(c.model) {
		required := budget + 1024
		if v, ok := reqBody["max_tokens"].(int); ok && v < required {
			reqBody["max_tokens"] = required
		}
		reqBody["thinking"] = map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": budget,
		}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	responseText, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		out, err := c.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
			ModelId:     stringPtr(c.model),
			ContentType: stringPtr("application/json"),
			Accept:      stringPtr("application/json"),
			Body:        payload,
		})
		if err != nil {
			return "", err
		}
		return parseAnthropicBody(out.Body)
	})

	if err != nil {
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "Bedrock"), zap.Error(err))
		return "", err
	}
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
			messages = append(messages, map[string]interface{}{"role": "assistant", "content": msg.Content})
		case "system":
			if len(msg.SystemParts) > 0 {
				for _, part := range msg.SystemParts {
					block := map[string]interface{}{
						"type": "text",
						"text": part.Text,
					}
					if part.CacheControl != nil {
						block["cache_control"] = map[string]string{"type": part.CacheControl.Type}
					}
					systemBlocks = append(systemBlocks, block)
				}
			} else if msg.Content != "" {
				plainSystemParts = append(plainSystemParts, msg.Content)
			}
		default:
			messages = append(messages, map[string]interface{}{"role": "user", "content": msg.Content})
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

func supportsExtendedThinking(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "opus-4") ||
		strings.Contains(m, "sonnet-4") ||
		strings.Contains(m, "3-7-sonnet") ||
		strings.Contains(m, "claude-3-7")
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
// default HTTP client (which honours AWS_CA_BUNDLE, HTTPS_PROXY, etc.).
func buildCorporateHTTPClient(logger *zap.Logger) (aws.HTTPClient, string) {
	insecure := strings.EqualFold(strings.TrimSpace(os.Getenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY")), "true")
	bundlePath := strings.TrimSpace(os.Getenv("CHATCLI_BEDROCK_CA_BUNDLE"))

	if !insecure && bundlePath == "" {
		return nil, ""
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	var note string

	if insecure {
		tlsCfg.InsecureSkipVerify = true
		note = "Bedrock: CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY=true — TLS verification is DISABLED. Do NOT use in production; configure CHATCLI_BEDROCK_CA_BUNDLE with your corporate CA instead."
	} else {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
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

// ListModels queries Bedrock's control plane for foundation models available
// in the configured region, filtered to Anthropic. Implements client.ModelLister
// so that `/switch --model` can suggest live models from the user's account.
func (c *BedrockClient) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	if err := c.ensureRuntime(ctx); err != nil {
		return nil, err
	}
	out, err := c.control.ListFoundationModels(ctx, &bedrocksvc.ListFoundationModelsInput{
		ByProvider:       stringPtr("anthropic"),
		ByOutputModality: bedrocktypes.ModelModalityText,
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock: ListFoundationModels: %w", err)
	}
	var result []client.ModelInfo
	for _, s := range out.ModelSummaries {
		id := ""
		if s.ModelId != nil {
			id = *s.ModelId
		}
		if id == "" {
			continue
		}
		displayName := id
		if s.ModelName != nil && *s.ModelName != "" {
			displayName = *s.ModelName + " (Bedrock)"
		}
		result = append(result, client.ModelInfo{
			ID:          id,
			DisplayName: displayName,
			Source:      client.ModelSourceAPI,
		})

		if _, ok := catalog.Resolve(catalog.ProviderBedrock, id); !ok {
			catalog.Register(catalog.ModelMeta{
				ID:           id,
				Aliases:      []string{id},
				DisplayName:  displayName,
				Provider:     catalog.ProviderBedrock,
				PreferredAPI: catalog.APIAnthropicMessages,
			})
		}
	}
	c.logger.Info(i18n.T("llm.info.fetched_models", "Bedrock"), zap.Int("count", len(result)))
	return result, nil
}

// Ensure BedrockClient satisfies the LLMClient and ModelLister interfaces.
var _ client.LLMClient = (*BedrockClient)(nil)
var _ client.ModelLister = (*BedrockClient)(nil)

// Keep config import used for defaults.
var _ = config.DefaultMaxRetries
