/*
 * ChatCLI - Request Size Error Annotation
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Providers annotate transport errors with the serialized request body size
 * so upper layers (agent-mode payload recovery) can derive an adaptive
 * payload cap from the exact size a proxy/WAF/gateway rejected, instead of
 * guessing. Error() is a pure passthrough: every string-based classifier
 * (retry policy, IsProxyWAFRejection, IsPayloadTooLargeError) keeps seeing
 * the original message unchanged.
 */
package client

import "errors"

// requestSizeError wraps a provider error with the request body size in
// bytes. Unexported on purpose: consumers go through WithRequestSize /
// RequestSizeFromError so the wrapping stays a private contract of this
// package.
type requestSizeError struct {
	err   error
	bytes int
}

func (e *requestSizeError) Error() string { return e.err.Error() }
func (e *requestSizeError) Unwrap() error { return e.err }

// WithRequestSize annotates err with the serialized request body size in
// bytes. Returns err unchanged when err is nil or bytes is non-positive,
// so call sites can wrap unconditionally.
func WithRequestSize(err error, bytes int) error {
	if err == nil || bytes <= 0 {
		return err
	}
	return &requestSizeError{err: err, bytes: bytes}
}

// RequestSizeFromError extracts the request body size recorded by
// WithRequestSize anywhere in err's chain. Returns (0, false) when the
// error carries no size annotation.
func RequestSizeFromError(err error) (int, bool) {
	var rse *requestSizeError
	if errors.As(err, &rse) {
		return rse.bytes, true
	}
	return 0, false
}
