/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Diagrams as knowledge for @docs-flatten.
 *
 * A documentation repo often explains its flows only in diagrams: a mermaid
 * flowchart of the request path, a sequence diagram of the login, a
 * draw.io architecture picture. Kept as raw source, a diagram is hard to
 * retrieve ("who calls the payment service?" shares few words with
 * "A-->|POST|B") and a draw.io file was not collected at all. Each diagram
 * therefore gains a plain-text summary of what it states — its kind, its
 * nodes with their labels, and every edge as "from → to: label" — which is
 * what retrieval matches and what the model reads.
 *
 * The parsers are deliberately tolerant line scanners, not full grammars:
 * a construct they do not understand is skipped, never fatal, and the
 * diagram source is kept next to the summary so nothing is lost.
 */
package plugins

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// maxDiagramEdges bounds a summary; a generated diagram with thousands of
// edges must not crowd out the text around it.
const maxDiagramEdges = 200

// diagramEdge is one relation a diagram states.
type diagramEdge struct {
	From, To, Label string
}

// diagramSummary is what a diagram says, in a form retrieval can match.
type diagramSummary struct {
	Lang   string // mermaid | plantuml | dot | d2 | drawio | svg
	Kind   string // flowchart, sequence, class, state, er, … ("" when unknown)
	Nodes  []string
	Edges  []diagramEdge
	Labels []string // other text the diagram carries (titles, notes, svg text)
}

// empty reports whether the summary says nothing.
func (d diagramSummary) empty() bool {
	return len(d.Nodes) == 0 && len(d.Edges) == 0 && len(d.Labels) == 0
}

// render writes the summary as Markdown-ish text.
func (d diagramSummary) render() string {
	var b strings.Builder
	kind := d.Lang
	if d.Kind != "" {
		kind += " " + d.Kind
	}
	fmt.Fprintf(&b, "Diagram (%s)", kind)
	if len(d.Edges) > 0 {
		b.WriteString(" — flows:")
		for i, e := range d.Edges {
			if i == maxDiagramEdges {
				fmt.Fprintf(&b, "\n- … %d more", len(d.Edges)-maxDiagramEdges)
				break
			}
			b.WriteString("\n- " + e.From + " → " + e.To)
			if e.Label != "" {
				b.WriteString(": " + e.Label)
			}
		}
	}
	if len(d.Nodes) > 0 {
		b.WriteString("\nElements: " + strings.Join(capList(d.Nodes, maxDiagramEdges), "; "))
	}
	if len(d.Labels) > 0 {
		b.WriteString("\nText: " + strings.Join(capList(d.Labels, maxDiagramEdges), "; "))
	}
	return b.String()
}

func capList(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return append(list[:n:n], fmt.Sprintf("… %d more", len(list)-n))
}

// diagramLang maps a fence info string or a file extension to a diagram
// language ("" when it is not one).
func diagramLang(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tag, ".")))
	if i := strings.IndexAny(tag, " \t{"); i >= 0 {
		tag = tag[:i]
	}
	switch tag {
	case "mermaid", "mmd":
		return "mermaid"
	case "plantuml", "puml", "pu", "iuml", "uml":
		return "plantuml"
	case "dot", "graphviz", "gv":
		return "dot"
	case "d2":
		return "d2"
	case "drawio", "dio":
		return "drawio"
	case "svg":
		return "svg"
	}
	return ""
}

// diagramFileLang returns the diagram language of a standalone diagram file.
func diagramFileLang(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".drawio.svg"):
		return "drawio"
	case strings.HasSuffix(lower, ".mermaid"):
		return "mermaid"
	case strings.HasSuffix(lower, ".plantuml"):
		return "plantuml"
	}
	return diagramLang(filepath.Ext(lower))
}

// summarizeDiagram parses source in lang.
func summarizeDiagram(lang, source string) diagramSummary {
	switch lang {
	case "mermaid":
		return summarizeMermaid(source)
	case "plantuml":
		return summarizePlantUML(source)
	case "dot":
		return summarizeDot(source)
	case "d2":
		return summarizeD2(source)
	case "drawio":
		return summarizeDrawio(source)
	case "svg":
		return summarizeSVG(source)
	}
	return diagramSummary{Lang: lang}
}

