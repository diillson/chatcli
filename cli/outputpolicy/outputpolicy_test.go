/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package outputpolicy

import (
	"strings"
	"testing"
)

func TestParseVerbosity(t *testing.T) {
	cases := map[string]Verbosity{
		"full": VerbosityFull, "off": VerbosityFull,
		"concise": VerbosityConcise, "": VerbosityConcise,
		"minimal": VerbosityMinimal, "terse": VerbosityMinimal,
	}
	for in, want := range cases {
		if got, ok := ParseVerbosity(in); got != want || !ok {
			t.Errorf("ParseVerbosity(%q) = %v ok=%v, want %v", in, got, ok, want)
		}
	}
	if got, ok := ParseVerbosity("garbage"); ok || got != VerbosityConcise {
		t.Errorf("garbage should fall back to concise,false; got %v,%v", got, ok)
	}
}

func TestVerbosityDirective(t *testing.T) {
	if VerbosityFull.Directive() != "" {
		t.Error("full verbosity must produce no directive")
	}
	if !strings.Contains(VerbosityConcise.Directive(), "OUTPUT STYLE") {
		t.Error("concise directive missing header")
	}
	if !strings.Contains(VerbosityMinimal.Directive(), "fewest tokens") {
		t.Error("minimal directive missing its instruction")
	}
	// Directives must be static (cache-friendly): no format verbs that would
	// imply per-turn interpolation and break the cached system-prompt prefix.
	if strings.Contains(VerbosityConcise.Directive(), "%") || strings.Contains(VerbosityMinimal.Directive(), "%") {
		t.Error("directive must be a static string (no format verbs)")
	}
}

func TestClassifyComplex(t *testing.T) {
	complex := []string{
		"refactor the auth module to use interfaces",
		"why does the build fail on windows",
		"investigate the race condition in the scheduler",
		"migrate the database layer to Bedrock",
		"```go\nfunc x(){}\n``` fix this",
		"otimizar a query lenta do hub",
	}
	for _, q := range complex {
		if got := Classify(q); got != ComplexityComplex {
			t.Errorf("Classify(%q) = %v, want complex", q, got)
		}
	}
}

func TestClassifyTrivial(t *testing.T) {
	trivial := []string{
		"what is a goroutine",
		"list the providers",
		"o que é um mutex",
		"hi",
		"show the version",
	}
	for _, q := range trivial {
		if got := Classify(q); got != ComplexityTrivial {
			t.Errorf("Classify(%q) = %v, want trivial", q, got)
		}
	}
}

func TestClassifyNormalAndEmpty(t *testing.T) {
	if Classify("") != ComplexityNormal {
		t.Error("empty prompt should be normal")
	}
	// A medium-length specific request that is neither a quick lookup nor a
	// flagged complex task.
	q := "add a field called Timeout to the Config struct and wire it into NewClient please"
	if got := Classify(q); got == ComplexityTrivial {
		t.Errorf("a concrete multi-clause request should not be trivial, got %v", got)
	}
}
