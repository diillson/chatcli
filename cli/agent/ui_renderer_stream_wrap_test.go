package agent

import (
	"strings"
	"testing"
)

// TestStreamOutputWrapsAndPrefixesEveryLine exercita o caminho de streaming
// ao vivo: toda linha emitida tem que carregar a borda lateral "│" e nenhuma
// pode exceder a largura do terminal (fallback 80 fora de tty), garantindo
// que o box não reflui com YAML largo.
func TestStreamOutputWrapsAndPrefixesEveryLine(t *testing.T) {
	r := NewUIRendererWithStyle(nil, UIStyleFull)

	out := captureStdout(t, func() {
		r.StreamOutput("kubectl get pods")                 // linha curta
		r.StreamOutput("    annotations: " + longRun(400)) // YAML largo, indentado
		r.StreamOutput("ERR: something failed " + longRun(300))
		r.StreamOutput("first\nsecond") // bloco com \n embutido
	})

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("StreamOutput não emitiu nada")
	}
	for i, ln := range lines {
		plain := stripANSI(ln)
		if !strings.HasPrefix(plain, "│") {
			t.Errorf("linha %d sem borda lateral: %q", i, plain)
		}
		if w := VisibleLen(plain); w > 80 {
			t.Errorf("linha %d excedeu 80 cols (got %d): %q", i, w, plain)
		}
	}
	// Garante que o bloco multi-linha gerou as duas linhas lógicas.
	joined := stripANSI(out)
	if !strings.Contains(joined, "first") || !strings.Contains(joined, "second") {
		t.Errorf("bloco com \\n não foi dividido: %q", joined)
	}
}

func longRun(n int) string { return strings.Repeat("x", n) }

// TestWrapStreamLineNeverExceedsWidth garante que nenhuma linha quebrada
// ultrapassa a largura útil do box — é exatamente isso que evita o reflow
// do terminal que rasgava a borda em outputs largos (ex.: kubectl -o yaml).
func TestWrapStreamLineNeverExceedsWidth(t *testing.T) {
	const width = 40

	cases := []string{
		strings.Repeat("a", 500),                       // palavra única gigante, sem espaços
		"    annotations: " + strings.Repeat("x", 300), // YAML indentado, valor enorme
		"short line",             // cabe sem quebrar
		"",                       // vazio
		strings.Repeat("日", 100), // wide runes (2 cols cada)
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
