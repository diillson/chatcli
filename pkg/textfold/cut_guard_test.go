/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package textfold

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// byteCutThenText matches a byte slice of a string joined to more text —
// "s[:n] + …" or "… + s[len(s)-n:]", the shape of a display or prompt cap.
// Only strings concatenate with +, so a slice of a slice never matches. Cut
// at a byte offset, it splits a multi-byte character and emits invalid
// UTF-8; use Cut / Tail.
var byteCutThenText = regexp.MustCompile(`\b[A-Za-z_][\w.]*\[:[^\][]+\]\s*\+|\+\s*([A-Za-z_][\w.]*)\[len\(\w[\w.]*\)\s*-[^\]:]+:\]`)

// byteCutAllowed are slices checked by hand: the offset is already moved to
// a rune boundary, or the expression is not a length cap.
var byteCutAllowed = map[string]string{
	"cli/gateway/telemetry.go":                      "masks a secret by a marker offset; not a cap",
	"cli/compress/prose.go":                         "head offset already snapped to a rune start",
	"pkg/coder/engine/commands_outline.go":          "stdlib-only package; offset snapped with utf8.RuneStart",
	"pkg/textfold/textfold.go":                      "Stem drops an ASCII suffix from a folded token; not a cap",
	"cli/workspace/memory/temporal.go":              "splices out a match located in the original by IndexIgnoreCase",
	"plugins-examples/chatcli-docker-clean/main.go": "Docker image IDs are hex",
	"plugins-examples/chatcli-docker-list/main.go":  "Docker image IDs are hex",
}

func TestNoByteCutBeforeAppendedText(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		if _, ok := byteCutAllowed[rel]; ok {
			return nil
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- repository source, test-only walk
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") {
				continue
			}
			if byteCutThenText.MatchString(line) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("byte-offset cut followed by appended text can split a UTF-8 character; use textfold.Cut:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
