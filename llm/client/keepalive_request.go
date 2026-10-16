/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A prompt-cache keep-alive is a real request to the provider, billed as a
 * cache read, so it belongs on the same request log, audit trail and live
 * dashboard as every other request. The adapters send it through
 * LogKeepAliveRequest, which brackets it with LogRequestStart and
 * LogRequestFinish and marks it kind=keepalive so it is never mistaken for
 * a turn.
 */
package client

import (
	"context"
	"errors"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// KeepAliveRequestKind is the kind field of a prompt-cache keep-alive
// request on the request log and the audit trail.
const KeepAliveRequestKind = "keepalive"

// LogKeepAliveRequest runs send — one keep-alive request of payloadBytes —
// between a start and a finish entry of the request log. The finish entry
// carries the usage the provider reported: the cache read the refresh cost.
func LogKeepAliveRequest(ctx context.Context, logger *zap.Logger, provider, model string, payloadBytes int, send func() (*models.UsageInfo, error)) (*models.UsageInfo, error) {
	kind := zap.String("kind", KeepAliveRequestKind)
	start := time.Now()
	LogRequestStart(logger, provider, model, CallerField(ctx), kind, zap.Int("payload_bytes", payloadBytes))
	usage, err := send()
	if err != nil {
		status := "error"
		if errors.Is(err, context.Canceled) {
			status = "canceled"
		}
		LogRequestFinish(logger, provider, model, status, time.Since(start), CallerField(ctx), kind)
		return usage, err
	}
	fields := []zap.Field{CallerField(ctx), kind}
	if usage != nil {
		fields = append(fields,
			zap.Int("input_tokens", usage.PromptTokens),
			zap.Int("output_tokens", usage.CompletionTokens),
			zap.Int("cache_read_tokens", usage.CacheReadInputTokens),
			zap.Int("cache_creation_tokens", usage.CacheCreationInputTokens),
		)
	}
	LogRequestFinish(logger, provider, model, "success", time.Since(start), fields...)
	return usage, nil
}
