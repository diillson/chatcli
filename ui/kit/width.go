/*
 * ChatCLI - UI kit: terminal width
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import (
	"os"

	"golang.org/x/term"
)

const (
	// RightMargin is the number of columns every full-width component leaves
	// free on the right so borders never touch the terminal edge (or a
	// native scrollbar). Previously scattered as ad-hoc "-2" and "-1".
	RightMargin = 2

	// MinContentWidth is the floor below which components stop shrinking and
	// accept horizontal overflow — matching the response envelope's historic
	// minimum so extremely narrow terminals degrade the same way everywhere.
	MinContentWidth = 40

	// fallbackWidth is used when stdout is not a terminal (pipe, CI) or the
	// size cannot be determined. 100 is the codebase's most recent deliberate
	// choice (response envelope); the kit makes it the only one.
	fallbackWidth = 100
)

// TermWidth returns the live terminal width in columns, or fallbackWidth
// when stdout is not a terminal. Queried per call so live resizes are
// honored by the next render.
func TermWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return fallbackWidth
}

// ContentWidth is the width components should actually occupy: the terminal
// width minus the right margin, clamped to MinContentWidth.
func ContentWidth() int {
	w := TermWidth() - RightMargin
	if w < MinContentWidth {
		return MinContentWidth
	}
	return w
}
