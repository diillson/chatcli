/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// fakeAlertServer serves GetAlerts and, unless withoutStream, StreamAlerts.
type fakeAlertServer struct {
	pb.UnimplementedChatCLIServiceServer
	withoutStream bool
	silent        bool // never send anything on the stream: stalls the client
	current       []*pb.WatcherAlert
	live          chan *pb.WatcherAlert
	pollCalls     atomic.Int32
	streamCalls   atomic.Int32
}

func (f *fakeAlertServer) GetAlerts(context.Context, *pb.GetAlertsRequest) (*pb.GetAlertsResponse, error) {
	f.pollCalls.Add(1)
	return &pb.GetAlertsResponse{Alerts: f.current}, nil
}

func (f *fakeAlertServer) StreamAlerts(req *pb.StreamAlertsRequest, stream pb.ChatCLIService_StreamAlertsServer) error {
	if f.withoutStream {
		return f.UnimplementedChatCLIServiceServer.StreamAlerts(req, stream)
	}
	f.streamCalls.Add(1)
	if f.silent {
		<-stream.Context().Done()
		return stream.Context().Err()
	}
	if req.GetIncludeCurrent() {
		for _, a := range f.current {
			if err := stream.Send(&pb.StreamAlertsResponse{Alert: a}); err != nil {
				return err
			}
		}
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case a := <-f.live:
			if err := stream.Send(&pb.StreamAlertsResponse{Alert: a}); err != nil {
				return err
			}
		case <-tick.C:
			if err := stream.Send(&pb.StreamAlertsResponse{Heartbeat: true}); err != nil {
				return err
			}
		}
	}
}

func alertFixture(object, deployment string) *pb.WatcherAlert {
	return &pb.WatcherAlert{
		Type: "HighRestartCount", Severity: "CRITICAL", Message: "restarts",
		Object: object, Namespace: "default", Deployment: deployment, TimestampUnix: time.Now().Unix(),
	}
}

// streamingBridge wires a bridge to an in-memory server, already connected,
// with short timers so the tests run in milliseconds.
func streamingBridge(t *testing.T, srv pb.ChatCLIServiceServer) *WatcherBridge {
	t.Helper()
	wb := setupFakeWatcherBridge()
	wb.serverClient = bufconnServerClient(t, srv)
	wb.pollInterval = 20 * time.Millisecond
	wb.heartbeatTimeout = 60 * time.Millisecond
	wb.connectedInstance = &platformv1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "chatcli-prod", Namespace: "chatcli-system"}}
	return wb
}

func anomalyCount(t *testing.T, wb *WatcherBridge) int {
	t.Helper()
	var list platformv1alpha1.AnomalyList
	if err := wb.client.List(context.Background(), &list, client.InNamespace("default")); err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func runBridge(t *testing.T, wb *WatcherBridge) (cancel func()) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = wb.Start(ctx); close(done) }()
	return func() {
		stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Start did not return after cancel")
		}
	}
}

func TestParseAlertTransport(t *testing.T) {
	for in, want := range map[string]AlertTransport{"": AlertTransportStream, "stream": AlertTransportStream, " Poll ": AlertTransportPoll} {
		got, err := ParseAlertTransport(in)
		if err != nil || got != want {
			t.Errorf("ParseAlertTransport(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseAlertTransport("push"); err == nil {
		t.Error("an unknown transport must be rejected")
	}
}

// The stream delivers the current alerts on open and new ones as they
// arrive; nothing polls GetAlerts while the stream is healthy.
func TestWatcherBridge_StreamCreatesAnomalies(t *testing.T) {
	srv := &fakeAlertServer{current: []*pb.WatcherAlert{alertFixture("web-1", "web")}, live: make(chan *pb.WatcherAlert, 4)}
	wb := streamingBridge(t, srv)
	stop := runBridge(t, wb)
	defer stop()

	eventually(t, func() bool { return anomalyCount(t, wb) == 1 }, "the snapshot anomaly")
	srv.live <- alertFixture("api-1", "api")
	eventually(t, func() bool { return anomalyCount(t, wb) == 2 }, "the live anomaly")

	if srv.pollCalls.Load() != 0 {
		t.Fatalf("GetAlerts was polled %d times while the stream was healthy", srv.pollCalls.Load())
	}
	if srv.streamCalls.Load() != 1 {
		t.Fatalf("stream opened %d times, want 1", srv.streamCalls.Load())
	}
}

// A server without StreamAlerts answers UNIMPLEMENTED once; the bridge then
// polls GetAlerts and still produces the anomalies. No regression for an
// operator ahead of its server.
func TestWatcherBridge_FallsBackToPollingWithoutStream(t *testing.T) {
	srv := &fakeAlertServer{withoutStream: true, current: []*pb.WatcherAlert{alertFixture("web-1", "web")}}
	wb := streamingBridge(t, srv)
	stop := runBridge(t, wb)
	defer stop()

	eventually(t, func() bool { return anomalyCount(t, wb) == 1 }, "the polled anomaly")
	eventually(t, func() bool { return srv.pollCalls.Load() >= 2 }, "a second poll")
	if wb.streamUnsupportedUntil.IsZero() {
		t.Fatal("the bridge must remember that the server lacks StreamAlerts")
	}
}

// The poll transport never opens the stream.
func TestWatcherBridge_PollTransportNeverStreams(t *testing.T) {
	srv := &fakeAlertServer{current: []*pb.WatcherAlert{alertFixture("web-1", "web")}, live: make(chan *pb.WatcherAlert)}
	wb := streamingBridge(t, srv)
	wb.SetAlertTransport(AlertTransportPoll)
	stop := runBridge(t, wb)
	defer stop()

	eventually(t, func() bool { return anomalyCount(t, wb) == 1 }, "the polled anomaly")
	if srv.streamCalls.Load() != 0 {
		t.Fatalf("stream opened %d times on the poll transport", srv.streamCalls.Load())
	}
}

// A stream that goes silent past the heartbeat timeout is reopened, and a
// run of silent attempts drops the connection for a rediscovery.
func TestWatcherBridge_ReopensStalledStream(t *testing.T) {
	srv := &fakeAlertServer{silent: true}
	wb := streamingBridge(t, srv)
	stop := runBridge(t, wb)
	defer stop()

	eventually(t, func() bool { return srv.streamCalls.Load() >= 2 }, "a reopened stream")
	eventually(t, func() bool { return !wb.serverClient.IsConnected() }, "the connection to be dropped for rediscovery")
}
