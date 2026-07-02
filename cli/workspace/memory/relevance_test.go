package memory

import (
	"testing"

	"go.uber.org/zap"
)

// TestSearchDoesNotMatchInsideWords pins lexical precision: raw substring
// matching made short keywords fire inside unrelated words ("go" inside
// "django", "art" inside "artifact"), polluting retrieval with noise facts
// that then get access-boosted and entrench themselves.
func TestSearchDoesNotMatchInsideWords(t *testing.T) {
	fi := NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
	fi.AddFact("the api service is built on the django framework", "architecture", nil)

	if got := fi.Search([]string{"go"}); len(got) != 0 {
		t.Fatalf("keyword \"go\" matched inside \"django\": %d facts returned", len(got))
	}
}

// TestSearchPrefixMatchKeepsMorphology pins the counterweight: longer
// keywords still match their morphological extensions ("compress" →
// "compression"), so word-boundary precision does not cost recall on
// inflected terms.
func TestSearchPrefixMatchKeepsMorphology(t *testing.T) {
	fi := NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
	fi.AddFact("compression uses reversible ccr markers for tool output", "architecture", nil)

	if got := fi.Search([]string{"compress"}); len(got) != 1 {
		t.Fatalf("keyword \"compress\" no longer matches \"compression\": %d facts", len(got))
	}
}

// TestSearchMatchesAccentedTokens: the tokenizer behind relevance must be the
// same Unicode-aware one used everywhere else.
func TestSearchMatchesAccentedTokens(t *testing.T) {
	fi := NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
	fi.AddFact("a configuração de produção fica no vault", "project", nil)

	if got := fi.Search([]string{"configuração"}); len(got) != 1 {
		t.Fatalf("accented keyword did not match accented token: %d facts", len(got))
	}
}
