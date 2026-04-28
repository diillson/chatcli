/*
 * ChatCLI - LLM request observability helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"time"

	"go.uber.org/zap"
)

// LogRequestStart emits a structured INFO log when an LLM request is
// dispatched. Designed to give operators end-to-end visibility into
// what is being sent on the wire — enough fields to correlate
// user-reported failures (rate limits, payload-too-large, cache_control
// quirks, OAuth flapping) with payload shape, without ever leaking the
// actual prompt content. Each provider attaches its own provider-specific
// fields (payload bytes, history length, tool count, cache markers, ...)
// via the variadic fields argument.
//
// INFO level is intentional: requests are the spine of any session
// post-mortem, and a /loop or scheduler firing every minute should be
// traceable without flipping the global logger to DEBUG.
func LogRequestStart(logger *zap.Logger, provider, model string, fields ...zap.Field) {
	if logger == nil {
		return
	}
	base := make([]zap.Field, 0, 2+len(fields))
	base = append(base, zap.String("provider", provider), zap.String("model", model))
	base = append(base, fields...)
	logger.Info("llm: send", base...)
}

// LogRequestFinish emits an INFO log paired with LogRequestStart when
// the response (or terminal error) returns. duration is wall time from
// the matching Start; status is one of: "success", "error", "canceled".
// Concrete error details belong on the caller's own zap.Error path —
// here we only signal the outcome so dashboards can plot success rates
// and tail latencies without parsing free-form error text.
func LogRequestFinish(logger *zap.Logger, provider, model, status string, duration time.Duration, fields ...zap.Field) {
	if logger == nil {
		return
	}
	base := make([]zap.Field, 0, 4+len(fields))
	base = append(base,
		zap.String("provider", provider),
		zap.String("model", model),
		zap.String("status", status),
		zap.Duration("duration", duration),
	)
	base = append(base, fields...)
	logger.Info("llm: recv", base...)
}

// CountAnthropicCacheMarkers walks an Anthropic-style request body and
// counts cache_control markers across `system`, `tools`, and message
// content blocks. Returns 0 for non-Anthropic shapes (string `system`,
// missing keys, etc.). Used by the claudeai and bedrock adapters so
// the start log can show how many breakpoints the request actually
// carries — crucial for debugging "more than 4 blocks with cache_control"
// errors after mode transitions.
func CountAnthropicCacheMarkers(reqBody map[string]interface{}) int {
	if reqBody == nil {
		return 0
	}
	total := 0
	count := func(v interface{}) {
		switch s := v.(type) {
		case []map[string]interface{}:
			for _, m := range s {
				if m == nil {
					continue
				}
				if _, ok := m["cache_control"]; ok {
					total++
				}
			}
		case []interface{}:
			for _, item := range s {
				if m, ok := item.(map[string]interface{}); ok {
					if _, ok := m["cache_control"]; ok {
						total++
					}
				}
			}
		}
	}
	if v, ok := reqBody["system"]; ok {
		count(v)
	}
	if v, ok := reqBody["tools"]; ok {
		count(v)
	}
	if v, ok := reqBody["messages"]; ok {
		switch msgs := v.(type) {
		case []map[string]interface{}:
			for _, m := range msgs {
				count(m["content"])
			}
		case []interface{}:
			for _, m := range msgs {
				if mm, ok := m.(map[string]interface{}); ok {
					count(mm["content"])
				}
			}
		}
	}
	return total
}
