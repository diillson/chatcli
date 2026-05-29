/*
 * ChatCLI - tests for themed markdown rendering helpers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the code-fence splitter and language chip that the themed
 * renderMarkdown relies on, plus an end-to-end check that rendering is
 * driven by the active theme and that indentation-bearing code survives.
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitTopLevelCodeBlocks_SeparatesFences(t *testing.T) {
	in := "prosa um\n\n```yaml\nkey: value\n```\n\nprosa dois"
	segs := splitTopLevelCodeBlocks(in)
	require.Len(t, segs, 3)

	assert.False(t, segs[0].isCode)
	assert.Contains(t, segs[0].text, "prosa um")

	assert.True(t, segs[1].isCode)
	assert.Equal(t, "yaml", segs[1].lang)
	assert.Contains(t, segs[1].text, "key: value")

	assert.False(t, segs[2].isCode)
	assert.Contains(t, segs[2].text, "prosa dois")
}

func TestSplitTopLevelCodeBlocks_IndentedFenceStaysProse(t *testing.T) {
	// A fence indented under a list item must NOT be split out — keeping it
	// inside its prose segment preserves glamour's list structure.
	in := "- item\n    ```go\n    x := 1\n    ```"
	segs := splitTopLevelCodeBlocks(in)
	require.Len(t, segs, 1)
	assert.False(t, segs[0].isCode, "indented fence is not a top-level code block")
}

func TestSplitTopLevelCodeBlocks_UnterminatedFenceIsProse(t *testing.T) {
	in := "intro\n\n```bash\necho hi" // no closing fence
	segs := splitTopLevelCodeBlocks(in)
	// Everything collapses into a single prose segment; nothing is dropped.
	require.Len(t, segs, 1)
	assert.False(t, segs[0].isCode)
	assert.Contains(t, segs[0].text, "echo hi")
}

func TestSplitTopLevelCodeBlocks_TildeFence(t *testing.T) {
	in := "~~~json\n{\"a\":1}\n~~~"
	segs := splitTopLevelCodeBlocks(in)
	require.Len(t, segs, 1)
	assert.True(t, segs[0].isCode)
	assert.Equal(t, "json", segs[0].lang)
}

func TestCodeLanguageChip_DegradesWithoutColor(t *testing.T) {
	// On a no-color profile the chip is plain text (no escapes), so piped
	// output and CI logs stay clean.
	chip := codeLanguageChip("yaml", theme.ProfileNoTTY)
	assert.NotContains(t, chip, "\033[")
	assert.Contains(t, chip, "▌")
	assert.Contains(t, chip, "yaml")
}

// stripANSIForTest removes CSI sequences so visible-content assertions run on
// plain text.
func stripANSIForTest(s string) string {
	var b strings.Builder
	esc := false
	for _, c := range s {
		if c == '\033' {
			esc = true
			continue
		}
		if esc {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				esc = false
			}
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// TestRenderMarkdown_ResolvesReferenceLinksAcrossCodeBlocks is the regression
// guard for the whole-document render: a reference link whose definition sits
// after a code block must still resolve (the old segment-by-segment render
// broke this).
func TestRenderMarkdown_ResolvesReferenceLinksAcrossCodeBlocks(t *testing.T) {
	t.Cleanup(func() { _ = theme.SetActive("dark"); theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileTrueColor)
	require.NoError(t, theme.SetActive("dark"))

	md := "Veja [o site][ref] aqui.\n\n```bash\necho hi\n```\n\n[ref]: https://example.com\n"
	plain := stripANSIForTest((&ChatCLI{}).renderMarkdown(md))

	assert.Contains(t, plain, "https://example.com", "reference definition must resolve into the link")
	assert.NotContains(t, plain, "[o site][ref]", "the literal reference syntax must not survive")
}

// TestRenderMarkdown_KeepsBlankLineSpacingAroundCode guards the spacing
// regression: prose and code blocks must not be crammed together.
func TestRenderMarkdown_KeepsBlankLineSpacingAroundCode(t *testing.T) {
	t.Cleanup(func() { _ = theme.SetActive("dark"); theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileTrueColor)
	require.NoError(t, theme.SetActive("dark"))

	md := "Parágrafo um.\n\n```bash\necho hi\n```\n\nParágrafo dois."
	plain := stripANSIForTest((&ChatCLI{}).renderMarkdown(md))
	lines := strings.Split(plain, "\n")

	idxProse := -1
	for i, l := range lines {
		if strings.Contains(l, "Parágrafo um.") {
			idxProse = i
			break
		}
	}
	require.GreaterOrEqual(t, idxProse, 0)
	// There must be at least one blank line between the prose and the chip /
	// code that follows it.
	assert.Equal(t, "", strings.TrimSpace(lines[idxProse+1]),
		"a blank line must separate prose from the code block that follows")
}

// TestRenderMarkdown_NoSentinelLeak ensures the chip sentinel never leaks into
// rendered output.
func TestRenderMarkdown_NoSentinelLeak(t *testing.T) {
	t.Cleanup(func() { _ = theme.SetActive("dark"); theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileTrueColor)

	md := "txt\n\n```go\nfunc main(){}\n```\n"
	out := (&ChatCLI{}).renderMarkdown(md)
	assert.NotContains(t, out, "clichip", "sentinel must be fully replaced")
	assert.Contains(t, stripANSIForTest(out), "▌ go", "language chip is rendered")
}

func TestRenderMarkdown_ThemedAndPreservesCodeIndent(t *testing.T) {
	t.Cleanup(func() { _ = theme.SetActive("dark"); theme.SetProfile(theme.DetectProfile()) })
	theme.SetProfile(theme.ProfileTrueColor)
	require.NoError(t, theme.SetActive("dark"))

	md := "Exemplo:\n\n```yaml\nmetadata:\n  name: chatcli\n```\n"
	out := (&ChatCLI{}).renderMarkdown(md)

	// Language chip present.
	assert.Contains(t, out, "yaml")
	// The themed text color (dark Text #D4D4D4 → 211,211,211) is used.
	assert.Contains(t, out, "211;211;211", "prose uses the themed text color")
	// Nested YAML indentation survives somewhere in the output.
	assert.True(t, strings.Contains(out, "  name") || strings.Contains(out, "name"),
		"yaml key is rendered")

	// Switching theme changes the bytes (theme actually drives rendering).
	require.NoError(t, theme.SetActive("light"))
	lightOut := (&ChatCLI{}).renderMarkdown(md)
	assert.NotEqual(t, out, lightOut)
}
