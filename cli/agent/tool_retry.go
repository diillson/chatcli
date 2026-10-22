/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * tool_retry.go — which tool failures are worth one more try, and what a
 * batch tells the model about an action it skipped.
 *
 * A read-only tool that failed on a timeout, a reset connection or a 5xx
 * is retried once before the failure reaches the model: the retry costs a
 * second, a model round-trip costs far more and usually ends in the same
 * retry. Mutating tools are never retried — a write that timed out may
 * have landed. When one action of a batch fails, the batch no longer
 * stops: read-only siblings still run (the model asked for them because
 * they were independent) and only mutating ones are skipped, each with a
 * note saying why, so nothing cascades on a broken state.
 */
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ToolRetryBackoff is the pause before the single retry of a transient
// read-only failure.
const ToolRetryBackoff = 1500 * time.Millisecond

// transientMarkers are the substrings of a transient failure: the network
// or the remote service hiccupped, not the request.
var transientMarkers = []string{
	"timeout", "timed out", "i/o timeout", "deadline exceeded",
	"connection reset", "connection refused", "broken pipe", "unexpected eof", "eof",
	"tls handshake", "no such host", "temporary failure in name resolution",
	"temporarily unavailable", "service unavailable", "bad gateway", "gateway timeout",
	"too many requests", "rate limit", "status 429", "status 502", "status 503", "status 504",
	"http 429", "http 502", "http 503", "http 504",
}

// IsTransientToolError reports whether err looks like a failure that a
// second attempt may not reproduce. A canceled context is never transient:
// the caller gave up.
func IsTransientToolError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context canceled") {
		return false
	}
	for _, m := range transientMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// Model-facing batch notes.
const (
	batchSkippedFmt = "[Action %d (%s) not executed: an earlier action of this batch failed and this one changes state, so it was skipped to avoid building on a broken step. Read-only actions of the batch still ran. Fix the failure and call this one again if it is still needed.]"
	batchRetriedFmt = "[Transient failure on the first attempt (%s); the action was retried once and this is the result of the retry.]\n"
)

// BatchSkippedNote is the result a mutating action gets when an earlier
// action of its batch failed.
func BatchSkippedNote(index int, toolName string) string {
	return fmt.Sprintf(batchSkippedFmt, index, toolName)
}

// BatchRetriedNote prefixes the result of a retried action with the
// failure that caused the retry.
func BatchRetriedNote(firstErr error) string {
	msg := ""
	if firstErr != nil {
		msg = firstErr.Error()
		if len(msg) > 160 {
			msg = msg[:160] + "…"
		}
	}
	return fmt.Sprintf(batchRetriedFmt, msg)
}
