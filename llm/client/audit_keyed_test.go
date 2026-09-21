/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

func resetAuditors(t *testing.T) {
	t.Helper()
	clear := func() {
		auditMu.Lock()
		auditSinks, auditOrder = nil, nil
		auditEnabled.Store(false)
		auditMu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// The audit sink used to be ONE slot, owned by the hash-chained audit log.
// A second consumer must attach without evicting it — an evicted audit log
// stops recording with no error anywhere.
func TestKeyedAuditorsComposeWithTheDefaultSlot(t *testing.T) {
	resetAuditors(t)
	var order []string
	RegisterRequestAuditor(func(RequestAuditEvent) { order = append(order, "auditlog") })
	RegisterRequestAuditorKeyed("pulse", func(RequestAuditEvent) { order = append(order, "pulse") })
	RegisterRequestAuditorKeyed("", func(RequestAuditEvent) { order = append(order, "ignored") })

	LogRequestStart(nil, "openai", "gpt-x", zap.Int("payload_bytes", 10))
	if len(order) != 2 || order[0] != "auditlog" || order[1] != "pulse" {
		t.Fatalf("delivery = %v, want [auditlog pulse] in key order", order)
	}

	RegisterRequestAuditorKeyed("pulse", nil)
	order = nil
	LogRequestFinish(zap.NewNop(), "openai", "gpt-x", "success", time.Second)
	if len(order) != 1 || order[0] != "auditlog" {
		t.Fatalf("after keyed detach: %v, want only the audit log", order)
	}

	RegisterRequestAuditorKeyed("pulse", func(RequestAuditEvent) { order = append(order, "pulse") })
	RegisterRequestAuditor(nil)
	order = nil
	LogRequestStart(zap.NewNop(), "openai", "gpt-x")
	if len(order) != 1 || order[0] != "pulse" {
		t.Fatalf("after RegisterRequestAuditor(nil): %v, want only pulse", order)
	}
}

func TestAuditFastPathTracksSinkCount(t *testing.T) {
	resetAuditors(t)
	if auditEnabled.Load() {
		t.Fatal("no sinks: fast path must be off")
	}
	RegisterRequestAuditorKeyed("a", func(RequestAuditEvent) {})
	RegisterRequestAuditorKeyed("b", func(RequestAuditEvent) {})
	RegisterRequestAuditorKeyed("a", nil)
	if !auditEnabled.Load() {
		t.Fatal("one sink left: fast path must stay on")
	}
	RegisterRequestAuditorKeyed("b", nil)
	if auditEnabled.Load() {
		t.Fatal("last sink removed: fast path must turn off")
	}
}

func TestAuditEventCarriesBothPhases(t *testing.T) {
	resetAuditors(t)
	var got []RequestAuditEvent
	RegisterRequestAuditorKeyed("t", func(ev RequestAuditEvent) { got = append(got, ev) })

	LogRequestStart(nil, "claudeai", "fable", zap.Int("history_len", 3))
	LogRequestFinish(nil, "claudeai", "fable", "error", 2*time.Second, zap.Bool("retried", true))

	if len(got) != 2 {
		t.Fatalf("events = %d", len(got))
	}
	if got[0].Phase != "send" || got[0].Fields["history_len"] != "3" {
		t.Fatalf("send = %+v", got[0])
	}
	if got[1].Phase != "recv" || got[1].Status != "error" || got[1].Duration != 2*time.Second || got[1].Fields["retried"] != "true" {
		t.Fatalf("recv = %+v", got[1])
	}
}

// Sinks are registered at runtime while requests are in flight; the race
// detector must stay quiet.
func TestAuditRegistrationRacesWithRequests(t *testing.T) {
	resetAuditors(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				LogRequestStart(nil, "p", "m")
				LogRequestFinish(nil, "p", "m", "success", time.Millisecond)
			}
		}
	}()
	for i := 0; i < 200; i++ {
		RegisterRequestAuditorKeyed("pulse", func(RequestAuditEvent) {})
		RegisterRequestAuditorKeyed("pulse", nil)
	}
	close(stop)
	wg.Wait()
}
