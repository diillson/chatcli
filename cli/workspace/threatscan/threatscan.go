/*
 * Package threatscan neutralizes prompt-injection and obvious
 * command-and-control / exfiltration payloads in text that is about to be
 * injected into the LLM system prompt (long-term memory, user profile,
 * bootstrap context files).
 *
 * Design constraints (this is a developer tool, so false positives are
 * themselves a regression):
 *
 *   - Matching is LINE-LEVEL and only the offending line is replaced with a
 *     marker; the surrounding context is preserved.
 *   - Patterns are HIGH-PRECISION. They target unambiguous injection
 *     imperatives ("ignore all previous instructions") and unmistakable
 *     remote-exec one-liners ("curl ... | sh", "bash -i >& /dev/tcp/..."),
 *     NOT generic shell commands that legitimately appear in AGENTS.md.
 *   - It never mutates what is stored on disk — only the injected copy.
 *
 * Scopes let callers dial strictness: ScopeContext (bootstrap files written
 * by the user — lenient, injection directives only) vs ScopeMemory
 * (auto-curated memory that should never contain shell scripts — also
 * screens exec/exfil/persistence payloads).
 */
package threatscan

import (
	"os"
	"regexp"
	"strings"
)

// Enabled reports whether threat scanning is active. It is on by default and
// can be disabled with CHATCLI_THREATSCAN=false (or 0/off/no) for users who
// keep deliberately "spicy" content in their context files.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_THREATSCAN"))) {
	case "false", "0", "off", "no":
		return false
	default:
		return true
	}
}

// Scope selects which pattern families apply.
type Scope int

const (
	// ScopeContext is for user-authored context files. Only blatant
	// prompt-injection directives are screened.
	ScopeContext Scope = iota
	// ScopeMemory is for auto-curated memory/profile. Adds exec/exfil and
	// persistence payloads on top of injection directives.
	ScopeMemory
)

// Match describes one neutralized line.
type Match struct {
	Category string // "prompt-injection" | "remote-exec" | "persistence"
	Line     string // the original offending line (for logging/inspection)
}

type pattern struct {
	re       *regexp.Regexp
	category string
}

// injectionPatterns target unambiguous attempts to override the system
// prompt. Kept deliberately narrow to avoid clobbering legitimate prose.
var injectionPatterns = []pattern{
	{regexp.MustCompile(`(?i)\bignore\s+(?:all\s+|any\s+)?(?:the\s+)?(?:previous|prior|above|earlier)\s+instructions?\b`), "prompt-injection"},
	{regexp.MustCompile(`(?i)\bdisregard\s+(?:all\s+|any\s+)?(?:the\s+)?(?:previous|prior|above|earlier|system)\s+(?:instructions?|prompts?|context)\b`), "prompt-injection"},
	{regexp.MustCompile(`(?i)\bforget\s+(?:all\s+|everything\s+)?(?:your\s+)?(?:previous|prior)\s+instructions?\b`), "prompt-injection"},
	{regexp.MustCompile(`(?i)<\s*/?\s*(?:system|im_start|im_end)\s*\|?\s*>`), "prompt-injection"},
	{regexp.MustCompile(`(?i)<\|\s*(?:im_start|im_end|system)\s*\|>`), "prompt-injection"},
	{regexp.MustCompile(`(?i)\byou\s+are\s+now\s+(?:in\s+)?(?:developer|dan|jailbreak|unrestricted)\b`), "prompt-injection"},
}

// execPatterns target unmistakable remote-exec / exfiltration one-liners.
// Only screened under ScopeMemory.
var execPatterns = []pattern{
	{regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\n]*\|\s*(?:sudo\s+)?(?:ba|z|d)?sh\b`), "remote-exec"},
	{regexp.MustCompile(`(?i)\bbase64\s+(?:--decode|-d)\b[^\n]*\|\s*(?:ba|z)?sh\b`), "remote-exec"},
	{regexp.MustCompile(`(?i)\bbash\s+-i\s*>&?\s*/dev/tcp/`), "remote-exec"},
	{regexp.MustCompile(`(?i)\bnc\b[^\n]*\s-e\s`), "remote-exec"},
}

// persistencePatterns target backdoor persistence. Only under ScopeMemory.
var persistencePatterns = []pattern{
	{regexp.MustCompile(`(?i)>>?\s*~?/?\.ssh/authorized_keys`), "persistence"},
	{regexp.MustCompile(`(?i)\b(?:crontab\s+-|/etc/cron)[^\n]*(?:curl|wget|/dev/tcp)`), "persistence"},
}

func patternsFor(scope Scope) []pattern {
	switch scope {
	case ScopeMemory:
		out := make([]pattern, 0, len(injectionPatterns)+len(execPatterns)+len(persistencePatterns))
		out = append(out, injectionPatterns...)
		out = append(out, execPatterns...)
		out = append(out, persistencePatterns...)
		return out
	default: // ScopeContext
		return injectionPatterns
	}
}

// Scan returns the matches found in text without modifying it.
func Scan(text string, scope Scope) []Match {
	if text == "" {
		return nil
	}
	pats := patternsFor(scope)
	var matches []Match
	for _, line := range strings.Split(text, "\n") {
		if cat, ok := firstMatch(line, pats); ok {
			matches = append(matches, Match{Category: cat, Line: line})
		}
	}
	return matches
}

// Sanitize replaces each offending line with a neutral marker and returns
// the cleaned text plus the number of lines blocked. When nothing matches
// it returns the input unchanged (and 0), so the hot path stays allocation
// -free for the overwhelmingly common clean case.
func Sanitize(text string, scope Scope) (string, int) {
	if text == "" {
		return text, 0
	}
	pats := patternsFor(scope)

	// Fast path: scan once; only rebuild if something matched.
	lines := strings.Split(text, "\n")
	blocked := 0
	for i, line := range lines {
		if cat, ok := firstMatch(line, pats); ok {
			lines[i] = "[BLOCKED: " + cat + "]"
			blocked++
		}
	}
	if blocked == 0 {
		return text, 0
	}
	return strings.Join(lines, "\n"), blocked
}

func firstMatch(line string, pats []pattern) (string, bool) {
	for _, p := range pats {
		if p.re.MatchString(line) {
			return p.category, true
		}
	}
	return "", false
}
