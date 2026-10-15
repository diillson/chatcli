/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Images and diagrams in @docs-flatten: nothing is dropped silently.
 *
 * Before: image files were never collected, ![alt](src) passed through as
 * raw syntax, MDX <Image alt caption/> components were stripped with their
 * attributes, inline SVG was deleted, and diagram source was kept but never
 * explained. Now every image reference becomes an explicit marker carrying
 * its alt text, title or caption and its path, each diagram (fenced or in
 * its own file) gains a plain-text summary of its flows, and each chunk
 * lists the images and diagrams it carries in "assets". With images:
 * "describe", the referenced images are also described by a vision model
 * (docsflatten_describe.go).
 */
package plugins

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// docsFlattenAsset is an image or diagram a chunk carries.
type docsFlattenAsset struct {
	// Path is the image path relative to the corpus root (or its absolute
	// URL); for a diagram, the file holding it.
	Path string `json:"path" yaml:"path"`
	Kind string `json:"kind" yaml:"kind"` // image | diagram
	Alt  string `json:"alt,omitempty" yaml:"alt,omitempty"`
	// Description is the vision model's text for an image (images:
	// "describe"); empty otherwise.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// marker is the text whose presence in a chunk means the chunk carries
	// this asset (the image path, the diagram's summary). Not serialized.
	marker string
}

// Chunk kinds. Text chunks leave Kind empty, so a corpus without images or
// diagrams is byte-identical to what earlier versions produced.
const (
	chunkKindDiagram = "diagram"
	chunkKindImage   = "image"
)

