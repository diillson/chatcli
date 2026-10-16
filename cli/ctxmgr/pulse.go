/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package ctxmgr

import "strconv"

// Live-dashboard nodes of context retrieval. Each is a hub aggregated by
// name, so the names are fixed: which base or query ran never goes in them.
const (
	// pulseKnowledgeSearchNode is an on-demand passage search over the
	// attached knowledge bases (the @knowledge tool's search).
	pulseKnowledgeSearchNode = "knowledge:search"

	// pulseContextRetrieveNode is the per-turn retrieval of the passages
	// relevant to the turn from knowledge and --rag attachments.
	pulseContextRetrieveNode = "context:retrieve"
)

// btoa renders a boolean span attribute.
func btoa(b bool) string { return strconv.FormatBool(b) }
