/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package textfold

import "testing"

func TestFold(t *testing.T) {
	cases := map[string]string{
		"ação":        "acao",
		"café":        "cafe",
		"naïve":       "naive",
		"Próximo":     "Proximo",
		"plain ascii": "plain ascii",
		"":            "",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStem(t *testing.T) {
	cases := map[string]string{
		"jogos":         "jogo",
		"deploys":       "deploy",
		"configuracoes": "configuracao",
		"servidores":    "servidor",
		"queries":       "query",
		"deployed":      "deploy",
		"deploying":     "deploy",
		"bus":           "bus", // too short to stem
		"api":           "api",
		"flamengo":      "flamengo",
	}
	for in, want := range cases {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
}
