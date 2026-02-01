package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenCounter(t *testing.T) {
	tc := NewTokenCounter("openai", "gpt-3.5-turbo", 4096)

	t.Run("Initial", func(t *testing.T) {
		assert.Equal(t, 0, tc.GetTotalTokens())
	})

	t.Run("AddTurn", func(t *testing.T) {
		tc.AddTurn("hello", "world")
		assert.Greater(t, tc.GetTotalTokens(), 0)
		prompt, completion := tc.GetLastTurnTokens()
		assert.Greater(t, prompt, 0)
		assert.Greater(t, completion, 0)
	})

	t.Run("Reset", func(t *testing.T) {
		tc.Reset()
		assert.Equal(t, 0, tc.GetTotalTokens())
	})
}
