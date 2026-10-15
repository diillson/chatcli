/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestSummarizeDiagram_Languages(t *testing.T) {
	cases := []struct {
		lang, src string
		want      []string
	}{
		{"mermaid", "flowchart LR\n  C[Client] -->|HTTPS| G(API Gateway)\n  G --> A{Auth?}\n  A -- yes --> O[(Orders DB)]\n  G & A --> L[Logs]\n  B>Flag] -.-> C",
			[]string{"mermaid flowchart", "Client → API Gateway: HTTPS", "Auth? → Orders DB: yes", "API Gateway → Logs", "Auth? → Logs", "Flag → Client"}},
		{"mermaid", "graph TD;\nA-->B;\nB==>C;", []string{"A → B", "B → C"}},
		{"mermaid", "sequenceDiagram\n  participant U as User\n  U->>S: POST /login\n  S-->>U: 200 token\n  Note over U,S: always TLS",
			[]string{"mermaid sequence", "User → S: POST /login", "S → User: 200 token", "always TLS"}},
		{"mermaid", "stateDiagram-v2\n  [*] --> Pending\n  Pending --> Paid : approved", []string{"start/end → Pending", "Pending → Paid: approved"}},
		{"mermaid", "erDiagram\n  CUSTOMER ||--o{ ORDER : places", []string{"CUSTOMER → ORDER: places"}},
		{"mermaid", "C4Context\n  Person(u, \"Customer\")\n  System(s, \"Shop\")\n  Rel(u, s, \"buys\")", []string{"Customer → Shop: buys"}},
		{"mermaid", "pie title Traffic\n  \"API\" : 70\n  \"Web\" : 30", []string{"mermaid pie", "API"}},
		{"plantuml", "@startuml\nactor Client\nparticipant \"Payment Service\" as Pay\nClient -> Pay : charge(amount)\nPay --> Client : receipt\n@enduml",
			[]string{"plantuml sequence", "Client → Payment Service: charge(amount)", "Payment Service → Client: receipt"}},
		{"plantuml", "@startuml\n[Web] --> [API] : REST\n[API] ..> [DB]\n@enduml", []string{"Web → API: REST", "API → DB"}},
		{"dot", "digraph G {\n api [label=\"API\"];\n db [label=\"Postgres\"];\n api -> db [label=\"SQL\"];\n api -> cache -> db;\n}",
			[]string{"dot directed graph", "API → Postgres: SQL", "API → cache", "cache → Postgres"}},
		{"d2", "api: API Server\ndb: Database {shape: cylinder}\napi -> db: queries\nuser -> api", []string{"API Server → Database: queries", "user → API Server"}},
		{"drawio", `<mxfile><diagram><mxGraphModel><root><mxCell id="0"/><mxCell id="a" value="Frontend" vertex="1"/><mxCell id="b" value="Backend&lt;br&gt;API" vertex="1"/><mxCell id="e" value="REST" edge="1" source="a" target="b"/></root></mxGraphModel></diagram></mxfile>`,
			[]string{"Frontend → Backend API: REST"}},
		{"svg", `<svg xmlns="http://www.w3.org/2000/svg"><title>Architecture</title><text>Gateway</text><text><tspan>Kafka</tspan> <tspan>queue</tspan></text></svg>`,
			[]string{"Architecture", "Gateway", "Kafka queue"}},
	}
	for _, c := range cases {
		got := summarizeDiagram(c.lang, c.src).render()
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: summary lacks %q:\n%s", c.lang, w, got)
			}
		}
	}
}

// Compressed draw.io files (base64 + raw deflate + URL-encoded XML, as older
// versions save them) are inflated.
func TestSummarizeDrawio_Compressed(t *testing.T) {
	// "<mxGraphModel><root><mxCell id=\"a\" value=\"Queue\" vertex=\"1\"/><mxCell id=\"b\" value=\"Worker\" vertex=\"1\"/><mxCell id=\"e\" edge=\"1\" source=\"a\" target=\"b\"/></root></mxGraphModel>"
	// URL-encoded, raw-deflated and base64-encoded.
	compressed := drawioCompressForTest(t, `<mxGraphModel><root><mxCell id="a" value="Queue" vertex="1"/><mxCell id="b" value="Worker" vertex="1"/><mxCell id="e" edge="1" source="a" target="b"/></root></mxGraphModel>`)
	got := summarizeDiagram("drawio", `<mxfile><diagram id="x" name="Page-1">`+compressed+`</diagram></mxfile>`).render()
	if !strings.Contains(got, "Queue → Worker") {
		t.Fatalf("compressed draw.io not read:\n%s", got)
	}
}

