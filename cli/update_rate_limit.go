/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"errors"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/version"
)

// releaseCheckFailure words a failed release check for /update and
// /version. GitHub's rate limit gets its own text: what was hit, when it
// lifts and how to get the larger allowance, instead of a bare status that
// reads like a blocked network.
func releaseCheckFailure(err error) string {
	var status *version.ReleaseStatusError
	if !errors.As(err, &status) || !status.RateLimited {
		return i18n.T("update.err.check", err.Error())
	}
	parts := []string{i18n.T("update.rate_limit.hit", status.StatusCode)}
	if !status.ResetAt.IsZero() {
		parts = append(parts, i18n.T("update.rate_limit.reset", status.ResetAt.Local().Format("15:04")))
	}
	if status.Authenticated {
		parts = append(parts, i18n.T("update.rate_limit.account"))
	} else {
		parts = append(parts, i18n.T("update.rate_limit.anonymous"))
	}
	return strings.Join(parts, " ")
}
