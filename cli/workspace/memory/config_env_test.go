package memory

import (
	"testing"

	"github.com/diillson/chatcli/config"
)

func TestConfigFromEnv_AppliesExistingOverrides(t *testing.T) {
	t.Setenv(config.MemoryRetrievalEnv, "8000")
	t.Setenv(config.MemoryMaxFactsEnv, "1200")
	c := ConfigFromEnv()
	if c.RetrievalBudget != 8000 {
		t.Errorf("RetrievalBudget = %d, want 8000 (env override must apply)", c.RetrievalBudget)
	}
	if c.MaxFactsCount != 1200 {
		t.Errorf("MaxFactsCount = %d, want 1200", c.MaxFactsCount)
	}
	// Untouched knobs keep their defaults.
	if c.DecayHalfLifeDays != 30.0 {
		t.Errorf("DecayHalfLifeDays = %v, want default 30", c.DecayHalfLifeDays)
	}
}

func TestConfigFromEnv_MalformedOverrideIgnored(t *testing.T) {
	t.Setenv(config.MemoryRetrievalEnv, "not-a-number")
	if c := ConfigFromEnv(); c.RetrievalBudget != 4000 {
		t.Errorf("malformed override should fall back to default 4000, got %d", c.RetrievalBudget)
	}
	t.Setenv(config.MemoryRetrievalEnv, "-5")
	if c := ConfigFromEnv(); c.RetrievalBudget != 4000 {
		t.Errorf("non-positive override should fall back to default 4000, got %d", c.RetrievalBudget)
	}
}

func TestConfig_Sanitized_ClampsDegenerate(t *testing.T) {
	bad := Config{
		MinCosineScore:   2.0,           // out of [0,1)
		VectorTopK:       -3,            // non-positive
		BackfillBatchMax: 0,             // non-positive
		RankWeights:      RankWeights{}, // all-zero
	}
	got := bad.sanitized()
	if got.MinCosineScore != 0.25 {
		t.Errorf("MinCosineScore = %v, want clamped to 0.25", got.MinCosineScore)
	}
	if got.VectorTopK != 12 {
		t.Errorf("VectorTopK = %d, want default 12", got.VectorTopK)
	}
	if got.BackfillBatchMax != 500 {
		t.Errorf("BackfillBatchMax = %d, want default 500", got.BackfillBatchMax)
	}
	if got.RankWeights != DefaultRankWeights() {
		t.Errorf("zero RankWeights should normalize to default, got %+v", got.RankWeights)
	}
}

func TestDefaultConfig_IsSane(t *testing.T) {
	// DefaultConfig must already be a fixed point of sanitized().
	d := DefaultConfig()
	if d.sanitized() != d {
		t.Errorf("DefaultConfig is not sanitized-stable: %+v vs %+v", d, d.sanitized())
	}
}
