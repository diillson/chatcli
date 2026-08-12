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

// maxCommandFileBytes bounds a single command file. Prompt templates are
// text; anything past this is a mistake (or an attack via a shared repo)
// and is rejected rather than silently truncated.
const maxCommandFileBytes = 512 * 1024

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
			return nil, fmt.Errorf("%s: invalid frontmatter: %w", path, err)
		}
	}

	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	name = normalizeName(name)
	if name == "" {
		return nil, fmt.Errorf("%s: empty command name", path)
	}

	return &Command{
		Name:         name,
		Namespace:    normalizeName(namespace),
		Description:  strings.TrimSpace(fm.Description),
		ArgumentHint: strings.TrimSpace(fm.ArgumentHint),
		Model:        strings.TrimSpace(fm.Model),
		Effort:       strings.TrimSpace(fm.Effort),
		AllowedTools: splitAllowedTools(fm.AllowedTools),
		Content:      strings.TrimSpace(body),
		Path:         path,
		Source:       source,
	}, nil
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
