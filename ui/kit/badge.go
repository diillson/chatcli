/*
 * ChatCLI - UI kit: badges
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package kit

import "github.com/diillson/chatcli/ui/theme"

// Badge renders a small bracketed chip — "[text]" — in the given role.
// Used for value qualifiers like source tags and "(default)" hints where a
// full component would be noise.
func Badge(text string, r theme.Role) string {
	return Colorize("["+text+"]", r)
}
