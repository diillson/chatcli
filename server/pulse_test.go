/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPulseGRPCNameKeepsServiceAndMethodOnly(t *testing.T) {
	for in, want := range map[string]string{
		"/chatcli.v1.ChatCLIService/SendPrompt": "ChatCLIService/SendPrompt",
		"/grpc.health.v1.Health/Check":          "Health/Check",
		"/Svc/Method":                           "Svc/Method",
		"":                                      "other",
		"/nomethod":                             "other",
		"/svc/has space":                        "other",
		"/svc/" + strings.Repeat("a", 80):       "other",
		"/svc/x?token=SECRET":                   "other",
	} {
		if got := pulseGRPCName(in); got != want {
			t.Errorf("pulseGRPCName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPulseUnaryInterceptorReportsCallAndNestsHandler(t *testing.T) {
	rec := pulsetest.Record(t)
	icpt := pulseUnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/chatcli.v1.ChatCLIService/SendPrompt"}

	var handlerParent string
	_, _ = icpt(context.Background(), "SECRET-REQ", info, func(ctx context.Context, _ interface{}) (interface{}, error) {
		handlerParent = pulse.ParentFrom(ctx)
		return "SECRET-RESP", nil
	})
	_, _ = icpt(context.Background(), nil, info, func(context.Context, interface{}) (interface{}, error) {
		return nil, status.Error(codes.Unauthenticated, "SECRET-ERR")
	})
	_, _ = icpt(context.Background(), nil, info, func(context.Context, interface{}) (interface{}, error) {
		return nil, status.Error(codes.Internal, "SECRET-ERR")
	})

	name := "grpc:ChatCLIService/SendPrompt"
	start := rec.WaitFor(t, "unary start", pulsetest.Is(pulse.KindRPC, name, pulse.PhaseStart))
	if handlerParent != start.ID {
		t.Fatalf("handler parent = %q, want %q", handlerParent, start.ID)
	}
	rec.WaitFor(t, "3 ends", func(pulse.Event) bool {
		return len(rec.Match(pulsetest.Is(pulse.KindRPC, name, pulse.PhaseEnd))) == 3
	})
	ends := rec.Match(pulsetest.Is(pulse.KindRPC, name, pulse.PhaseEnd))
	wantStatus := []string{pulse.StatusOK, pulse.StatusBlocked, pulse.StatusError}
	wantCode := []string{"OK", "Unauthenticated", "Internal"}
	for i, ev := range ends {
		if ev.Status != wantStatus[i] || ev.Attrs["code"] != wantCode[i] || ev.Attrs["direction"] != "inbound" || ev.Attrs["shape"] != "unary" {
			t.Errorf("end %d = %+v", i, ev)
		}
	}
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("request, response or error text leaked: %s", wire)
	}
}

type pulseTestStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *pulseTestStream) Context() context.Context { return s.ctx }

func TestPulseStreamInterceptorPropagatesTheCallNode(t *testing.T) {
	rec := pulsetest.Record(t)
	icpt := pulseStreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/chatcli.v1.ChatCLIService/StreamPrompt"}

	var handlerParent string
	err := icpt(nil, &pulseTestStream{ctx: context.Background()}, info, func(_ interface{}, ss grpc.ServerStream) error {
		handlerParent = pulse.ParentFrom(ss.Context())
		return status.Error(codes.Canceled, "gone")
	})
	if status.Code(err) != codes.Canceled {
		t.Fatalf("the handler's error must pass through: %v", err)
	}
	name := "grpc:ChatCLIService/StreamPrompt"
	start := rec.WaitFor(t, "stream start", pulsetest.Is(pulse.KindRPC, name, pulse.PhaseStart))
	if handlerParent != start.ID {
		t.Fatalf("stream handler parent = %q, want %q", handlerParent, start.ID)
	}
	end := rec.WaitFor(t, "stream end", pulsetest.Is(pulse.KindRPC, name, pulse.PhaseEnd))
	if end.Status != pulse.StatusCancelled || end.Attrs["shape"] != "stream" || end.Attrs["code"] != "Canceled" {
		t.Fatalf("end = %+v", end)
	}
}

func TestPulseGRPCInterceptorsAreSilentWhileOff(t *testing.T) {
	ctx := context.Background()
	got, span := pulseGRPCBegin(ctx, "/a/b", "unary")
	if span != nil || got != ctx {
		t.Fatal("dashboard off: no span and the context untouched")
	}
	pulseGRPCEnd(nil, nil)
	ss := &pulseTestStream{ctx: ctx}
	_ = pulseStreamInterceptor()(nil, ss, &grpc.StreamServerInfo{FullMethod: "/a/b"}, func(_ interface{}, got grpc.ServerStream) error {
		if got != ss {
			t.Fatal("dashboard off: the stream must not be wrapped")
		}
		return nil
	})
}

func TestHealthProbesStayOffTheBus(t *testing.T) {
	_ = pulsetest.Record(t)
	ctx, span := pulseGRPCBegin(context.Background(), "/grpc.health.v1.Health/Check", "unary")
	if span != nil || ctx != context.Background() {
		t.Fatal("a health probe must not open a node")
	}
}
