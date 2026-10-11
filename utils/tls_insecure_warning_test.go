/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// Disabling certificate verification must be visible at boot, not only in a
// log file: once per process for the global and the Bedrock switch alike.
func TestInsecureTLSWarningIsPrintedOnce(t *testing.T) {
	var out bytes.Buffer
	prev := insecureTLSWarnings
	insecureTLSWarnings = &out
	insecureTLSWarned = sync.Map{}
	t.Cleanup(func() { insecureTLSWarnings = prev; insecureTLSWarned = sync.Map{} })

	t.Setenv(envInsecureSkipVerify, "")
	t.Setenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "")
	warnInsecureTLS()
	if out.Len() != 0 {
		t.Fatalf("no switch set, no warning: %q", out.String())
	}

	t.Setenv(envInsecureSkipVerify, "true")
	t.Setenv("CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY", "TRUE")
	warnInsecureTLS()
	warnInsecureTLS()
	got := out.String()
	if strings.Count(got, "\n") != 2 ||
		!strings.Contains(got, envInsecureSkipVerify) ||
		!strings.Contains(got, "CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY") {
		t.Fatalf("want one line per switch: %q", got)
	}
}
