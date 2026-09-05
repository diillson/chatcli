/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"sync"
)

// authOutcome carries the result of authentication back out to the
// interceptors that wrap it.
//
// The audit interceptor sits outside auth on purpose: it must record the
// calls that auth *rejects* just as much as the ones it admits. But that
// position means it never sees the context auth builds — UserInfo is
// injected into a new context handed inward, so reading it on the way out
// yields nothing, and every audit line said "anonymous" no matter who
// called. A holder placed in the context on the way in, filled by auth and
// read on the way out, is how the outer layer learns who the caller turned
// out to be.
type authOutcome struct {
	mu     sync.Mutex
	user   *UserInfo
	denied bool
}

type authOutcomeKey struct{}

// withAuthOutcome attaches a fresh outcome holder to the context and
// returns both, for an interceptor that wraps authentication.
func withAuthOutcome(ctx context.Context) (context.Context, *authOutcome) {
	out := &authOutcome{}
	return context.WithValue(ctx, authOutcomeKey{}, out), out
}

// authOutcomeFrom returns the holder attached to the context, or nil when
// no wrapping interceptor placed one (a direct handler call in a test).
func authOutcomeFrom(ctx context.Context) *authOutcome {
	out, _ := ctx.Value(authOutcomeKey{}).(*authOutcome)
	return out
}

// recordUser notes the identity authentication resolved.
func (o *authOutcome) recordUser(user *UserInfo) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.user = user
}

// recordDenied notes that authentication refused the call.
func (o *authOutcome) recordDenied() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.denied = true
}

// snapshot reads the outcome. Locked because a streaming RPC authenticates
// on the accepting goroutine and may be audited from another.
func (o *authOutcome) snapshot() (user *UserInfo, denied bool) {
	if o == nil {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.user, o.denied
}
