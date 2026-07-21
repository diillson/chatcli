package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// O loop do agent/coder ignorava o override de sessão do /max-tokens
// (currentMaxTokens fixo em 0 = provider default). adoptSessionMaxTokens é a
// reconciliação por turno: adota o override sempre que o usuário o altera,
// sem clobberar a escalation de truncação quando o override não mudou.
func TestAdoptSessionMaxTokens(t *testing.T) {
	cases := []struct {
		name        string
		current     int
		lastAdopted int
		override    int
		wantCurrent int
		wantAdopted int
	}{
		{
			name:        "no override keeps provider default",
			current:     0,
			lastAdopted: 0,
			override:    0,
			wantCurrent: 0,
			wantAdopted: 0,
		},
		{
			name:        "first override is adopted",
			current:     0,
			lastAdopted: 0,
			override:    32000,
			wantCurrent: 32000,
			wantAdopted: 32000,
		},
		{
			name:        "raised override is adopted",
			current:     32000,
			lastAdopted: 32000,
			override:    64000,
			wantCurrent: 64000,
			wantAdopted: 64000,
		},
		{
			name:        "lowered override is adopted",
			current:     64000,
			lastAdopted: 64000,
			override:    16000,
			wantCurrent: 16000,
			wantAdopted: 16000,
		},
		{
			name:        "unchanged override preserves truncation escalation",
			current:     96000, // escalation raised beyond the 64000 override
			lastAdopted: 64000,
			override:    64000,
			wantCurrent: 96000,
			wantAdopted: 64000,
		},
		{
			name:        "override change wins over prior escalation",
			current:     96000,
			lastAdopted: 64000,
			override:    128000,
			wantCurrent: 128000,
			wantAdopted: 128000,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCurrent, gotAdopted := adoptSessionMaxTokens(tc.current, tc.lastAdopted, tc.override)
			assert.Equal(t, tc.wantCurrent, gotCurrent, "current")
			assert.Equal(t, tc.wantAdopted, gotAdopted, "lastAdopted")
		})
	}
}
