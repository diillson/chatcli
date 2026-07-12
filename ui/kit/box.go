/*
 * ChatCLI - UI kit: box border geometry
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The two border builders every card in the app uses, moved verbatim from
 * cli/agent. They return PLAIN (uncolored) lines: color belongs to the
 * caller — the agent wrappers keep their legacy ANSI-constant coloring
 * byte-identical, and kit-native components colorize through theme roles.
 * targetWidth is always the EXACT visible width to produce, measured with
 * VisibleLen (lipgloss.Width) on the matching body so every border row
 * agrees with every body row even under emoji-width drift.
 */
package kit

import "strings"

// TitledTopBorder produces a `╭── header ─────╮` line whose VISIBLE width
// equals targetWidth. The two padding rules cover the two ways the header
// can fall short of the card width:
//   - normal case: header fits, fill with dashes
//   - header longer than card: emit a minimal top without filling (the
//     card still closes at the right width because callers honor the
//     body measurement; 1-2 cols of overflow is accepted degradation)
func TitledTopBorder(header string, targetWidth int) string {
	// Visible cols reserved: `╭── ` (4) + header + ` ` (1) + dashes + `╮` (1)
	usedNoFill := 4 + VisibleLen(header) + 1 + 1
	fill := targetWidth - usedNoFill
	if fill < 0 {
		return "╭── " + header + " ╮"
	}
	return "╭── " + header + " " + strings.Repeat("─", fill) + "╮"
}

// BilateralBorder constructs a horizontal border with optional left and
// right labels embedded between the corner glyphs:
//
//	<lc>─ LeftLabel ──────── RightLabel ─<rc>
//
// Layout rules (in visible columns):
//   - <lc> + '─' + leftLabel  if leftLabel != ""   (else <lc> + '─')
//   - fill of '─' to absorb remaining width
//   - rightLabel + '─' + <rc> if rightLabel != ""  (else '─' + <rc>)
//
// A degenerate case where the labels alone exceed targetWidth falls back
// to a minimal border (the labels survive; the fill goes to zero).
func BilateralBorder(lc, rc rune, leftLabel, rightLabel string, targetWidth int) string {
	const cornerCols = 1    // lc / rc themselves
	const dashCornerPad = 1 // the "─" that hugs each corner

	leftBlock := string(lc) + "─"
	rightBlock := "─" + string(rc)
	reserved := cornerCols*2 + dashCornerPad*2 // 4 cols of "<lc>─...─<rc>"

	leftVis := VisibleLen(leftLabel)
	rightVis := VisibleLen(rightLabel)

	fill := targetWidth - reserved - leftVis - rightVis
	if fill < 0 {
		// Labels overflow the target — emit minimal border with the labels
		// intact so callers can still read them.
		return leftBlock + leftLabel + rightLabel + rightBlock
	}

	dashes := strings.Repeat("─", fill)

	var sb strings.Builder
	sb.WriteString(leftBlock)
	if leftVis > 0 {
		sb.WriteString(leftLabel)
	}
	sb.WriteString(dashes)
	if rightVis > 0 {
		sb.WriteString(rightLabel)
	}
	sb.WriteString(rightBlock)
	return sb.String()
}
