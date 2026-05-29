/*
 * ChatCLI - test bootstrap for the agent renderer package
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The renderer's color tests assert the exact 16-color SGR codes the UI
 * emits on a real terminal (e.g. cyan tool labels, green success). In a test
 * process stdout is a pipe, so the auto-detected color profile is NoTTY and
 * the theme would (correctly, for production) strip all color. Pinning the
 * profile to ANSI for the package's tests reflects the on-a-terminal reality
 * the assertions describe; at ANSI the theme remap is a byte-for-byte no-op,
 * so legacy expectations hold. Tests needing a different profile set it
 * locally and restore ProfileANSI on cleanup.
 */
package agent

import (
	"os"
	"testing"

	"github.com/diillson/chatcli/ui/theme"
)

func TestMain(m *testing.M) {
	theme.SetProfile(theme.ProfileANSI)
	os.Exit(m.Run())
}
