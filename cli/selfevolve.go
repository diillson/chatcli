/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * selfevolve.go — the self-evolution engine.
 *
 * Root cause it addresses: long-term memory "happens by itself" because a
 * background worker extracts facts every few turns, but skills only ever got
 * created when the model remembered to call @skill — so users had to ASK for
 * them. This engine gives skills the same proactive treatment WITHOUT a second
 * LLM call: it piggybacks on the memory worker's existing extraction pass. The
 * worker appends selfEvolveSkillDirective to that prompt, and the same response
 * that yields facts also yields ## SKILL_CANDIDATES. applySkillCandidates then
 * authors or evolves them.
 *
 * Safety model (mode=auto): every authored skill is engine-OWNED and tracked by
 * a content hash in a sidecar manifest, mirroring builtin.Seed. The engine only
 * ever evolves a skill it still owns and the user has not hand-edited; a skill
 * the user authored or touched is never clobbered. Every authored skill is
 * reversible via `/skill remove` and surfaced with a one-line notice. mode=
 * suggest never writes to disk; mode=off disables the directive entirely.
 *
 * The only operator-facing knob is CHATCLI_SELFEVOLVE_MODE. Cadence, cost and
 * resilience are inherited from the memory worker — no new tuning surface.
 */
package cli

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
)

// selfEvolveMode is the autonomy level for skill authoring.
type selfEvolveMode int

const (
	selfEvolveOff selfEvolveMode = iota
	selfEvolveSuggest
	selfEvolveAuto
)

// resolveSelfEvolveMode reads CHATCLI_SELFEVOLVE_MODE. Unknown or empty values
// fall back to the default so a typo degrades to defined behavior rather than
// silently disabling the engine.
func resolveSelfEvolveMode() selfEvolveMode {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(config.SelfEvolveModeEnv)))
	if v == "" {
		v = config.DefaultSelfEvolveMode
	}
	switch v {
	case "off", "false", "0", "disabled":
		return selfEvolveOff
	case "suggest", "propose", "ask":
		return selfEvolveSuggest
	case "auto", "on", "true", "1":
		return selfEvolveAuto
	default:
		// Mirror the default for anything unrecognized.
		if config.DefaultSelfEvolveMode == "suggest" {
			return selfEvolveSuggest
		}
		if config.DefaultSelfEvolveMode == "off" {
			return selfEvolveOff
		}
		return selfEvolveAuto
	}
}

func (m selfEvolveMode) String() string {
	switch m {
	case selfEvolveOff:
		return "off"
	case selfEvolveSuggest:
		return "suggest"
	default:
		return "auto"
	}
}

// selfEvolveSkillDirective is appended to the memory-extraction prompt when the
// engine is enabled. It defines a strictly-delimited block format so the
// response stays trivially and robustly parseable (no YAML inference). The bar
// is deliberately high: most turns must yield NOTHING_NEW, or the skills
// directory fills with noise.
const selfEvolveSkillDirective = `
## ADDITIONAL SECTION: SKILL_CANDIDATES

Besides the memory sections above, detect REUSABLE SKILLS. A skill is a
multi-step PROCEDURE, project convention, or workflow that the user performed or
asked for and is likely to repeat — something worth auto-loading next time its
topic comes up. This is NOT for one-off facts (those are LONGTERM/PROFILE).

Emit a skill ONLY when ALL hold:
  - it is genuinely reusable (would help a future, similar request);
  - it is non-trivial (more than a single obvious command);
  - it is concrete enough to write down as steps.
If nothing qualifies, write exactly: NOTHING_NEW

You are given an index of EXISTING SKILLS below (names + one-line descriptions
ONLY — not their full content). There are two kinds of candidate:

(A) NEW skill — the topic is NOT covered by any indexed skill. Emit a full block:
[[skill]]
name: kebab-case-slug
description: one specific line; this decides when the skill auto-activates
triggers: comma, separated, keywords
body:
# Title

Concrete, ordered steps in markdown.
[[/skill]]

(B) EVOLVE an existing skill — your insight improves a skill already in the
index. Reuse its EXACT name and describe ONLY WHAT TO CHANGE. Do NOT rewrite the
whole skill and do NOT guess its current text: the engine has the real body and
will merge your improvement into it. Emit:
[[skill]]
name: existing-skill-name
action: evolve
improvement:
- the step or refinement to add
- anything to correct
[[/skill]]

Rules: name is a-z/0-9/dash only. PREFER (B) evolve whenever the index already
has a related skill; only create a NEW skill when nothing covers the topic.`

