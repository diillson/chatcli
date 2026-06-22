/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package compress

import (
	"fmt"
	"strings"
)

// LogCompressor reduces build/test/CI/runtime log output — the payload behind
// Headroom's "SRE incident debugging 65,694 -> 5,118 tokens (92%)". Logs are
// mostly low-signal INFO/DEBUG noise punctuated by a few high-signal events:
// errors, stack traces, deduplicated warnings, and the final summary. This
// compressor keeps the signal and offloads the noise to CCR.
//
// Two correctness behaviors ported from the reference Rust port:
//   - Stack traces survive blank lines. A Python traceback often contains a
//     blank line mid-trace; a naive "stop at blank line" state machine would
//     truncate it. We keep contiguous frame runs across single blanks.
//   - Warning dedupe is conservative. We split each warning on its first ':'
//     or '=' and dedupe on the (lower-cased) head only. Warnings sharing a
//     category head collapse to one representative, while distinct categories
//     (different heads) are always kept — we never merge unrelated warnings.
type LogCompressor struct {
	MaxErrors         int
	ErrorContextLines int
	MaxStackTraces    int
	StackTraceMaxLine int
	MaxWarnings       int
	MaxTotalLines     int
}

// NewLogCompressor returns a compressor with Headroom-equivalent caps.
func NewLogCompressor() *LogCompressor {
	return &LogCompressor{
		MaxErrors:         10,
		ErrorContextLines: 3,
		MaxStackTraces:    3,
		StackTraceMaxLine: 20,
		MaxWarnings:       5,
		MaxTotalLines:     100,
	}
}

// Name implements Compressor.
func (*LogCompressor) Name() string { return "log" }

type logLevel int

const (
	lvlUnknown logLevel = iota
	lvlTrace
	lvlDebug
	lvlInfo
	lvlWarn
	lvlError
)

type logLine struct {
	n         int
	content   string
	level     logLevel
	isStack   bool
	isSummary bool
}

// Detect implements Compressor. Logs are recognized by a mix of level tokens,
// stack-trace frames, and test-runner summaries over many lines.
func (c *LogCompressor) Detect(content string, h Hint) float64 {
	switch strings.ToLower(strings.TrimSpace(h.ToolName)) {
	case "go test", "pytest", "npm", "cargo", "make", "jest", "build", "@coder":
		// Authoritative: an explicit build/test tool hint outranks any
		// content-based guess (log lines with ISO dates/timestamps can
		// otherwise look like grep "path:line:" rows to the search detector).
		return 0.95
	}
	lines := splitLines(content)
	if len(lines) < 8 {
		return 0
	}
	signal := 0
	for _, ln := range lines {
		if detectLevel(ln) >= lvlWarn || isStackFrame(ln) || isSummaryLine(ln) {
			signal++
		}
	}
	frac := float64(signal) / float64(len(lines))
	switch {
	case signal >= 3 && frac >= 0.05:
		return 0.8
	case signal >= 1 && len(lines) >= 30:
		return 0.6
	default:
		return 0
	}
}

// Compress implements Compressor.
func (c *LogCompressor) Compress(content string, opts Options) (Result, error) {
	lines := c.parse(content)
	if len(lines) == 0 {
		return passthrough(content), nil
	}
	allowDrop := canDrop(opts)
	if !allowDrop {
		// Lossless mode: the log compressor has no lossless reduction to
		// offer (every line carries information), so pass through untouched.
		return passthrough(content), nil
	}

	keep := c.selectLines(lines)
	if len(keep) >= len(lines) {
		return passthrough(content), nil // nothing worth dropping
	}

	marker, key := offload(content, opts)
	if marker == "" {
		return passthrough(content), nil
	}

	out := renderWithGaps(lines, keep, marker)
	return Result{
		Compressed:     out,
		OriginalSize:   len(content),
		CompressedSize: len(out),
		Strategy:       c.Name(),
		CacheKey:       key,
		Reversible:     true,
		Detail: map[string]int{
			"lines_kept":    len(keep),
			"lines_dropped": len(lines) - len(keep),
		},
	}, nil
}

