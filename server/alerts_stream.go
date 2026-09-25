/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// alertStreamHeartbeat is how often an idle StreamAlerts stream sends a
// liveness tick. Clients treat three missed ticks as a dead connection.
var alertStreamHeartbeat = 15 * time.Second

// alertSubscriberBuffer bounds how far a slow subscriber may fall behind
// before the broadcaster drops it. The client reopens the stream with the
// current snapshot, so nothing is lost: dedup on its side absorbs repeats.
const alertSubscriberBuffer = 256

// AlertBroadcaster fans watcher alerts out to StreamAlerts subscribers. The
// watcher goroutine publishes; every subscriber owns a bounded channel. A
// subscriber that cannot keep up is closed rather than allowed to stall the
// watcher or grow without bound.
type AlertBroadcaster struct {
	mu   sync.Mutex
	subs map[uint64]chan AlertInfo
	next uint64
}

// NewAlertBroadcaster creates an empty broadcaster.
func NewAlertBroadcaster() *AlertBroadcaster {
	return &AlertBroadcaster{subs: make(map[uint64]chan AlertInfo)}
}

// Publish hands the alert to every live subscriber without blocking. A
// subscriber whose buffer is full is dropped: its channel closes, which the
// stream reports as ABORTED so the client resyncs.
func (b *AlertBroadcaster) Publish(a AlertInfo) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.subs {
		select {
		case ch <- a:
		default:
			close(ch)
			delete(b.subs, id)
		}
	}
}

// Subscribe registers a subscriber and returns its channel plus the function
// that removes it. The channel closes only when the broadcaster drops the
// subscriber for falling behind; the caller's own cancel never closes it,
// so a reader can tell the two apart.
func (b *AlertBroadcaster) Subscribe() (<-chan AlertInfo, func()) {
	ch := make(chan AlertInfo, alertSubscriberBuffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// Subscribers reports how many streams are attached.
func (b *AlertBroadcaster) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// alertMatches applies the optional namespace and deployment filters shared
// by GetAlerts and StreamAlerts.
func alertMatches(namespace, deployment string, a AlertInfo) bool {
	if namespace != "" && a.Namespace != namespace {
		return false
	}
	if deployment != "" && a.Deployment != deployment {
		return false
	}
	return true
}

func alertToProto(a AlertInfo) *pb.WatcherAlert {
	return &pb.WatcherAlert{
		Type:          a.Type,
		Severity:      a.Severity,
		Message:       a.Message,
		Object:        a.Object,
		Namespace:     a.Namespace,
		Deployment:    a.Deployment,
		TimestampUnix: a.Timestamp.Unix(),
	}
}

// StreamAlerts pushes watcher alerts to the caller as the watcher raises
// them. With include_current the stream opens with the alerts active now.
// Heartbeats keep flowing while the watcher is quiet; without a watcher the
// stream is heartbeats only, mirroring GetAlerts returning an empty list.
func (h *Handler) StreamAlerts(req *pb.StreamAlertsRequest, stream pb.ChatCLIService_StreamAlertsServer) error {
	ctx := stream.Context()
	ns, dep := req.GetNamespace(), req.GetDeployment()

	if req.GetIncludeCurrent() && h.watcherAlertsFunc != nil {
		for _, a := range h.watcherAlertsFunc() {
			if !alertMatches(ns, dep, a) {
				continue
			}
			if err := stream.Send(&pb.StreamAlertsResponse{Alert: alertToProto(a)}); err != nil {
				return err
			}
		}
	}

	var events <-chan AlertInfo
	if h.alertBroadcaster != nil {
		ch, cancel := h.alertBroadcaster.Subscribe()
		defer cancel()
		events = ch
	}

	ticker := time.NewTicker(alertStreamHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case <-ticker.C:
			if err := stream.Send(&pb.StreamAlertsResponse{Heartbeat: true}); err != nil {
				return err
			}
		case a, ok := <-events:
			if !ok {
				// Dropped for falling behind: the client reopens with the
				// current snapshot, exactly like the hub's resync.
				return status.Errorf(codes.Aborted, "%s", i18n.T("server.alerts.stream_resync"))
			}
			if !alertMatches(ns, dep, a) {
				continue
			}
			if err := stream.Send(&pb.StreamAlertsResponse{Alert: alertToProto(a)}); err != nil {
				return err
			}
		}
	}
}
