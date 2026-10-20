/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "testing"

func TestStripInlineSVG_SurvivesCaseMappingLengthChanges(t *testing.T) {
	got, inSVG, keep := stripInlineSVG(`İİİ antes <SVG width="1"><path d="M0"/></svg> depois`, false)
	if got != "İİİ antes  depois" || inSVG || !keep {
		t.Errorf("got=%q inSVG=%v keep=%v", got, inSVG, keep)
	}
}
