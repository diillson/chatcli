/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// memSessionStore is an in-memory SessionStore.
type memSessionStore struct {
	mu       sync.Mutex
	sessions map[string][]models.Message
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{sessions: map[string][]models.Message{}}
}

func (m *memSessionStore) SaveSession(name string, history []models.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[name] = history
	return nil
}

func (m *memSessionStore) LoadSession(name string) ([]models.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.sessions[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return h, nil
}

func (m *memSessionStore) ListSessions() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sessions))
	for n := range m.sessions {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (m *memSessionStore) DeleteSession(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[name]; !ok {
		return errors.New("not found")
	}
	delete(m.sessions, name)
	return nil
}

// legacyAdminCtx is the caller the server injects when no authentication
// is configured: an admin on the shared namespace.
func legacyAdminCtx() context.Context {
	return contextWithPrincipal(context.Background(), &UserInfo{Subject: "system", Role: RoleAdmin}, authSourceNone)
}

func principalCtx(subject string, role UserRole, source string) context.Context {
	return contextWithPrincipal(context.Background(), &UserInfo{Subject: subject, Role: role}, source)
}

// tenancyHandler wires a shared legacy store holding one operator session
// and a per-principal factory, and returns both so tests can inspect them.
func tenancyHandler(t *testing.T, logger *zap.Logger) (*Handler, *memSessionStore, map[string]*memSessionStore) {
	t.Helper()
	legacy := newMemSessionStore()
	_ = legacy.SaveSession("ops-runbook", []models.Message{{Role: "user", Content: "legacy secret"}})
	opened := map[string]*memSessionStore{}
	var mu sync.Mutex
	h := &Handler{logger: logger, sessionManager: legacy}
	h.SetPrincipalSessionStores(func(key string) (SessionStore, error) {
		mu.Lock()
		defer mu.Unlock()
		s := newMemSessionStore()
		opened[key] = s
		return s, nil
	})
	return h, legacy, opened
}

func saveReq(name, content string) *pb.SaveSessionRequest {
	return &pb.SaveSessionRequest{Name: name, Messages: []*pb.ChatMessage{{Role: "user", Content: content}}}
}

func TestSessionTenancy_UserCannotReachAnotherUsersSessions(t *testing.T) {
	h, _, _ := tenancyHandler(t, zap.NewNop())
	alice := principalCtx("alice", RoleUser, authSourceJWT)
	bob := principalCtx("bob", RoleUser, authSourceJWT)

	if _, err := h.SaveSession(alice, saveReq("notes", "alice private")); err != nil {
		t.Fatalf("alice save: %v", err)
	}

	list, err := h.ListSessions(bob, &pb.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("bob list: %v", err)
	}
	if len(list.Sessions) != 0 {
		t.Fatalf("bob listed %v, want nothing", list.Sessions)
	}
	if _, err := h.LoadSession(bob, &pb.LoadSessionRequest{Name: "notes"}); status.Code(err) != codes.NotFound {
		t.Fatalf("bob load of alice's session: got %v, want NotFound", err)
	}
	if _, err := h.DeleteSession(bob, &pb.DeleteSessionRequest{Name: "notes"}); err == nil {
		t.Fatal("bob deleted alice's session")
	}
	if _, err := h.SaveSession(bob, saveReq("notes", "bob overwrite")); err != nil {
		t.Fatalf("bob save in own store: %v", err)
	}

	got, err := h.LoadSession(alice, &pb.LoadSessionRequest{Name: "notes"})
	if err != nil {
		t.Fatalf("alice load: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "alice private" {
		t.Fatalf("alice's session was changed by bob: %+v", got.Messages)
	}
}

func TestSessionTenancy_UserCannotReachLegacyNamespace(t *testing.T) {
	h, legacy, _ := tenancyHandler(t, zap.NewNop())
	alice := principalCtx("alice", RoleUser, authSourceJWT)

	list, err := h.ListSessions(alice, &pb.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, n := range list.Sessions {
		if n == "ops-runbook" {
			t.Fatal("non-admin listed a legacy shared session")
		}
	}
	if _, err := h.LoadSession(alice, &pb.LoadSessionRequest{Name: "ops-runbook"}); err == nil {
		t.Fatal("non-admin loaded a legacy shared session")
	}
	if _, err := h.DeleteSession(alice, &pb.DeleteSessionRequest{Name: "ops-runbook"}); err == nil {
		t.Fatal("non-admin deleted a legacy shared session")
	}
	if _, err := legacy.LoadSession("ops-runbook"); err != nil {
		t.Fatalf("legacy session gone: %v", err)
	}
}

func TestSessionTenancy_ReadonlyCannotSaveOrDelete(t *testing.T) {
	h, _, opened := tenancyHandler(t, zap.NewNop())
	viewer := principalCtx("viewer-1", RoleReadonly, authSourceJWT)

	if _, err := h.SaveSession(viewer, saveReq("x", "y")); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("readonly save: got %v, want PermissionDenied", err)
	}
	if _, err := h.DeleteSession(viewer, &pb.DeleteSessionRequest{Name: "x"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("readonly delete: got %v, want PermissionDenied", err)
	}

	// Its own sessions stay readable.
	key := principalKey(authSourceJWT, &UserInfo{Subject: "viewer-1", Role: RoleReadonly})
	if _, err := h.ListSessions(viewer, &pb.ListSessionsRequest{}); err != nil {
		t.Fatalf("readonly list: %v", err)
	}
	_ = opened[key].SaveSession("mine", []models.Message{{Role: "user", Content: "hi"}})
	if _, err := h.LoadSession(viewer, &pb.LoadSessionRequest{Name: "mine"}); err != nil {
		t.Fatalf("readonly load own session: %v", err)
	}
}

func TestSessionTenancy_AdminReachesLegacyAndIsAudited(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	h, _, _ := tenancyHandler(t, zap.New(core))
	admin := principalCtx("root-operator", RoleAdmin, authSourceLegacy)

	list, err := h.ListSessions(admin, &pb.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0] != "ops-runbook" {
		t.Fatalf("admin list = %v, want the legacy session", list.Sessions)
	}
	got, err := h.LoadSession(admin, &pb.LoadSessionRequest{Name: "ops-runbook"})
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("admin load legacy: %v %+v", err, got)
	}

	var audited bool
	for _, e := range logs.All() {
		f := e.ContextMap()
		if f["subject"] == "root-operator" && f["session"] == "ops-runbook" && f["op"] == sessionOpLoad {
			audited = true
		}
	}
	if !audited {
		t.Fatalf("admin load was not audited with subject and session: %+v", logs.All())
	}
}

func TestSessionTenancy_FailsClosed(t *testing.T) {
	h, _, _ := tenancyHandler(t, zap.NewNop())
	if _, err := h.ListSessions(context.Background(), &pb.ListSessionsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no principal: got %v, want Unauthenticated", err)
	}

	h.SetPrincipalSessionStores(nil)
	if _, err := h.ListSessions(principalCtx("alice", RoleUser, authSourceJWT), &pb.ListSessionsRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("no per-principal stores: got %v, want Unavailable", err)
	}
}

func TestPrincipalKey_SeparatesSourcesAndTenants(t *testing.T) {
	u := &UserInfo{Subject: "mtls:alice"}
	if principalKey(authSourceJWT, u) == principalKey(authSourceMTLS, u) {
		t.Fatal("a JWT subject equal to a certificate subject shares its store")
	}
	other := &UserInfo{Subject: "mtls:alice", TenantID: "acme"}
	if principalKey(authSourceMTLS, u) == principalKey(authSourceMTLS, other) {
		t.Fatal("two tenants with one subject share a store")
	}
	// Length prefixing: moving a byte between fields changes the key.
	if principalKey("ab", &UserInfo{Subject: "c"}) == principalKey("a", &UserInfo{Subject: "bc"}) {
		t.Fatal("field boundaries are ambiguous")
	}
	if len(principalKey(authSourceJWT, u)) != 64 {
		t.Fatal("principal key is not a hex SHA-256 digest")
	}
}

func TestAuthorize_RecordsAuthSource(t *testing.T) {
	a := NewTokenAuthInterceptor("synthetic-shared-token", zap.NewNop())
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer synthetic-shared-token"))
	out, err := a.authorize(ctx, "/chatcli.v1.ChatCLIService/ListSessions")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if got := authSourceFromContext(out); got != authSourceLegacy {
		t.Fatalf("auth source = %q, want %q", got, authSourceLegacy)
	}

	open := NewTokenAuthInterceptor("", zap.NewNop())
	out, err = open.authorize(context.Background(), "/chatcli.v1.ChatCLIService/ListSessions")
	if err != nil {
		t.Fatalf("authorize without auth: %v", err)
	}
	if got := authSourceFromContext(out); got != authSourceNone {
		t.Fatalf("auth source = %q, want %q", got, authSourceNone)
	}
}
