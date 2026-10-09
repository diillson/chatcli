/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
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

func TestTakeCoderHandoff(t *testing.T) {
	task, reply := takeCoderHandoff("Sure, I'll list them.\n<coder_handoff>list the files here</coder_handoff>")
	assert.Equal(t, "list the files here", task)
	assert.Equal(t, "Sure, I'll list them.", reply)

	task, reply = takeCoderHandoff("<coder_handoff>run the tests</coder_handoff>")
	assert.Equal(t, "run the tests", task)
	assert.Equal(t, i18n.T("handoff.only_tag"), reply, "a tag-only reply never stores an empty turn")

	task, reply = takeCoderHandoff("a goroutine is a lightweight thread")
	assert.Empty(t, task)
	assert.Equal(t, "a goroutine is a lightweight thread", reply)
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
	assert.Contains(t, cli.modeAndLanguagePart().Text, ChatModeSystemHint, "headless: the banner keeps its exact bytes")
	cli.replActive = true
	text := cli.modeAndLanguagePart().Text
	assert.Contains(t, text, "<coder_handoff>")
	assert.True(t, strings.HasPrefix(text, "[ACTIVE MODE: chat]"), "the mode marker stays first")
	assert.Equal(t, 0, strings.Index(text, chatModeBanner(true)), "the handoff banner leads the block")
}

// The redirect rule ("please use /coder") contradicted the handoff
// instruction and models followed it: "execute ls" got "use /coder ls"
// instead of a proposal. On an attended REPL turn the rule is replaced.
func TestChatModeBanner_HandoffReplacesTheRedirectRule(t *testing.T) {
	assert.Contains(t, ChatModeSystemHint, chatModeRedirectRule,
		"the banner's rule 3 changed; update chatModeRedirectRule or the handoff banner keeps the contradiction")

	assert.Equal(t, ChatModeSystemHint, chatModeBanner(false), "no handoff: the banner is untouched")

	b := chatModeBanner(true)
	assert.NotEqual(t, ChatModeSystemHint, b)
	assert.NotContains(t, b, "please use /coder", "never tell the user to type /coder when a proposal is possible")
	assert.Contains(t, b, chatModeHandoffRule)
	assert.Contains(t, b, "[ACTIVE MODE: chat]", "mode detection still sees chat")
	for _, rule := range []string{"1. You MUST NOT emit execute blocks", "2. Your role is purely conversational", "4. You CAN show code snippets"} {
		assert.Contains(t, b, rule, "the other rules stay")
	}
}

func TestModeAndLanguagePart_HandoffBlockIsConsistent(t *testing.T) {
	t.Setenv(coderHandoffEnv, "")
	cli := &ChatCLI{logger: zap.NewNop(), replActive: true}
	text := cli.modeAndLanguagePart().Text
	assert.NotContains(t, text, "please use /coder", "no instruction in the block points the user to type /coder")
	assert.Contains(t, text, "execute ls", "bare commands are named as handoff cases")
	instr, lang := strings.Index(text, coderHandoffInstruction), strings.Index(text, i18n.T("ai.response_language"))
	assert.True(t, instr > 0 && instr < lang, "the instruction sits next to the banner, before the language directive")
	assert.Equal(t, text, cli.modeAndLanguagePart().Text, "byte-stable across turns: the block is cached")
}
