/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package codingplan

import (
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/stretchr/testify/assert"
)

func TestResolveAPIURL_Precedence(t *testing.T) {
	t.Setenv("ZAI_API_URL", "")
	t.Setenv("ZAI_USE_CODING_PLAN", "")
	assert.Equal(t, "https://fallback", ResolveAPIURL("https://fallback"))
	assert.False(t, Active())

	t.Setenv("ZAI_USE_CODING_PLAN", "true")
	assert.Equal(t, config.ZAICodingAPIURL, ResolveAPIURL("https://fallback"))
	assert.True(t, Active())

	t.Setenv("ZAI_USE_CODING_PLAN", "not-a-bool")
	assert.Equal(t, "https://fallback", ResolveAPIURL("https://fallback"))

	t.Setenv("ZAI_API_URL", "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions")
	t.Setenv("ZAI_USE_CODING_PLAN", "false")
	assert.Equal(t, "https://open.bigmodel.cn/api/coding/paas/v4/chat/completions", ResolveAPIURL("x"))
	assert.True(t, Active(), "an explicit /api/coding/ URL is the plan too")

	t.Setenv("ZAI_API_URL", "https://api.z.ai/api/paas/v4/chat/completions")
	assert.False(t, Active())
}
