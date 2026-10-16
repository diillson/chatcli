/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * The /moa panel view: each member's position is printed the moment its
 * answer lands, so the user sees where the models agree and where they
 * split before the synthesis arrives. CHATCLI_MOA_PANEL picks how much:
 *
 *   lead  each member's opening statement, one line (default)
 *   full  each member's whole answer
 *   off   only which members answered
 */
package cli

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/mattn/go-runewidth"
)

const (
	moaPanelLead = "lead"
	moaPanelFull = "full"
	moaPanelOff  = "off"
)

// moaPanelMode reads CHATCLI_MOA_PANEL; anything unrecognized is "lead".
func moaPanelMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_MOA_PANEL"))) {
	case moaPanelFull:
		return moaPanelFull
	case moaPanelOff:
		return moaPanelOff
	default:
		return moaPanelLead
	}
}

// moaPanelPrinter returns the observer that prints each member as it
// finishes, timed from start.
func (cli *ChatCLI) moaPanelPrinter(mode string, start time.Time) moa.Observer {
	return func(r moa.RefResult) {
		elapsed := fmt.Sprintf("%.1fs", time.Since(start).Seconds())
		if !r.OK() {
			reason := i18n.T("moa.panel.empty")
			if r.Err != nil {
				reason = moaOneLine(r.Err.Error())
			}
			fmt.Printf("    %s %s  %s\n      %s\n", colorize("✗", ColorRed), colorize(r.Ref.String(), ColorGray),
				colorize(elapsed, ColorGray), colorize(moaClip(reason, kit.TermWidth()-8), ColorGray))
			return
		}
		fmt.Printf("    %s %s  %s\n", colorize("✓", ColorGreen), r.Ref.String(), colorize(elapsed, ColorGray))
		switch mode {
		case moaPanelFull:
			fmt.Println(cli.renderMarkdown(r.Output))
			fmt.Println()
		case moaPanelLead:
			if lead := moaLead(r.Output); lead != "" {
				fmt.Printf("      %s\n", colorize(moaClip(lead, kit.TermWidth()-8), ColorGray))
			}
		}
	}
}

// renderMoAEnvelope draws the synthesis in the same response envelope as
// a chat reply — word-wrapped to the terminal — labeled with the
// aggregator, how many members answered and the round's wall time.
func (cli *ChatCLI) renderMoAEnvelope(final string, aggregator moa.Ref, results []moa.RefResult, elapsed time.Duration) {
	answered := 0
	for _, r := range results {
		if r.OK() {
			answered++
		}
	}
	agg := moa.Ref{Provider: strings.ToLower(aggregator.Provider), Model: aggregator.Model}.String()
	renderer := agent.NewUIRendererWithStyle(cli.logger, agent.UIStyleFull)
	renderer.RenderResponseEnvelope(agent.ResponseEnvelopeOptions{
		HeaderLeft:  colorize(" MoA · "+agg+" ", ColorPurple+ColorBold),
		HeaderRight: colorize(" "+i18n.T("moa.panel.envelope", answered, len(results), formatLatency(elapsed))+" ", ColorGray),
		Body:        ensureANSIReset(cli.renderMarkdown(final)),
		Color:       agent.ColorGray,
		Typewriter:  true,
	})
	fmt.Println()
}

// moaLead is the opening statement of an answer: the first line of prose
// (headings, rules, fences and table rows skipped), stripped of markdown
// markers and cut at the end of its first sentence. Answers are expected
// to open with their bottom line, which is what makes the panel's
// positions comparable at a glance.
func moaLead(answer string) string {
	inFence := false
	for _, raw := range strings.Split(answer, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|") ||
			strings.Trim(line, "-*_= ") == "" {
			continue
		}
		line = strings.TrimLeft(line, ">-*+ ")
		for _, marker := range []string{"**", "__", "`"} {
			line = strings.ReplaceAll(line, marker, "")
		}
		line = strings.TrimSpace(line)
		if !strings.ContainsFunc(line, unicode.IsLetter) {
			continue
		}
		return moaFirstSentence(line)
	}
	return ""
}

// moaFirstSentence cuts s after the first sentence-ending punctuation that is
// followed by a space, so "Sim. Mas..." keeps "Sim." and "v1.2 is" stays
// whole.
func moaFirstSentence(s string) string {
	runes := []rune(s)
	for i := 0; i+1 < len(runes); i++ {
		if strings.ContainsRune(".!?", runes[i]) && runes[i+1] == ' ' && i >= 12 {
			return string(runes[:i+1])
		}
	}
	return s
}

// moaClip fits s into width display columns, ending with "…" when cut.
func moaClip(s string, width int) string {
	if width < 20 {
		width = 20
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	return runewidth.Truncate(s, width, "…")
}

// moaOneLine folds s onto one line: provider errors often carry a
// pretty-printed JSON body whose first line alone says nothing.
func moaOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
