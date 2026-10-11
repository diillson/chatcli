/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isRemoteCoderPlugin reports whether a plugin name is the @coder tool,
// builtin or an external binary installed under the same name: either runs
// shell commands and edits files on the server host.
func isRemoteCoderPlugin(name string) bool {
	return strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(name), "@"), "coder")
}

// remoteCoderPolicy loads the coder security policy; a test seam.
var remoteCoderPolicy = func(logger *zap.Logger) (*coder.PolicyManager, error) {
	return coder.NewPolicyManager(logger)
}

// authorizeRemoteCoder gates ExecuteRemotePlugin for @coder the way the
// pipeline gates RunCoder: admin only, and then the coder security policy
// with no human to approve. A policy "ask" is refused unless the operator set
// CHATCLI_POLICY_AUTOMODE=true; deny rules, safety-immune operations and a
// policy that fails to load always refuse.
func (h *Handler) authorizeRemoteCoder(ctx context.Context, args []string) error {
	if _, err := RequireRole(ctx, RoleAdmin); err != nil {
		return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.remote.coder_requires_admin"))
	}
	pm, err := remoteCoderPolicy(h.logger)
	if err != nil {
		h.logger.Error("remote @coder denied: policy unavailable", zap.Error(err))
		return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.remote.coder_policy_unavailable"))
	}
	switch coder.ResolveWithoutApprover(pm, "@coder", strings.Join(args, " "), coder.OperatorPolicyAutoMode(h.logger)) {
	case coder.ActionAllow:
		return nil
	case coder.ActionDeny:
		h.logger.Warn("remote @coder denied by policy rule", zap.Strings("args", args))
		return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.remote.coder_policy_denied"))
	default:
		h.logger.Warn("remote @coder denied: policy asks for approval and no one can approve", zap.Strings("args", args))
		return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.remote.coder_policy_needs_approval"))
	}
}
