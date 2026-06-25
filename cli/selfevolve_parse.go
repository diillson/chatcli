/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * selfevolve_parse.go — robust, YAML-free parser for SKILL_CANDIDATES blocks.
 *
 * The extraction LLM emits zero or more self-delimited [[skill]]...[[/skill]]
 * blocks. We scan for those delimiters directly rather than inferring YAML, so a
 * stray indent or quote can never corrupt a parse. Blocks are recognized
 * anywhere in the response (they are self-delimiting), which tolerates models
 * that reorder or relabel the section header.
 */
package cli

import (
	"regexp"
	"strings"
)

// skillSlugRe mirrors the @skill writer's slug constraint so candidates that
// could never be saved are dropped before any disk work.
var skillSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// placeholderSkillNames are literal names from the directive's own example. A
// model that echoes the template must not create a junk skill from it.
var placeholderSkillNames = map[string]bool{
	"kebab-case-slug": true,
	"name":            true,
}

const (
	skillBlockOpen  = "[[skill]]"
	skillBlockClose = "[[/skill]]"
)

// parseSkillCandidates extracts every well-formed skill block from the response.
func parseSkillCandidates(response string) []skillCandidate {
	var out []skillCandidate
	rest := response
	for {
		start := strings.Index(rest, skillBlockOpen)
		if start < 0 {
			break
		}
		rest = rest[start+len(skillBlockOpen):]

		block := rest
		end := strings.Index(rest, skillBlockClose)
		if end >= 0 {
			block = rest[:end]
			rest = rest[end+len(skillBlockClose):]
		} else {
			rest = ""
		}

		c := parseSkillBlock(block)
		if c.Name != "" && !placeholderSkillNames[c.Name] {
			out = append(out, c)
		}
		if end < 0 {
			break
		}
	}
	return out
}

// parseSkillBlock parses the key/value preamble (name/description/triggers) and
// captures everything after a "body:" marker verbatim as the skill content.
func parseSkillBlock(block string) skillCandidate {
	var c skillCandidate
	// "body:" captures verbatim markdown; "improvement:" captures free text
	// (evolve blocks). Whichever marker appears, subsequent lines flow into it.
	var captureLines []string
	capture := "" // "", "body", or "improvement"

	for _, ln := range strings.Split(block, "\n") {
		if capture != "" {
			captureLines = append(captureLines, ln)
			continue
		}
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		lower := strings.ToLower(t)
		switch {
		case lower == "body:" || strings.HasPrefix(lower, "body:"):
			capture = "body"
			if v := strings.TrimSpace(t[len("body:"):]); v != "" && v != "|" && v != "|-" && v != ">" {
				captureLines = append(captureLines, v)
			}
		case lower == "improvement:" || strings.HasPrefix(lower, "improvement:"):
			capture = "improvement"
			if v := strings.TrimSpace(t[len("improvement:"):]); v != "" && v != "|" && v != "|-" && v != ">" {
				captureLines = append(captureLines, v)
			}
		case strings.HasPrefix(lower, "name:"):
			c.Name = normalizeSlug(t[len("name:"):])
		case strings.HasPrefix(lower, "action:"):
			c.Action = strings.ToLower(strings.TrimSpace(t[len("action:"):]))
		case strings.HasPrefix(lower, "description:"):
			c.Description = strings.TrimSpace(t[len("description:"):])
		case strings.HasPrefix(lower, "triggers:"):
			c.Triggers = splitTriggers(t[len("triggers:"):])
		}
	}

	captured := strings.Trim(strings.Join(captureLines, "\n"), "\n")
	if capture == "improvement" {
		c.Improvement = captured
	} else {
		c.Body = captured
	}
	return c
}

// normalizeSlug lowercases, trims, and strips surrounding quotes so a quoted or
// upper-cased name still validates against skillSlugRe.
func normalizeSlug(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	return strings.ToLower(strings.TrimSpace(s))
}

// splitTriggers turns a comma-separated list into clean, de-duplicated keywords.
func splitTriggers(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'[]`)
	if s == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, raw := range strings.Split(s, ",") {
		t := strings.TrimSpace(strings.Trim(raw, `"'`))
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}
