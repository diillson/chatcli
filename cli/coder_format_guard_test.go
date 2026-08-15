/*
 * ChatCLI - coder format guard tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */

package cli

import "testing"

func TestLooksLikeLooseCode_AgentCallWithHashLinesExempt(t *testing.T) {
	// Real-world shape: squad dispatch whose task embeds Python file contents,
	// including comment lines starting with "# " at column zero. The old
	// inline guard matched ^[$#]\s+ against the whole response and rejected
	// the dispatch with a FORMAT ERROR before it was ever parsed.
	resp := `<agent_call agent="coder" task="Crie os arquivos abaixo.

ARQUIVO app/core/init.py - conteudo:

# core
ARQUIVO app/services/init.py - conteudo:

# services" />`
	if looksLikeLooseCode(resp) {
		t.Fatal("agent_call payload with hash-comment lines must not be flagged as loose code")
	}
}

func TestLooksLikeLooseCode_AgentCallWithFenceExempt(t *testing.T) {
	resp := "<agent_call agent=\"coder\" task=\"Documente com:\n```go\nfunc main() {}\n```\" />"
	if looksLikeLooseCode(resp) {
		t.Fatal("agent_call payload with a markdown fence must not be flagged as loose code")
	}
}

func TestLooksLikeLooseCode_PlainFenceFlagged(t *testing.T) {
	resp := "Here is the file:\n```go\npackage main\n```"
	if !looksLikeLooseCode(resp) {
		t.Fatal("bare markdown fence without tool or agent calls must be flagged")
	}
}

func TestLooksLikeLooseCode_ShellPromptFlagged(t *testing.T) {
	resp := "Run this:\n$ go build ./...\nthen check the output"
	if !looksLikeLooseCode(resp) {
		t.Fatal("shell-prompt line without tool or agent calls must be flagged")
	}
}

func TestLooksLikeLooseCode_PlainProseClean(t *testing.T) {
	if looksLikeLooseCode("All files created successfully. The API is ready.") {
		t.Fatal("plain prose must not be flagged")
	}
}
