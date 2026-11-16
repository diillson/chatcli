/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/version"
	"github.com/stretchr/testify/assert"
)

func TestReleaseCheckFailure(t *testing.T) {
	plain := &version.ReleaseStatusError{StatusCode: 403}
	assert.Equal(t, i18n.T("update.err.check", plain.Error()), releaseCheckFailure(plain), "a plain 403 keeps the message it had")
	network := errors.New("connection reset by peer")
	assert.Equal(t, i18n.T("update.err.check", network.Error()), releaseCheckFailure(network))

	reset := time.Now().Add(15 * time.Minute)
	anon := releaseCheckFailure(fmt.Errorf("check: %w", &version.ReleaseStatusError{StatusCode: 403, RateLimited: true, ResetAt: reset}))
	assert.Contains(t, anon, i18n.T("update.rate_limit.hit", 403))
	assert.Contains(t, anon, i18n.T("update.rate_limit.reset", reset.Local().Format("15:04")))
	assert.Contains(t, anon, i18n.T("update.rate_limit.anonymous"))

	account := releaseCheckFailure(&version.ReleaseStatusError{StatusCode: 429, RateLimited: true, Authenticated: true})
	assert.Contains(t, account, i18n.T("update.rate_limit.account"))
	assert.NotContains(t, account, i18n.T("update.rate_limit.anonymous"), "a token is already in use")
	assert.NotContains(t, account, i18n.T("update.rate_limit.reset", ""), "no reset time, no reset sentence")
}
