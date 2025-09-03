package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNeedsUpdate(t *testing.T) {
	testCases := []struct {
		name     string
		current  string
		latest   string
		expected bool
	}{
		{"Major update needed", "1.0.0", "2.0.0", true},
		{"Minor update needed", "1.1.0", "1.2.0", true},
		{"Patch update needed", "1.1.1", "1.1.2", true},
		{"No update needed (same)", "1.2.3", "1.2.3", false},
		{"No update needed (older)", "2.0.0", "1.9.9", false},
		{"With 'v' prefix", "v1.2.0", "v1.3.0", true},
		{"Dev version", "dev", "1.0.0", false},
		{"Unknown version", "unknown", "1.0.0", false},
		{"Pseudo-version", "v0.0.0-20240101-abcdef", "1.0.0", false},
		// *** CORREÇÃO DA EXPECTATIVA E NOVOS CASOS ***
		{"Current is pre-release, needs update", "1.2.3-alpha", "1.2.3", true},
		{"Current is pre-release, latest is newer pre-release", "1.2.3-alpha", "1.2.3-beta", true},
		{"Current is stable, latest is pre-release (no update)", "1.2.3", "1.2.3-beta", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := needsUpdate(tc.current, tc.latest)
			assert.Equal(t, tc.expected, result)
		})
	}
}
