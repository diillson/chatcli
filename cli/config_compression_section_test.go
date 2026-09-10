/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	"github.com/diillson/chatcli/cli/compress"
	"github.com/stretchr/testify/assert"
)

// The compression section must name the variable behind every value it
// shows, with and without a live layer, so a user reading /config knows
// what to set — and the defaults registry must be able to explain each.
func TestShowConfigCompression_NamesEveryKnob(t *testing.T) {
	knobs := []string{
		"CHATCLI_COMPRESSION", "CHATCLI_COMPRESSION_PROFILE", "CHATCLI_COMPRESSION_THRESHOLD",
		"CHATCLI_COMPRESSION_CCR_DIR", "CHATCLI_COMPRESSION_CCR_MAX_MB", "CHATCLI_COMPRESSION_CCR_TTL",
	}

	bare := &ChatCLI{}
	out := captureStdout(t, bare.showConfigCompression)
	for _, k := range knobs {
		assert.Contains(t, out, k, "without a layer")
	}
	assert.Contains(t, out, "off", "no layer renders as mode off")

	t.Setenv("CHATCLI_COMPRESSION", "lossless")
	t.Setenv("CHATCLI_COMPRESSION_PROFILE", "aggressive")
	t.Setenv("CHATCLI_COMPRESSION_THRESHOLD", "1234")
	live := &ChatCLI{compressionLayer: compress.NewLayerFromEnv(t.TempDir())}
	out = captureStdout(t, live.showConfigCompression)
	for _, k := range knobs {
		assert.Contains(t, out, k, "with a layer")
	}
	assert.Contains(t, out, "lossless")
	assert.Contains(t, out, "aggressive")
	assert.Contains(t, out, "1234")

	for _, k := range knobs {
		def, ok := envDefaults[k]
		assert.True(t, ok, "%s missing from the defaults registry", k)
		assert.NotEmpty(t, def.Source, "%s has no documented source", k)
	}
}
