/*
 * ChatCLI - Shared AWS runtime client construction.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"go.uber.org/zap"
)

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
	return bedrockruntime.NewFromConfig(cfg), cfg.Region, nil
}
