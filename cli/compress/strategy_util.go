/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import "strings"

// offload persists the full original to the CCR store when lossy reduction is
// permitted, returning the retrieval marker to embed and the key. When CCR is
// not available (lossless mode or no store), it returns ("", "") — the signal
// to the caller that it MUST NOT drop any information, preserving the
// never-degrade contract.
func offload(original string, opts Options) (marker, key string) {
	if opts.Mode != ModeLossyWithCCR || opts.Store == nil {
		return "", ""
	}
	k, err := opts.Store.Put(original)
	if err != nil {
		return "", ""
	}
	opts.Metrics.RecordCCRPut()
	return FormatMarker(k), k
}

// canDrop reports whether the caller may discard low-value content (because a
// CCR store is available to make it recoverable).
func canDrop(opts Options) bool {
	return opts.Mode == ModeLossyWithCCR && opts.Store != nil
}

// errorKeywords are the substrings that mark a line as high-value across logs,
// search results and diffs. Lower-cased; matching is case-insensitive.
var errorKeywords = []string{
	"error", "err:", "fail", "panic", "fatal", "exception",
	"traceback", "assert", "warning", "warn:", "todo", "fixme",
	"undefined", "cannot", "denied", "refused", "timeout",
}

// hasErrorSignal reports whether s contains any high-value keyword.
func hasErrorSignal(s string) bool {
	low := strings.ToLower(s)
	for _, kw := range errorKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// isAlpha reports whether b is an ASCII letter (used for Windows drive-letter
// detection in the search parser).
func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// splitLines splits s on newlines without allocating a trailing empty element
// for a final newline — the common shape of tool output.
func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
