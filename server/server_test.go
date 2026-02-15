package server

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReflectionConfig_DisabledByDefault(t *testing.T) {
	cfg := Config{}
	assert.False(t, cfg.EnableReflection, "gRPC reflection should be disabled by default")
}

func TestReflectionConfig_EnabledViaField(t *testing.T) {
	cfg := Config{EnableReflection: true}
	assert.True(t, cfg.EnableReflection)
}

func TestReflectionConfig_EnvVarParsing(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string
		expected bool
	}{
		{"true lowercase", "true", true},
		{"TRUE uppercase", "TRUE", true},
		{"True mixed", "True", true},
		{"false", "false", false},
		{"empty", "", false},
		{"random", "yes", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CHATCLI_GRPC_REFLECTION", tc.envVal)
			result := strings.EqualFold(os.Getenv("CHATCLI_GRPC_REFLECTION"), "true")
			assert.Equal(t, tc.expected, result)
		})
	}
}
