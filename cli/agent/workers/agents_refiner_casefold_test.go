/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package workers

import "testing"

func TestParseRefineSections_SurvivesCaseMappingLengthChanges(t *testing.T) {
	task, draft := parseRefineSections("İİİ Task: revisar o parser\n\nDraft: versão nova")
	if task != "revisar o parser" || draft != "versão nova" {
		t.Errorf("task=%q draft=%q", task, draft)
	}
}
