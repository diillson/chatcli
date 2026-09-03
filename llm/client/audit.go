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
	"sync"
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

var (
	auditMu      sync.RWMutex
	auditSink    RequestAuditor
	auditEnabled bool
)

// RegisterRequestAuditor installs the sink (nil clears it).
func RegisterRequestAuditor(fn RequestAuditor) {
	auditMu.Lock()
	auditSink = fn
	auditEnabled = fn != nil
	auditMu.Unlock()
}

func emitAudit(ev RequestAuditEvent) {
	auditMu.RLock()
	fn := auditSink
	auditMu.RUnlock()
	if fn != nil {
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
