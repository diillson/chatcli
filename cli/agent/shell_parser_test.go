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

// TestParseShellSegments_SinglePipeline verifies the parser splits on `|`
// correctly. This is the foundation for per-segment validation policies
// (e.g., "left of pipe is a read, right of pipe is a transform → safe").
func TestParseShellSegments_SinglePipeline(t *testing.T) {
	segs := ParseShellSegments(`ls -l | grep go`)
	if assert.Len(t, segs, 2) {
		assert.Equal(t, "ls", segs[0].Cmd)
		assert.Equal(t, []string{"-l"}, segs[0].Args)
		assert.False(t, segs[0].HasPipe, "first command does not live inside a pipe")
		assert.Equal(t, "grep", segs[1].Cmd)
		assert.Equal(t, []string{"go"}, segs[1].Args)
		assert.True(t, segs[1].HasPipe, "second command IS the right-hand side of a pipe")
	}
}

// TestParseShellSegments_AndOrSemicolon confirms that other shell
// operators also produce multiple segments — but only `|` flips HasPipe.
// This matters because policy "safe stdin consumer on the right of a
// pipe" doesn't apply to `; jq` (where jq has no inherited stdin).
func TestParseShellSegments_AndOrSemicolon(t *testing.T) {
	cases := []struct {
		line       string
		want       int
		hasPipeAt1 bool
	}{
		{`a && b`, 2, false},
		{`a || b`, 2, false},
		{`a ; b`, 2, false},
		{`a | b`, 2, true},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			segs := ParseShellSegments(c.line)
			if assert.Len(t, segs, c.want) {
				assert.Equal(t, c.hasPipeAt1, segs[1].HasPipe)
			}
		})
	}
}

// TestParseShellSegments_QuotedPipeIsLiteral guarantees the parser is
// robust against the regex-trap classic: a `|` inside a quoted string
// looks like a pipe to naive split but isn't.
func TestParseShellSegments_QuotedPipeIsLiteral(t *testing.T) {
	segs := ParseShellSegments(`echo "a | b" | wc -l`)
	if assert.Len(t, segs, 2, "outer | splits; inner | is preserved inside the echo arg") {
		assert.Equal(t, "echo", segs[0].Cmd)
		assert.Equal(t, []string{"a | b"}, segs[0].Args,
			"the quoted pipe must arrive as a single arg, not split")
	}
}

// TestParseShellSegments_FallbackOnParseError ensures we don't silently
// bypass policy when the parser chokes. Returning nil would mean the
// validator never sees the command; we return a single opaque segment
// instead so regex fallback still applies.
func TestParseShellSegments_FallbackOnParseError(t *testing.T) {
	// Unterminated string — would fail to parse cleanly. The parser
	// recovers with a best-effort single-segment fallback.
	segs := ParseShellSegments(`echo "unterminated`)
	if assert.NotEmpty(t, segs) {
		assert.Equal(t, `echo "unterminated`, segs[0].Full)
	}
}

// TestSegmentIsInlineCodeInvocation pins the contract used by the
// CommandValidator to find python/node/perl/ruby/php/lua interpreter
// segments. Detection is base-name aware (so /usr/bin/python3 works) and
// requires the standard exec flag in argv.
func TestSegmentIsInlineCodeInvocation(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		wantLang   string
		wantFlagAt int
		wantOk     bool
	}{
		{"python -c", `python -c "print(1)"`, "python", 0, true},
		{"python3 -c", `python3 -c 'print(1)'`, "python3", 0, true},
		{"node -e", `node -e "console.log(1)"`, "node", 0, true},
		{"perl -e", `perl -e 'print 1'`, "perl", 0, true},
		{"ruby -e", `ruby -e 'puts 1'`, "ruby", 0, true},
		{"php -r", `php -r 'echo 1;'`, "php", 0, true},
		{"absolute path python", `/usr/bin/python3 -c "print(1)"`, "python3", 0, true},
		{"plain python script (no -c)", `python script.py`, "", 0, false},
		{"plain ls", `ls -la`, "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs := ParseShellSegments(c.line)
			if assert.NotEmpty(t, segs) {
				lang, flagPos, ok := segs[0].IsInlineCodeInvocation()
				assert.Equal(t, c.wantOk, ok)
				if c.wantOk {
					assert.Equal(t, c.wantLang, lang)
					assert.Equal(t, c.wantFlagAt, flagPos)
				}
			}
		})
	}
}

// TestSegmentInlineSource extracts the source string passed via -c/-e/-r
// from the argv. Joined quoting is unwrapped by the parser, so we get the
// raw program text the user intended to execute.
func TestSegmentInlineSource(t *testing.T) {
	segs := ParseShellSegments(`python -c "import sys; print(sys.version)"`)
	if assert.Len(t, segs, 1) {
		_, flagPos, _ := segs[0].IsInlineCodeInvocation()
		assert.Equal(t, "import sys; print(sys.version)", segs[0].InlineSource(flagPos))
	}
}

// TestPureStdinConsumers documents which right-of-pipe commands are
// inherently no-side-effect. Used by the CommandValidator to short-circuit
// "safe | safe" pipelines. sed is conditional (in-place edits via -i are
// NOT safe).
func TestPureStdinConsumers(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`grep pattern`, true},
		{`jq .`, true},
		{`awk '{print}'`, true},
		{`head -1`, true},
		{`sort`, true},
		{`uniq -c`, true},
		{`sed -n '1p'`, true},
		{`sed -i 's/x/y/'`, false}, // in-place edit
		{`tee file.out`, false},    // tee writes file
		{`curl`, false},
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

// TestParseShellSegments_EmptyInput returns nil — no segments to evaluate,
// and the caller (validator) treats nil as a no-op.
func TestParseShellSegments_EmptyInput(t *testing.T) {
	assert.Empty(t, ParseShellSegments(""))
	assert.Empty(t, ParseShellSegments("   \n  "))
}
