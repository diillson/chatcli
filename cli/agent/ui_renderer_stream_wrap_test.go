package agent

import (
	"strings"
	"testing"
)

// TestWrapStreamLineNeverExceedsWidth garante que nenhuma linha quebrada
// ultrapassa a largura útil do box — é exatamente isso que evita o reflow
// do terminal que rasgava a borda em outputs largos (ex.: kubectl -o yaml).
func TestWrapStreamLineNeverExceedsWidth(t *testing.T) {
	const width = 40

	cases := []string{
		strings.Repeat("a", 500),                       // palavra única gigante, sem espaços
		"    annotations: " + strings.Repeat("x", 300), // YAML indentado, valor enorme
		"short line",                                    // cabe sem quebrar
		"",                                              // vazio
		strings.Repeat("日", 100),                        // wide runes (2 cols cada)
	}

	for _, in := range cases {
		for _, line := range wrapStreamLine(in, width) {
			if w := VisibleLen(line); w > width {
				t.Errorf("linha excedeu largura %d (got %d): %q", width, w, line)
			}
		}
	}
}

// TestWrapStreamLinePreservesIndent confirma que a indentação inicial do
// YAML é repetida nas continuações, mantendo a estrutura legível.
func TestWrapStreamLinePreservesIndent(t *testing.T) {
	const indent = "      "
	in := indent + strings.Repeat("v", 200)

	got := wrapStreamLine(in, 40)
	if len(got) < 2 {
		t.Fatalf("esperava múltiplas linhas, got %d", len(got))
	}
	for i, line := range got {
		if !strings.HasPrefix(line, indent) {
			t.Errorf("linha %d perdeu a indentação: %q", i, line)
		}
	}
}

// TestWrapStreamLineShortLineUnchanged garante que linhas que cabem não são
// tocadas (sem alocação extra de continuação).
func TestWrapStreamLineShortLineUnchanged(t *testing.T) {
	in := "kubectl get pods"
	got := wrapStreamLine(in, 80)
	if len(got) != 1 || got[0] != in {
		t.Errorf("linha curta foi alterada: %#v", got)
	}
}
