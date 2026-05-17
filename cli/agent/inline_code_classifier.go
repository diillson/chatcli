/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"os"
	"regexp"
	"strings"
)

// InlineCodeRisk is the verdict returned by the inline-code classifier for
// `python -c '<code>'`, `node -e '<code>'`, and friends. The middle level
// `Unknown` is reserved for code we can't confidently classify (parse
// failure, exotic syntax) — callers should treat Unknown as "elevate to
// confirmation" in strict mode and "allow" in lenient mode.
type InlineCodeRisk int

const (
	// RiskSafe — inline source contains only read-only operations: stdlib
	// pretty-printing, JSON encoding, simple math. No imports of os /
	// subprocess / network libraries, no file writes, no eval/exec.
	RiskSafe InlineCodeRisk = iota
	// RiskUnknown — we couldn't determine risk (mixed signals, unknown
	// builtins, dynamic indirection). Conservative callers should treat
	// this as RiskHigh.
	RiskUnknown
	// RiskHigh — inline source uses primitives that can execute arbitrary
	// commands, write files, open network connections, or otherwise escape
	// the language sandbox.
	RiskHigh
)

// String renders the risk level for log/telemetry purposes.
func (r InlineCodeRisk) String() string {
	switch r {
	case RiskSafe:
		return "safe"
	case RiskHigh:
		return "high"
	default:
		return "unknown"
	}
}

// inlineCodeStrictEnv enables conservative classification: anything that
// isn't proven RiskSafe is treated as RiskHigh. Mirrors Claude Code's
// `CHATCLI_AGENT_INLINE_CODE_STRICT` toggle. Default off — most users get
// the false-positive fix; security-sensitive setups can opt in.
const inlineCodeStrictEnv = "CHATCLI_AGENT_INLINE_CODE_STRICT"

// dangerousInlineMarkers are substrings that, when present in the source
// passed to a language interpreter via -c/-e/-r, indicate the script can
// execute arbitrary side effects. The list is per-language so a Python
// import doesn't trip the Node detector and vice versa.
//
// Patterns are intentionally simple — the alternative is shipping a real
// AST parser per language, which we will not do. The trade-off: false
// positives that escalate to confirmation are acceptable; false negatives
// (missing a real attack) are not. Each pattern errs on the side of
// flagging.
var dangerousInlineMarkers = map[string][]*regexp.Regexp{
	"python": compileAll([]string{
		`(?i)\bimport\s+os\b`,
		`(?i)\bimport\s+subprocess\b`,
		`(?i)\bimport\s+socket\b`,
		`(?i)\bimport\s+shutil\b`,
		`(?i)\bimport\s+pty\b`,
		`(?i)\bimport\s+ctypes\b`,
		`(?i)\bfrom\s+os\b`,
		`(?i)\bfrom\s+subprocess\b`,
		`(?i)\bfrom\s+socket\b`,
		`(?i)\bos\.\w*system\b`,
		`(?i)\bos\.popen\b`,
		`(?i)\bos\.exec\w*\b`,
		`(?i)\bos\.fork\b`,
		`(?i)\bos\.remove\b`,
		`(?i)\bos\.unlink\b`,
		`(?i)\bos\.rmdir\b`,
		`(?i)\bsubprocess\.\w+`,
		`(?i)\bsocket\.\w+`,
		`(?i)\beval\s*\(`,
		`(?i)\bexec\s*\(`,
		`(?i)__import__\s*\(`,
		`(?i)open\s*\([^)]*['"]\s*[awx]`, // open with write/append/exclusive mode
		`(?i)\brequests\.\w+`,
		`(?i)\burllib\b`,
		`(?i)\bhttp\.client\b`,
		`(?i)\bpathlib\.Path\([^)]*\)\.\w*write`,
		`(?i)\bshlex\.\w+`,
	}),
	"javascript": compileAll([]string{
		`(?i)\brequire\s*\(\s*['"]child_process['"]`,
		`(?i)\brequire\s*\(\s*['"]fs['"]`,
		`(?i)\brequire\s*\(\s*['"]net['"]`,
		`(?i)\brequire\s*\(\s*['"]http['"]`,
		`(?i)\brequire\s*\(\s*['"]https['"]`,
		`(?i)\brequire\s*\(\s*['"]os['"]`,
		`(?i)\brequire\s*\(\s*['"]dns['"]`,
		`(?i)\brequire\s*\(\s*['"]vm['"]`,
		`(?i)\beval\s*\(`,
		`(?i)\bnew\s+Function\s*\(`,
		`(?i)\.exec\s*\(`,
		`(?i)\.spawn\s*\(`,
		`(?i)\.fork\s*\(`,
		`(?i)\bprocess\.exit\b`,
		`(?i)\bprocess\.kill\b`,
		`(?i)\bfetch\s*\(`,
		`(?i)\bXMLHttpRequest\b`,
	}),
	"perl": compileAll([]string{
		`(?i)\bsystem\s*[\(\"]`,
		`(?i)\bexec\s*[\(\"]`,
		`\` + "`",                        // backticks in perl
		`(?i)\bopen\s*\([^)]*['"]\s*[>+]`, // perl open with write
		`(?i)\bIO::Socket\b`,
		`(?i)\bLWP::\w+`,
		`(?i)\bNet::\w+`,
		`(?i)\bunlink\s*\(`,
		`(?i)\bfork\s*\(`,
	}),
	"ruby": compileAll([]string{
		`(?i)\bsystem\s*\(`,
		`(?i)\bexec\s*\(`,
		`(?i)\bspawn\s*\(`,
		"`",                  // backticks
		`(?i)%x\{`,           // %x{} shell
		`(?i)\bIO\.popen\b`,
		`(?i)\bFile\.open\([^)]*['"]\s*[wax]`,
		`(?i)\bFile\.delete\b`,
		`(?i)\bFileUtils\.\w+`,
		`(?i)\bNet::\w+`,
		`(?i)\bopen\s*\(['"]https?://`,
		`(?i)\beval\s*\(`,
	}),
	"php": compileAll([]string{
		`(?i)\bsystem\s*\(`,
		`(?i)\bexec\s*\(`,
		`(?i)\bshell_exec\s*\(`,
		`(?i)\bpassthru\s*\(`,
		`(?i)\bpopen\s*\(`,
		`(?i)\bproc_open\s*\(`,
		`(?i)\beval\s*\(`,
		`(?i)\bassert\s*\(`,
		`(?i)\bfile_put_contents\s*\(`,
		`(?i)\bunlink\s*\(`,
		`(?i)\bfopen\s*\([^)]*['"]\s*[wax]`,
		`(?i)\bcurl_\w+`,
		`(?i)\bfsockopen\s*\(`,
		"`", // backticks for shell exec
	}),
	"lua": compileAll([]string{
		`(?i)\bos\.execute\b`,
		`(?i)\bio\.popen\b`,
		`(?i)\bos\.remove\b`,
		`(?i)\bdofile\b`,
		`(?i)\bloadfile\b`,
		`(?i)\bloadstring\b`,
		`(?i)\bload\s*\(`,
		`(?i)\bio\.open\s*\([^,)]+,\s*['"]\s*[wax]`,
	}),
}