// skillCandidate is one parsed SKILL_CANDIDATES block. It is either a NEW-skill
// candidate (Body set) or an EVOLVE candidate (Improvement set); the engine
// merges the latter into the existing body on demand rather than from the prompt.
type skillCandidate struct {
	Name        string
	Description string
	Triggers    []string
	Body        string // NEW skills: the full markdown body
	Action      string // optional hint: "evolve" | "create"
	Improvement string // EVOLVE: what to change (merged into the current body)
}

// isEvolve reports whether the candidate targets an existing skill rather than
// creating one. An explicit action wins; otherwise an improvement-without-body
// is an evolution.
func (c skillCandidate) isEvolve() bool {
	switch strings.ToLower(strings.TrimSpace(c.Action)) {
	case "evolve", "update":
		return true
	case "create", "new":
		return false
	}
	return strings.TrimSpace(c.Improvement) != "" && strings.TrimSpace(c.Body) == ""
}

// validCreate reports whether the candidate can be authored as a new skill.
func (c skillCandidate) validCreate() bool {
	return skillSlugRe.MatchString(c.Name) &&
		strings.TrimSpace(c.Description) != "" &&
		strings.TrimSpace(c.Body) != ""
}

// changeText is the improvement to fold in when evolving; it falls back to the
// body if the model supplied a full block for an existing skill.
func (c skillCandidate) changeText() string {
	if t := strings.TrimSpace(c.Improvement); t != "" {
		return t
	}
	return strings.TrimSpace(c.Body)
}

// skillMerger folds an improvement into a skill's current body and returns the
// full merged markdown body. The worker injects an LLM-backed implementation;
// keeping it a parameter makes the engine deterministically testable, and a nil
// merger simply disables evolution of existing skills.
type skillMerger func(ctx context.Context, name, currentBody, improvement string) (string, error)

// selfEvolveSummary records what one extraction pass changed, for the notice.
type selfEvolveSummary struct {
	Authored      []string // newly created skill names
	Evolved       []string // engine-owned skills updated in place
	EvolvedBackup []string // user-owned skills evolved after a reversible backup
	Suggested     []string // detected but not written (suggest mode / backup failed)
}

func (s selfEvolveSummary) isEmpty() bool {
	return len(s.Authored) == 0 && len(s.Evolved) == 0 &&
		len(s.EvolvedBackup) == 0 && len(s.Suggested) == 0
}

