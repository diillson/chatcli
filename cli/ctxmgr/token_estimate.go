/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Token estimates inside the context manager (digests, chunker, validator,
 * processor) read one chars-per-token source. The CLI points it at its
 * calibrator so every estimate follows what the session's provider
 * actually counts; without a source the historical 4.0 applies.
 */
package ctxmgr

import "sync"

const defaultCharsPerToken = 4.0

var (
	charsPerTokenMu     sync.RWMutex
	charsPerTokenSource func() float64
)

// SetCharsPerTokenSource installs the live ratio source (nil restores the
// default). The source is read on every estimate, never cached.
func SetCharsPerTokenSource(src func() float64) {
	charsPerTokenMu.Lock()
	charsPerTokenSource = src
	charsPerTokenMu.Unlock()
}

// CharsPerToken returns the current ratio (bounded to a sane band).
func CharsPerToken() float64 {
	charsPerTokenMu.RLock()
	src := charsPerTokenSource
	charsPerTokenMu.RUnlock()
	if src == nil {
		return defaultCharsPerToken
	}
	r := src()
	if r < 1 || r > 16 {
		return defaultCharsPerToken
	}
	return r
}

// EstimateTokens converts characters to tokens with the current ratio.
func EstimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return int(float64(chars)/CharsPerToken() + 0.5)
}

// EstimateTokens64 is EstimateTokens for byte sizes.
func EstimateTokens64(chars int64) int64 {
	if chars <= 0 {
		return 0
	}
	return int64(float64(chars)/CharsPerToken() + 0.5)
}