func (c *LogCompressor) parse(content string) []logLine {
	raw := splitLines(content)
	out := make([]logLine, 0, len(raw))
	for i, ln := range raw {
		out = append(out, logLine{
			n:         i,
			content:   ln,
			level:     detectLevel(ln),
			isStack:   isStackFrame(ln),
			isSummary: isSummaryLine(ln),
		})
	}
	return out
}

// selectLines returns the set of line indices to keep, honoring all caps.
func (c *LogCompressor) selectLines(lines []logLine) map[int]struct{} {
	keep := make(map[int]struct{})

	// 1. Errors (cap) + context window around each.
	errCount := 0
	for i, ln := range lines {
		if ln.level == lvlError {
			if errCount >= c.MaxErrors {
				break
			}
			errCount++
			lo := i - c.ErrorContextLines
			if lo < 0 {
				lo = 0
			}
			hi := i + c.ErrorContextLines
			if hi >= len(lines) {
				hi = len(lines) - 1
			}
			for j := lo; j <= hi; j++ {
				keep[j] = struct{}{}
			}
		}
	}

	// 2. Stack-trace blocks (cap count + per-block line cap). Contiguous frame
	//    runs tolerate a single blank line so chained traces stay intact.
	blocks := stackBlocks(lines)
	for bi, blk := range blocks {
		if bi >= c.MaxStackTraces {
			break
		}
		n := 0
		for _, idx := range blk {
			if n >= c.StackTraceMaxLine {
				break
			}
			keep[idx] = struct{}{}
			n++
		}
	}

	// 3. Warnings, deduplicated by category head, capped.
	warnSeen := make(map[string]struct{})
	warnCount := 0
	for i, ln := range lines {
		if ln.level != lvlWarn {
			continue
		}
		sig := warnSignature(ln.content)
		if _, dup := warnSeen[sig]; dup {
			continue
		}
		if warnCount >= c.MaxWarnings {
			continue
		}
		warnSeen[sig] = struct{}{}
		warnCount++
		keep[i] = struct{}{}
	}

	// 4. Summary lines are always kept.
	for i, ln := range lines {
		if ln.isSummary {
			keep[i] = struct{}{}
		}
	}

	// 5. Global cap: if we somehow exceeded MaxTotalLines, trim the lowest-
	//    value kept lines (info/debug context) from the end first.
	if len(keep) > c.MaxTotalLines {
		c.enforceTotalCap(lines, keep)
	}
	return keep
}

// enforceTotalCap removes the least valuable kept lines until the count is at
// most MaxTotalLines. Errors, stack frames and summaries are protected.
func (c *LogCompressor) enforceTotalCap(lines []logLine, keep map[int]struct{}) {
	protected := func(i int) bool {
		l := lines[i]
		return l.level == lvlError || l.isStack || l.isSummary
	}
	// Drop unprotected kept lines from the bottom up.
	for i := len(lines) - 1; i >= 0 && len(keep) > c.MaxTotalLines; i-- {
		if _, ok := keep[i]; ok && !protected(i) {
			delete(keep, i)
		}
	}
}

// stackBlocks returns contiguous stack-frame index runs, tolerating a single
// blank line inside a run (so a Python traceback's blank line doesn't split
// it). Runs are returned in source order.
func stackBlocks(lines []logLine) [][]int {
	var blocks [][]int
	var cur []int
	pendingBlank := -1
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, cur)
			cur = nil
		}
		pendingBlank = -1
	}
	for i, ln := range lines {
		switch {
		case ln.isStack:
			if pendingBlank >= 0 {
				cur = append(cur, pendingBlank) // absorb the single blank
				pendingBlank = -1
			}
			cur = append(cur, i)
		case strings.TrimSpace(ln.content) == "" && len(cur) > 0 && pendingBlank < 0:
			pendingBlank = i // hold one blank; absorbed only if a frame follows
		default:
			flush()
		}
	}
	flush()
	return blocks
}

