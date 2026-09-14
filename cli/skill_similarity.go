/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Skill generalization at authoring time.
 *
 * The self-evolve engine learns skills from runs, and a run is about an
 * entity: one team, one service, one repository. Left alone the engine
 * would learn flamengo-match-info today and corinthians-match-info
 * tomorrow, when both are the same competence with a parameter. The
 * authoring prompt now asks for the competence, not the entity; and
 * before a new skill is written its keywords are compared with every
 * installed skill's name, description and triggers. A candidate that
 * overlaps an existing skill enough is folded into it as an evolution,
 * so the library keeps one team-match-info instead of one skill per team.
 */
package cli

import (
	"sort"
	"strings"
	"unicode"
)

// skillSimilarityThreshold is the keyword overlap at or above which a
// new candidate is treated as an evolution of an existing skill. The
// overlap is the shared keywords over the SMALLER set, so a terse
// candidate and a richly described skill still match, and it needs at
// least skillSimilarityMinShared words in common so two short texts
// sharing one word never fold.
const (
	skillSimilarityThreshold = 0.5
	skillSimilarityMinShared = 3
)

// skillMeta is what the comparison sees of an installed skill.
type skillMeta struct {
	Name        string
	Description string
	Triggers    []string
}

// skillStopwords are the words that carry no topic in either language
// ChatCLI's users write in; anything shorter than four runes is dropped
// before this list is consulted.
var skillStopwords = map[string]bool{
	"with": true, "from": true, "that": true, "this": true, "when": true, "then": true, "into": true, "using": true,
	"para": true, "como": true, "sobre": true, "pelo": true, "pela": true, "onde": true, "quando": true, "usando": true,
	"skill": true, "info": true, "information": true, "informações": true, "consultar": true, "consulta": true,
}

// skillKeywords tokenizes text into the set of topic words.
func skillKeywords(parts ...string) map[string]bool {
	out := map[string]bool{}
	for _, p := range parts {
		for _, w := range strings.FieldsFunc(strings.ToLower(p), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsNumber(r)
		}) {
			if len([]rune(w)) < 4 || skillStopwords[w] {
				continue
			}
			out[w] = true
		}
	}
	return out
}

// overlap is the shared keywords over the smaller set, 0 when fewer than
// skillSimilarityMinShared words are shared.
func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	if inter < skillSimilarityMinShared {
		return 0
	}
	smaller := len(a)
	if len(b) < smaller {
		smaller = len(b)
	}
	return float64(inter) / float64(smaller)
}

// closestSkill returns the installed skill whose keywords overlap the
// candidate's the most, with the overlap. "" when there is none.
func closestSkill(existing []skillMeta, c skillCandidate) (string, float64) {
	cand := skillKeywords(strings.ReplaceAll(c.Name, "-", " "), c.Description, strings.Join(c.Triggers, " "))
	best, bestScore := "", 0.0
	names := make([]string, 0, len(existing))
	byName := make(map[string]skillMeta, len(existing))
	for _, m := range existing {
		if m.Name == "" {
			continue
		}
		names = append(names, m.Name)
		byName[m.Name] = m
	}
	sort.Strings(names) // deterministic on ties
	for _, name := range names {
		m := byName[name]
		score := overlap(cand, skillKeywords(strings.ReplaceAll(m.Name, "-", " "), m.Description, strings.Join(m.Triggers, " ")))
		if score > bestScore {
			best, bestScore = name, score
		}
	}
	return best, bestScore
}

// existingSkillMetas lists the installed skills for the comparison.
func (cli *ChatCLI) existingSkillMetas() []skillMeta {
	if cli == nil || cli.personaHandler == nil {
		return nil
	}
	skills, err := cli.personaHandler.GetManager().ListSkills()
	if err != nil {
		return nil
	}
	out := make([]skillMeta, 0, len(skills))
	for _, s := range skills {
		if s == nil || s.Name == "" {
			continue
		}
		out = append(out, skillMeta{Name: s.Name, Description: s.Description, Triggers: []string(s.Triggers)})
	}
	return out
}

// redirectToExisting rewrites a NEW candidate as an evolution of the
// installed skill it overlaps, when the overlap reaches the threshold.
// Returns the candidate to apply and whether it was redirected.
func redirectToExisting(existing []skillMeta, c skillCandidate) (skillCandidate, bool) {
	if c.Body == "" {
		return c, false
	}
	name, score := closestSkill(existing, c)
	if name == "" || name == c.Name || score < skillSimilarityThreshold {
		return c, false
	}
	c.Name = name
	c.Improvement = c.Body
	c.Body = ""
	return c, true
}
