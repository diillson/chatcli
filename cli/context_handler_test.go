package cli

import (
	"testing"
)

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		name   string
		tokens int64
		want   string
	}{
		{
			name:   "zero",
			tokens: 0,
			want:   "0",
		},
		{
			name:   "small number",
			tokens: 500,
			want:   "500",
		},
		{
			name:   "just below 1K",
			tokens: 999,
			want:   "999",
		},
		{
			name:   "exactly 1K",
			tokens: 1_000,
			want:   "1.0K",
		},
		{
			name:   "mid thousands",
			tokens: 25_500,
			want:   "25.5K",
		},
		{
			name:   "large thousands",
			tokens: 999_999,
			want:   "1000.0K",
		},
		{
			name:   "exactly 1M",
			tokens: 1_000_000,
			want:   "1.0M",
		},
		{
			name:   "multi million",
			tokens: 2_500_000,
			want:   "2.5M",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatTokenCount(tc.tokens)
			if got != tc.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tc.tokens, got, tc.want)
			}
		})
	}
}
