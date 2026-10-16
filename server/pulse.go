/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the gRPC server: every call a client makes is one node
 * on the dashboard, named by its method, timed from the moment it enters the
 * interceptor chain to the moment the handler returns. The handler runs under
 * the call's node, so the turns, agents and LLM requests it starts nest under
 * it. Only the method and the status code leave the process: never the
 * request, the response, the metadata or the error text.
 */
package server

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/pkg/pulse"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// pulseGRPCNameMaxLen bounds a method name shown on the dashboard.
const pulseGRPCNameMaxLen = 64

// pulseGRPCName turns "/chatcli.v1.ChatCLIService/SendPrompt" into
// "ChatCLIService/SendPrompt": the package prefix adds nothing on a graph of
// one service. Each distinct name becomes a node, so anything that does not
// look like a service/method pair collapses into one "other" node.
func pulseGRPCName(fullMethod string) string {
	svc, method, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok || svc == "" || method == "" {
		return "other"
	}
	if i := strings.LastIndexByte(svc, '.'); i >= 0 {
		svc = svc[i+1:]
	}
	name := svc + "/" + method
	if svc == "" || len(name) > pulseGRPCNameMaxLen {
		return "other"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '/', r == '_':
		default:
			return "other"
		}
	}
	return name
}

// pulseGRPCBegin opens the span of an inbound call and returns the context its
// handler must run under.
func pulseGRPCBegin(ctx context.Context, fullMethod, shape string) (context.Context, *pulse.Span) {
	// Health probes arrive every few seconds from orchestrators and say
	// nothing about what the server is doing: they would only churn the
	// spool.
	if !pulse.Enabled() || strings.HasPrefix(fullMethod, "/grpc.health.") {
		return ctx, nil
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindRPC, "grpc:"+pulseGRPCName(fullMethod), "")
	return ctx, span.With("direction", "inbound").With("shape", shape)
}

// pulseGRPCEnd closes a call span with its gRPC status code. A call refused by
// auth, validation or the rate limiter is blocked, not failed: the server did
// its job.
func pulseGRPCEnd(span *pulse.Span, err error) {
	if span == nil {
		return
	}
	code := status.Code(err)
	span = span.With("code", code.String())
	switch code {
	case codes.OK:
		span.End(pulse.StatusOK)
	case codes.Canceled, codes.DeadlineExceeded:
		span.End(pulse.StatusCancelled)
	case codes.Unauthenticated, codes.PermissionDenied, codes.ResourceExhausted, codes.InvalidArgument:
		span.End(pulse.StatusBlocked)
	default:
		span.End(pulse.StatusError)
	}
}

// pulseUnaryInterceptor reports each unary call on the live dashboard.
func pulseUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		ctx, span := pulseGRPCBegin(ctx, info.FullMethod, "unary")
		resp, err := handler(ctx, req)
		pulseGRPCEnd(span, err)
		return resp, err
	}
}

// pulseStreamInterceptor reports each streaming call on the live dashboard,
// open for as long as the stream lives.
func pulseStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, span := pulseGRPCBegin(ss.Context(), info.FullMethod, "stream")
		if span != nil {
			ss = &wrappedServerStream{ServerStream: ss, ctx: ctx}
		}
		err := handler(srv, ss)
		pulseGRPCEnd(span, err)
		return err
	}
}
