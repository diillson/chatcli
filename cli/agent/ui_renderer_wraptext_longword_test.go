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

// TestWrapPreserveKeepsIndentAndColumns garante que o wrap do card de
// resultado preserva indentação de YAML e espaçamento de colunas (linhas que
// cabem ficam idênticas), diferente do word-wrap de prosa que colapsava tudo.
func TestWrapPreserveKeepsIndentAndColumns(t *testing.T) {
	const limit = 80
	yaml := "metadata:\n" +
		"  annotations:\n" +
		"    deployment.kubernetes.io/revision: \"1\""
	table := "NAME            CPU(cores)   MEMORY(bytes)\n" +
		"pod-lvw77       1m           26Mi"

	for _, in := range []string{yaml, table} {
		got := wrapPreserve(in, limit)
		want := strings.Split(in, "\n")
		if len(got) != len(want) {
			t.Fatalf("contagem de linhas mudou: got %d want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("linha %d alterada:\n got:  %q\n want: %q", i, got[i], want[i])
			}
		}
	}
}

// TestWrapPreserveStillWrapsOverlongLine confirma que linhas que estouram o
// limite continuam sendo quebradas (o box não pode esticar), preservando a
// indentação inicial nas continuações.
func TestWrapPreserveStillWrapsOverlongLine(t *testing.T) {
	const limit = 40
	const indent = "      "
	in := indent + strings.Repeat("x", 300)

	got := wrapPreserve(in, limit)
	if len(got) < 2 {
		t.Fatalf("esperava quebra em múltiplas linhas, got %d", len(got))
	}
	for i, ln := range got {
		if w := VisibleLen(ln); w > limit {
			t.Errorf("linha %d excedeu limit %d (got %d)", i, limit, w)
		}
		if !strings.HasPrefix(ln, indent) {
			t.Errorf("linha %d perdeu a indentação: %q", i, ln)
		}
	}
}
