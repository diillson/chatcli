/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"math"
	"strconv"
)

func formatInt(v int64) string { return strconv.FormatInt(v, 10) }

// formatFloatBits renders a zap Float64 field (stored as IEEE bits).
func formatFloatBits(bits int64) string {
	return strconv.FormatFloat(math.Float64frombits(uint64(bits)), 'g', -1, 64) // #nosec G115 -- zap stores float64 fields as their raw IEEE-754 bit pattern in Integer; this is the documented inverse
}
