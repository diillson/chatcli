/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package version

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Corpo no formato real do release-please: título com link de compare,
// seções e bullets com links de PR e de commit.
const samplePleaseBody = "## [1.162.0](https://github.com/diillson/chatcli/compare/v1.161.0...v1.162.0) (2026-07-25)\n" +
	"\n" +
	"### Features\n" +
	"\n" +
	"* **update:** auto-update por canal de instalação ([#1242](https://github.com/diillson/chatcli/pull/1242)) ([ab12cd3](https://github.com/diillson/chatcli/commit/ab12cd3))\n" +
	"* **cli:** novo painel de custo ([9f8e7d6](https://github.com/diillson/chatcli/commit/9f8e7d6))\n" +
	"\n" +
	"### Bug Fixes\n" +
	"\n" +
	"* corrige quebra no Windows ([1a2b3c4](https://github.com/diillson/chatcli/commit/1a2b3c4))\n"

func TestReleaseHighlights_ParsesReleasePleaseBody(t *testing.T) {
	notes, more := ReleaseHighlights(samplePleaseBody, 10)

	assert.Zero(t, more)
	assert.Len(t, notes, 3)

	assert.Equal(t, "Features", notes[0].Section)
	assert.Equal(t, "update: auto-update por canal de instalação (#1242)", notes[0].Text)
	assert.Equal(t, "Features", notes[1].Section)
	assert.Equal(t, "cli: novo painel de custo", notes[1].Text, "ref de commit solta é ruído")
	assert.Equal(t, "Bug Fixes", notes[2].Section)
	assert.Equal(t, "corrige quebra no Windows", notes[2].Text)
}

func TestReleaseHighlights_TruncatesAndCounts(t *testing.T) {
	notes, more := ReleaseHighlights(samplePleaseBody, 2)
	assert.Len(t, notes, 2)
	assert.Equal(t, 1, more)
}

func TestReleaseHighlights_EmptyAndPlainBodies(t *testing.T) {
	notes, more := ReleaseHighlights("", 5)
	assert.Empty(t, notes)
	assert.Zero(t, more)

	// Corpo sem bullets (texto corrido) não produz destaques.
	notes, more = ReleaseHighlights("Apenas uma descrição em prosa.\nSem listas.", 5)
	assert.Empty(t, notes)
	assert.Zero(t, more)

	// max não-positivo é defensivo: nada a exibir.
	notes, more = ReleaseHighlights(samplePleaseBody, 0)
	assert.Empty(t, notes)
	assert.Zero(t, more)
}

func TestReleaseHighlights_BulletsWithoutSection(t *testing.T) {
	notes, _ := ReleaseHighlights("- primeiro item\n- **segundo** item", 5)
	assert.Len(t, notes, 2)
	assert.Empty(t, notes[0].Section)
	assert.Equal(t, "primeiro item", notes[0].Text)
	assert.Equal(t, "segundo item", notes[1].Text)
}