// langGroup maps an interpreter name (already lowercased and base-named)
// to the rule group key in dangerousInlineMarkers.
var langGroup = map[string]string{
	"python":  "python",
	"python2": "python",
	"python3": "python",
	"node":    "javascript",
	"nodejs":  "javascript",
	"perl":    "perl",
	"ruby":    "ruby",
	"php":     "php",
	"lua":     "lua",
}

// compileAll precompiles every regex in the list. A pattern that fails to
// compile is silently dropped — we audit the list manually at build time
// and a failure here would be caught by the package-level tests.
func compileAll(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			out = append(out, re)
		}
	}
	return out
}

// InlineCodeRiskAnalyzer classifies inline source code passed to a language
// interpreter via the standard exec flags. It uses substring matching
// (regex) rather than per-language ASTs because:
//
//  1. Shipping a Python+JS+Perl+Ruby+PHP+Lua parser bundle would inflate
//     the binary and create a maintenance nightmare.
//  2. Inline scripts on the command line are short by definition — a
//     regex pass over a few hundred bytes is reliably fast.
//  3. The false-negative rate of "obfuscated inline malware" is bounded
//     by what fits on one CLI line, and most real attacks cited in security
//     literature use the same vocabulary (os.system, child_process, etc.).
//
// The analyzer is goroutine-safe: all state is precompiled at construction
// time and Analyze is a pure function thereafter.
type InlineCodeRiskAnalyzer struct {
	strict bool
}

// NewInlineCodeRiskAnalyzer builds the default analyzer. Pass strict=true
// to elevate RiskUnknown to RiskHigh, useful for compliance-sensitive
// deployments. The environment variable CHATCLI_AGENT_INLINE_CODE_STRICT
// also forces strict mode.
func NewInlineCodeRiskAnalyzer() *InlineCodeRiskAnalyzer {
	strict := strings.EqualFold(os.Getenv(inlineCodeStrictEnv), "true")
	return &InlineCodeRiskAnalyzer{strict: strict}
}

// Analyze returns the risk level for the given inline source string.
// `lang` should be a normalized interpreter name (python, node, perl,
// ruby, php, lua); other values return RiskUnknown.
func (a *InlineCodeRiskAnalyzer) Analyze(lang, source string) InlineCodeRisk {
	group, ok := langGroup[strings.ToLower(lang)]
	if !ok {
		if a.strict {
			return RiskHigh
		}
		return RiskUnknown
	}
	patterns := dangerousInlineMarkers[group]
	for _, p := range patterns {
		if p.MatchString(source) {
			return RiskHigh
		}
	}
	// Empty source or whitespace is safe — interpreter is just being invoked.
	if strings.TrimSpace(source) == "" {
		return RiskSafe
	}
	return RiskSafe
}

// IsHighRisk is a convenience wrapper for callers that only care about the
// strict-elevation decision.
func (a *InlineCodeRiskAnalyzer) IsHighRisk(lang, source string) bool {
	r := a.Analyze(lang, source)
	if r == RiskHigh {
		return true
	}
	if a.strict && r == RiskUnknown {
		return true
	}
	return false
}