// applySkillCandidates parses the extraction response for skill candidates and,
// per the mode, authors new skills or evolves existing ones (auto), or records
// suggestions (suggest). Evolving an existing skill loads only THAT skill's body
// from disk and folds the improvement in via merge — no skill bodies are ever
// injected into the per-turn prompt. It is a no-op in off mode or when no
// candidates are present, and never returns an error: self-evolution is
// best-effort and must never disrupt the turn.
func (cli *ChatCLI) applySkillCandidates(ctx context.Context, response string, mode selfEvolveMode, merge skillMerger) selfEvolveSummary {
	var sum selfEvolveSummary
	if mode == selfEvolveOff {
		return sum
	}
	candidates := parseSkillCandidates(response)
	if len(candidates) == 0 {
		return sum
	}

	man := loadSelfEvolveManifest()
	seen := make(map[string]bool, len(candidates))
	dirty := false

	for _, c := range candidates {
		if c.Name == "" || seen[c.Name] || !skillSlugRe.MatchString(c.Name) {
			continue
		}
		seen[c.Name] = true

		existing, exists := plugins.ReadSkillContent(c.Name)

		// suggest mode never touches disk: record what WOULD be actionable.
		if mode == selfEvolveSuggest {
			if (exists && c.changeText() != "") || (!exists && c.validCreate()) {
				sum.Suggested = append(sum.Suggested, c.Name)
			}
			continue
		}

		// An existing name always means "evolve" (never clobber via create).
		if exists {
			if cli.evolveExistingSkill(ctx, c, existing, man, merge, &sum) {
				dirty = true
			}
			continue
		}
		if cli.authorNewSkill(c, man, &sum) {
			dirty = true
		}
	}

	if dirty {
		man.save()
		// Make the new/evolved skills auto-activatable in the SAME session
		// rather than only on the next launch.
		if cli.personaHandler != nil {
			if _, err := cli.personaHandler.GetManager().RefreshSkills(); err != nil {
				cli.logger.Debug("selfevolve: skill refresh failed: " + err.Error())
			}
		}
	}
	return sum
}

// authorNewSkill writes a brand-new skill. Returns whether the manifest changed.
func (cli *ChatCLI) authorNewSkill(c skillCandidate, man *selfEvolveManifest, sum *selfEvolveSummary) bool {
	if !c.validCreate() {
		return false
	}
	if _, err := plugins.SaveSkill(c.toInput(), false); err != nil {
		cli.logger.Debug("selfevolve: author failed: " + err.Error())
		return false
	}
	man.record(c.Name)
	sum.Authored = append(sum.Authored, c.Name)
	return true
}

// evolveExistingSkill folds the candidate's improvement into the skill's current
// body via the merge callback, preserving working content. A user-owned skill is
// backed up first so the change is reversible. Returns whether the manifest
// changed.
func (cli *ChatCLI) evolveExistingSkill(ctx context.Context, c skillCandidate, existing string, man *selfEvolveManifest, merge skillMerger, sum *selfEvolveSummary) bool {
	change := c.changeText()
	if change == "" || merge == nil {
		return false
	}
	currentBody := strings.TrimSpace(skillFileBody(existing))
	merged, err := merge(ctx, c.Name, currentBody, change)
	if err != nil {
		cli.logger.Debug("selfevolve: merge failed, not evolving: " + err.Error())
		return false
	}
	merged = strings.TrimSpace(merged)
	if merged == "" || merged == currentBody {
		return false // merge produced nothing new
	}

	// Carry over description/triggers the model omitted from an evolve block.
	desc, triggers := strings.TrimSpace(c.Description), c.Triggers
	if desc == "" || len(triggers) == 0 {
		ed, et := cli.existingSkillMeta(c.Name)
		if desc == "" {
			desc = ed
		}
		if len(triggers) == 0 {
			triggers = et
		}
	}
	if desc == "" {
		desc = c.Name // writeSkill requires a non-empty description
	}

	// Preserve a user-authored (or hand-edited) original once before changing it.
	ownedByEngine := man.owns(c.Name, existing)
	if !ownedByEngine {
		if _, err := plugins.BackupSkill(c.Name); err != nil {
			cli.logger.Debug("selfevolve: backup failed, not evolving: " + err.Error())
			sum.Suggested = append(sum.Suggested, c.Name)
			return false
		}
	}

	in := plugins.AutoSkillInput{Name: c.Name, Description: desc, Content: merged, Triggers: triggers}
	if _, err := plugins.SaveSkill(in, true); err != nil {
		cli.logger.Debug("selfevolve: evolve failed: " + err.Error())
		return false
	}
	man.record(c.Name)
	if ownedByEngine {
		sum.Evolved = append(sum.Evolved, c.Name)
	} else {
		sum.EvolvedBackup = append(sum.EvolvedBackup, c.Name)
	}
	return true
}

