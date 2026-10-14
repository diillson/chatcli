/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * skill_update_merge.go — an update changes what it was given and nothing else.
 *
 * renderSkill knows four frontmatter keys. A SKILL.md carries more: paths
 * (path-glob activation), model, effort, version, user-invocable,
 * disable-model-invocation, argument-hint, source, and allowed-tools written
 * in any of the shapes the loader accepts. Rendering an update from the input
 * alone silently deleted every one of them — a skill evolved by the
 * self-evolution engine lost its allowed-tools, and one with paths would have
 * stopped activating on file paths without a trace.
 *
 * On update the existing frontmatter is therefore the base: a field the
 * caller sent replaces the stored one, a field it left out is carried over
 * exactly as it was written.
 */
package plugins

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// storedSkill is what an update starts from.
type storedSkill struct {
	// fields maps a frontmatter key to its key/value node pair, as written.
	fields map[string][2]*yaml.Node
	// order is the frontmatter keys in file order.
	order []string
	body  string
}

// readStoredSkill loads an existing SKILL.md for an update. ok is false when
// the file is missing or its frontmatter is not a YAML mapping — an update
// is also how a broken skill gets repaired, so it then renders from the
// input alone, as before.
func readStoredSkill(file string) (storedSkill, bool) {
	data, err := os.ReadFile(file) // #nosec G304 -- file derived from a slug-validated skill name
	if err != nil {
		return storedSkill{}, false
	}
	fm, body, ok := splitSkillMD(string(data))
	if !ok {
		return storedSkill{body: strings.TrimSpace(string(data))}, true
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil ||
		len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return storedSkill{}, false
	}
	m := doc.Content[0]
	st := storedSkill{fields: make(map[string][2]*yaml.Node, len(m.Content)/2), body: strings.TrimSpace(body)}
	for i := 0; i+1 < len(m.Content); i += 2 {
		key := m.Content[i].Value
		if _, dup := st.fields[key]; dup {
			continue
		}
		st.fields[key] = [2]*yaml.Node{m.Content[i], m.Content[i+1]}
		st.order = append(st.order, key)
	}
	return st, true
}

// splitSkillMD separates the frontmatter from the body. ok is false when the
// file has no frontmatter block.
func splitSkillMD(content string) (fm, body string, ok bool) {
	s := strings.TrimLeft(content, "\uFEFF\n")
	if !strings.HasPrefix(s, "---") {
		return "", content, false
	}
	rest := s[len("---"):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 || strings.TrimSpace(rest[:nl]) != "" {
		return "", content, false
	}
	rest = rest[nl+1:]
	if strings.HasPrefix(rest, "---") {
		return "", afterDelimiter(rest[len("---"):]), true
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", content, false
	}
	return rest[:idx], afterDelimiter(rest[idx+len("\n---"):]), true
}

// afterDelimiter drops the remainder of the closing delimiter line.
func afterDelimiter(s string) string {
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		return s[nl+1:]
	}
	return ""
}

// value returns the stored scalar for key, "" when absent or not a scalar.
func (st storedSkill) value(key string) string {
	pair, ok := st.fields[key]
	if !ok || pair[1].Kind != yaml.ScalarNode {
		return ""
	}
	return pair[1].Value
}

// has reports whether the stored frontmatter carries key.
func (st storedSkill) has(key string) bool {
	_, ok := st.fields[key]
	return ok
}

// renderField writes one stored key/value pair as the file had it (modulo
// YAML formatting). The nodes came out of a successful parse, so encoding
// them back cannot fail in practice.
func (st storedSkill) renderField(b *strings.Builder, key string) {
	pair := st.fields[key]
	node := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{pair[0], pair[1]}}
	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return
	}
	_ = enc.Close()
	b.WriteString(out.String())
}
