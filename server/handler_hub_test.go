/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newHubHandler(t *testing.T) *Handler {
	t.Helper()
	store, err := hub.OpenSQLiteStore(filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatalf("OpenSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &Handler{logger: zap.NewNop()}
	h.SetHub(hub.NewManager(store, nil, 8))
	return h
}

func userCtx(subject string, role UserRole) context.Context {
	return ContextWithUser(context.Background(), &UserInfo{Subject: subject, Role: role})
}

func TestHubResolveAppendReadFlow(t *testing.T) {
	h := newHubHandler(t)
	ctx := userCtx("alice", RoleUser)

	res, err := h.ResolveActiveConversation(ctx, &pb.ResolveActiveConversationRequest{})
	if err != nil {
		t.Fatalf("ResolveActiveConversation: %v", err)
	}
	if res.ConvId == "" || res.Principal != "alice" {
		t.Fatalf("unexpected resolve response: %+v", res)
	}

	_, err = h.AppendEvent(ctx, &pb.AppendEventRequest{
		ConvId: res.ConvId, Channel: "local", Role: "user", Content: "hi from notebook",
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	read, err := h.ReadConversation(ctx, &pb.ReadConversationRequest{ConvId: res.ConvId})
	if err != nil {
		t.Fatalf("ReadConversation: %v", err)
	}
	if len(read.Events) != 1 || read.Events[0].Content != "hi from notebook" {
		t.Fatalf("unexpected read: %+v", read.Events)
	}
}

func TestHubCrossPrincipalIsolation(t *testing.T) {
	h := newHubHandler(t)

	// alice creates a conversation and writes to it.
	aliceCtx := userCtx("alice", RoleUser)
	res, _ := h.ResolveActiveConversation(aliceCtx, &pb.ResolveActiveConversationRequest{})
	if _, err := h.AppendEvent(aliceCtx, &pb.AppendEventRequest{ConvId: res.ConvId, Channel: "local", Role: "user", Content: "secret"}); err != nil {
		t.Fatalf("alice append: %v", err)
	}

	// mallory must not read or write alice's conversation.
	malloryCtx := userCtx("mallory", RoleUser)
	if _, err := h.ReadConversation(malloryCtx, &pb.ReadConversationRequest{ConvId: res.ConvId}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied on cross-principal read, got %v", err)
	}
	if _, err := h.AppendEvent(malloryCtx, &pb.AppendEventRequest{ConvId: res.ConvId, Channel: "local", Role: "user", Content: "tamper"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied on cross-principal append, got %v", err)
	}
}

func TestHubNewConversationRotates(t *testing.T) {
	h := newHubHandler(t)
	ctx := userCtx("alice", RoleUser)

	first, _ := h.ResolveActiveConversation(ctx, &pb.ResolveActiveConversationRequest{})
	rotated, err := h.NewConversation(ctx, &pb.NewConversationRequest{})
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	if rotated.ConvId == first.ConvId {
		t.Fatal("NewConversation did not rotate")
	}
	again, _ := h.ResolveActiveConversation(ctx, &pb.ResolveActiveConversationRequest{})
	if again.ConvId != rotated.ConvId {
		t.Fatalf("pointer not following rotation: %s != %s", again.ConvId, rotated.ConvId)
	}
}

func TestHubBindingAndPrincipalOverrideRequiresAdmin(t *testing.T) {
	h := newHubHandler(t)

	// A regular user binding their own telegram id to themselves is fine.
	userC := userCtx("alice", RoleUser)
	if _, err := h.SetBinding(userC, &pb.SetBindingRequest{Platform: "telegram", UserId: "123"}); err != nil {
		t.Fatalf("self-bind: %v", err)
	}

	// Acting on behalf of another principal requires admin.
	if _, err := h.SetBinding(userC, &pb.SetBindingRequest{Platform: "telegram", UserId: "999", Principal: "bob"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied binding for another principal, got %v", err)
	}

	adminC := userCtx("root", RoleAdmin)
	if _, err := h.SetBinding(adminC, &pb.SetBindingRequest{Platform: "telegram", UserId: "999", Principal: "bob"}); err != nil {
		t.Fatalf("admin bind on behalf: %v", err)
	}

	// Non-admin ListBindings is scoped to self regardless of filter.
	list, err := h.ListBindings(userC, &pb.ListBindingsRequest{Principal: "bob"})
	if err != nil {
		t.Fatalf("ListBindings: %v", err)
	}
	for _, b := range list.Bindings {
		if b.Principal != "alice" {
			t.Fatalf("non-admin leaked binding for %q", b.Principal)
		}
	}
}

func TestHubDisabledReturnsUnavailable(t *testing.T) {
	h := &Handler{logger: zap.NewNop()} // no hub set
	ctx := userCtx("alice", RoleUser)
	if _, err := h.ResolveActiveConversation(ctx, &pb.ResolveActiveConversationRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable when hub disabled, got %v", err)
	}
}

func TestHubUnauthenticatedRejected(t *testing.T) {
	h := newHubHandler(t)
	if _, err := h.ResolveActiveConversation(context.Background(), &pb.ResolveActiveConversationRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated without user, got %v", err)
	}
}
