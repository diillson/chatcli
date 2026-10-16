/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/plugins"
)

type recallE2EAdapter struct{ content string }

func (r recallE2EAdapter) Recall(string) (string, bool)               { return r.content, true }
func (r recallE2EAdapter) Compress(_, content string) (string, error) { return content, nil }
func (r recallE2EAdapter) Stats() string                              { return "" }

// A recall goes through the agent's own argument parser and comes back in
// pages that each fit one tool result, so the loop never cuts it to a
// preview; walking the pages rebuilds the original.
func TestRecallPagesFitTheAgentResultCap(t *testing.T) {
	t.Setenv("CHATCLI_TOOL_RESULT_MAX_CHARS", "")
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "row %05d: payload %d\n", i, i*7)
	}
	original := b.String()
	plugins.SetCompressionAdapter(recallE2EAdapter{content: original})
	t.Cleanup(func() { plugins.SetCompressionAdapter(nil) })

	const key = "0123abcd0123abcd"
	var rebuilt strings.Builder
	offset := 0
	for page := 0; page < 50; page++ {
		argv, err := parseToolArgsWithJSON(fmt.Sprintf(`{"cmd":"recall","args":{"key":"%s","offset":%d}}`, key, offset))
		if err != nil {
			t.Fatal(err)
		}
		out, err := plugins.NewBuiltinRecallPlugin().Execute(context.Background(), argv)
		if err != nil {
			t.Fatalf("page %d (argv %v): %v", page, argv, err)
		}
		if len(out) > agent.DefaultPerResultMaxChars {
			t.Fatalf("page %d is %d chars, over the agent's per-result cap %d", page, len(out), agent.DefaultPerResultMaxChars)
		}
		chunk, footer, _ := strings.Cut(out, "\n\n[@recall: characters ")
		rebuilt.WriteString(chunk)
		offset += len(chunk)
		if strings.Contains(footer, "end of the original") {
			if rebuilt.String() != original {
				t.Fatal("the pages must rebuild the original")
			}
			return
		}
	}
	t.Fatal("paging never reached the end")
}
