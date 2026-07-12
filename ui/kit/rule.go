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

// RuleHeader renders a full-width bilateral rule with pre-formatted labels
// embedded — the borderless reply header of the "sóbrio" treatment:
//
//	── {left} ─────────────── {right} ──
//
// Labels arrive pre-colored (with any breathing spaces the caller wants);
// the dashes are dim. width <= 0 resolves to ContentWidth; a positive width
// pins the line for tests. Degenerate widths fall back to a minimal rule
// with the labels intact.
func RuleHeader(left, right string, width int) string {
	if width <= 0 {
		width = ContentWidth()
	}
	const edge = 2 // "──" on each end
	fill := width - edge*2 - VisibleLen(left) - VisibleLen(right)
	if fill < 0 {
		fill = 0
	}
	var b strings.Builder
	b.WriteString(Dim(strings.Repeat("─", edge)))
	b.WriteString(left)
	b.WriteString(Dim(strings.Repeat("─", fill)))
	b.WriteString(right)
	b.WriteString(Dim(strings.Repeat("─", edge)))
	return b.String()
}

// Header renders a section title in the title weight of the hierarchy.
func Header(title string) string {
	return Bold(title, theme.RoleHeader)
}

// Subheader renders a secondary title in the metadata weight.
func Subheader(title string) string {
	return Dim(title)
}
