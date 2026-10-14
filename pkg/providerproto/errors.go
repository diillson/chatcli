/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerproto

import (
	"encoding/json"
	"fmt"
)

// JSON-RPC 2.0 reserved codes.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603
)

// Provider error codes. The host maps them onto its retry, failover and
// re-login logic, so a plugin must pick the code that matches the cause
// rather than a generic one.
const (
	// ErrCodeAuthRequired: no usable credential; the host runs auth/login
	// (interactive) or fails with a login hint (unattended).
	ErrCodeAuthRequired = -33001
	// ErrCodeRateLimited: back off; Data.RetryAfterMs when known.
	ErrCodeRateLimited = -33002
	// ErrCodeContextTooLong: the host compacts the history and retries.
	ErrCodeContextTooLong = -33003
	// ErrCodeModelNotFound: the model id is unknown or not enabled.
	ErrCodeModelNotFound = -33004
	// ErrCodeUnavailable: transient upstream failure; retryable and a
	// failover trigger.
	ErrCodeUnavailable = -33005
	// ErrCodeBadRequest: the request is invalid for the backend; not
	// retryable.
	ErrCodeBadRequest = -33006
	// ErrCodeCancelled: the request was abandoned after $/cancelRequest.
	ErrCodeCancelled = -33007
	// ErrCodeRefused: the backend declined the content (safety policy).
	ErrCodeRefused = -33008
)

// ErrorData is the optional structured payload of an Error.
type ErrorData struct {
	RetryAfterMs int `json:"retryAfterMs,omitempty"`
	// HTTPStatus is the upstream status when the failure came from HTTP.
	HTTPStatus int `json:"httpStatus,omitempty"`
}

// Error is a JSON-RPC error object; it implements error.
type Error struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider plugin error %d: %s", e.Code, e.Message)
}

// Retryable reports whether the host may retry the same request.
func (e *Error) Retryable() bool {
	switch e.Code {
	case ErrCodeRateLimited, ErrCodeUnavailable:
		return true
	}
	return false
}

// NewError builds an Error.
func NewError(code int, message string) *Error {
	return &Error{Code: code, Message: message}
}

// errorFromRaw decodes a wire error, tolerating peers that send extra or
// malformed data.
func errorFromRaw(raw json.RawMessage) *Error {
	var e Error
	if err := json.Unmarshal(raw, &e); err != nil || e.Code == 0 {
		return &Error{Code: ErrCodeInternal, Message: "malformed error object from peer"}
	}
	return &e
}
