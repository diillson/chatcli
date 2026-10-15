/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// htmlVisuals rewrites a crawled page before its text is extracted, so its
// images and diagrams survive as text: <img> becomes an
// "[image: alt — url]" marker (the cleaner keeps only text nodes and would
// drop it), inline <svg> becomes its text, and a mermaid/plantuml/dot
// source block (<pre class="mermaid">, <code class="language-dot">, …)
// gains a summary of its flows. Image URLs are absolute.
func htmlVisuals(htmlContent, pageURL string) (string, []docsFlattenAsset) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return htmlContent, nil
	}
	base, _ := url.Parse(pageURL)
	notes := &visualNotes{}
	diagrams := 0

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type != html.ElementNode {
				walk(c)
				c = next
				continue
			}
			switch c.Data {
			case "img":
				src := firstNonEmptyStr(getAttr(c, "src"), getAttr(c, "data-src"))
				abs := absoluteURL(base, src)
				marker := imageMarker(getAttr(c, "alt"), getAttr(c, "title"), abs, "", notes)
				replaceWithText(n, c, marker)
			case "svg":
				var buf bytes.Buffer
				_ = html.Render(&buf, c)
				sum := summarizeSVG(buf.String())
				text := ""
				if len(sum.Labels) > 0 {
					text = "[image (inline svg): " + strings.Join(sum.Labels, "; ") + "]"
				}
				replaceWithText(n, c, text)
			default:
				if lang := htmlDiagramLang(c); lang != "" {
					src := textContent(c)
					if sum := summarizeDiagram(lang, src); !sum.empty() {
						diagrams++
						rendered := sum.render()
						after := &html.Node{Type: html.TextNode, Data: "\n" + rendered + "\n"}
						n.InsertBefore(after, next)
						notes.add(docsFlattenAsset{Path: pageURL, Kind: chunkKindDiagram, Alt: fmt.Sprintf("%s #%d", lang, diagrams), marker: firstLines(rendered, 2)})
					}
					c = next
					continue
				}
				walk(c)
			}
			c = next
		}
	}
	walk(doc)
	var out bytes.Buffer
	if err := html.Render(&out, doc); err != nil {
		return htmlContent, nil
	}
	return out.String(), notes.assets
}

// htmlDiagramLang reports the diagram language of a source element, by the
// class names docs generators use (mermaid, language-mermaid, lang-dot…).
func htmlDiagramLang(n *html.Node) string {
	if n.Data != "pre" && n.Data != "code" && n.Data != "div" {
		return ""
	}
	for _, cls := range strings.Fields(getAttr(n, "class")) {
		cls = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(cls), "language-"), "lang-")
		if l := diagramLang(cls); l != "" && l != "svg" && l != "drawio" {
			return l
		}
	}
	return ""
}

func replaceWithText(parent, child *html.Node, text string) {
	if text != "" {
		parent.InsertBefore(&html.Node{Type: html.TextNode, Data: " " + text + " "}, child)
	}
	parent.RemoveChild(child)
}

// absoluteURL resolves src against the page; "" for data: and empty srcs.
func absoluteURL(base *url.URL, src string) string {
	src = strings.TrimSpace(src)
	if src == "" || strings.HasPrefix(src, "data:") {
		return ""
	}
	ref, err := url.Parse(src)
	if err != nil || base == nil {
		return src
	}
	return base.ResolveReference(ref).String()
}

// docsFlattenImageFetcher downloads crawled images for images: "describe",
// through the shared web client (proxy, TLS, SSRF checks), same host as
// the seed only unless the crawl allows other hosts.
func docsFlattenImageFetcher(ctx context.Context, cfg docsFlattenArgs) func(string) ([]byte, error) {
	seedHost := hostOf(cfg.URL)
	return func(raw string) ([]byte, error) {
		safe, err := validateWebTarget(raw)
		if err != nil {
			return nil, err
		}
		if cfg.SameHost && hostOf(safe) != seedHost {
			return nil, fmt.Errorf("image on another host")
		}
		resp, err := webGet(ctx, safe, map[string]string{"Accept": "image/*"})
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, maxDescribeBytes))
	}
}
