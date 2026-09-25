/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestExtractCoderHandoff(t *testing.T) {
	task, cleaned := extractCoderHandoff("I see, the tests fail on the parser.\n\n<coder_handoff>Fix the failing parser tests in cli/parser_test.go</coder_handoff>")
	assert.Equal(t, "Fix the failing parser tests in cli/parser_test.go", task)
	assert.Equal(t, "I see, the tests fail on the parser.", cleaned)

	task, cleaned = extractCoderHandoff("no tag here")
	assert.Empty(t, task)
	assert.Equal(t, "no tag here", cleaned)

	task, cleaned = extractCoderHandoff("< Coder_Handoff >\n  add   a  README\n</CODER_HANDOFF>")
	assert.Equal(t, "add a README", task, "case-insensitive, whitespace collapsed")
	assert.Empty(t, cleaned)

	task, _ = extractCoderHandoff("<coder_handoff></coder_handoff> text <coder_handoff>second</coder_handoff>")
	assert.Equal(t, "second", task, "an empty tag is skipped, the first non-empty wins")

	long := strings.Repeat("x", coderHandoffMaxTask+50)
	task, _ = extractCoderHandoff("<coder_handoff>" + long + "</coder_handoff>")
	assert.Len(t, task, coderHandoffMaxTask)
}

func TestClassifyHandoffAnswer(t *testing.T) {
	for _, in := range []string{"", "  ", "y", "Y", "yes", "s", "sim", "ok"} {
		assert.Equal(t, handoffAccept, classifyHandoffAnswer(in), "%q accepts", in)
	}
	for _, in := range []string{"n", "no", "nao", "não", "N"} {
		assert.Equal(t, handoffDecline, classifyHandoffAnswer(in), "%q declines", in)
	}
	for _, in := range []string{"actually, explain the parser first", "/help", "yes please do it carefully"} {
		assert.Equal(t, handoffOther, classifyHandoffAnswer(in), "%q keeps chatting", in)
	}
}

func TestConsumeCoderHandoffAnswer(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}

	in, handled := cli.consumeCoderHandoffAnswer("hello")
	assert.False(t, handled)
	assert.Equal(t, "hello", in, "nothing pending: input untouched")

	cli.pendingCoderHandoff = "fix the parser tests"
	in, handled = cli.consumeCoderHandoffAnswer("")
	assert.True(t, handled)
	assert.Equal(t, "/coder fix the parser tests", in, "Enter accepts and becomes the /coder invocation")
	assert.Empty(t, cli.pendingCoderHandoff, "consumed")

	cli.pendingCoderHandoff = "fix the parser tests"
	in, handled = cli.consumeCoderHandoffAnswer("n")
	assert.True(t, handled)
	assert.Empty(t, in, "decline: nothing to run")
	assert.Empty(t, cli.pendingCoderHandoff)

	cli.pendingCoderHandoff = "fix the parser tests"
	in, handled = cli.consumeCoderHandoffAnswer("explain the parser first")
	assert.False(t, handled)
	assert.Equal(t, "explain the parser first", in, "other text is a normal chat turn")
	assert.Empty(t, cli.pendingCoderHandoff, "the proposal does not linger")
}

func TestCoderHandoffActive_OnlyOnAttendedREPL(t *testing.T) {
	t.Setenv(coderHandoffEnv, "")
	cli := &ChatCLI{logger: zap.NewNop()}
	assert.False(t, cli.coderHandoffActive(), "not in the REPL")
	cli.replActive = true
	assert.True(t, cli.coderHandoffActive())
	cli.unattended = true
	assert.False(t, cli.coderHandoffActive(), "headless surfaces never propose")
	cli.unattended = false
	t.Setenv(coderHandoffEnv, "false")
	assert.False(t, cli.coderHandoffActive(), "explicit opt-out")
	t.Setenv(coderHandoffEnv, "off")
	assert.False(t, coderHandoffEnabled())
	t.Setenv(coderHandoffEnv, "anything-else")
	assert.True(t, coderHandoffEnabled(), "only false/0/off/no disable")
}

func TestModeAndLanguagePart_CarriesTheHandoffInstructionOnlyWhenActive(t *testing.T) {
	t.Setenv(coderHandoffEnv, "")
	cli := &ChatCLI{logger: zap.NewNop()}
	assert.NotContains(t, cli.modeAndLanguagePart().Text, "<coder_handoff>", "headless: the model is not asked to propose")
	cli.replActive = true
	text := cli.modeAndLanguagePart().Text
	assert.Contains(t, text, "<coder_handoff>")
	assert.Contains(t, text, ChatModeSystemHint, "the base chat hint stays first")
}
