/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseShellSegments_IfClause exercises the if/then/else walker so
// security policy applies across every branch of a conditional. A
// dangerous command hiding inside an else block must still surface in
// the segment list.
func TestParseShellSegments_IfClause(t *testing.T) {
	segs := ParseShellSegments(`if true; then echo a; else rm -rf /tmp; fi`)
	names := segCmdNames(segs)
	assert.Contains(t, names, "true")
	assert.Contains(t, names, "echo")
	assert.Contains(t, names, "rm")
}

// TestParseShellSegments_IfElifChain ensures every branch of an
// if/elif/elif/else cascade is visited. The walker iterates the Else
// chain recursively.
func TestParseShellSegments_IfElifChain(t *testing.T) {
	segs := ParseShellSegments(`if a; then b; elif c; then d; elif e; then f; else g; fi`)
	names := segCmdNames(segs)
	for _, want := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		assert.Contains(t, names, want, "missing segment: %s", want)
	}
}

// TestParseShellSegments_ForClause confirms commands inside a for body
// land in the segment list. Used to catch `for x in *; do rm $x; done`.
func TestParseShellSegments_ForClause(t *testing.T) {
	segs := ParseShellSegments(`for x in a b c; do echo $x; rm $x; done`)
	names := segCmdNames(segs)
	assert.Contains(t, names, "echo")
	assert.Contains(t, names, "rm")
}

// TestParseShellSegments_WhileClause is the analog for while loops.
func TestParseShellSegments_WhileClause(t *testing.T) {
	segs := ParseShellSegments(`while read line; do echo $line; done`)
	names := segCmdNames(segs)
	assert.Contains(t, names, "read")
	assert.Contains(t, names, "echo")
}

// TestParseShellSegments_CaseClause walks each case arm.
func TestParseShellSegments_CaseClause(t *testing.T) {
	segs := ParseShellSegments(`case "$x" in a) echo aa;; b) echo bb;; *) echo cc;; esac`)
	// At least the three echo invocations should be reachable. Note: case
	// patterns themselves are not CallExpr nodes; we only require the
	// command bodies.
	names := segCmdNames(segs)
	echoCount := 0
	for _, n := range names {
		if n == "echo" {
			echoCount++
		}
	}
	assert.GreaterOrEqual(t, echoCount, 3, "every case body must produce a segment")
}

// TestParseShellSegments_BlockGroup covers the `{ ... }` grouping syntax.
func TestParseShellSegments_BlockGroup(t *testing.T) {
	segs := ParseShellSegments(`{ echo a; echo b; }`)
	names := segCmdNames(segs)
	assert.Contains(t, names, "echo")
	assert.GreaterOrEqual(t, len(segs), 2)
}

// TestParseShellSegments_Subshell — `(cmd; cmd)` runs in a subshell.
// The walker treats the inner commands as regular segments.
func TestParseShellSegments_Subshell(t *testing.T) {
	segs := ParseShellSegments(`(cd /tmp && ls && pwd)`)
	names := segCmdNames(segs)
	assert.Contains(t, names, "cd")
	assert.Contains(t, names, "ls")
	assert.Contains(t, names, "pwd")
}

// TestParseShellSegments_BackgroundDoesNotConfuseSplit verifies that
// `cmd1 & cmd2` produces two segments. The `&` operator is treated as
// a sequence terminator equivalent to `;`.
func TestParseShellSegments_BackgroundDoesNotConfuseSplit(t *testing.T) {
	segs := ParseShellSegments(`sleep 1 & echo done`)
	names := segCmdNames(segs)
	assert.Contains(t, names, "sleep")
	assert.Contains(t, names, "echo")
}

// TestSegmentInlineSource_NoExtraArgsReturnsEmpty pins the edge case
// where the interpreter was invoked with -c but no source was passed
// (real user mistake). InlineSource must not panic.
func TestSegmentInlineSource_NoExtraArgsReturnsEmpty(t *testing.T) {
	segs := ParseShellSegments(`python -c`)
	if assert.Len(t, segs, 1) {
		_, pos, ok := segs[0].IsInlineCodeInvocation()
		assert.True(t, ok)
		assert.Empty(t, segs[0].InlineSource(pos))
	}
}

// TestSegmentInlineSource_FlagAtEndOfArgs covers `python script.py -c`
// where -c appears AFTER the script name; semantics are still "flag
// position recorded" but the value just after is the empty fallback.
func TestSegmentInlineSource_FlagAtEndOfArgs(t *testing.T) {
	segs := ParseShellSegments(`python script.py -c`)
	if assert.Len(t, segs, 1) {
		_, pos, ok := segs[0].IsInlineCodeInvocation()
		if assert.True(t, ok) {
			assert.Empty(t, segs[0].InlineSource(pos))
		}
	}
}

// segCmdNames extracts just the .Cmd field from each segment for easy
// table-driven asserts.
func segCmdNames(segs []ShellSegment) []string {
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		out = append(out, s.Cmd)
	}
	return out
}

// TestPureStdinConsumer_SedInPlaceIsNotPure pins the contract: sed -i
// (in-place) is a mutation; sed without -i is a transformer. This
// matters for the orchestrator's partition policy.
func TestPureStdinConsumer_SedInPlaceIsNotPure(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`sed -n '1,5p'`, true},
		{`sed 's/x/y/'`, true},
		{`sed -i 's/x/y/' file`, false},
		{`sed -i.bak 's/x/y/' file`, false},
		{`sed -ibak 's/x/y/' file`, false},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			segs := ParseShellSegments(c.line)
			if assert.NotEmpty(t, segs) {
				assert.Equal(t, c.want, segs[0].IsPureStdinConsumer())
			}
		})
	}
}

// TestParseShellSegments_HeredocPreservesBody ensures heredoc content
// (which may contain operators that look like pipes/semicolons) doesn't
// create spurious segments.
func TestParseShellSegments_HeredocPreservesBody(t *testing.T) {
	segs := ParseShellSegments("cat <<EOF\n|||\n;;;\nEOF")
	// One segment for cat; the heredoc body is part of cat's input, not
	// a separate command.
	assert.Equal(t, 1, len(segs))
	if len(segs) > 0 {
		assert.Equal(t, "cat", segs[0].Cmd)
	}
}