var (
	// ![alt](src "title") — the title is optional; src may be <wrapped>.
	mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(\s*(?:<([^<>\n]+)>|([^)\s]+))(?:\s+["']([^"']*)["'])?\s*\)`)
	// Obsidian / wiki embeds: ![[diagram.png]], ![[img/a.png|alt or width]].
	wikiImageRe = regexp.MustCompile(`!\[\[([^\]|#\n]+)(?:#[^\]|\n]*)?(?:\|([^\]\n]*))?\]\]`)
	// ![alt][ref] — reference-style; the definition is resolved per file.
	mdImageRefRe = regexp.MustCompile(`!\[([^\]]*)\]\[([^\]]*)\]`)
	mdRefDefRe   = regexp.MustCompile(`(?m)^\s*\[([^\]]+)\]:\s*(?:<([^<>\n]+)>|(\S+))(?:\s+["'(]([^"')]*)["')])?\s*$`)
	// <img …> and capitalized MDX components that carry an image or caption
	// (<Image src alt/>, <Frame caption>, <Figure>, <ThemedImage sources>).
	htmlImgRe    = regexp.MustCompile(`(?is)<img\b([^>]*)>`)
	mdxMediaRe   = regexp.MustCompile(`(?s)<([A-Z][A-Za-z0-9.]*)\b([^<>]*?(?:\bsrc|\balt|\bcaption|\bimg|\bimage)\s*=[^<>]*?)/?>`)
	htmlAttrRe   = regexp.MustCompile(`(?is)\b([a-zA-Z:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|\{\s*["'\x60]([^"'\x60]*)["'\x60]\s*\}|\{([^{}]*)\}|([^\s"'>]+))`)
	inlineSVGRe  = regexp.MustCompile(`(?is)<svg\b.*?</svg>`)
	figcaptionRe = regexp.MustCompile(`(?is)<figcaption[^>]*>(.*?)</figcaption>`)
)

// visualNotes collects what a file's images and diagrams contribute.
type visualNotes struct {
	assets []docsFlattenAsset
	seen   map[string]bool
}

func (v *visualNotes) add(a docsFlattenAsset) {
	if v.seen == nil {
		v.seen = map[string]bool{}
	}
	key := a.Kind + "\x00" + a.Path + "\x00" + a.Alt
	if v.seen[key] {
		return
	}
	v.seen[key] = true
	v.assets = append(v.assets, a)
}

// annotateVisuals rewrites a Markdown/MDX document so its images and
// diagrams survive as knowledge: outside code fences, image syntax becomes
// "[image: alt — path]" markers and inline SVG becomes its text; after each
// diagram fence a summary of the diagram is added. It returns the text and
// the assets found. relPath locates relative image paths.
func annotateVisuals(content, relPath string) (string, []docsFlattenAsset) {
	notes := &visualNotes{}
	refs := map[string][2]string{}
	for _, m := range mdRefDefRe.FindAllStringSubmatch(content, -1) {
		refs[strings.ToLower(m[1])] = [2]string{m[2] + m[3], m[4]}
	}

	var out strings.Builder
	segments := splitFenceSegments(content)
	diagrams := 0
	for _, seg := range segments {
		if !seg.fence {
			out.WriteString(annotateProse(seg.text, relPath, refs, notes))
			continue
		}
		out.WriteString(seg.text)
		lang := diagramLang(seg.info)
		if lang == "" || !seg.closed {
			continue
		}
		sum := summarizeDiagram(lang, seg.body)
		if sum.empty() {
			continue
		}
		diagrams++
		rendered := sum.render()
		out.WriteString("\n" + rendered + "\n")
		notes.add(docsFlattenAsset{Path: relPath, Kind: chunkKindDiagram, Alt: fmt.Sprintf("%s #%d", lang, diagrams), marker: firstLines(rendered, 2)})
	}
	return out.String(), notes.assets
}

// fenceSegment is a run of prose or one fenced code block (fences included).
type fenceSegment struct {
	text   string
	fence  bool
	closed bool
	info   string // the fence's info string (language)
	body   string // the code between the fences
}

// splitFenceSegments splits Markdown into prose runs and ``` / ~~~ fenced
// blocks, honoring the opening fence's character and length.
func splitFenceSegments(content string) []fenceSegment {
	lines := strings.SplitAfter(content, "\n")
	var segs []fenceSegment
	var prose, block, body strings.Builder
	fenceMark, info := "", ""
	flushProse := func() {
		if prose.Len() > 0 {
			segs = append(segs, fenceSegment{text: prose.String()})
			prose.Reset()
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if fenceMark == "" {
			if mark := fenceOpener(trimmed); mark != "" {
				flushProse()
				fenceMark, info = mark, strings.TrimSpace(strings.TrimLeft(trimmed, mark[:1]))
				block.WriteString(line)
				continue
			}
			prose.WriteString(line)
			continue
		}
		block.WriteString(line)
		if strings.HasPrefix(trimmed, fenceMark) && strings.Trim(trimmed, fenceMark[:1]) == "" {
			segs = append(segs, fenceSegment{text: block.String(), fence: true, closed: true, info: info, body: body.String()})
			block.Reset()
			body.Reset()
			fenceMark = ""
			continue
		}
		body.WriteString(line)
	}
	if fenceMark != "" {
		segs = append(segs, fenceSegment{text: block.String(), fence: true, info: info, body: body.String()})
	}
	flushProse()
	return segs
}

// fenceOpener returns the fence run ("```", "~~~~") opening a code block.
func fenceOpener(trimmed string) string {
	for _, c := range []string{"`", "~"} {
		n := len(trimmed) - len(strings.TrimLeft(trimmed, c))
		if n >= 3 {
			return strings.Repeat(c, n)
		}
	}
	return ""
}

// annotateProse rewrites the image syntax of a prose run.
func annotateProse(text, relPath string, refs map[string][2]string, notes *visualNotes) string {
	text = inlineSVGRe.ReplaceAllStringFunc(text, func(svg string) string {
		sum := summarizeSVG(svg)
		if len(sum.Labels) == 0 && len(sum.Edges) == 0 {
			return "" // a drawing with no text says nothing retrievable
		}
		return "[image (inline svg): " + strings.Join(append(sum.Labels, edgeTexts(sum.Edges)...), "; ") + "]"
	})
	text = figcaptionRe.ReplaceAllStringFunc(text, func(f string) string {
		m := figcaptionRe.FindStringSubmatch(f)
		return "[caption: " + cleanDiagramText(m[1]) + "]"
	})
	text = mdImageRe.ReplaceAllStringFunc(text, func(s string) string {
		m := mdImageRe.FindStringSubmatch(s)
		return imageMarker(m[1], m[4], m[2]+m[3], relPath, notes)
	})
	text = wikiImageRe.ReplaceAllStringFunc(text, func(s string) string {
		m := wikiImageRe.FindStringSubmatch(s)
		target := strings.TrimSpace(m[1])
		alt := strings.TrimSpace(m[2])
		if !describableExts[strings.ToLower(path.Ext(target))] && strings.ToLower(path.Ext(target)) != ".svg" {
			return s // an embedded note or PDF, not an image
		}
		if alt == "" || isDimension(alt) {
			alt = strings.TrimSuffix(path.Base(target), path.Ext(target))
		}
		return imageMarker(alt, "", target, relPath, notes)
	})
	text = mdImageRefRe.ReplaceAllStringFunc(text, func(s string) string {
		m := mdImageRefRe.FindStringSubmatch(s)
		key := strings.ToLower(m[2])
		if key == "" {
			key = strings.ToLower(m[1])
		}
		def, ok := refs[key]
		if !ok {
			return s
		}
		return imageMarker(m[1], def[1], def[0], relPath, notes)
	})
	text = htmlImgRe.ReplaceAllStringFunc(text, func(s string) string {
		attrs := htmlAttrs(htmlImgRe.FindStringSubmatch(s)[1])
		return imageMarker(attrs["alt"], attrs["title"], attrs["src"], relPath, notes)
	})
	text = mdxMediaRe.ReplaceAllStringFunc(text, func(s string) string {
		m := mdxMediaRe.FindStringSubmatch(s)
		attrs := htmlAttrs(m[2])
		src := firstNonEmptyStr(attrs["src"], attrs["img"], attrs["image"])
		alt := attrs["alt"]
		caption := firstNonEmptyStr(attrs["caption"], attrs["title"])
		if src == "" && alt == "" && caption == "" {
			return s
		}
		if src == "" {
			// <Frame caption="…">: keep the caption; sanitizeMDX drops the tag.
			return "[caption: " + cleanDiagramText(firstNonEmptyStr(caption, alt)) + "]"
		}
		return imageMarker(alt, caption, src, relPath, notes)
	})
	return text
}

func edgeTexts(edges []diagramEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.From+" → "+e.To)
	}
	return out
}

// htmlAttrs reads tag attributes (HTML quotes or JSX {"…"} expressions).
func htmlAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range htmlAttrRe.FindAllStringSubmatch(s, -1) {
		out[strings.ToLower(m[1])] = strings.TrimSpace(firstNonEmptyStr(m[2], m[3], m[4], m[5], m[6]))
	}
	return out
}

