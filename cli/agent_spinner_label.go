/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import "fmt"

// defaultSpinnerLabel produces the legacy spinner label shape
// "EXECUTANDO: <toolName> <subcmd>" used before Item 7. The agent
// loop falls back to it when the plugin doesn't ship DescriberWithInput
// (legacy/external plugins) or when DescribeCall returns an empty string
// for some unusual args shape.
//
// Kept as a standalone helper so the spinner-label decision logic can
// be unit-tested without spinning up an AgentMode.
func defaultSpinnerLabel(toolName string, args []string) string {
	subCmd := "ação"
	if len(args) > 0 {
		subCmd = args[0]
	}
	return fmt.Sprintf("EXECUTANDO: %s %s", toolName, subCmd)
}
