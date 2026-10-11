/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Saved sessions are owned by the authenticated principal.
//
// Every non-admin principal reads and writes its own store, rooted in a
// directory named by principalKey: a hash of how the caller authenticated
// and who the credential says it is. Nothing the client sends in the
// request takes part in choosing that directory, so no request can reach
// another principal's sessions.
//
// The store handed to NewHandler is the shared namespace every session
// lived in before ownership existed. Only RoleAdmin reaches it, and every
// admin access is logged with the subject and the session name.
// RoleReadonly may list and load its own sessions but never save or delete.

// Authentication sources, kept apart in the principal key so that two
// credentials that happen to carry the same subject (a JWT "sub" equal to
// a certificate subject, say) are still two principals.
const (
	authSourceNone   = "none"
	authSourceLegacy = "legacy-token"
	authSourceJWT    = "jwt"
	authSourceMTLS   = "mtls"
)

// Session operations, as they appear in the audit log and in the role check.
const (
	sessionOpList   = "list"
	sessionOpLoad   = "load"
	sessionOpSave   = "save"
	sessionOpDelete = "delete"
)

type authSourceKey struct{}

// contextWithPrincipal attaches the authenticated user and the source that
// authenticated it.
func contextWithPrincipal(ctx context.Context, user *UserInfo, source string) context.Context {
	return context.WithValue(ContextWithUser(ctx, user), authSourceKey{}, source)
}

// authSourceFromContext returns the authentication source recorded by the
// auth interceptor, or "" when the context never went through it.
func authSourceFromContext(ctx context.Context) string {
	s, _ := ctx.Value(authSourceKey{}).(string)
	return s
}

// principalKey derives the directory name of a principal's session store.
// Each field is length-prefixed so that no pair of different identities
// can produce the same hash input.
func principalKey(source string, u *UserInfo) string {
	h := sha256.New()
	for _, part := range []string{"chatcli-session-principal", source, u.TenantID, u.Subject} {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PrincipalSessionStoreFactory opens the session store of one principal.
// The key is a hex SHA-256 digest computed by the server; the factory maps
// it to a directory and never sees client input.
type PrincipalSessionStoreFactory func(principalKey string) (SessionStore, error)

// principalSessionStores caches one opened store per principal key.
type principalSessionStores struct {
	open PrincipalSessionStoreFactory

	mu     sync.Mutex
	stores map[string]SessionStore
}

func (p *principalSessionStores) storeFor(key string) (SessionStore, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.stores[key]; ok {
		return s, nil
	}
	s, err := p.open(key)
	if err != nil {
		return nil, err
	}
	p.stores[key] = s
	return s, nil
}

// SetPrincipalSessionStores enables per-principal session storage. Without
// it only RoleAdmin can use the session RPCs, on the shared namespace.
func (h *Handler) SetPrincipalSessionStores(open PrincipalSessionStoreFactory) {
	if open == nil {
		h.principalSessions = nil
		return
	}
	h.principalSessions = &principalSessionStores{open: open, stores: make(map[string]SessionStore)}
}

// sessionStoreFor resolves the store a session RPC acts on for the caller.
func (h *Handler) sessionStoreFor(ctx context.Context, op, name string) (SessionStore, error) {
	u := UserFromContext(ctx)
	if u == nil {
		return nil, status.Errorf(codes.Unauthenticated, "%s", i18n.T("server.session_tenancy.no_principal"))
	}
	if (op == sessionOpSave || op == sessionOpDelete) && !u.HasRole(RoleUser) {
		return nil, status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.session_tenancy.readonly_write", string(u.Role)))
	}

	if u.HasRole(RoleAdmin) {
		if h.sessionManager == nil {
			return nil, status.Errorf(codes.Unavailable, "%s", i18n.T("server.session.mgmt_unavailable"))
		}
		h.logger.Info(i18n.T("server.session_tenancy.admin_access"),
			zap.String("subject", u.Subject),
			zap.String("tenant", u.TenantID),
			zap.String("op", op),
			zap.String("session", name))
		return h.sessionManager, nil
	}

	if h.principalSessions == nil {
		return nil, status.Errorf(codes.Unavailable, "%s", i18n.T("server.session_tenancy.unavailable"))
	}
	store, err := h.principalSessions.storeFor(principalKey(authSourceFromContext(ctx), u))
	if err != nil {
		h.logger.Error(i18n.T("server.session_tenancy.open_failed"), zap.String("subject", u.Subject), zap.Error(err))
		return nil, status.Errorf(codes.Unavailable, "%s", i18n.T("server.session.mgmt_unavailable"))
	}
	return store, nil
}