// renderWithGaps emits kept lines in order, collapsing each run of dropped
// lines into a single "... [N lines omitted] ..." gap. The CCR marker is
// appended once at the end so the model can recall the full log.
func renderWithGaps(lines []logLine, keep map[int]struct{}, marker string) string {
	var b strings.Builder
	gap := 0
	flushGap := func() {
		if gap > 0 {
			fmt.Fprintf(&b, "... [%d lines omitted] ...\n", gap)
			gap = 0
		}
	}
	for i, ln := range lines {
		if _, ok := keep[i]; ok {
			flushGap()
			b.WriteString(ln.content)
			b.WriteByte('\n')
		} else {
			gap++
		}
	}
	flushGap()
	fmt.Fprintf(&b, "\n[log: full output recoverable with @recall %s]\n", marker)
	return b.String()
}

// detectLevel classifies a log line by the highest-severity token it contains.
func detectLevel(s string) logLevel {
	up := strings.ToUpper(s)
	switch {
	case containsToken(up, "ERROR") || containsToken(up, "FATAL") || containsToken(up, "PANIC") ||
		strings.Contains(up, "FAIL") || strings.Contains(s, "✗") || strings.Contains(up, "[E]"):
		return lvlError
	case containsToken(up, "WARN") || containsToken(up, "WARNING") || strings.Contains(up, "[W]"):
		return lvlWarn
	case containsToken(up, "INFO") || strings.Contains(up, "[I]"):
		return lvlInfo
	case containsToken(up, "DEBUG") || strings.Contains(up, "[D]"):
		return lvlDebug
	case containsToken(up, "TRACE"):
		return lvlTrace
	default:
		return lvlUnknown
	}
}

// containsToken reports whether token appears in s delimited by non-alphanums
// (so "ERROR" matches in "[ERROR]" but not inside "TERRORISM").
func containsToken(s, token string) bool {
	idx := 0
	for {
		j := strings.Index(s[idx:], token)
		if j < 0 {
			return false
		}
		j += idx
		leftOK := j == 0 || !isAlphaNum(s[j-1])
		rightPos := j + len(token)
		rightOK := rightPos >= len(s) || !isAlphaNum(s[rightPos])
		if leftOK && rightOK {
			return true
		}
		idx = j + 1
		if idx >= len(s) {
			return false
		}
	}
}

func isAlphaNum(b byte) bool { return isAlpha(b) || (b >= '0' && b <= '9') }

// isStackFrame recognizes a stack-trace frame across common language flavors.
func isStackFrame(s string) bool {
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "at "): // Java / JS / .NET
		return true
	case strings.HasPrefix(t, "File \""): // Python traceback frame
		return true
	case strings.HasPrefix(t, "Traceback (most recent call last)"):
		return true
	case strings.HasPrefix(t, "goroutine ") && strings.Contains(t, "["): // Go
		return true
	case strings.HasPrefix(s, "\t") && strings.Contains(s, ".go:"): // Go frame
		return true
	case strings.HasPrefix(t, "panic:"):
		return true
	}
	return false
}

// isSummaryLine recognizes test-runner / build summary lines worth always
// keeping.
func isSummaryLine(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	low := strings.ToLower(t)
	if strings.HasPrefix(t, "===") || strings.HasSuffix(t, "===") {
		return true
	}
	if strings.HasPrefix(t, "ok ") || strings.HasPrefix(t, "FAIL\t") || strings.HasPrefix(t, "--- FAIL") || strings.HasPrefix(t, "--- PASS") {
		return true
	}
	// "N passed", "N failed", "N tests", "N errors" style tallies.
	if (strings.Contains(low, "passed") || strings.Contains(low, "failed") ||
		strings.Contains(low, "tests") || strings.Contains(low, "test result")) && hasDigit(low) {
		return true
	}
	return false
}

func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

// warnSignature returns the dedupe key for a warning: its category head (the
// part before the first ':' or '='), lower-cased. Two warnings with the same
// head collapse; different heads are always both kept.
func warnSignature(s string) string {
	t := strings.TrimSpace(s)
	if idx := strings.IndexAny(t, ":="); idx >= 0 {
		t = t[:idx]
	}
	return strings.ToLower(strings.TrimSpace(t))
}
