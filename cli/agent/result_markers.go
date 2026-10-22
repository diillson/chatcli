/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * result_markers.go — the framing the model sees around each tool result
 * of a batch. One place defines it so the loop, the one-shot bridge, the
 * park resume and the history trimmer agree; the trimmer also keeps
 * recognizing the former Portuguese header in sessions saved before
 * the change.
 */
package agent

import "fmt"

// ActionResultPrefix opens the header of one tool result in a batch.
const ActionResultPrefix = "--- Action Result"

// LegacyActionResultPrefix is the header sessions saved before the
// English framing carry; readers accept both.
const LegacyActionResultPrefix = "--- Resultado da Ação"

// ActionErrorLabel opens the error line of a failed tool result.
const ActionErrorLabel = "ERROR: "

// ActionPartialOutputLabel opens the partial output of a failed result.
const ActionPartialOutputLabel = "Partial output: "

// ActionResultHeader frames result i (1-based) of toolName.
func ActionResultHeader(i int, toolName string) string {
	return fmt.Sprintf("%s %d (%s) ---\n", ActionResultPrefix, i, toolName)
}

// ActionFailure frames a failed result: the error and whatever output
// the tool produced before failing.
func ActionFailure(err string, partial string) string {
	return ActionErrorLabel + err + "\n" + ActionPartialOutputLabel + partial + "\n\n"
}
