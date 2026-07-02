package memory

import (
	"testing"

	"go.uber.org/zap"
)

// ChatCLI's primary audience writes memory in Portuguese. Tokenization that
// only accepts ASCII a-z0-9 shreds accented words ("configuração" →
// "configura" + debris), which silently degrades the Jaccard similarity that
// drives dedupe/supersede and the subject gate — precisely for pt-BR facts.

func TestSigTokensPreservesAccentedWords(t *testing.T) {
	toks := sigTokens("Configuração do serviço de importação usa criptografia homomórfica")
	want := map[string]bool{
		"configuração": false, "serviço": false, "importação": false,
		"criptografia": false, "homomórfica": false,
	}
	for _, tok := range toks {
		if _, ok := want[tok]; ok {
			want[tok] = true
		}
	}
	for w, seen := range want {
		if !seen {
			t.Errorf("sigTokens mangled accented word %q (got %v)", w, toks)
		}
	}
}

// TestReconcileReinforcesAccentedRephrasing pins the behavioral consequence:
// an exact-meaning pt-BR rephrasing (same significant tokens, one word
// reordered) must REINFORCE the stored fact, not pile up a duplicate.
func TestReconcileReinforcesAccentedRephrasing(t *testing.T) {
	fi := NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())

	if !fi.AddFact("configuração da importação usa criptografia homomórfica no serviço", "pattern", nil) {
		t.Fatal("first fact not added")
	}
	// Rephrasing with identical significant-token set.
	added := fi.AddFact("no serviço, a configuração da importação usa criptografia homomórfica", "pattern", nil)
	if added {
		t.Fatalf("accented rephrasing stored as a duplicate: %d facts", fi.Count())
	}
	if fi.Count() != 1 {
		t.Fatalf("Count = %d, want 1 (reinforced)", fi.Count())
	}
}
