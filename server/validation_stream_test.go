/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// recvStream hands the handler one canned message per RecvMsg call.
type recvStream struct {
	grpc.ServerStream
	msgs []interface{}
	i    int
}

func (r *recvStream) Context() context.Context { return context.Background() }
func (r *recvStream) RecvMsg(m interface{}) error {
	if r.i >= len(r.msgs) {
		return context.Canceled
	}
	// proto.Merge rather than a struct copy: a generated message carries a
	// mutex in its state, and copying it is exactly what go vet flags.
	if dst, ok := m.(proto.Message); ok {
		if src, ok := r.msgs[r.i].(proto.Message); ok {
			proto.Reset(dst)
			proto.Merge(dst, src)
		}
	}
	r.i++
	return nil
}

func recvThrough(t *testing.T, method string, msg, into interface{}) error {
	t.Helper()
	var gotErr error
	err := ValidationStreamInterceptor()(nil, &recvStream{msgs: []interface{}{msg}},
		&grpc.StreamServerInfo{FullMethod: method},
		func(_ interface{}, ss grpc.ServerStream) error {
			gotErr = ss.RecvMsg(into)
			return gotErr
		})
	if err == nil {
		return gotErr
	}
	return err
}

// A server-streaming RPC's request never passed through the unary
// validation interceptor, so validateStreamPrompt was dead code.
func TestStreamValidation_BoundsTheServerStreamingRequest(t *testing.T) {
	oversized := &pb.StreamPromptRequest{Prompt: strings.Repeat("x", maxPromptBytes+1)}
	err := recvThrough(t, "/chatcli.v1.ChatCLIService/StreamPrompt", oversized, &pb.StreamPromptRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("oversized StreamPrompt accepted: err=%v", err)
	}

	ok := &pb.StreamPromptRequest{Prompt: "hello"}
	if err := recvThrough(t, "/chatcli.v1.ChatCLIService/StreamPrompt", ok, &pb.StreamPromptRequest{}); err != nil {
		t.Fatalf("valid StreamPrompt rejected: %v", err)
	}
}

// The bidirectional stream is the only path where one connection can keep
// pushing for as long as it stays open.
func TestStreamValidation_BoundsEveryInteractiveMessage(t *testing.T) {
	method := "/chatcli.v1.ChatCLIService/InteractiveSession"

	huge := &pb.SessionMessage{Content: strings.Repeat("x", maxPromptBytes+1)}
	if code := status.Code(recvThrough(t, method, huge, &pb.SessionMessage{})); code != codes.InvalidArgument {
		t.Errorf("oversized content accepted (code=%v)", code)
	}

	tooManyKeys := &pb.SessionMessage{Content: "hi", Metadata: map[string]string{}}
	for i := 0; i < maxSessionMetadataEntries+1; i++ {
		tooManyKeys.Metadata[strings.Repeat("k", 3)+string(rune('a'+i%26))+strings.Repeat("n", i)] = "v"
	}
	if code := status.Code(recvThrough(t, method, tooManyKeys, &pb.SessionMessage{})); code != codes.InvalidArgument {
		t.Errorf("unbounded metadata map accepted (code=%v)", code)
	}

	unknownType := &pb.SessionMessage{Content: "hi", Type: pb.SessionMessage_Type(99)}
	if code := status.Code(recvThrough(t, method, unknownType, &pb.SessionMessage{})); code != codes.InvalidArgument {
		t.Errorf("unknown message type accepted (code=%v)", code)
	}

	good := &pb.SessionMessage{Content: "hi", Type: pb.SessionMessage_USER_INPUT}
	if err := recvThrough(t, method, good, &pb.SessionMessage{}); err != nil {
		t.Errorf("valid session message rejected: %v", err)
	}
}

// Kubernetes naming rules, which the AIOps requests carry and nothing
// checked.
func TestValidation_KubernetesNamingRules(t *testing.T) {
	bad := []struct{ ns, name string }{
		{"Default", ""},                // uppercase namespace
		{"my_ns", ""},                  // underscore
		{"-leading", ""},               // leading dash
		{strings.Repeat("n", 64), ""},  // too long
		{"", "Bad_Name"},               // uppercase + underscore
		{"", strings.Repeat("n", 254)}, // too long
		{"", "../../etc/passwd"},       // traversal shape
	}
	for _, c := range bad {
		req := &pb.GetAlertsRequest{Namespace: c.ns, Deployment: c.name}
		if err := validateGetAlerts(req); err == nil {
			t.Errorf("accepted invalid k8s names ns=%q name=%q", c.ns, c.name)
		}
	}

	good := &pb.GetAlertsRequest{Namespace: "kube-system", Deployment: "my-app.v2"}
	if err := validateGetAlerts(good); err != nil {
		t.Errorf("rejected valid k8s names: %v", err)
	}
	if err := validateGetAlerts(&pb.GetAlertsRequest{}); err != nil {
		t.Errorf("empty optional filters must stay valid: %v", err)
	}
}

func TestValidation_SeverityEnum(t *testing.T) {
	for _, s := range []string{"critical", "high", "medium", "low", "warning", "info", "CRITICAL", ""} {
		if err := validateSeverity("severity", s); err != nil {
			t.Errorf("severity %q rejected: %v", s, err)
		}
	}
	for _, s := range []string{"catastrophic", "p0", "sev1"} {
		if err := validateSeverity("severity", s); err == nil {
			t.Errorf("severity %q accepted", s)
		}
	}
}
