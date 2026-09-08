/*
 * ChatCLI - the Devin workspace-trust knob is reachable from every surface
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * A provider env var that only the provider package knows about is invisible:
 * it never shows up in /config, never survives /reload, and never reports its
 * default. This locks the three surfaces the project requires for any new
 * knob, using the workspace-trust waiver as the case in point — the setting
 * that decides whether the DEVIN provider answers at all on a current CLI.
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const devinTrustEnv = "DEVIN_CLI_RESPECT_WORKSPACE_TRUST"

func TestDevinWorkspaceTrust_IsReachableFromEverySurface(t *testing.T) {
	i18n.Init()

	t.Run("survives a config reload", func(t *testing.T) {
		assert.Contains(t, reloadableEnvVars, devinTrustEnv,
			"a value cleared by /reload but never restored silently reverts the waiver mid-session")
	})

	t.Run("reports its default", func(t *testing.T) {
		def, ok := envDefaults[devinTrustEnv]
		require.True(t, ok, "/config truth must be able to explain where the value comes from")
		assert.Equal(t, "false", def.Value)
		assert.True(t, def.IsBool)
		assert.NotEmpty(t, def.Source)
	})

	t.Run("shows up under /config providers", func(t *testing.T) {
		out := captureStdout(t, (&ChatCLI{}).showConfigProviders)
		assert.Contains(t, out, devinTrustEnv)
	})
}

// TestDevinWorkspaceTrust_DefaultWaivesTheCheck pins the value itself. It is
// deliberately false: every turn runs in a throwaway temp directory the CLI
// has never seen, and --print cannot raise the trust prompt there, so
// honoring the check would fail every turn instead of protecting anything.
func TestDevinWorkspaceTrust_DefaultWaivesTheCheck(t *testing.T) {
	assert.False(t, config.DevinCLIDefaultRespectWorkspaceTrust)
	assert.Equal(t, "false", strings.ToLower(envDefaults[devinTrustEnv].Value),
		"the documented default and the constant must not drift")
}
