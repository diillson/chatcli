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

// Detection sampling bounds. Detect runs on EVERY above-threshold tool result
// before any compression work, and content-based detectors only need line
// fractions to score confidence — scanning a multi-megabyte payload in full
// (and allocating its complete line slice, once per detector) would tax the
// agent-loop hot path for no additional signal.
const (
	detectSampleMaxBytes = 64 * 1024
	detectSampleMaxLines = 200
)

// detectSampleLines returns up to detectSampleMaxLines lines drawn from the
// HEAD and TAIL of s (half each, bounded to detectSampleMaxBytes total, cut to
// complete lines so truncation never skews parse fractions). Sampling both
// ends matters: build/test logs concentrate their signal at the tail (the
// failure summary), while search/diff shapes show at the head — a head-only
// sample would misroute exactly the "long build that fails at the end" payload
// the log compressor exists for. For payloads within both bounds it is
// equivalent to splitLines.
func detectSampleLines(s string) []string {
	if len(s) > detectSampleMaxBytes {
		headEnd := detectSampleMaxBytes / 2
		if cut := strings.LastIndexByte(s[:headEnd], '\n'); cut > 0 {
			headEnd = cut
		}
		tailStart := len(s) - detectSampleMaxBytes/2
		if cut := strings.IndexByte(s[tailStart:], '\n'); cut >= 0 {
			tailStart += cut + 1
		}
		head, tail := splitLines(s[:headEnd]), splitLines(s[tailStart:])
		return sampleHeadTail(head, tail, detectSampleMaxLines)
	}
	lines := splitLines(s)
	if len(lines) > detectSampleMaxLines {
		half := detectSampleMaxLines / 2
		return sampleHeadTail(lines[:half], lines[len(lines)-half:], detectSampleMaxLines)
	}
	return lines
}

// sampleHeadTail combines a head and a tail line set into a single sample of
// at most maxLines (half from each end), copying into a fresh slice so the
// result never aliases the callers' backing arrays.
func sampleHeadTail(head, tail []string, maxLines int) []string {
	half := maxLines / 2
	if len(head) > half {
		head = head[:half]
	}
	if len(tail) > half {
		tail = tail[len(tail)-half:]
	}
	out := make([]string, 0, len(head)+len(tail))
	out = append(out, head...)
	out = append(out, tail...)
	return out
}
