/*
 * ChatCLI - Slash command file parser
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// flexString is a YAML string that tolerates the shapes humans (and other
// tools' files) actually write. `argument-hint: [days]` is the canonical
// trap: YAML reads a bare bracketed token as a flow SEQUENCE, and a strict
// string field fails the whole unmarshal — silently dropping the command.
// Scalars pass through; sequences re-render as "[a, b]" (the literal the
// author saw); anything else re-serializes to its YAML text.
type flexString string

// UnmarshalYAML implements yaml.Unmarshaler.
func (f *flexString) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*f = flexString(value.Value)
	case yaml.SequenceNode:
		parts := make([]string, 0, len(value.Content))
		for _, n := range value.Content {
			parts = append(parts, n.Value)
		}
		*f = flexString("[" + strings.Join(parts, ", ") + "]")
	default:
		raw, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		*f = flexString(strings.TrimSpace(string(raw)))
	}
	return nil
}

// maxCommandFileBytes bounds a single command file. Prompt templates are
// text; anything past this is a mistake (or an attack via a shared repo)
// and is rejected rather than silently truncated.
const maxCommandFileBytes = 512 * 1024

// matchesFormat reports whether a filename belongs to a source's format.
// Plain markdown deliberately excludes *.prompt.md so a repo carrying both
// conventions never double-loads a Copilot file under two names.
func matchesFormat(name string, format fileFormat) bool {
	switch format {
	case formatTOML:
		return strings.HasSuffix(name, ".toml")
	case formatPromptMD:
		return strings.HasSuffix(name, ".prompt.md")
	default:
		return strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".prompt.md")
	}
}

// parseCommandFileAs dispatches to the right parser for the source's
// format. Markdown and .prompt.md share the frontmatter parser (name
// derivation strips the ".prompt" convention suffix); TOML is Gemini/Qwen.
func parseCommandFileAs(path string, spec dirSpec, namespace string) (*Command, error) {
	if spec.format == formatTOML {
		return parseCommandFileTOML(path, spec.source, namespace)
	}
	return parseCommandFile(path, spec.source, namespace)
}

// parseCommandFileTOML loads one Gemini/Qwen-style TOML command file.
func parseCommandFileTOML(path string, source Source, namespace string) (*Command, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from the resolved command dirs walk, not user input
	if err != nil {
		return nil, err
	}
	if len(data) > maxCommandFileBytes {
		return nil, fmt.Errorf("command file too large (%d bytes, cap %d): %s", len(data), maxCommandFileBytes, path)
	}
	parsed, err := parseCommandTOML(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	name := normalizeName(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if name == "" {
		return nil, fmt.Errorf("%s: empty command name", path)
	}
	return &Command{
		Name:        name,
		Namespace:   normalizeName(namespace),
		Description: strings.TrimSpace(parsed.Description),
		Content:     strings.TrimSpace(parsed.Prompt),
		Path:        path,
		Source:      source,
	}, nil
}

// parseCommandFile loads one .md file into a Command. Unlike the skill
// loader's bufio.Scanner (which silently drops files containing a single
// line longer than 64KB), this reads line-wise via bufio.Reader with no
// per-line cap — prompt templates legitimately carry long lines.
func parseCommandFile(path string, source Source, namespace string) (*Command, error) {
	f, err := os.Open(path) // #nosec G304 -- path comes from the resolved command dirs walk, not user input
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	if fi, err := f.Stat(); err == nil && fi.Size() > maxCommandFileBytes {
		return nil, fmt.Errorf("command file too large (%d bytes, cap %d): %s", fi.Size(), maxCommandFileBytes, path)
	}

	fmRaw, body, err := splitFrontmatter(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	var fm frontmatter
	if fmRaw != "" {
		if err := yaml.Unmarshal([]byte(fmRaw), &fm); err != nil {
			// Real-world frontmatter is often not valid YAML at all —
			// Codex's own documented example writes
			//   argument-hint: [FILES=<paths>] [PR_TITLE="<title>"]
			// which is a YAML syntax error (two flow scalars). Fall back to
			// a line-wise "key: rest of line verbatim" read of the known
			// keys; only files that fail BOTH parses are skipped.
			fm = parseFrontmatterLoose(fmRaw)
		}
	}

	name := strings.TrimSpace(string(fm.Name))
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		// Copilot prompt files are "<name>.prompt.md": the remaining
		// ".prompt" is convention, not identity — /review, not
		// /review.prompt (which normalizeName would reject anyway).
		name = strings.TrimSuffix(name, ".prompt")
	}
	name = normalizeName(name)
	if name == "" {
		return nil, fmt.Errorf("%s: empty command name", path)
	}

	return &Command{
		Name:         name,
		Namespace:    normalizeName(namespace),
		Description:  strings.TrimSpace(string(fm.Description)),
		ArgumentHint: strings.TrimSpace(string(fm.ArgumentHint)),
		Model:        strings.TrimSpace(string(fm.Model)),
		Effort:       strings.TrimSpace(string(fm.Effort)),
		AllowedTools: splitAllowedTools(string(fm.AllowedTools)),
		Content:      strings.TrimSpace(body),
		Path:         path,
		Source:       source,
	}, nil
}

// parseFrontmatterLoose reads the known frontmatter keys as "key: verbatim
// rest of line" — the shape authors actually mean when their YAML does not
// parse. Multi-line values are out of scope here (they parse fine as YAML).
func parseFrontmatterLoose(fmRaw string) frontmatter {
	var fm frontmatter
	for _, line := range strings.Split(fmRaw, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value := flexString(strings.TrimSpace(rest))
		switch strings.TrimSpace(key) {
		case "name":
			fm.Name = value
		case "description":
			fm.Description = value
		case "argument-hint":
			fm.ArgumentHint = value
		case "model":
			fm.Model = value
		case "effort":
			fm.Effort = value
		case "allowed-tools":
			fm.AllowedTools = value
		}
	}
	return fm
}

// splitFrontmatter separates an optional leading "---\n...\n---" YAML block
// from the body. A file with no frontmatter is all body.
func splitFrontmatter(r io.Reader) (fm string, body string, err error) {
	br := bufio.NewReader(r)

	first, err := readLine(br)
	if errors.Is(err, io.EOF) {
		return "", first, nil
	}
	if err != nil {
		return "", "", err
	}
	if strings.TrimRight(first, "\r") != "---" {
		rest, rerr := io.ReadAll(br)
		if rerr != nil {
			return "", "", rerr
		}
		return "", first + "\n" + string(rest), nil
	}

	var fmB strings.Builder
	for {
		line, lerr := readLine(br)
		if strings.TrimRight(line, "\r") == "---" {
			break
		}
		fmB.WriteString(line)
		fmB.WriteByte('\n')
		if errors.Is(lerr, io.EOF) {
			// Unterminated frontmatter: treat the whole file as broken
			// rather than guessing where the body starts.
			return "", "", fmt.Errorf("unterminated frontmatter (missing closing ---)")
		}
		if lerr != nil {
			return "", "", lerr
		}
	}
	rest, rerr := io.ReadAll(br)
	if rerr != nil {
		return "", "", rerr
	}
	return fmB.String(), string(rest), nil
}

// readLine reads until \n (returned without it). io.EOF is returned along
// with any final unterminated line.
func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	return strings.TrimSuffix(line, "\n"), err
}

// normalizeName lowercases and validates an invocation token: letters,
// digits, '-', '_' only. Anything else empties the name (file skipped) so a
// hostile filename cannot smuggle spaces or shell metacharacters into the
// dispatch table.
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return ""
		}
	}
	return s
}