// imageMarker renders one image reference and records it as an asset.
func imageMarker(alt, title, src, relPath string, notes *visualNotes) string {
	alt, title = cleanDiagramText(alt), cleanDiagramText(title)
	label := alt
	if title != "" && title != alt {
		if label != "" {
			label += " — "
		}
		label += title
	}
	ref := resolveAssetPath(relPath, src)
	if ref == "" {
		if label == "" {
			return ""
		}
		return "[image: " + label + "]"
	}
	notes.add(docsFlattenAsset{Path: ref, Kind: chunkKindImage, Alt: label, marker: ref})
	if label == "" {
		return "[image: " + ref + "]"
	}
	return "[image: " + label + " — " + ref + "]"
}

// resolveAssetPath turns an image src into a path relative to the corpus
// root (relative srcs resolve against the document's directory; "/x.png"
// is root-relative, as docs sites serve them) or keeps an absolute URL.
// data: URIs carry no path worth keeping.
func resolveAssetPath(relPath, src string) string {
	src = strings.TrimSpace(src)
	switch {
	case src == "", strings.HasPrefix(src, "data:"), strings.HasPrefix(src, "#"):
		return ""
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"), strings.HasPrefix(src, "//"):
		return src
	}
	if i := strings.IndexAny(src, "?#"); i >= 0 {
		src = src[:i]
	}
	// Markdown writes spaces and accents percent-encoded
	// (modelo%20de%20c%C3%A9lulas.png); the file on disk is not.
	if dec, err := url.PathUnescape(src); err == nil {
		src = dec
	}
	if strings.HasPrefix(src, "/") {
		return strings.TrimPrefix(path.Clean(src), "/")
	}
	return strings.TrimPrefix(path.Clean(path.Join(path.Dir(relPath), src)), "/")
}

