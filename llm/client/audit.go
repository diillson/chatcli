/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Request audit sink. LogRequestStart/LogRequestFinish are the one
 * chokepoint every provider adapter passes through, so a sink registered
 * here sees every LLM request on every surface (REPL, one-shot, gateway,
 * MCP/ACP server, workers) without touching the adapters. The sink
 * receives the same structured fields the logs carry — provider, model,
 * payload size, history length, cache markers, tokens, status, duration —
 * and never the prompt content.
 */
package client

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// RequestAuditEvent is one send or receive observation.
type RequestAuditEvent struct {
	Time     time.Time
	Phase    string // "send" | "recv"
	Provider string
	Model    string
	Status   string        // recv only: success | error | canceled
	Duration time.Duration // recv only
	Fields   map[string]string
}

// RequestAuditor consumes audit events.
type RequestAuditor func(RequestAuditEvent)

// Sinks are keyed so independent consumers (the hash-chained audit log, the
// live telemetry bus) compose instead of clobbering each other. auditOrder
// is the key-sorted delivery list, rebuilt on registration so the per-request
// path only takes a read lock and copies nothing.
var (
	auditMu      sync.RWMutex
	auditSinks   map[string]RequestAuditor
	auditOrder   []RequestAuditor
	auditEnabled atomic.Bool // read lock-free on every request
)

// defaultAuditorKey is the slot RegisterRequestAuditor writes to. It is kept
// for API stability: that function delegates to RegisterRequestAuditorKeyed
// under this fixed key, so its single owner keeps working unchanged next to
// keyed consumers.
const defaultAuditorKey = "default"

// RegisterRequestAuditor installs the sink (nil clears it).
func RegisterRequestAuditor(fn RequestAuditor) {
	RegisterRequestAuditorKeyed(defaultAuditorKey, fn)
}

// RegisterRequestAuditorKeyed installs (fn != nil) or removes (fn == nil)
// the sink stored under key, leaving every other sink in place. Sinks run
// synchronously on the request goroutine and must be fast and non-blocking.
func RegisterRequestAuditorKeyed(key string, fn RequestAuditor) {
	if key == "" {
		return
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	if fn == nil {
		delete(auditSinks, key)
	} else {
		if auditSinks == nil {
			auditSinks = make(map[string]RequestAuditor)
		}
		auditSinks[key] = fn
	}
	keys := make([]string, 0, len(auditSinks))
	for k := range auditSinks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	order := make([]RequestAuditor, 0, len(keys))
	for _, k := range keys {
		order = append(order, auditSinks[k])
	}
	auditOrder = order
	auditEnabled.Store(len(order) > 0)
}

func emitAudit(ev RequestAuditEvent) {
	auditMu.RLock()
	sinks := auditOrder
	auditMu.RUnlock()
	for _, fn := range sinks {
		fn(ev)
	}
}

// fieldsToStrings flattens zap fields into the string map audit lines
// carry. Only scalar types are rendered; anything else is skipped rather
// than dumped, so a stray object field can never leak content.
func fieldsToStrings(fields []zap.Field) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		switch f.Type {
		case zapcore.StringType:
			out[f.Key] = f.String
		case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type,
			zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type:
			out[f.Key] = formatInt(f.Integer)
		case zapcore.BoolType:
			if f.Integer == 1 {
				out[f.Key] = "true"
			} else {
				out[f.Key] = "false"
			}
		case zapcore.DurationType:
			out[f.Key] = time.Duration(f.Integer).String()
		case zapcore.Float64Type:
			out[f.Key] = formatFloatBits(f.Integer)
		}
	}
	return out
}
