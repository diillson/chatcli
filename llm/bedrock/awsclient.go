/*
 * ChatCLI - Shared AWS runtime client construction.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"go.uber.org/zap"
)

// runtimeBaseURLOverride resolves the operator-configured Bedrock runtime
// base URL, if any. Corporate environments (VPC interface endpoints,
// API gateways, custom DNS) need the data-plane host swapped out — the
// same need Claude Code covers with ANTHROPIC_BEDROCK_BASE_URL.
//
// Precedence: BEDROCK_BASE_URL (chatcli-native, always wins — it is set
// on the client as BaseEndpoint, which overrides SDK endpoint resolution)
// → the AWS-standard AWS_ENDPOINT_URL_BEDROCK_RUNTIME / AWS_ENDPOINT_URL,
// which the SDK itself applies inside NewFromConfig unless
// AWS_IGNORE_CONFIGURED_ENDPOINT_URLS disables them. The AWS-standard
// branch exists here only so logs report the endpoint actually in use;
// a services-section endpoint_url in ~/.aws/config is also honored by
// the SDK but is not reflected in logs.
func runtimeBaseURLOverride() string {
	if v := strings.TrimSpace(os.Getenv("BEDROCK_BASE_URL")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if ig := strings.ToLower(strings.TrimSpace(os.Getenv("AWS_IGNORE_CONFIGURED_ENDPOINT_URLS"))); ig == "true" || ig == "1" {
		return ""
	}
	for _, key := range []string{"AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "AWS_ENDPOINT_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	return ""
}

// envBaseURL reads an endpoint-override variable and validates it,
// returning "" when unset. The error names the variable so a bad value
// fails fast with an actionable message at client construction instead
// of producing opaque SigV4/transport errors mid-request.
func envBaseURL(key string) (string, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return "", nil
	}
	normalized, err := normalizeBaseURL(raw)
	if err != nil {
		return "", fmt.Errorf("bedrock: invalid %s %q: %w", key, raw, err)
	}
	return normalized, nil
}

// normalizeBaseURL validates an operator-supplied endpoint override and
// returns it without a trailing slash. Only absolute http(s) URLs with a
// host are accepted.
func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("must be an absolute http(s) URL, e.g. https://bedrock-runtime.internal.example.com")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// RuntimeEndpointURL returns the AWS Bedrock runtime endpoint for a given
// region, honoring operator endpoint overrides (BEDROCK_BASE_URL and the
// AWS-standard AWS_ENDPOINT_URL* variables). The SDK resolves the default
// internally through the service endpoint resolver, but we expose it
// explicitly so it can land in structured logs — every other LLM client
// in the project logs the URL it talks to, and Bedrock used to be the
// odd one out.
//
// Returns an empty string for an empty region with no override so
// callers can decide whether to log the field at all.
func RuntimeEndpointURL(region string) string {
	if custom := runtimeBaseURLOverride(); custom != "" {
		return custom
	}
	if region == "" {
		return ""
	}
	return "https://bedrock-runtime." + region + ".amazonaws.com"
}

// LoadBedrockRuntime builds a bedrockruntime.Client using the same
// credential chain, region resolution, IMDS gating and corporate-CA
// support that the chat client (BedrockClient) uses. Exported so other
// callers — notably the embedding provider in llm/embedding/bedrock.go —
// can consume Bedrock without duplicating the AWS config plumbing.
//
// The returned region is the one effectively in use after the SDK
// finishes resolving env vars / shared config / SSO profile defaults.
// Callers that previously stored the input region should overwrite
// their cached value with this resolved one (the chat client does).
//
// A nil logger is rejected — callers should pass zap.NewNop() if they
// don't care about Bedrock-side observability.
func LoadBedrockRuntime(ctx context.Context, region, profile string, logger *zap.Logger) (*bedrockruntime.Client, string, error) {
	if logger == nil {
		return nil, "", fmt.Errorf("bedrock: logger is required (pass zap.NewNop() if you don't need logs)")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	if httpClient, note := buildCorporateHTTPClient(logger); httpClient != nil {
		opts = append(opts, awsconfig.WithHTTPClient(httpClient))
		if note != "" {
			logger.Warn(note)
		}
	}
	if shouldDisableIMDS() {
		opts = append(opts, awsconfig.WithEC2IMDSClientEnableState(imds.ClientDisabled))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, "", fmt.Errorf("bedrock: failed to load AWS config: %w", err)
	}
	if cfg.Region == "" {
		return nil, "", fmt.Errorf("bedrock: AWS region not configured (set AWS_REGION, BEDROCK_REGION, or configure ~/.aws/config)")
	}
	var clientOpts []func(*bedrockruntime.Options)
	if base, err := envBaseURL("BEDROCK_BASE_URL"); err != nil {
		return nil, "", err
	} else if base != "" {
		// BaseEndpoint set in code overrides the SDK's own endpoint-URL
		// resolution (AWS_ENDPOINT_URL* / shared-config endpoint_url), so
		// the chatcli-native variable always wins.
		clientOpts = append(clientOpts, func(o *bedrockruntime.Options) {
			o.BaseEndpoint = aws.String(base)
		})
		logger.Info("bedrock: custom runtime endpoint configured via BEDROCK_BASE_URL",
			zap.String("endpoint", base))
	}
	return bedrockruntime.NewFromConfig(cfg, clientOpts...), cfg.Region, nil
}
