/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeAlertStream captures what StreamAlerts sends. With gate set, the
// first Send parks until the gate closes, which pins the handler mid-send
// so a test can fill its subscriber buffer without the handler draining it.
type fakeAlertStream struct {
	grpc.ServerStream
	ctx     context.Context
	mu      sync.Mutex
	sent    []*pb.StreamAlertsResponse
	gate    chan struct{}
	parked  bool
	gateHit bool
}

func (f *fakeAlertStream) Context() context.Context { return f.ctx }
func (f *fakeAlertStream) Send(r *pb.StreamAlertsResponse) error {
	f.mu.Lock()
	gate := f.gate
	if gate != nil && !f.gateHit {
		f.gateHit = true
		f.parked = true
		f.mu.Unlock()
		<-gate
		f.mu.Lock()
		f.parked = false
	}
	f.sent = append(f.sent, r)
	f.mu.Unlock()
	return nil
}
func (f *fakeAlertStream) isParked() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parked
}
func (f *fakeAlertStream) snapshot() []*pb.StreamAlertsResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*pb.StreamAlertsResponse(nil), f.sent...)
}

func alertsOf(rs []*pb.StreamAlertsResponse) []string {
	var out []string
	for _, r := range rs {
		if a := r.GetAlert(); a != nil {
			out = append(out, a.Type+"/"+a.Object)
		}
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newAlertHandler(t *testing.T, b *AlertBroadcaster, current []AlertInfo) *Handler {
	t.Helper()
	h := NewHandler(nil, nil, zap.NewNop(), "openai", "gpt-4")
	h.SetWatcher(WatcherConfig{
		AlertsFunc:  func() []AlertInfo { return current },
		AlertStream: b,
	})
	return h
}

func TestAlertBroadcaster_FanOutAndDropSlowSubscriber(t *testing.T) {
	b := NewAlertBroadcaster()
	fast, cancelFast := b.Subscribe()
	defer cancelFast()
	slow, _ := b.Subscribe()
	if b.Subscribers() != 2 {
		t.Fatalf("subscribers = %d, want 2", b.Subscribers())
	}

	// Fill the slow subscriber's buffer and one more: it must be dropped
	// while the fast one, drained as we go, keeps every alert.
	got := 0
	for i := 0; i <= alertSubscriberBuffer; i++ {
		b.Publish(AlertInfo{Type: "T", Object: "o"})
		<-fast
		got++
	}
	if got != alertSubscriberBuffer+1 {
		t.Fatalf("fast subscriber got %d alerts", got)
	}
	if _, ok := <-slow; ok {
		// buffered values remain readable; drain until closed
		for range slow {
		}
	}
	if b.Subscribers() != 1 {
		t.Fatalf("slow subscriber should have been dropped, subscribers = %d", b.Subscribers())
	}
	cancelFast()
	if b.Subscribers() != 0 {
		t.Fatalf("cancel must remove the subscriber, got %d", b.Subscribers())
	}
}

func TestStreamAlerts_SnapshotThenLiveWithFilter(t *testing.T) {
	b := NewAlertBroadcaster()
	current := []AlertInfo{
		{Type: "OOMKilled", Object: "api-1", Namespace: "prod", Deployment: "api", Timestamp: time.Now()},
		{Type: "PodNotReady", Object: "web-1", Namespace: "staging", Deployment: "web", Timestamp: time.Now()},
	}
	h := newAlertHandler(t, b, current)
	ctx, cancel := context.WithCancel(context.Background())
	stream := &fakeAlertStream{ctx: ctx}
	done := make(chan error, 1)
	go func() {
		done <- h.StreamAlerts(&pb.StreamAlertsRequest{Namespace: "prod", IncludeCurrent: true}, stream)
	}()

	// Only the prod alert of the snapshot is delivered.
	waitFor(t, func() bool { return len(stream.snapshot()) == 1 })
	waitFor(t, func() bool { return b.Subscribers() == 1 })

	b.Publish(AlertInfo{Type: "HighRestartCount", Object: "api-2", Namespace: "prod", Deployment: "api", Timestamp: time.Now()})
	b.Publish(AlertInfo{Type: "HighRestartCount", Object: "web-2", Namespace: "staging", Deployment: "web", Timestamp: time.Now()})
	waitFor(t, func() bool { return len(stream.snapshot()) == 2 })

	got := alertsOf(stream.snapshot())
	want := []string{"OOMKilled/api-1", "HighRestartCount/api-2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("delivered %v, want %v", got, want)
	}

	cancel()
	err := <-done
	if status.Code(err) != codes.Canceled {
		t.Fatalf("stream end = %v, want Canceled from the client context", err)
	}
	if b.Subscribers() != 0 {
		t.Fatalf("subscriber leaked after the stream ended")
	}
}

func TestStreamAlerts_HeartbeatsWithoutWatcher(t *testing.T) {
	old := alertStreamHeartbeat
	alertStreamHeartbeat = 10 * time.Millisecond
	defer func() { alertStreamHeartbeat = old }()

	h := NewHandler(nil, nil, zap.NewNop(), "openai", "gpt-4") // no watcher at all
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := &fakeAlertStream{ctx: ctx}
	go func() { _ = h.StreamAlerts(&pb.StreamAlertsRequest{IncludeCurrent: true}, stream) }()

	waitFor(t, func() bool { return len(stream.snapshot()) >= 2 })
	for _, r := range stream.snapshot() {
		if !r.GetHeartbeat() || r.GetAlert() != nil {
			t.Fatalf("without a watcher only heartbeats may flow, got %v", r)
		}
	}
}

func TestStreamAlerts_AbortsWhenDroppedForFallingBehind(t *testing.T) {
	b := NewAlertBroadcaster()
	h := newAlertHandler(t, b, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Park the handler inside its first Send so it cannot drain the
	// subscriber buffer, fill the buffer past capacity, then release it:
	// the handler drains what was buffered and meets the closed channel.
	gate := make(chan struct{})
	stream := &fakeAlertStream{ctx: ctx, gate: gate}
	done := make(chan error, 1)
	go func() { done <- h.StreamAlerts(&pb.StreamAlertsRequest{}, stream) }()
	waitFor(t, func() bool { return b.Subscribers() == 1 })

	b.Publish(AlertInfo{Type: "T", Object: "first"})
	waitFor(t, func() bool { return stream.isParked() })
	for i := 0; i <= alertSubscriberBuffer; i++ {
		b.Publish(AlertInfo{Type: "T", Object: "o"})
	}
	if b.Subscribers() != 0 {
		t.Fatalf("the parked subscriber should have been dropped, subscribers = %d", b.Subscribers())
	}
	close(gate)

	select {
	case err := <-done:
		if status.Code(err) != codes.Aborted {
			t.Fatalf("stream end = %v, want Aborted (resync)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not end after the broadcaster dropped it")
	}
}

func TestValidateStreamAlerts_BoundsFilters(t *testing.T) {
	if err := validateStreamAlerts(&pb.StreamAlertsRequest{Namespace: "kube-system", Deployment: "my-app.v2"}); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := validateStreamAlerts(&pb.StreamAlertsRequest{Namespace: "Bad Namespace!"}); err == nil {
		t.Fatal("invalid namespace accepted")
	}
	if _, ok := requestValidators["StreamAlerts"]; !ok {
		t.Fatal("StreamAlerts is missing from the validator registry")
	}
}
