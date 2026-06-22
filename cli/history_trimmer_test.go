package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestIsInjectedContextMessage(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "memory context marker with date",
			content: "[MEMORY CONTEXT — loaded from 2026-03-04]\nSome memory content here...",
			want:    true,
		},
		{
			name:    "memory context marker different date",
			content: "[MEMORY CONTEXT — loaded from 2025-12-31]\nOlder memory",
			want:    true,
		},
		{
			name:    "attached context marker Portuguese",
			content: "\U0001F4E6 CONTEXTO:\nFile contents follow...",
			want:    true,
		},
		{
			name:    "attached context marker English",
			content: "\U0001F4E6 CONTEXT:\nFile contents follow...",
			want:    true,
		},
		{
			name:    "attached context marker on later line",
			content: "Some preamble\n\U0001F4E6 CONTEXTO:\nFile contents follow...",
			want:    true,
		},
		{
			name:    "normal user message",
			content: "Please explain how goroutines work",
			want:    false,
		},
		{
			name:    "empty string",
			content: "",
			want:    false,
		},
		{
			name:    "similar but not matching marker",
			content: "[MEMORY loaded from 2026-03-04]",
			want:    false,
		},
		{
			name:    "partial memory marker missing date",
			content: "[MEMORY CONTEXT — loaded from ]",
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isInjectedContextMessage(tc.content)
			if got != tc.want {
				t.Errorf("isInjectedContextMessage(%q) = %v, want %v", truncForLog(tc.content), got, tc.want)
			}
		})
	}
}

func TestIsToolFeedbackMessage(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "tool feedback English prefix",
			content: "The tool 'read' returned: file contents...",
			want:    true,
		},
		{
			name:    "tool feedback Portuguese prefix",
			content: "A ferramenta 'read' retornou: conteudo...",
			want:    true,
		},
		{
			name:    "result prefix Portuguese",
			content: "--- Resultado da Ação ---\nOutput here",
			want:    true,
		},
		{
			name:    "format error prefix",
			content: "FORMAT ERROR: invalid JSON response",
			want:    true,
		},
		{
			name:    "tool output XML tag",
			content: "Some prefix\n<tool_output>\nresult\n</tool_output>",
			want:    true,
		},
		{
			name:    "normal user message",
			content: "How do I use goroutines?",
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isToolFeedbackMessage(tc.content)
			if got != tc.want {
				t.Errorf("isToolFeedbackMessage(%q) = %v, want %v", truncForLog(tc.content), got, tc.want)
			}
		})
	}
}

func TestTrimInjectedContext(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	trimmer := NewMessageTrimmer(logger)

	tests := []struct {
		name          string
		content       string
		wantTrimmed   bool
		wantSubstring string // expected to appear in output
	}{
		{
			name:        "short content is not trimmed",
			content:     "[MEMORY CONTEXT — loaded from 2026-03-04]\nShort note",
			wantTrimmed: false,
		},
		{
			name:        "content at exactly 3000 chars is not trimmed",
			content:     "[MEMORY CONTEXT — loaded from 2026-03-04]\n" + strings.Repeat("x", 3000-len("[MEMORY CONTEXT — loaded from 2026-03-04]\n")),
			wantTrimmed: false,
		},
		{
			name:          "long content is truncated",
			content:       "[MEMORY CONTEXT — loaded from 2026-03-04]\nLine two\nLine three\n" + strings.Repeat("A", 5000),
			wantTrimmed:   true,
			wantSubstring: "chars of context omitted during compaction",
		},
		{
			name:          "preserves header lines",
			content:       "[MEMORY CONTEXT — loaded from 2026-03-04]\nSecond header line\nThird header line\n" + strings.Repeat("B", 5000),
			wantTrimmed:   true,
			wantSubstring: "[MEMORY CONTEXT",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := trimmer.trimInjectedContext(tc.content)

			if tc.wantTrimmed {
				if len(result) >= len(tc.content) {
					t.Errorf("expected content to be trimmed, but len(result)=%d >= len(original)=%d",
						len(result), len(tc.content))
				}
				if tc.wantSubstring != "" && !strings.Contains(result, tc.wantSubstring) {
					t.Errorf("expected result to contain %q, got:\n%s", tc.wantSubstring, truncForLog(result))
				}
			} else {
				if result != tc.content {
					t.Errorf("expected content to be unchanged, but it was modified")
				}
			}
		})
	}
}

func TestTrimMessagePreservesVerbatimFlagged(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	trimmer := NewMessageTrimmer(logger)

	// The model called @recall to read a previously-compressed original in full.
	// Its feedback message is flagged PreserveVerbatim at construction, so the
	// trimmer must leave it byte-identical — no knowledge of the tool-output
	// text format required. Re-trimming would discard the detail and force
	// another recall.
	original := strings.Repeat("important recalled line that must survive compaction\n", 200)
	feedback := "The tool 'batch_execution' was executed and returned the following:\n\n" +
		"<tool_output>\n--- Resultado da Ação 1 (@recall) ---\n" + original + "\n</tool_output>\n\n" +
		"(If the task is done, reply only with a final summary.)"

	flagged := models.Message{Role: "user", Content: feedback, Meta: &models.MessageMeta{PreserveVerbatim: true}}
	out := trimmer.trimMessage(flagged, []models.Message{flagged}, 0)
	if out.Content != feedback {
		t.Fatalf("PreserveVerbatim message was reduced during compaction; it must survive byte-identical")
	}

	// Control: the same content WITHOUT the flag is still truncated.
	unflagged := models.Message{Role: "user", Content: feedback}
	if outPlain := trimmer.trimMessage(unflagged, []models.Message{unflagged}, 0); strings.Contains(outPlain.Content, original) {
		t.Fatal("control: unflagged tool output should still be truncated, but it survived intact")
	}
}

// truncForLog truncates a string for use in test error messages.
func truncForLog(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
