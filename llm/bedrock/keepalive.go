/*
 * ChatCLI - Bedrock prompt-cache keep-alive (Mantle Messages endpoint)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"fmt"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// KeepPromptCacheWarm re-sends the last Mantle Messages request asking
// for one output token, so the cache entry at the request's breakpoints
// is refreshed for the price of a read. Only the Mantle surface can do
// it today: it is the Messages wire, so the remembered body is re-sent as
// is. Converse and InvokeModel build their requests through the SDK and
// report unsupported, which stops the caller from asking again.
func (c *BedrockClient) KeepPromptCacheWarm(ctx context.Context) (*models.UsageInfo, error) {
	if c == nil || resolveFamily(c.model) != familyAnthropic || !usesMantleEndpoint(c.model) {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	last, ok := c.lastMantleRequest.Take()
	if !ok {
		return nil, client.ErrPromptCacheKeepAliveUnsupported
	}
	body, err := client.KeepAliveRequestBody(last, 1)
	if err != nil {
		return nil, err
	}
	c.resetUsage()
	if _, err := c.doMantleRequest(ctx, mantleMessagesURL(c.region), body); err != nil {
		return nil, err
	}
	usage := c.LastUsage()
	if usage == nil {
		return nil, fmt.Errorf("bedrock-mantle: keep-alive response carried no usage")
	}
	c.logger.Debug("bedrock-mantle: prompt cache kept warm",
		zap.Int("cache_read", usage.CacheReadInputTokens), zap.String("model", c.model))
	return usage, nil
}
