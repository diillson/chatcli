/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * The @moa tool's view of the panel — the same information the /moa
 * command shows the user. While the run is going, each round and each
 * member's position stream as progress lines; the result then carries
 * every member's position per round ahead of the synthesis, so the model
 * that called the tool can see where the panel agreed, split, and who
 * revised its answer after the cross-review.
 */
package cli

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/agent/moa"
	"github.com/diillson/chatcli/i18n"
)

// Model-facing layout of the positions report.
const (
	moaPositionsHeading   = "## Panel positions\n"
	moaRoundHeadingFmt    = "\n### Round %d\n"
	moaReviewHeadingFmt   = "\n### Round %d (cross-review: each model read the others' answers)\n"
	moaLeadLineFmt        = "- %s%s: %s\n"
	moaFullAnswerFmt      = "\n#### %s%s\n%s\n"
	moaFailedLineFmt      = "- %s: no answer (%s)\n"
	moaStanceTagFmt       = " [%s]"
	moaSynthesisHeading   = "\n## Synthesis\n"
	moaFallbackHeading    = "\n## Longest answer (the synthesis failed)\n"
	moaProgressMemberFmt  = "  ✓ %s  %s%s"
	moaProgressFailedFmt  = "  ✗ %s  %s  %s"
	moaProgressLeadFmt    = "      %s"
	moaProgressLeadMaxCol = 110
)

// moaPositionsMode resolves the positions option: an explicit value wins,
// otherwise CHATCLI_MOA_PANEL — the same setting as the /moa panel.
func moaPositionsMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case moaPanelLead:
		return moaPanelLead
	case moaPanelFull:
		return moaPanelFull
	case moaPanelOff:
		return moaPanelOff
	default:
		return moaPanelMode()
	}
}

// moaToolPanel streams a run's progress to the tool's output callback.
type moaToolPanel struct {
	emit  func(string)
	start time.Time
	mu    sync.Mutex
}

func newMoAToolPanel(emit func(string), start time.Time) *moaToolPanel {
	return &moaToolPanel{emit: emit, start: start}
}

func (p *moaToolPanel) line(s string) {
	if p.emit == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.emit(s)
}

// hooks reports each round (when there is more than one) and each member
// as it answers.
func (p *moaToolPanel) hooks() moa.Hooks {
	return moa.Hooks{
		OnLayer: func(layer, total, members int) {
			if total < 2 {
				return
			}
			key := "moa.layers.review"
			if layer == 1 {
				key = "moa.layers.first"
			}
			p.line(i18n.T(key, layer, total, members))
		},
		OnDone: func(_ int, r moa.RefResult, stance moa.Stance) {
			elapsed := fmt.Sprintf("%.1fs", time.Since(p.start).Seconds())
			if !r.OK() {
				reason := i18n.T("moa.panel.empty")
				if r.Err != nil {
					reason = moaOneLine(r.Err.Error())
				}
				p.line(fmt.Sprintf(moaProgressFailedFmt, r.Ref.String(), elapsed, moaClip(reason, moaProgressLeadMaxCol)))
				return
			}
			badge := ""
			switch stance {
			case moa.StanceRevised:
				badge = "  ↻ " + i18n.T("moa.layers.revised")
			case moa.StanceKept:
				badge = "  = " + i18n.T("moa.layers.kept")
			}
			p.line(fmt.Sprintf(moaProgressMemberFmt, r.Ref.String(), elapsed, badge))
			if lead := moaLead(r.Output); lead != "" {
				p.line(fmt.Sprintf(moaProgressLeadFmt, moaClip(lead, moaProgressLeadMaxCol)))
			}
		},
	}
}

// synthesizing reports that the aggregator took over.
func (p *moaToolPanel) synthesizing(aggregator moa.Ref) {
	p.line(i18n.T("moa.panel.synthesizing", aggregator.String()))
}

// moaPositionsReport renders every member's position per round for the
// model that called @moa: its bottom line ("lead") or whole answer
// ("full"), tagged [kept]/[revised] in review rounds. "off" renders
// nothing. Members left out of a round are not listed in it.
func moaPositionsReport(mode string, out moa.Outcome) string {
	if mode == moaPanelOff || len(out.Layers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(moaPositionsHeading)
	for li, layer := range out.Layers {
		if li == 0 {
			fmt.Fprintf(&b, moaRoundHeadingFmt, li+1)
		} else {
			fmt.Fprintf(&b, moaReviewHeadingFmt, li+1)
		}
		for i, r := range layer {
			if errors.Is(r.Err, moa.ErrSkipped) {
				continue
			}
			if !r.OK() {
				reason := "empty answer"
				if r.Err != nil {
					reason = moaOneLine(r.Err.Error())
				}
				fmt.Fprintf(&b, moaFailedLineFmt, r.Ref.String(), reason)
				continue
			}
			tag := ""
			if li < len(out.Stances) && out.Stances[li][i] != moa.StanceNone {
				tag = fmt.Sprintf(moaStanceTagFmt, out.Stances[li][i])
			}
			if mode == moaPanelFull {
				fmt.Fprintf(&b, moaFullAnswerFmt, r.Ref.String(), tag, strings.TrimSpace(r.Output))
				continue
			}
			lead := moaLead(r.Output)
			if lead == "" {
				lead = moaClip(moaOneLine(r.Output), moaProgressLeadMaxCol)
			}
			fmt.Fprintf(&b, moaLeadLineFmt, r.Ref.String(), tag, lead)
		}
	}
	return b.String()
}
