/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The block the coder requires before any action is a work plan — the
 * actions the model will take, as a numbered task list — and the prompts
 * ask for it as <plan>. It used to be asked for as <reasoning>, and a
 * prompt that demands the model's reasoning in the reply is what the
 * newest Claude models decline as reasoning_extraction; the rename
 * removes the demand. Everything that reads the block accepts both
 * spellings, so older prompts, skills and personas keep working.
 */
package cli

import (
	"regexp"
	"strings"
)

// planTagNames are the spellings of the plan block, newest first.
var planTagNames = []string{"plan", "reasoning"}

// planBlockRe matches a whole plan block in either spelling.
var planBlockRe = regexp.MustCompile(`(?is)<plan>.*?</plan>|<reasoning>.*?</reasoning>`)

// hasPlanTag reports whether the reply carries a complete plan block, in
// either spelling.
func hasPlanTag(s string) bool {
	ls := strings.ToLower(s)
	for _, name := range planTagNames {
		if strings.Contains(ls, "<"+name+">") && strings.Contains(ls, "</"+name+">") {
			return true
		}
	}
	return false
}

// extractPlanBlock returns the content of the reply's plan block: the
// <plan> block when there is one, else the <reasoning> block, else "".
func extractPlanBlock(text string) string {
	for _, name := range planTagNames {
		if content, ok := extractXMLTagContent(text, name); ok && strings.TrimSpace(content) != "" {
			return content
		}
	}
	return ""
}

// stripPlanBlocks removes every plan block, in either spelling.
func stripPlanBlocks(text string) string {
	return planBlockRe.ReplaceAllString(text, "")
}
