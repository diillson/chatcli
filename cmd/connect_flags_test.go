/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// RunConnect defines its flag set before anything dials; an unknown flag
// returns the parse error so the flag definitions (and their help text)
// are exercised without a server.
func TestRunConnect_RejectsUnknownFlagBeforeDialing(t *testing.T) {
	err := RunConnect(context.Background(), []string{"--definitely-not-a-flag"}, nil, zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "definitely-not-a-flag")
}

func TestRunConnect_PositionalAddressDoesNotSwallowFlags(t *testing.T) {
	err := RunConnect(context.Background(), []string{"host:50051", "--nope"}, nil, zap.NewNop())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}
