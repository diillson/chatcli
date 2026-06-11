/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import "testing"

// TestPaletteTriggerGating validates the decision layer that guards the
// overlay, independently of the live completer: the palette must never open
// outside the interactive REPL, must yield to a suppressed bare prefill, must
// never hijack a mode-switch command, and must ignore lines that already carry
// arguments. None of these paths touch the completer, so a zero-value ChatCLI
// is enough.
func TestPaletteTriggerGating(t *testing.T) {
	c := &ChatCLI{}

	// Headless (replActive == false): never triggers, even for a root alias.
	if _, ok := c.paletteTrigger("/menu"); ok {
		t.Error("palette triggered while not in the interactive REPL")
	}

	c.replActive = true

	// Root aliases open the categorized root (target == "").
	for _, in := range []string{"/", "/menu", "/commands", "/palette"} {
		target, ok := c.paletteTrigger(in)
		if !ok || target != "" {
			t.Errorf("paletteTrigger(%q) = (%q, %v), want (\"\", true)", in, target, ok)
		}
	}

	// Mode-switch commands must keep entering their mode, never open a picker.
	for _, in := range []string{"/agent", "/run", "/coder", "/plan"} {
		if _, ok := c.paletteTrigger(in); ok {
			t.Errorf("mode-switch %q opened the palette", in)
		}
	}

	// A line that already carries an argument is not bare → run it as typed.
	if _, ok := c.paletteTrigger("/config ui"); ok {
		t.Error("paletteTrigger fired for a command that already has arguments")
	}

	// A bare command just prefilled by the palette must run its own action
	// once instead of reopening the overlay (prevents an infinite loop).
	c.suppressPaletteOnce = true
	if _, ok := c.paletteTrigger("/menu"); ok {
		t.Error("suppressPaletteOnce did not skip the trigger")
	}
	if c.suppressPaletteOnce {
		t.Error("suppressPaletteOnce was not cleared after use")
	}
}

// TestPaletteSuggest_ContextModeValuesIncludeKnowledge pins the palette
// surface for `/context … --mode`: the overlay renders whatever the live
// completer returns, so every processing mode — knowledge included — must
// come back through paletteSuggest with a human description attached.
func TestPaletteSuggest_ContextModeValuesIncludeKnowledge(t *testing.T) {
	c := newCompleterTestCLI(t)
	out := c.paletteSuggest("/context create myctx ./src --mode ")
	seen := map[string]string{}
	for _, s := range out {
		seen[s.Text] = s.Desc
	}
	for _, want := range []string{"full", "summary", "chunked", "smart", "knowledge"} {
		desc, ok := seen[want]
		if !ok {
			t.Errorf("palette is missing mode %q after --mode; got %+v", want, out)
			continue
		}
		if desc == "" {
			t.Errorf("palette mode %q has an empty description", want)
		}
	}
}