func TestAnnotateVisuals_Markdown(t *testing.T) {
	src := strings.Join([]string{
		"# Payments",
		"",
		`![Payment architecture](img/arch.png "Overview")`,
		"![ref style][diagram]",
		"",
		"[diagram]: ./img/ref.png",
		"",
		`<img src="../shared/flow.jpg" alt="Login flow">`,
		`<Frame caption="Deploy pipeline">`,
		`  <Image src="/img/deploy.png" alt="Deploy" />`,
		`</Frame>`,
		`<svg viewBox="0 0 10 10"><title>Inline chart</title><text>Latency p99</text></svg>`,
		`![inline](data:image/png;base64,AAAA)`,
		"",
		"```mermaid",
		"flowchart LR",
		"  A[Checkout] --> B[Payments]",
		"```",
		"",
		"```bash",
		"# not a heading, not a diagram",
		"echo hi",
		"```",
	}, "\n")
	out, assets := annotateVisuals(src, "docs/pay/index.md")
	for _, want := range []string{
		"[image: Payment architecture — Overview — docs/pay/img/arch.png]",
		"[image: ref style — docs/pay/img/ref.png]",
		"[image: Login flow — docs/shared/flow.jpg]",
		"[caption: Deploy pipeline]",
		"[image: Deploy — img/deploy.png]",
		"[image (inline svg): Inline chart; Latency p99]",
		"Diagram (mermaid flowchart) — flows:\n- Checkout → Payments",
		"# not a heading, not a diagram",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<svg") || strings.Contains(out, "data:image") || strings.Contains(out, "![") {
		t.Errorf("raw image syntax left behind:\n%s", out)
	}
	// The summary follows the closed fence; the fence itself is intact.
	if !strings.Contains(out, "  A[Checkout] --> B[Payments]\n```\n\nDiagram") {
		t.Errorf("diagram fence altered or summary misplaced:\n%s", out)
	}
	var images, diagrams int
	for _, a := range assets {
		switch a.Kind {
		case chunkKindImage:
			images++
		case chunkKindDiagram:
			diagrams++
		}
	}
	// The inline SVG is folded into the text; it is not a file to describe.
	if images != 4 || diagrams != 1 {
		t.Fatalf("assets = %+v, want 4 images and 1 diagram", assets)
	}
}

func TestResolveAssetPath(t *testing.T) {
	cases := map[[2]string]string{
		{"docs/a/b.md", "img/x.png"}:             "docs/a/img/x.png",
		{"docs/a/b.md", "../img/x.png?v=2"}:      "docs/img/x.png",
		{"docs/a/b.md", "/static/x.png"}:         "static/x.png",
		{"docs/a/b.md", "https://cdn.ex/x.png"}:  "https://cdn.ex/x.png",
		{"docs/a/b.md", "data:image/png;base64"}: "",
		// Kept as written; resolveLocalImage refuses to read outside the root.
		{"b.md", "../../etc/passwd.png"}: "../../etc/passwd.png",
	}
	for in, want := range cases {
		if got := resolveAssetPath(in[0], in[1]); got != want {
			t.Errorf("resolveAssetPath(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

// Without fences the oversized-section packer is exactly chunkLines, so
// corpora without diagrams keep their chunks (and their embeddings).
func TestChunkSectionFenceAware(t *testing.T) {
	plain := strings.Repeat("line of prose number x\n", 40)
	if a, b := chunkSectionFenceAware(plain, 200), chunkLines(plain, 200); strings.Join(a, "|") != strings.Join(b, "|") {
		t.Fatal("without fences the packer must match chunkLines")
	}
	fence := "```mermaid\nflowchart LR\n" + strings.Repeat("  A --> B\n", 8) + "```"
	sec := strings.Repeat("prose line\n", 10) + fence + "\n" + strings.Repeat("more prose\n", 10)
	for _, c := range chunkSectionFenceAware(sec, 160) {
		opens := strings.Count(c, "```")
		if opens%2 != 0 {
			t.Fatalf("a chunk cuts the fence in half:\n%s", c)
		}
	}
}

func TestDiagramFilePieces(t *testing.T) {
	pieces, ok := diagramFilePieces("flowchart LR\n A-->B", "arch/flow.mmd", 16000)
	if !ok || len(pieces) != 1 || pieces[0].Kind != chunkKindDiagram || !strings.Contains(pieces[0].Content, "A → B") ||
		!strings.Contains(pieces[0].Content, "```mermaid") {
		t.Fatalf("mermaid file = %+v", pieces)
	}
	if _, ok := diagramFilePieces(`<svg><title>arrow</title><path d="M0"/></svg>`, "icons/arrow.svg", 16000); ok {
		t.Fatal("an icon (one title, no text) must not become a chunk")
	}
	if p, ok := diagramFilePieces(`<svg><title>Network</title><text>VPC</text><text>Subnet A</text></svg>`, "arch/net.svg", 16000); !ok ||
		strings.Contains(p[0].Content, "```") {
		t.Fatalf("an SVG with text is kept, as its text only: %+v", p)
	}
}

// writeDocsFixture builds a small docs tree with diagrams and images.
func writeDocsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"docs/overview.md": "# Overview\n\n![Order architecture](img/arch.png)\n\n```mermaid\nflowchart LR\n  App --> API\n```\n",
		"docs/plain.md":    "# Plain\n\nNo pictures here.\n",
		"docs/infra.drawio": `<mxfile><diagram><mxGraphModel><root><mxCell id="a" value="LB" vertex="1"/><mxCell id="b" value="App" vertex="1"/>` +
			`<mxCell id="e" edge="1" source="a" target="b"/></root></mxGraphModel></diagram></mxfile>`,
		"docs/img/icon.svg": `<svg><title>x</title></svg>`,
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePNG(t, filepath.Join(root, "docs", "img", "arch.png"), 200, 120)
	return root
}

func writePNG(t *testing.T, p string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(w*7919 + h)
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			seed = seed*1664525 + 1013904223 // noise keeps the PNG from compressing to nothing
			img.Set(x, y, color.RGBA{uint8(seed >> 24), uint8(seed >> 16), uint8(x ^ y), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func flattenFixture(t *testing.T, args string) ([]docsFlattenChunk, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "corpus.jsonl")
	res, err := NewBuiltinDocsFlattenPlugin().Execute(context.Background(), []string{strings.Replace(args, "OUT", filepath.ToSlash(out), 1)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var chunks []docsFlattenChunk
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var c docsFlattenChunk
		if err := json.Unmarshal([]byte(l), &c); err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, c)
	}
	return chunks, res
}

func TestDocsFlatten_VisualsEndToEnd(t *testing.T) {
	root := writeDocsFixture(t)
	chunks, summary := flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT"}`)
	bySource := map[string]docsFlattenChunk{}
	for _, c := range chunks {
		bySource[c.Source] = c
	}
	ov := bySource["docs/overview.md"]
	if !strings.Contains(ov.Content, "[image: Order architecture — docs/img/arch.png]") || !strings.Contains(ov.Content, "App → API") {
		t.Fatalf("overview chunk:\n%s", ov.Content)
	}
	if len(ov.Assets) != 2 || ov.Kind != "" {
		t.Fatalf("overview assets/kind = %+v %q", ov.Assets, ov.Kind)
	}
	if d := bySource["docs/infra.drawio"]; d.Kind != chunkKindDiagram || !strings.Contains(d.Content, "LB → App") {
		t.Fatalf("draw.io chunk = %+v", d)
	}
	if _, ok := bySource["docs/img/icon.svg"]; ok {
		t.Fatal("the icon must not become a chunk")
	}
	// A document without visuals carries no new fields: its JSONL line is
	// what earlier versions wrote.
	if p := bySource["docs/plain.md"]; p.Kind != "" || p.Assets != nil {
		t.Fatalf("plain chunk gained fields: %+v", p)
	}
	if !strings.Contains(summary, "visuals: 2 diagram(s) summarized, 1 image reference(s)") {
		t.Fatalf("summary = %q", summary)
	}
}

// fakeDescriber records calls.
type fakeDescriber struct {
	calls atomic.Int32
	fail  bool
}

func (f *fakeDescriber) DescribeImage(_ context.Context, prompt string, img models.ImageContent) (string, string, error) {
	f.calls.Add(1)
	if f.fail {
		return "", "", errors.New("vision down")
	}
	if img.MediaType != "image/png" || !strings.Contains(prompt, "Transcribe ALL visible text") || !strings.Contains(prompt, "alt text: Order architecture") {
		return "", "", errors.New("unexpected request")
	}
	return "Architecture diagram.\n- App → Orders API: HTTPS\nText: Orders API", "fake/vision-1", nil
}

func TestDocsFlatten_DescribeImages(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // private vision cache
	root := writeDocsFixture(t)
	fake := &fakeDescriber{}
	SetDocsImageDescriber(fake)
	t.Cleanup(func() { SetDocsImageDescriber(nil) })

	chunks, summary := flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT","images":"describe"}`)
	var imageChunk, doc docsFlattenChunk
	for _, c := range chunks {
		switch c.Source {
		case "docs/img/arch.png":
			imageChunk = c
		case "docs/overview.md":
			doc = c
		}
	}
	if imageChunk.Kind != chunkKindImage || !strings.Contains(imageChunk.Content, "App → Orders API: HTTPS") ||
		!strings.Contains(imageChunk.Content, "Referenced by: docs/overview.md") {
		t.Fatalf("image chunk = %+v", imageChunk)
	}
	if !strings.Contains(doc.Content, "docs/img/arch.png | shows: Architecture diagram.") || doc.Assets[0].Description == "" {
		t.Fatalf("referencing chunk not linked to the description:\n%s", doc.Content)
	}
	if !strings.Contains(summary, "1 described (0 from cache) by fake/vision-1") {
		t.Fatalf("summary = %q", summary)
	}
	// Second run: from cache, no call.
	_, summary = flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT","images":"describe"}`)
	if fake.calls.Load() != 1 || !strings.Contains(summary, "(1 from cache)") {
		t.Fatalf("calls = %d, summary = %q", fake.calls.Load(), summary)
	}
}

func TestDocsFlatten_EstimateAndFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := writeDocsFixture(t)
	fake := &fakeDescriber{}
	SetDocsImageDescriber(fake)
	t.Cleanup(func() { SetDocsImageDescriber(nil) })

	_, summary := flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT","images":"estimate"}`)
	if fake.calls.Load() != 0 || !strings.Contains(summary, "1 would be described") {
		t.Fatalf("estimate must not call the model: calls=%d summary=%q", fake.calls.Load(), summary)
	}

	fake.fail = true
	chunks, summary := flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT","images":"describe"}`)
	if !strings.Contains(summary, "description failed") {
		t.Fatalf("a failed description must be reported: %q", summary)
	}
	for _, c := range chunks {
		if c.Kind == chunkKindImage {
			t.Fatal("no image chunk without a description")
		}
	}

	SetDocsImageDescriber(nil)
	_, summary = flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT","images":"describe"}`)
	if !strings.Contains(summary, "no vision pipeline") {
		t.Fatalf("summary = %q", summary)
	}
	if _, err := parseDocsFlattenArgs([]string{`{"root":"x","images":"bogus"}`}); err == nil {
		t.Fatal("an unknown images mode must be rejected")
	}
}

func TestLoadImageJob_Filters(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "static", "img"), 0o750); err != nil {
		t.Fatal(err)
	}
	writePNG(t, filepath.Join(root, "small.png"), 20, 20)
	writePNG(t, filepath.Join(root, "company-logo.png"), 300, 200)
	writePNG(t, filepath.Join(root, "static", "img", "big.png"), 300, 200)
	cases := map[string]string{
		"small.png":        "tiny",
		"img/big.png":      "", // served from static/
		"company-logo.png": "logos/icons",
		"missing.png":      "not found",
		"diagram.svg":      "svg (its text is already in the corpus)",
		"https://x/y.png":  "remote (not downloaded)",
		"../outside.png":   "not found",
	}
	for asset, want := range cases {
		if got := loadImageJob(&docsImageJob{asset: asset}, root, nil); got != want {
			t.Errorf("%s: reason %q, want %q", asset, got, want)
		}
	}
}

// drawioCompressForTest encodes a model the way draw.io compresses it:
// URL-encode, raw deflate, base64.
func drawioCompressForTest(t *testing.T, xmlDoc string) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(url.QueryEscape(xmlDoc)))
	_ = w.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestHTMLVisuals(t *testing.T) {
	page := `<html><body><h1>Guide</h1>
<figure><img src="/img/arch.png" alt="Service map" title="Map"><figcaption>How requests flow</figcaption></figure>
<svg><title>Uptime</title><text>99.9%</text></svg>
<pre class="mermaid">flowchart LR
  Web --> API</pre>
<pre><code class="language-dot">digraph { a -> b }</code></pre>
<p>Text stays.</p></body></html>`
	out, assets := htmlVisuals(page, "https://docs.example.com/guide/")
	text := extractText(out)
	for _, want := range []string{
		"[image: Service map — Map — https://docs.example.com/img/arch.png]",
		"How requests flow",
		"[image (inline svg): Uptime; 99.9%]",
		"Web → API",
		"a → b",
		"Text stays.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("page text lacks %q:\n%s", want, text)
		}
	}
	var images, diagrams int
	for _, a := range assets {
		if a.Kind == chunkKindImage {
			images++
		} else {
			diagrams++
		}
	}
	if images != 1 || diagrams != 2 {
		t.Fatalf("assets = %+v", assets)
	}
}

// The default markers run calls no model but says the images were left
// undescribed and how to describe them.
func TestDocsFlatten_MarkersSaysImagesUndescribed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := writeDocsFixture(t)
	fake := &fakeDescriber{}
	SetDocsImageDescriber(fake)
	t.Cleanup(func() { SetDocsImageDescriber(nil) })

	_, summary := flattenFixture(t, `{"root":"`+filepath.ToSlash(root)+`","format":"jsonl","output":"OUT"}`)
	if fake.calls.Load() != 0 || !strings.Contains(summary, "markers only") || !strings.Contains(summary, "images=describe") {
		t.Fatalf("calls=%d summary=%q", fake.calls.Load(), summary)
	}
}
