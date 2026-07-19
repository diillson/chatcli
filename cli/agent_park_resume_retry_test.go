/*
 * ChatCLI - Park resume retry-path tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func TestRunResumeForToken_MissingSnapshotReturnsFalse(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}

	ok := cli.runResumeForToken(context.Background(), "zz-token-that-does-not-exist", "manual", "")

	// A failed resume must report failure so drainPendingResumes does NOT
	// stamp the recently-resumed guard — stamping it silently swallowed the
	// manual /resume retry for 30s.
	assert.False(t, ok)
}

func TestDrainPendingResumes_FailedResumeDoesNotStampGuard(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.pendingResumeQueue = []string{"zz-token-that-does-not-exist"}

	processed := cli.drainPendingResumes(context.Background())

	assert.True(t, processed)
	// The failed token must remain retryable: the 30s idempotency guard is
	// only for resumes that actually ran.
	assert.False(t, cli.wasRecentlyResumed("zz-token-that-does-not-exist"))
}
