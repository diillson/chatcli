/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import "github.com/diillson/chatcli/llm/pricing"

// devinStaticPricing returns the static Cognition rate for a Devin model
// id, family slug or variant uid. The table and its matching rules live in
// llm/pricing (DevinStaticRate) so the gRPC server and the operator price a
// Devin turn the same way the CLI does; this wrapper keeps the tracker's
// historical name for its callers and tests.
func devinStaticPricing(model string) (inputCost, outputCost float64, ok bool) {
	return pricing.DevinStaticRate(model)
}
