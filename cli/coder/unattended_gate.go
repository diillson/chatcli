/*
 * ChatCLI - Policy resolution without a human approver
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Surfaces that run without a person able to answer a confirmation (the
 * messaging gateway, the gRPC server, the MCP/ACP tool passthrough,
 * `chatcli tool`, scripted one-shots) resolve a policy "ask" here. The
 * secure default is that "ask" is NOT approval: the action is refused and
 * the caller says which approval it needed. The operator can opt the
 * process into policy automode with CHATCLI_POLICY_AUTOMODE=true; deny
 * rules and safety-immune operations keep blocking even then.
 */
package coder

import (
	"os"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// PolicyAutoModeEnv is the operator opt-in that starts every session of the
// process in policy automode: a policy "ask" verdict auto-approves when no
// human can be asked. Deny rules and safety-immune operations still block.
const PolicyAutoModeEnv = "CHATCLI_POLICY_AUTOMODE"

var policyAutoModeWarnOnce sync.Once

// OperatorPolicyAutoMode reports whether the operator set PolicyAutoModeEnv
// to a true value. The first positive answer with a logger logs a warning,
// once per process, so the widened autonomy is always visible in the logs.
func OperatorPolicyAutoMode(logger *zap.Logger) bool {
	on, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(PolicyAutoModeEnv)))
	if err != nil || !on {
		return false
	}
	if logger != nil {
		policyAutoModeWarnOnce.Do(func() {
			logger.Warn("security policy automode enabled by operator setting: 'ask' verdicts auto-approve when no human can confirm; deny rules and safety-immune operations still block",
				zap.String("env", PolicyAutoModeEnv))
		})
	}
	return true
}

// ResolveWithoutApprover resolves the policy verdict for a call that no
// human can confirm. A nil manager (it failed to load) denies. ActionAsk is
// returned unchanged when the call still needs an approval nobody can give:
// callers must refuse it and tell the requester which approval is missing.
// With autoMode on, "ask" becomes "allow" unless the call is safety-immune.
func ResolveWithoutApprover(pm *PolicyManager, toolName, args string, autoMode bool) Action {
	if pm == nil {
		return ActionDeny
	}
	action := pm.Check(toolName, args)
	if action != ActionAsk || !autoMode {
		return action
	}
	_, normalized := NormalizeCoderArgs(args)
	if IsSafetyImmune(toolName, args) || IsSafetyImmune(toolName, normalized) {
		return ActionAsk
	}
	return ActionAllow
}
