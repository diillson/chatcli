package agent

import (
	"strings"
	"testing"
)

// TestWrapTextBreaksOverlongFirstWord trava o bug do card de resultado: uma
// linha cujo ÚNICO token é gigante (ex.: o JSON de
// `last-applied-configuration` do `kubectl get -o yaml`, sem espaços) era
// escrita inteira quando começava uma linha vazia (curLen == 0), estourando
// a largura do box. Agora todo pedaço tem que caber no limite.
func TestWrapTextBreaksOverlongFirstWord(t *testing.T) {
	const limit = 40
	giant := `{"apiVersion":"apps/v1",` + strings.Repeat("x", 800) + `}`

	for _, line := range wrapText(giant, limit) {
		if w := VisibleLen(line); w > limit {
			t.Errorf("linha excedeu limit %d (got %d): %q", limit, w, line)
		}
	}
}

// TestWrapTextMixedParagraphs garante que o caso normal (prosa) continua
// quebrando por palavra e que linhas com palavra longa no meio também cabem.
func TestWrapTextMixedParagraphs(t *testing.T) {
	const limit = 30
	text := "uma linha curta\n" +
		strings.Repeat("z", 120) + "\n" +
		"palavra normal " + strings.Repeat("y", 100) + " fim"

	lines := wrapText(text, limit)
	if len(lines) < 3 {
		t.Fatalf("esperava múltiplas linhas, got %d", len(lines))
	}
	for i, ln := range lines {
		if w := VisibleLen(ln); w > limit {
			t.Errorf("linha %d excedeu limit %d (got %d): %q", i, limit, w, ln)
		}
	}
}

// TestHardBreakWordWideRunes confirma a quebra rune-aware com glyphs de 2
// colunas, sem cortar UTF-8 no meio.
func TestHardBreakWordWideRunes(t *testing.T) {
	const limit = 10
	for _, chunk := range hardBreakWord(strings.Repeat("日", 50), limit) {
		if w := VisibleLen(chunk); w > limit {
			t.Errorf("chunk excedeu limit %d (got %d): %q", limit, w, chunk)
		}
		// cada chunk tem que ser UTF-8 válido (runas inteiras)
		if !utf8ValidWholeRunes(chunk) {
			t.Errorf("chunk cortou runa no meio: %q", chunk)
		}
	}
}

func utf8ValidWholeRunes(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}
