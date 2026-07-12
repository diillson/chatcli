/*
 * ChatCLI - UI kit: lists
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import (
	"fmt"
	"strings"
)

// List renders items as a bulleted list on the grid:
//
//	· item
func List(items []string) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  ")
		b.WriteString(Sym(GlyphBullet))
		b.WriteString(" ")
		b.WriteString(it)
	}
	return b.String()
}

// Numbered renders items as a numbered list with right-aligned numbers so
// double-digit lists keep their text column straight:
//
//  9. item
//  10. item
func Numbered(items []string) string {
	numW := len(fmt.Sprint(len(items)))
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "  %s. %s", Dim(fmt.Sprintf("%*d", numW, i+1)), it)
	}
	return b.String()
}
