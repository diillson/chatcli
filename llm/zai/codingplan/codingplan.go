/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package codingplan decides whether Z.AI requests are going to a GLM
// Coding Plan endpoint. It is a leaf (environment plus config only) so the
// pricing engine can consult it without pulling the whole zai client in:
// tokens sent to the subscription endpoint are not invoiced per token,
// and every surface that prices usage must agree on that.
package codingplan

import (
	"os"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/config"
)

// ResolveAPIURL returns the effective Z.AI chat-completions endpoint.
// Precedence: an explicit ZAI_API_URL, then ZAI_USE_CODING_PLAN (the GLM
// Coding Plan subscription endpoint), then the given fallback (the official
// pay-as-you-go endpoint). The same platform key works on both endpoints;
// the /coding/ path is what decides whether a request debits the plan or
// the credits.
func ResolveAPIURL(fallback string) string {
	if v := strings.TrimSpace(os.Getenv("ZAI_API_URL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("ZAI_USE_CODING_PLAN")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil && b {
			return config.ZAICodingAPIURL
		}
	}
	return fallback
}

// Active reports whether requests are going to a GLM Coding Plan endpoint,
// either through ZAI_USE_CODING_PLAN or through a ZAI_API_URL that points
// at an /api/coding/ path (api.z.ai or open.bigmodel.cn). Subscription
// tokens are not billed per token, so the pricing engine zeroes the rate
// while this holds.
func Active() bool {
	return strings.Contains(ResolveAPIURL(config.ZAIAPIURL), "/api/coding/")
}
