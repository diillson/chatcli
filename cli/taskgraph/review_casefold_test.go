/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package taskgraph

import "testing"

func TestParseVerdict_SurvivesCaseMappingLengthChanges(t *testing.T) {
	verdict, evidence := parseVerdict("análise\nıııı ſſ verdict: PASS — testes verdes")
	if verdict != "PASS" || evidence != "testes verdes" {
		t.Errorf("verdict=%q evidence=%q", verdict, evidence)
	}
}