// labelSet keeps node ids → labels and the order they appeared in.
type labelSet struct {
	label map[string]string
	order []string
}

func newLabelSet() *labelSet { return &labelSet{label: map[string]string{}} }

func (l *labelSet) add(id, label string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	label = cleanDiagramText(label)
	if _, seen := l.label[id]; !seen {
		l.order = append(l.order, id)
	}
	if label != "" || l.label[id] == "" {
		l.label[id] = label
	}
}

func (l *labelSet) name(id string) string {
	id = strings.TrimSpace(id)
	if v := l.label[id]; v != "" {
		return v
	}
	return id
}

// nodes lists the labeled nodes as "label (id)" or the id itself.
func (l *labelSet) nodes() []string {
	out := make([]string, 0, len(l.order))
	for _, id := range l.order {
		if v := l.label[id]; v != "" && v != id {
			out = append(out, v)
		} else {
			out = append(out, id)
		}
	}
	return out
}

var (
	diagramTagRe   = regexp.MustCompile(`<[^>]+>`)
	diagramSpaceRe = regexp.MustCompile(`\s+`)
)

// cleanDiagramText strips quotes, HTML and markup escapes from a label.
func cleanDiagramText(s string) string {
	s = strings.ReplaceAll(s, "<br>", " ")
	s = strings.ReplaceAll(s, "<br/>", " ")
	s = strings.ReplaceAll(s, "\\n", " ")
	s = diagramTagRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&quot;", `"`, "&amp;", "&", "&lt;", "<", "&gt;", ">", "&nbsp;", " ", "&#39;", "'").Replace(s)
	s = strings.Trim(strings.TrimSpace(s), `"'`+"`")
	return strings.TrimSpace(diagramSpaceRe.ReplaceAllString(s, " "))
}

// ---------------------------------------------------------------- mermaid

var (
	// A node with a shaped label: id[...], id(...), id{...}, id((...)),
	// id[(...)], id>...], id{{...}}, id[/.../], id(["…"]).
	// The id starts with a word character and touches its shape: "-->" is
	// never an id, and "A --> B" never reads as an asymmetric node.
	mermaidNodeRe = regexp.MustCompile(`\b([A-Za-z0-9_][A-Za-z0-9_\-.]*)(\(\[|\[\[|\[\(|\(\(\(|\(\(|\{\{|\[/|\[\\|\[|\(|\{|>)\s*("?)([^\]\)\}"]*?)("?)\s*(\]\)|\]\]|\)\]|\)\)\)|\)\)|\}\}|/\]|\\\]|\]|\)|\})`)
	// "A -- text --> B" and "A == text ==> B" carry their label between dashes.
	mermaidInlineLabelRe = regexp.MustCompile(`\s(--|==|-\.)\s*([^-=>|][^>]*?)\s*(-->|==>|-\.->|---|===)\s`)
	// The arrows of flowcharts.
	mermaidArrowRe = regexp.MustCompile(`\s*(<?-{2,}>|<?={2,}>|<?-\.+->|-{3,}|={3,}|-\.+-|--[ox]|<-->|o--o|x--x)\s*(\|([^|]*)\|)?\s*`)
	mermaidSeqRe   = regexp.MustCompile(`^\s*([^:\-+>]+?)\s*(-{1,2}>>|-{1,2}>|-{1,2}x|-{1,2}\)|<<-{1,2}>>)\s*[+\-]?\s*([^:]+?)\s*:\s*(.*)$`)
	mermaidPartRe  = regexp.MustCompile(`^\s*(participant|actor)\s+(\S+)(\s+as\s+(.+))?$`)
	mermaidRelRe   = regexp.MustCompile(`^\s*("?[^\s"]+"?)\s+("[^"]*"\s+)?(<\|--|\*--|o--|-->|--\*|--o|--\|>|<--|\.\.>|<\.\.|\.\.\|>|<\|\.\.|--|\.\.|\|\|--o\{|\}o--\|\||\|o--o\||\|\|--\|\||\}o--o\{|\|\|--\|\{|\}\|--\|\||[|}][o|]-[-.][o|][|{])\s+("[^"]*"\s+)?("?[^\s":]+"?)\s*(:\s*(.*))?$`)
	mermaidStateRe = regexp.MustCompile(`^\s*(\S+)\s*-->\s*(\S+)\s*(:\s*(.*))?$`)
)

func summarizeMermaid(src string) diagramSummary {
	d := diagramSummary{Lang: "mermaid"}
	lines := strings.Split(src, "\n")
	first := ""
	body := 0
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "%%") || strings.HasPrefix(t, "---") {
			continue
		}
		first, body = strings.ToLower(t), i+1
		break
	}
	head := strings.Fields(first)
	if len(head) == 0 {
		return d
	}
	labels := newLabelSet()
	switch kw := head[0]; {
	case kw == "graph" || kw == "flowchart":
		d.Kind = "flowchart"
		for _, l := range lines[body:] {
			mermaidFlowLine(l, labels, &d)
		}
	case kw == "sequencediagram":
		d.Kind = "sequence"
		for _, l := range lines[body:] {
			t := strings.TrimSpace(l)
			if m := mermaidPartRe.FindStringSubmatch(t); m != nil {
				labels.add(m[2], m[4])
				continue
			}
			if m := mermaidSeqRe.FindStringSubmatch(t); m != nil {
				labels.add(m[1], "")
				labels.add(m[3], "")
				d.Edges = append(d.Edges, diagramEdge{From: labels.name(m[1]), To: labels.name(m[3]), Label: cleanDiagramText(m[4])})
				continue
			}
			if strings.HasPrefix(strings.ToLower(t), "note ") {
				if i := strings.Index(t, ":"); i >= 0 {
					d.Labels = append(d.Labels, cleanDiagramText(t[i+1:]))
				}
			}
		}
	case strings.HasPrefix(kw, "statediagram"):
		d.Kind = "state"
		for _, l := range lines[body:] {
			if m := mermaidStateRe.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
				d.Edges = append(d.Edges, diagramEdge{From: mermaidState(m[1]), To: mermaidState(m[2]), Label: cleanDiagramText(m[4])})
			}
		}
	case kw == "classdiagram" || kw == "erdiagram" || strings.HasPrefix(kw, "c4"):
		d.Kind = map[bool]string{true: "class", false: "entity-relationship"}[kw == "classdiagram"]
		if strings.HasPrefix(kw, "c4") {
			d.Kind = "c4"
		}
		for _, l := range lines[body:] {
			t := strings.TrimSpace(l)
			if m := mermaidRelRe.FindStringSubmatch(t); m != nil {
				d.Edges = append(d.Edges, diagramEdge{From: cleanDiagramText(m[1]), To: cleanDiagramText(m[5]), Label: cleanDiagramText(m[7])})
				continue
			}
			if strings.HasPrefix(kw, "c4") {
				mermaidC4Line(t, labels, &d)
			}
		}
	default:
		// gantt, pie, journey, mindmap, timeline, quadrant…: keep their text.
		d.Kind = kw
		for _, l := range lines[body:] {
			t := cleanDiagramText(strings.TrimSpace(l))
			if t != "" && !strings.HasPrefix(t, "%%") {
				d.Labels = append(d.Labels, t)
			}
		}
	}
	d.Nodes = labels.nodes()
	return d
}

func mermaidState(s string) string {
	if s == "[*]" {
		return "start/end"
	}
	return s
}

// mermaidFlowLine reads one flowchart statement: node definitions and edge
// chains ("A --> B --> C", "A & B --> C").
func mermaidFlowLine(line string, labels *labelSet, d *diagramSummary) {
	t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ";"))
	low := strings.ToLower(t)
	if t == "" || strings.HasPrefix(t, "%%") || strings.HasPrefix(low, "classdef") || strings.HasPrefix(low, "class ") ||
		strings.HasPrefix(low, "style ") || strings.HasPrefix(low, "linkstyle") || strings.HasPrefix(low, "click ") || low == "end" || strings.HasPrefix(low, "direction") {
		return
	}
	if strings.HasPrefix(low, "subgraph") {
		rest := strings.TrimSpace(t[len("subgraph"):])
		if m := mermaidNodeRe.FindStringSubmatch(rest); m != nil {
			d.Labels = append(d.Labels, "group: "+cleanDiagramText(m[4]))
		} else if rest != "" {
			d.Labels = append(d.Labels, "group: "+cleanDiagramText(rest))
		}
		return
	}
	// Register shaped nodes, then refer to each by its id.
	for _, m := range mermaidNodeRe.FindAllStringSubmatch(t, -1) {
		labels.add(m[1], m[4])
	}
	stmt := mermaidNodeRe.ReplaceAllString(t, "$1")
	stmt = mermaidInlineLabelRe.ReplaceAllString(" "+stmt+" ", " -->|$2| ")
	locs := mermaidArrowRe.FindAllStringSubmatchIndex(stmt, -1)
	if len(locs) == 0 {
		for _, id := range strings.Split(stmt, "&") {
			if id = strings.TrimSpace(id); id != "" && !strings.ContainsAny(id, " :") {
				labels.add(id, "")
			}
		}
		return
	}
	var groups [][]string
	var edgeLabels []string
	prev := 0
	for _, loc := range locs {
		groups = append(groups, splitMermaidIDs(stmt[prev:loc[0]]))
		label := ""
		if loc[6] >= 0 {
			label = stmt[loc[6]:loc[7]]
		}
		edgeLabels = append(edgeLabels, cleanDiagramText(label))
		prev = loc[1]
	}
	groups = append(groups, splitMermaidIDs(stmt[prev:]))
	for i := 0; i+1 < len(groups); i++ {
		for _, from := range groups[i] {
			for _, to := range groups[i+1] {
				labels.add(from, "")
				labels.add(to, "")
				d.Edges = append(d.Edges, diagramEdge{From: labels.name(from), To: labels.name(to), Label: edgeLabels[i]})
			}
		}
	}
}

func splitMermaidIDs(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "&") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

var mermaidC4Re = regexp.MustCompile(`^(Person|System|Container|Component|ContainerDb|SystemDb|Rel|BiRel|Rel_[A-Za-z]+)(_Ext)?\s*\((.*)\)\s*$`)

// mermaidC4Line reads C4 elements: Person(id, "Label", …), Rel(a, b, "label").
func mermaidC4Line(t string, labels *labelSet, d *diagramSummary) {
	m := mermaidC4Re.FindStringSubmatch(t)
	if m == nil {
		return
	}
	args := splitDiagramArgs(m[3])
	if strings.HasPrefix(m[1], "Rel") || strings.HasPrefix(m[1], "BiRel") {
		if len(args) >= 2 {
			label := ""
			if len(args) >= 3 {
				label = args[2]
			}
			d.Edges = append(d.Edges, diagramEdge{From: labels.name(args[0]), To: labels.name(args[1]), Label: cleanDiagramText(label)})
		}
		return
	}
	if len(args) >= 2 {
		labels.add(args[0], args[1])
	}
}

// splitDiagramArgs splits a comma list, honoring double quotes.
func splitDiagramArgs(s string) []string {
	var out []string
	var b strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
			continue
		}
		b.WriteRune(r)
	}
	if strings.TrimSpace(b.String()) != "" {
		out = append(out, strings.TrimSpace(b.String()))
	}
	for i := range out {
		out[i] = cleanDiagramText(out[i])
	}
	return out
}

// --------------------------------------------------------------- plantuml

var (
	pumlDeclRe  = regexp.MustCompile(`^\s*(actor|participant|boundary|control|entity|database|collections|queue|component|node|cloud|frame|package|rectangle|interface|usecase|class|state|folder|storage|artifact|agent)\s+("([^"]+)"|\[([^\]]+)\]|\(([^)]+)\)|(\S+))(\s+as\s+(\S+))?`)
	pumlArrowRe = regexp.MustCompile(`^\s*(.+?)\s+(<?[-.=]+(\[[^\]]*\])?[-.=]*>?>?|<<?[-.=]+>?|[-.]+\|>|<\|[-.]+)\s+(.+?)\s*(:\s*(.*))?$`)
)

func summarizePlantUML(src string) diagramSummary {
	d := diagramSummary{Lang: "plantuml"}
	labels := newLabelSet()
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		low := strings.ToLower(t)
		if t == "" || strings.HasPrefix(t, "'") || strings.HasPrefix(low, "@start") || strings.HasPrefix(low, "@end") ||
			strings.HasPrefix(low, "skinparam") || strings.HasPrefix(low, "!") || t == "}" {
			continue
		}
		if strings.HasPrefix(low, "title ") {
			d.Labels = append(d.Labels, "title: "+cleanDiagramText(t[6:]))
			continue
		}
		if m := pumlDeclRe.FindStringSubmatch(t); m != nil && !strings.Contains(t, "->") && !strings.Contains(t, "--") {
			if d.Kind == "" {
				d.Kind = pumlKind(m[1])
			}
			name := firstNonEmptyStr(m[3], m[4], m[5], m[6])
			id := name
			if m[8] != "" {
				id = m[8]
			}
			labels.add(id, name)
			continue
		}
		if m := pumlArrowRe.FindStringSubmatch(t); m != nil && strings.ContainsAny(m[2], "->.=") && !strings.HasPrefix(low, "note") {
			from, to := pumlRef(m[1]), pumlRef(m[4])
			if strings.HasPrefix(m[2], "<") && !strings.HasSuffix(m[2], ">") {
				from, to = to, from
			}
			labels.add(from, "")
			labels.add(to, "")
			d.Edges = append(d.Edges, diagramEdge{From: labels.name(from), To: labels.name(to), Label: cleanDiagramText(m[6])})
			continue
		}
		if strings.HasPrefix(low, "note") {
			if i := strings.Index(t, ":"); i >= 0 {
				d.Labels = append(d.Labels, cleanDiagramText(t[i+1:]))
			}
		}
	}
	if d.Kind == "" && len(d.Edges) > 0 {
		d.Kind = "diagram"
	}
	d.Nodes = labels.nodes()
	return d
}

func pumlKind(decl string) string {
	switch decl {
	case "actor", "participant", "boundary", "control", "entity", "collections", "queue":
		return "sequence"
	case "class", "interface":
		return "class"
	case "usecase":
		return "use case"
	case "state":
		return "state"
	}
	return "component"
}

// pumlRef normalizes an endpoint: "[Comp]", "(UC)", ":Actor:", "\"Long\"".
func pumlRef(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `[]():"`)
	return strings.TrimSpace(s)
}

// -------------------------------------------------------------------- dot

var (
	dotEdgeRe  = regexp.MustCompile(`^\s*(.+?)\s*(\[(.*)\])?\s*;?\s*$`)
	dotLabelRe = regexp.MustCompile(`(?:^|[,\s\[])(label|xlabel)\s*=\s*("((?:[^"\\]|\\.)*)"|<(.*)>|([^,\]\s]+))`)
	dotNodeRe  = regexp.MustCompile(`^\s*("[^"]+"|[A-Za-z0-9_.]+)\s*\[(.*)\]\s*;?\s*$`)
)

func summarizeDot(src string) diagramSummary {
	d := diagramSummary{Lang: "dot", Kind: "graph"}
	labels := newLabelSet()
	for _, raw := range splitDotStatements(src) {
		t := strings.TrimSpace(raw)
		low := strings.ToLower(t)
		if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(low, "digraph") || strings.HasPrefix(low, "strict") {
			d.Kind = "directed graph"
			continue
		}
		if strings.HasPrefix(low, "graph ") || strings.HasPrefix(low, "graph{") || strings.HasPrefix(low, "node ") ||
			strings.HasPrefix(low, "node[") || strings.HasPrefix(low, "edge ") || strings.HasPrefix(low, "edge[") ||
			strings.HasPrefix(low, "rankdir") || t == "}" || t == "{" {
			continue
		}
		if strings.HasPrefix(low, "subgraph") {
			continue
		}
		if strings.HasPrefix(low, "label") {
			if m := dotLabelRe.FindStringSubmatch(" " + t); m != nil {
				d.Labels = append(d.Labels, "group: "+cleanDiagramText(firstNonEmptyStr(m[3], m[4], m[5])))
			}
			continue
		}
		if strings.Contains(t, "->") || strings.Contains(t, "--") {
			m := dotEdgeRe.FindStringSubmatch(t)
			if m == nil {
				continue
			}
			label := ""
			if lm := dotLabelRe.FindStringSubmatch(" " + m[3]); lm != nil {
				label = cleanDiagramText(firstNonEmptyStr(lm[3], lm[4], lm[5]))
			}
			parts := regexp.MustCompile(`\s*(->|--)\s*`).Split(m[1], -1)
			for i := 0; i+1 < len(parts); i++ {
				from, to := strings.Trim(strings.TrimSpace(parts[i]), `"`), strings.Trim(strings.TrimSpace(parts[i+1]), `"`)
				labels.add(from, "")
				labels.add(to, "")
				d.Edges = append(d.Edges, diagramEdge{From: labels.name(from), To: labels.name(to), Label: label})
			}
			continue
		}
		if m := dotNodeRe.FindStringSubmatch(t); m != nil {
			id := strings.Trim(m[1], `"`)
			if lm := dotLabelRe.FindStringSubmatch(" " + m[2]); lm != nil {
				labels.add(id, firstNonEmptyStr(lm[3], lm[4], lm[5]))
			} else {
				labels.add(id, "")
			}
		}
	}
	// Edges were named before their node statements may have labeled them.
	for i := range d.Edges {
		d.Edges[i].From, d.Edges[i].To = labels.name(d.Edges[i].From), labels.name(d.Edges[i].To)
	}
	d.Nodes = labels.nodes()
	return d
}

// splitDotStatements splits DOT source into statements (newlines and ';'
// outside quotes and brackets).
func splitDotStatements(src string) []string {
	var out []string
	var b strings.Builder
	quoted, depth := false, 0
	for _, r := range src {
		switch {
		case r == '"':
			quoted = !quoted
		case !quoted && r == '[':
			depth++
		case !quoted && r == ']':
			depth--
		case !quoted && depth == 0 && (r == ';' || r == '\n' || r == '{' || r == '}'):
			out = append(out, b.String())
			b.Reset()
			if r == '{' || r == '}' {
				out = append(out, string(r))
			}
			continue
		}
		b.WriteRune(r)
	}
	return append(out, b.String())
}

// --------------------------------------------------------------------- d2

var d2EdgeRe = regexp.MustCompile(`^\s*(.+?)\s*(<->|->|<-|--)\s*(.+?)\s*(:\s*(.*?))?\s*(\{.*)?$`)

func summarizeD2(src string) diagramSummary {
	d := diagramSummary{Lang: "d2"}
	labels := newLabelSet()
	for _, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || t == "}" {
			continue
		}
		if m := d2EdgeRe.FindStringSubmatch(t); m != nil && !strings.Contains(m[1], ":") && !strings.Contains(m[1], "{") {
			from, to := strings.Trim(m[1], `"`), strings.Trim(m[3], `"`)
			if m[2] == "<-" {
				from, to = to, from
			}
			labels.add(from, "")
			labels.add(to, "")
			d.Edges = append(d.Edges, diagramEdge{From: labels.name(from), To: labels.name(to), Label: cleanDiagramText(m[5])})
			continue
		}
		if i := strings.Index(t, ":"); i > 0 {
			key := strings.TrimSpace(t[:i])
			val := t[i+1:]
			if j := strings.Index(val, "{"); j >= 0 {
				val = val[:j] // a style block: {shape: cylinder}
			}
			val = strings.TrimSpace(val)
			if val != "" && !strings.Contains(key, ".") && !d2Keyword(key) {
				labels.add(key, val)
			}
		}
	}
	d.Nodes = labels.nodes()
	return d
}

func d2Keyword(k string) bool {
	switch strings.ToLower(k) {
	case "shape", "style", "direction", "label", "icon", "width", "height", "near", "link", "tooltip", "class", "classes", "vars":
		return true
	}
	return false
}

// ----------------------------------------------------------------- drawio

// summarizeDrawio reads a draw.io / diagrams.net file: every cell's label,
// and every edge between labeled cells. Compressed diagrams (base64 of
// raw-deflated, URL-encoded XML, the format older versions write) are
// inflated first.
func summarizeDrawio(src string) diagramSummary {
	d := diagramSummary{Lang: "drawio", Kind: "diagram"}
	for _, model := range drawioModels(src) {
		drawioModel(model, &d)
	}
	return d
}

// drawioModels returns the <mxGraphModel> documents in a draw.io file.
func drawioModels(src string) []string {
	if strings.Contains(src, "<mxGraphModel") {
		return []string{src}
	}
	var out []string
	dec := xml.NewDecoder(strings.NewReader(src))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "diagram" {
			continue
		}
		var inner struct {
			Text string `xml:",chardata"`
		}
		if dec.DecodeElement(&inner, &se) != nil {
			continue
		}
		if x := inflateDrawio(strings.TrimSpace(inner.Text)); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func inflateDrawio(s string) string {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	r := flate.NewReader(bytes.NewReader(raw))
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return ""
	}
	if dec, err := url.QueryUnescape(string(data)); err == nil {
		return dec
	}
	return string(data)
}

func drawioModel(model string, d *diagramSummary) {
	type cell struct {
		ID     string `xml:"id,attr"`
		Value  string `xml:"value,attr"`
		Label  string `xml:"label,attr"`
		Edge   string `xml:"edge,attr"`
		Source string `xml:"source,attr"`
		Target string `xml:"target,attr"`
		Parent string `xml:"parent,attr"`
	}
	var cells []cell
	dec := xml.NewDecoder(strings.NewReader(model))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "mxCell":
			var c cell
			if dec.DecodeElement(&c, &se) == nil {
				cells = append(cells, c)
			}
		case "object", "UserObject":
			// A labeled wrapper: <object label="…" id="…"><mxCell …/></object>.
			var c cell
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "id":
					c.ID = a.Value
				case "label":
					c.Value = a.Value
				}
			}
			cells = append(cells, c)
		}
	}
	labels := newLabelSet()
	for _, c := range cells {
		if c.Edge != "1" && c.ID != "" {
			labels.add(c.ID, firstNonEmptyStr(c.Value, c.Label))
		}
	}
	// Edge labels may live on child cells parented to the edge.
	edgeLabel := map[string]string{}
	for _, c := range cells {
		if c.Edge != "1" && c.Parent != "" && c.Value != "" {
			edgeLabel[c.Parent] = cleanDiagramText(c.Value)
		}
	}
	for _, c := range cells {
		if c.Edge != "1" || c.Source == "" || c.Target == "" {
			continue
		}
		label := cleanDiagramText(c.Value)
		if label == "" {
			label = edgeLabel[c.ID]
		}
		d.Edges = append(d.Edges, diagramEdge{From: labels.name(c.Source), To: labels.name(c.Target), Label: label})
	}
	for _, id := range labels.order {
		if v := labels.label[id]; v != "" && edgeLabel[id] == "" {
			d.Nodes = append(d.Nodes, v)
		}
	}
}

// -------------------------------------------------------------------- svg

// summarizeSVG keeps the text an SVG carries: <title>, <desc>, <text> and
// <tspan> runs, aria-label attributes. Drawing paths are no knowledge.
func summarizeSVG(src string) diagramSummary {
	d := diagramSummary{Lang: "svg", Kind: "image"}
	if strings.Contains(src, "mxGraphModel") || strings.Contains(src, "&lt;mxfile") || strings.Contains(src, "content=\"&lt;mxfile") {
		// A draw.io SVG export embeds its diagram in the content attribute.
		if m := regexp.MustCompile(`content="([^"]*)"`).FindStringSubmatch(src); m != nil {
			un := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&amp;", "&", "&#xa;", "\n").Replace(m[1])
			if dd := summarizeDrawio(un); !dd.empty() {
				return dd
			}
		}
	}
	dec := xml.NewDecoder(strings.NewReader(src))
	dec.Strict = false
	var stack []string
	seen := map[string]bool{}
	add := func(s string) {
		s = cleanDiagramText(s)
		if s != "" && !seen[s] {
			seen[s] = true
			d.Labels = append(d.Labels, s)
		}
	}
	var run strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			for _, a := range t.Attr {
				if a.Name.Local == "aria-label" {
					add(a.Value)
				}
			}
			if t.Name.Local == "text" {
				run.Reset()
			}
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			if t.Name.Local == "text" {
				add(run.String())
				run.Reset()
			}
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			switch stack[len(stack)-1] {
			case "title", "desc":
				add(string(t))
			case "text", "tspan", "textPath":
				run.WriteString(" " + string(t))
			}
		}
	}
	return d
}
