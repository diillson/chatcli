/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"go.uber.org/zap"
)

func TestAppendLessonDocWritesMarkdown(t *testing.T) {
	ws := t.TempDir()
	cli := &ChatCLI{logger: zap.NewNop()}
	lesson := quality.Lesson{
		Situation:  "ran a destructive command without confirmation",
		Mistake:    "deleted files outside the workspace",
		Correction: "always scope rm to the project and confirm first",
		Tags:       []string{"safety", "shell"},
		Trigger:    "error",
		CreatedAt:  time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC),
	}
	cli.appendLessonDoc(ws, lesson)

	path := filepath.Join(ws, ".chatcli", "reflexion", "LESSONS.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("LESSONS.md not written: %v", err)
	}
	doc := string(data)
	for _, want := range []string{
		"# Lessons learned",
		"trigger: error",
		"**Mistake:** deleted files outside the workspace",
		"**Correction:** always scope rm",
		"safety, shell",
		"2026-06-22T12:00:00Z",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("LESSONS.md missing %q\n---\n%s", want, doc)
		}
	}

	// A second lesson appends (title written only once).
	cli.appendLessonDoc(ws, quality.Lesson{Correction: "second lesson", Trigger: "manual", CreatedAt: time.Now()})
	data2, _ := os.ReadFile(path)
	if strings.Count(string(data2), "# Lessons learned") != 1 {
		t.Error("title should be written exactly once")
	}
	if !strings.Contains(string(data2), "second lesson") {
		t.Error("second lesson should be appended")
	}
}

func TestAppendLessonDocRespectsOffSwitch(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("CHATCLI_QUALITY_REFLEXION_DOC", "off")
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.appendLessonDoc(ws, quality.Lesson{Correction: "x", Trigger: "error", CreatedAt: time.Now()})
	if _, err := os.Stat(filepath.Join(ws, ".chatcli", "reflexion", "LESSONS.md")); !os.IsNotExist(err) {
		t.Fatal("doc must not be written when CHATCLI_QUALITY_REFLEXION_DOC=off")
	}
}
