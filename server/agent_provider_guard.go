/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Agent providers on the server.
//
// Most providers are a model behind an HTTP API. An agent provider instead
// runs a local agent CLI on the server host that has tools of its own: DEVIN
// spawns the Devin CLI, whose least permissive mode ("auto") still
// auto-approves read-only tools, so a turn can read any file the server
// user can read. Selecting one remotely is execution on the server host,
// so only RoleAdmin may route a turn to it. That covers a provider named
// in the request, the server's own default provider and a fallback chain
// that contains one; a non-admin is refused rather than silently rerouted.
//
// A provider joins this set when it wraps an external agent process
// instead of a model API.
var agentProviders = map[string]bool{
	"DEVIN": true,
}

// isAgentProvider reports whether provider runs an agent on the server host.
func isAgentProvider(provider string) bool {
	return agentProviders[strings.ToUpper(strings.TrimSpace(provider))]
}

// callerIsAdmin reports whether the authenticated caller is RoleAdmin.
func callerIsAdmin(ctx context.Context) bool {
	u := UserFromContext(ctx)
	return u != nil && u.HasRole(RoleAdmin)
}

// authorizeRouteProvider refuses a non-admin route that would reach an
// agent provider. usesChain tells whether the request falls through to the
// fallback chain (it names no provider, no model and no credential).
func (h *Handler) authorizeRouteProvider(ctx context.Context, provider string, usesChain bool) error {
	if callerIsAdmin(ctx) {
		return nil
	}
	if usesChain {
		for _, entry := range h.fallbackChain.GetHealth() {
			if isAgentProvider(entry.Name) {
				return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.agent_provider.chain_denied", entry.Name))
			}
		}
		return nil
	}
	if strings.TrimSpace(provider) != "" {
		if isAgentProvider(provider) {
			return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.agent_provider.denied", strings.ToUpper(strings.TrimSpace(provider))))
		}
		return nil
	}
	if isAgentProvider(h.defaultProvider) {
		return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.agent_provider.default_denied", strings.ToUpper(h.defaultProvider)))
	}
	return nil
}

// routeFailure turns a resolveRoute error into the RPC status: a refusal
// keeps its PermissionDenied code, anything else is Internal with msgKey.
func routeFailure(err error, msgKey string) error {
	if s, ok := status.FromError(err); ok && s.Code() == codes.PermissionDenied {
		return err
	}
	return status.Errorf(codes.Internal, "%s", i18n.T(msgKey, err))
}
