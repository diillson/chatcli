/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package engine

import (
	"context"
	"strings"
	"testing"
)

// TestExecDirAliases: --workingDir/--workdir/--cwd must be accepted as
// spellings of --dir instead of failing the whole call (models keep
// inventing them, and a strict parser turns each into a retry loop).
func TestExecDirAliases(t *testing.T) {
	for _, alias := range []string{"--workingDir", "--workdir", "--cwd"} {
		dir := t.TempDir()
		var out strings.Builder
		e := NewEngine(&out, &out, dir)
		err := e.Execute(context.Background(), "exec", []string{"--cmd", "pwd", alias, dir})
		if err != nil {
			t.Fatalf("exec with %s: %v (out: %s)", alias, err, out.String())
		}
		if !strings.Contains(out.String(), dir) {
			t.Fatalf("exec with %s did not run in the aliased dir: %s", alias, out.String())
		}
	}
}