// existingSkillMeta returns the description and triggers of an already-saved
// skill, so an evolve block that omits them keeps the originals.
func (cli *ChatCLI) existingSkillMeta(name string) (desc string, triggers []string) {
	if cli.personaHandler == nil {
		return "", nil
	}
	s, err := cli.personaHandler.GetManager().GetSkillByName(name)
	if err != nil || s == nil {
		return "", nil
	}
	return s.Description, []string(s.Triggers)
}

// buildSkillIndex renders the compact, stable index card injected into the
// extraction prompt: one "name — description" line per skill, nothing more.
// Mirroring memory's index mode, it carries NO skill bodies — the model needs
// only awareness of what exists to target an evolution; the body is pulled on
// demand at merge time. Returns "" when there is no persona manager or no skills.
func (cli *ChatCLI) buildSkillIndex() string {
	if cli.personaHandler == nil {
		return ""
	}
	skills, err := cli.personaHandler.GetManager().ListSkills()
	if err != nil || len(skills) == 0 {
		return ""
	}
	names := make([]string, 0, len(skills))
	desc := make(map[string]string, len(skills))
	for _, s := range skills {
		if s == nil || s.Name == "" || desc[s.Name] != "" {
			continue
		}
		names = append(names, s.Name)
		d := strings.TrimSpace(s.Description)
		if d == "" {
			d = "(no description)"
		}
		desc[s.Name] = d
	}
	return formatSkillIndex(names, desc)
}

// skillIndexEntryCap bounds each index line so one verbose description cannot
// bloat the card.
const skillIndexEntryCap = 140

// formatSkillIndex renders the stable, sorted index card. Sorting keeps the
// block byte-stable across turns so it stays prompt-cache friendly.
func formatSkillIndex(names []string, desc map[string]string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("EXISTING SKILLS (index — reuse a name to EVOLVE that skill; do not duplicate):\n")
	for _, n := range sorted {
		b.WriteString("- " + n + " — " + truncateForLog(desc[n], skillIndexEntryCap) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c skillCandidate) toInput() plugins.AutoSkillInput {
	return plugins.AutoSkillInput{
		Name:        c.Name,
		Description: strings.TrimSpace(c.Description),
		Content:     strings.TrimSpace(c.Body),
		Triggers:    c.Triggers,
	}
}

// skillFileBody returns the markdown body of a SKILL.md (everything after the
// closing frontmatter delimiter), or the whole file if it has no frontmatter.
func skillFileBody(content string) string {
	s := strings.TrimLeft(content, "\n")
	if !strings.HasPrefix(s, "---") {
		return content
	}
	// Skip the opening "---" line, then find the closing "---" line.
	rest := s[len("---"):]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return content
	}
	after := rest[idx+len("\n---"):]
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		after = after[nl+1:]
	}
	return after
}

// formatSelfEvolveNotice renders a one-line summary of what the engine did this
// pass, e.g. "skills: authored deploy-this-project" or "skill suggestion: ...".
// Returns "" when nothing happened, so the worker stays silent on quiet turns.
func formatSelfEvolveNotice(s selfEvolveSummary) string {
	if s.isEmpty() {
		return ""
	}
	var parts []string
	if len(s.Authored) > 0 {
		parts = append(parts, i18n.T("selfevolve.notice.authored", strings.Join(s.Authored, ", ")))
	}
	if len(s.Evolved) > 0 {
		parts = append(parts, i18n.T("selfevolve.notice.evolved", strings.Join(s.Evolved, ", ")))
	}
	if len(s.EvolvedBackup) > 0 {
		parts = append(parts, i18n.T("selfevolve.notice.evolved_backup", strings.Join(s.EvolvedBackup, ", ")))
	}
	if len(s.Suggested) > 0 {
		parts = append(parts, i18n.T("selfevolve.notice.suggested", strings.Join(s.Suggested, ", ")))
	}
	if len(parts) == 0 {
		return ""
	}
	return i18n.T("selfevolve.notice.prefix") + " " + strings.Join(parts, "; ")
}