// chunkAssets keeps the assets whose marker lands in content.
func chunkAssets(content string, assets []docsFlattenAsset) []docsFlattenAsset {
	var out []docsFlattenAsset
	for _, a := range assets {
		if a.marker != "" && strings.Contains(content, a.marker) {
			out = append(out, a)
		}
	}
	return out
}

// firstLines returns the first n lines of s.
func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// diagramFilePieces turns a standalone diagram file (.mmd, .puml, .dot,
// .d2, .drawio, .svg) into one chunk: the summary, plus the source for the
// text languages. ok is false when the file says nothing retrievable (an
// SVG icon with no text, an empty diagram).
func diagramFilePieces(raw, relPath string, maxChars int) ([]flatChunk, bool) {
	lang := diagramFileLang(relPath)
	sum := summarizeDiagram(lang, raw)
	if sum.empty() || (lang == "svg" && len(sum.Edges) == 0 && len(sum.Labels) < 2) {
		// An SVG with a single <title> is an icon, not a diagram.
		return nil, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Diagram file: %s\n\n%s\n", relPath, sum.render())
	switch lang {
	case "mermaid", "plantuml", "dot", "d2":
		fmt.Fprintf(&b, "\n```%s\n%s\n```\n", lang, strings.TrimSpace(raw))
	}
	asset := docsFlattenAsset{Path: relPath, Kind: chunkKindDiagram, Alt: lang}
	parts := chunkMarkdown(b.String(), maxChars)
	pieces := make([]flatChunk, 0, len(parts))
	for _, p := range parts {
		pieces = append(pieces, flatChunk{Content: p, Title: "Diagram: " + path.Base(relPath), Kind: chunkKindDiagram, Assets: []docsFlattenAsset{asset}})
	}
	return pieces, true
}

// chunkSectionFenceAware packs an oversized Markdown section by lines, as
// chunkLines does, but treats each fenced block as one unit so a diagram or
// code sample that fits the budget is never cut in half. Without fences the
// output is exactly chunkLines'.
func chunkSectionFenceAware(sec string, maxChars int) []string {
	if !strings.Contains(sec, "```") && !strings.Contains(sec, "~~~") {
		return chunkLines(sec, maxChars)
	}
	var units []string
	for _, seg := range splitFenceSegments(sec) {
		if seg.fence {
			units = append(units, strings.TrimSuffix(seg.text, "\n"))
			continue
		}
		units = append(units, strings.Split(strings.TrimSuffix(seg.text, "\n"), "\n")...)
	}
	var chunks []string
	var buf strings.Builder
	for _, u := range units {
		if len(u) > maxChars {
			if buf.Len() > 0 {
				chunks = append(chunks, buf.String())
				buf.Reset()
			}
			chunks = append(chunks, chunkLines(u, maxChars)...)
			continue
		}
		if buf.Len() > 0 && buf.Len()+len(u)+1 > maxChars {
			chunks = append(chunks, buf.String())
			buf.Reset()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(u)
	}
	if buf.Len() > 0 {
		chunks = append(chunks, buf.String())
	}
	return chunks
}

// isDimension reports whether a wiki-embed option is a size ("300",
// "300x200") rather than alt text.
func isDimension(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != 'x' {
			return false
		}
	}
	return true
}
