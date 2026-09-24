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

	"github.com/diillson/chatcli/models"
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

// legacyPlanTagRe matches the old spelling of the plan tag, open or close,
// in any case.
var legacyPlanTagRe = regexp.MustCompile(`(?i)<(/?)reasoning>`)

// normalizeLegacyPlanTags rewrites the old <reasoning> spelling to <plan>
// in the user and assistant turns of a history that was recorded before
// the rename — a loaded session, a resumed park, a hub conversation. The
// system slot is rebuilt from the current prompts every run, so it needs
// nothing; but the model's own earlier plan blocks and the format nudges
// ChatCLI stored as user turns ("you MUST write a <reasoning> block") would
// otherwise travel back to the API as they were written, and that is the
// demand the newest Claude models decline as reasoning_extraction. Tool
// results are left alone: quoted documentation is not an instruction. It
// edits in place and reports how many messages changed.
func normalizeLegacyPlanTags(history []models.Message) int {
	changed := 0
	for i := range history {
		if history[i].Role != "user" && history[i].Role != "assistant" {
			continue
		}
		if !strings.Contains(strings.ToLower(history[i].Content), "reasoning>") {
			continue
		}
		history[i].Content = legacyPlanTagRe.ReplaceAllString(history[i].Content, "<${1}plan>")
		changed++
	}
	return changed
}
