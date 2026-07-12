/*
 * ChatCLI - UI kit: horizontal rules and headers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import (
	"strings"

	"github.com/diillson/chatcli/ui/theme"
)

// Rule renders a dim full-content-width horizontal rule — the single
// replacement for the fixed 39/50/60/70/80-column separators.
func Rule() string {
	return Dim(strings.Repeat("─", ContentWidth()))
}

// RuleTitled renders a rule with an inline title:
//
//	── Title ────────────────
//
// The title is bold in the header role; the dashes are dim. The line always
// spans ContentWidth.
func RuleTitled(title string) string {
	const lead = 2 // dashes before the title
	w := ContentWidth()
	tail := w - lead - VisibleLen(title) - 2 // spaces around the title
	if tail < 1 {
		tail = 1
	}
	return Dim(strings.Repeat("─", lead)) + " " + Bold(title, theme.RoleHeader) + " " +
		Dim(strings.Repeat("─", tail))
}

// Header renders a section title in the title weight of the hierarchy.
func Header(title string) string {
	return Bold(title, theme.RoleHeader)
}

// Subheader renders a secondary title in the metadata weight.
func Subheader(title string) string {
	return Dim(title)
}
