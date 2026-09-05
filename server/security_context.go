/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"fmt"
	"strings"
)

// UserRole defines the access level for authenticated users.
type UserRole string

const (
	RoleAdmin    UserRole = "admin"
	RoleUser     UserRole = "user"
	RoleReadonly UserRole = "readonly"
)

// Documented claim names for the same three access levels. A deployment
// issuing tokens with role "viewer" or "operator" gets exactly the level
// those names promise, and code comparing against RoleReadonly / RoleUser
// keeps working unchanged, because these are aliases rather than a fourth
// and fifth level.
const (
	// RoleViewer is read-only access: the "viewer" claim.
	RoleViewer = RoleReadonly
	// RoleOperator is day-to-day operational access: the "operator" claim.
	RoleOperator = RoleUser
)

// UserInfo holds the identity and role information extracted from authentication.
type UserInfo struct {
	// Subject is the unique user identifier (from JWT "sub" claim or token hash).
	Subject string
	// TenantID is the optional tenant/organization identifier.
	TenantID string
	// Role is the user's access level.
	Role UserRole
	// Email is an optional user email (from JWT "email" claim).
	Email string
}

// HasRole checks if the user has at least the given role level.
// Role hierarchy: admin > user > readonly
func (u *UserInfo) HasRole(required UserRole) bool {
	switch required {
	case RoleReadonly:
		return true // any role satisfies readonly
	case RoleUser:
		return u.Role == RoleUser || u.Role == RoleAdmin
	case RoleAdmin:
		return u.Role == RoleAdmin
	default:
		return false
	}
}

// String returns a human-readable representation (safe for logging).
func (u *UserInfo) String() string {
	return fmt.Sprintf("user=%s tenant=%s role=%s", u.Subject, u.TenantID, u.Role)
}

type contextKey int

const userInfoKey contextKey = iota

// ContextWithUser returns a new context with the UserInfo attached.
func ContextWithUser(ctx context.Context, user *UserInfo) context.Context {
	return context.WithValue(ctx, userInfoKey, user)
}

// UserFromContext extracts the UserInfo from context. Returns nil if not present.
func UserFromContext(ctx context.Context) *UserInfo {
	u, _ := ctx.Value(userInfoKey).(*UserInfo)
	return u
}

// RequireRole checks that the context has a user with at least the given role.
// Returns the UserInfo on success or an error suitable for gRPC status responses.
func RequireRole(ctx context.Context, required UserRole) (*UserInfo, error) {
	u := UserFromContext(ctx)
	if u == nil {
		return nil, fmt.Errorf("no authenticated user in context")
	}
	if !u.HasRole(required) {
		return nil, fmt.Errorf("role %q required, user has %q", required, u.Role)
	}
	return u, nil
}

// ParseRole converts a role claim to a UserRole.
//
// An absent claim keeps the historical default of RoleUser: tokens that
// never carried a role were minted against a server that granted them
// that level, and silently demoting them would lock out working
// deployments on upgrade.
//
// A role that is present but not recognized resolves to RoleReadonly, not
// RoleUser. That is the direction a name typo has to fail: a token asking
// for "vewer", or for a role this server has never heard of, must not end
// up with write access because the switch fell through to the default.
func ParseRole(s string) UserRole {
	role, _ := ParseRoleStrict(s)
	return role
}

// ParseRoleStrict resolves a role claim and reports whether it was
// recognized, so the caller can tell an unknown role from a deliberate
// read-only one and say so in the log.
func ParseRoleStrict(s string) (role UserRole, recognized bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "admin":
		return RoleAdmin, true
	case "operator", "user":
		return RoleUser, true
	case "viewer", "readonly":
		return RoleReadonly, true
	case "":
		// No role claim at all: the pre-RBAC default, kept for tokens
		// minted before roles existed.
		return RoleUser, true
	default:
		return RoleReadonly, false
	}
}
