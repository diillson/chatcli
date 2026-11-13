/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package theme

// UseForTest activates a theme that is not in the registry and returns the
// call that restores the previous one. The registry only holds the built-in
// themes, so this is how a test reaches a combination of switches no
// built-in theme has.
func UseForTest(t Theme) (restore func()) {
	prev := activeTheme.Load()
	activeTheme.Store(&t)
	return func() { activeTheme.Store(prev) }
}
