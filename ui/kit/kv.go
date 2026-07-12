/*
 * ChatCLI - UI kit: key/value tables
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import (
	"strings"

	"github.com/diillson/chatcli/ui/theme"
)

// KVRow is one row of a KVTable. Key and Value arrive already translated.
// ValueRole tints the value; the zero role renders it as plain text (a
// border-colored value is never what a table wants, so the zero value is
// repurposed as "unstyled"). Note is an optional dim qualifier appended
// after the value — e.g. a "(default)" hint.
type KVRow struct {
	Key       string
	Value     string
	ValueRole theme.Role
	Note      string
}

// KVTable renders aligned key/value rows on the grid. The key column width
// is measured from the TRANSLATED keys at render time (VisibleLen), so
// alignment holds in every locale — unlike the fixed "%-32s" and the
// English-tuned literal spaces it replaces. Keys render dim; values in
// their role (or plain).
func KVTable(rows []KVRow) string {
	keyW := 0
	for _, r := range rows {
		if w := VisibleLen(r.Key); w > keyW {
			keyW = w
		}
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		pad := strings.Repeat(" ", keyW-VisibleLen(r.Key))
		b.WriteString("  ")
		b.WriteString(Dim(r.Key))
		b.WriteString(pad)
		b.WriteString("  ")
		if r.ValueRole == theme.RoleBorder {
			b.WriteString(r.Value)
		} else {
			b.WriteString(Colorize(r.Value, r.ValueRole))
		}
		if r.Note != "" {
			b.WriteString(" ")
			b.WriteString(Dim(r.Note))
		}
	}
	return b.String()
}
