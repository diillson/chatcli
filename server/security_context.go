/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"fmt"
)

// UserRole defines the access level for authenticated users.
type UserRole string

const (
	RoleAdmin    UserRole = "admin"
	RoleUser     UserRole = "user"
	RoleReadonly UserRole = "readonly"
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

// ParseRole converts a string to UserRole, defaulting to RoleUser for unknown values.
func ParseRole(s string) UserRole {
	switch s {
	case "admin":
		return RoleAdmin
	case "readonly":
		return RoleReadonly
	case "user":
		return RoleUser
	default:
		return RoleUser
	}
}
