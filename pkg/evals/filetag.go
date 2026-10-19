/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"gopkg.in/yaml.v3"
)

// fileTag makes a files: entry take its content from a file next to the
// suite instead of from the YAML: `go.mod: !file testdata/go.mod`.
const fileTag = "!file"

// maxFileTagBytes caps one !file entry: its content is held in memory and
// written into every trial's sandbox. A var, not a const, purely as a test
// seam.
var maxFileTagBytes int64 = 16 << 20

// fileTagRef is one files: entry tagged !file. An entry under a YAML anchor
// is shared by every case that aliases it, so one node can have several.
type fileTagRef struct {
	caseIndex int
	name      string
}

// resolveFileTags replaces every `!file <path>` value under a case's files:
// with the content of that file, resolved against the suite directory, and
// rejects any other application tag anywhere in the suite.
//
// It walks the parsed node tree after the strict decode instead of hooking
// the decoder: yaml.v3 decodes a custom UnmarshalYAML's subtree without
// KnownFields, so a misspelled key below it would stop being an error.
func (s *Suite) resolveFileTags(data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	refs := fileTagRefs(&root)

	var walkErr error
	walkNodes(&root, func(n *yaml.Node) bool {
		if !isApplicationTag(n.Tag) {
			return true
		}
		if n.Tag != fileTag {
			walkErr = errors.New(i18n.T("evals.load.tag_unknown", n.Line, n.Tag))
			return false
		}
		if _, ok := refs[n]; !ok || n.Kind != yaml.ScalarNode {
			walkErr = errors.New(i18n.T("evals.load.tag_misplaced", n.Line))
			return false
		}
		return true
	})
	if walkErr != nil {
		return walkErr
	}

	for n, list := range refs {
		for _, r := range list {
			if r.caseIndex >= len(s.Cases) || s.Cases[r.caseIndex] == nil {
				continue // normalize reports the empty case
			}
			c := s.Cases[r.caseIndex]
			content, perm, err := readFileTag(s.Dir, r.name, n.Value)
			if err != nil {
				return fmt.Errorf("%s: %w", i18n.T("evals.load.case_prefix", c.ID), err)
			}
			c.Files[r.name] = content
			if c.filePerms == nil {
				c.filePerms = map[string]fs.FileMode{}
			}
			c.filePerms[r.name] = perm
		}
	}
	return nil
}

// fileTagRefs finds the value nodes of cases[*].files entries tagged !file,
// following an aliased files: mapping to the anchored one.
func fileTagRefs(root *yaml.Node) map[*yaml.Node][]fileTagRef {
	refs := map[*yaml.Node][]fileTagRef{}
	cases := mappingValue(document(root), "cases")
	if cases == nil || cases.Kind != yaml.SequenceNode {
		return refs
	}
	for i, c := range cases.Content {
		files := deref(mappingValue(deref(c), "files"))
		if files == nil || files.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(files.Content); j += 2 {
			if v := files.Content[j+1]; v.Tag == fileTag {
				refs[v] = append(refs[v], fileTagRef{caseIndex: i, name: files.Content[j].Value})
			}
		}
	}
	return refs
}

// readFileTag reads one !file entry. The path resolves like fixture: does,
// against the suite directory unless it is absolute.
func readFileTag(suiteDir, name, path string) (string, fs.FileMode, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", 0, errors.New(i18n.T("evals.load.file_tag_empty", name))
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(suiteDir, full)
	}
	info, err := os.Stat(full)
	switch {
	case err != nil:
		return "", 0, errors.New(i18n.T("evals.load.file_tag_missing", name, path))
	case info.IsDir():
		return "", 0, errors.New(i18n.T("evals.load.file_tag_dir", name, path))
	case !info.Mode().IsRegular():
		return "", 0, errors.New(i18n.T("evals.load.file_tag_irregular", name, path))
	case info.Size() > maxFileTagBytes:
		return "", 0, errors.New(i18n.T("evals.load.file_tag_too_big", name, path, info.Size(), maxFileTagBytes))
	}
	f, err := os.Open(full) //#nosec G304 -- a path the operator's own suite names, like fixture:
	if err != nil {
		return "", 0, errors.New(i18n.T("evals.load.file_tag_read", name, path, err))
	}
	defer func() { _ = f.Close() }()
	// Bounded again while reading: the file can grow after the Stat.
	data, err := io.ReadAll(io.LimitReader(f, maxFileTagBytes+1))
	if err != nil {
		return "", 0, errors.New(i18n.T("evals.load.file_tag_read", name, path, err))
	}
	if int64(len(data)) > maxFileTagBytes {
		return "", 0, errors.New(i18n.T("evals.load.file_tag_too_big", name, path, int64(len(data)), maxFileTagBytes))
	}
	return string(data), info.Mode().Perm(), nil
}

// isApplicationTag reports whether a tag is one the suite author wrote (a
// local "!name" tag), as opposed to a standard YAML tag ("!!str") or the
// non-specific "!".
func isApplicationTag(tag string) bool {
	return strings.HasPrefix(tag, "!") && !strings.HasPrefix(tag, "!!") && tag != "!"
}

// walkNodes visits n and everything below it, depth first, until visit
// returns false. Aliases are not followed: the anchored node is visited
// where it is defined.
func walkNodes(n *yaml.Node, visit func(*yaml.Node) bool) bool {
	if n == nil {
		return true
	}
	if !visit(n) {
		return false
	}
	if n.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range n.Content {
		if !walkNodes(child, visit) {
			return false
		}
	}
	return true
}

func document(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

func deref(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode {
		return n.Alias
	}
	return n
}

// mappingValue returns the value node of key in a mapping node, or nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}
