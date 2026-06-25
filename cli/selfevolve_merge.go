/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * selfevolve_merge.go — the on-demand skill-body merge (the "pull" half).
 *
 * Per-turn the engine injects only a tiny skill index card (names + one-line
 * descriptions). The full body of a skill is never in the prompt. When — and
 * only when — an evolution is actually detected, this makes ONE targeted LLM
 * call that loads just that single skill's body and folds the improvement in.
 * Cost therefore scales with real evolutions, not with skill-count × turns,
 * mirroring memory's index/recall split.
 */
package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

const (
	// selfEvolveMergeBodyCap bounds how much of a current skill body is sent to
	// the merge call, so an unusually large skill cannot blow up the request.
	selfEvolveMergeBodyCap = 6000
	// selfEvolveMergeTimeout caps the targeted merge call.
	selfEvolveMergeTimeout = 45 * time.Second
)

const skillMergePrompt = `You are improving an existing ChatCLI skill named %q.

CURRENT SKILL BODY (markdown):
---
%s
---

IMPROVEMENT TO INTEGRATE (from a recent conversation):
%s

Return ONLY the full improved markdown body. PRESERVE the steps that still
apply, integrate the improvement, and keep it focused and well-ordered. Do NOT
include YAML frontmatter, do NOT wrap the output in code fences, and do NOT add
any commentary before or after.`

// mergeSkillBody implements skillMerger via a single, targeted LLM call. It is
// passed to applySkillCandidates by the worker; tests substitute a stub.
func (cli *ChatCLI) mergeSkillBody(ctx context.Context, name, currentBody, improvement string) (string, error) {
	client := cli.getClient()
	if client == nil {
		return "", fmt.Errorf("no LLM client available for skill merge")
	}

	body := currentBody
	if len(body) > selfEvolveMergeBodyCap {
		body = body[:selfEvolveMergeBodyCap] + "\n... [truncated] ..."
	}
	prompt := fmt.Sprintf(skillMergePrompt, name, body, strings.TrimSpace(improvement))

	ctx, cancel := context.WithTimeout(ctx, selfEvolveMergeTimeout)
	defer cancel()

	history := []models.Message{{Role: "user", Content: prompt}}
	out, err := client.SendPrompt(ctx, prompt, history, 0)
	if err != nil {
		return "", err
	}
	return cleanMergedBody(out), nil
}

// cleanMergedBody strips an accidental code fence or YAML frontmatter the model
// may have added despite instructions, so only the markdown body is persisted.
func cleanMergedBody(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		s = strings.TrimRight(s, "\n")
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(skillFileBody(s))
}
