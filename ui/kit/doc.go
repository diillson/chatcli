/*
 * ChatCLI - UI kit: shared presentation components
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Package kit is the single presentation vocabulary for everything chatcli
 * prints: one width helper, one style API over ui/theme roles, one status
 * glyph set, and small pure components (Notice, Rule, Header, Badge, KVTable,
 * List). It exists because the CLI accreted five terminal-width helpers,
 * three box idioms and three success glyphs — every surface reinvented
 * presentation, which is exactly what read as unpolished.
 *
 * Contracts:
 *   - LEAF package: may import ui/theme, lipgloss, runewidth, x/term and the
 *     stdlib — never i18n or anything under cli/. Components receive
 *     already-translated strings and only decorate them, so the package can
 *     be shared by cli and cli/agent and stays out of the i18n gates.
 *   - Components are pure functions returning strings; printing (and the
 *     single blank line that separates top-level blocks) belongs to the call
 *     site.
 *   - Color always flows through a theme.Role. On colorless profiles every
 *     escape vanishes (theme.ANSI/Reset return "") so piped output is plain.
 *   - Spacing grid: 2-space indent, right margin of RightMargin columns,
 *     components never emit leading/trailing blank lines.
 */
package kit
