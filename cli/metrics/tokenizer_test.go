package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCountTokens valida a contagem de tokens usando o tiktoken
func TestCountTokens(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		model    string
		expected int
		isApprox bool
	}{
		{name: "Very simple", text: "hello", model: "gpt-3.5-turbo", expected: 1, isApprox: false},
		{name: "Simple sentence", text: "hello world", model: "gpt-4", expected: 2, isApprox: false},
		{name: "Empty", text: "", model: "gpt-4", expected: 0, isApprox: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count := CountTokens(tt.text, tt.model)
			if tt.isApprox {
				// Aceita margem de erro para fallbacks
				assert.InDelta(t, tt.expected, count, 1.0)
			} else {
				// BPE evolui, mas para hello/hello world nao deve mudar
				assert.Equal(t, tt.expected, count, "Contagem de tokens incorreta para %s", tt.text)
			}
		})
	}
}
