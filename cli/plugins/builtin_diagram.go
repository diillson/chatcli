/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinDiagramPlugin — @diagram as a native ReAct tool.
 *
 * Renders architecture / dependency / flow diagrams to PNG, SVG or JPG from
 * Graphviz DOT — deterministically, with crisp and 100% correct text labels.
 * Graphviz is embedded: go-graphviz ships the upstream engine compiled to
 * WebAssembly and runs it through the pure-Go wazero runtime, so it works with
 * NO cgo, NO install and NO network call — the same "embedded, self-contained,
 * keyless-first" approach the project uses for TTS/STT (sherpa-onnx) and voice
 * notes (pion/opus). When a system Graphviz (`dot`) is on PATH the backend
 * defaults to using it (backend=auto): rendering the SAME DOT through fontconfig
 * + the system fonts + cairo yields crisper, better-laid-out output. The
 * embedded engine remains the fallback so the tool never requires an install.
 *
 * Why this exists: LLMs are unreliable at rendering legible text inside raster
 * images. A vision model "guesses" letters; a layout engine does not. By giving
 * the agent a first-class DOT renderer it produces architecture diagrams whose
 * labels come from real source — package names, module edges — instead of being
 * hallucinated pixels, and it does so without the install-graphviz dance.
 *
 * Two subcommands:
 *   render  DOT source (inline or a .dot file) → PNG/SVG/JPG.
 *   gomod   Build the real import graph of a Go module (via `go list`) into a
 *           clustered DOT and render it — a 1:1, code-faithful dependency graph
 *           with no manual package enumeration.
 */
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	// Register PNG/JPEG decoders so imageDimensions can read the output size.
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/goccy/go-graphviz"
)

// diagramDefaultDPI is the raster resolution used when none is given. 150 DPI
// is a good screen/retina default; print-grade callers pass dpi=300.
const diagramDefaultDPI = 150

// diagramMinDPI / diagramMaxDPI bound the raster resolution so a stray value
// can't ask the WASM renderer to allocate a multi-gigapixel canvas.
const (
	diagramMinDPI = 30
	diagramMaxDPI = 600
)

// Rendering backends. The embedded WASM engine is fully self-contained but
// rasterizes with a bundled font and no cairo/pango, so requested fonts like
// Helvetica/Menlo fall back and text-heavy diagrams look plainer. A system
// `dot` (e.g. `brew install graphviz`) renders the SAME DOT with fontconfig +
// the system fonts and the cairo backend, producing crisper, better-laid-out
// output. The backend is selectable so users get the nicer result when they
// have Graphviz installed, while the binary stays self-contained for everyone
// else.
const (
	diagramBackendAuto     = "auto"     // prefer system `dot` if on PATH, else embedded
	diagramBackendSystem   = "system"   // require a system `dot`, error if absent
	diagramBackendEmbedded = "embedded" // always the bundled WASM engine
)

// diagramBackendEnv selects the default rendering backend process-wide. A
// per-call "backend" arg overrides it.
const diagramBackendEnv = "CHATCLI_DIAGRAM_BACKEND"

// configuredDiagramBackend returns the backend requested via the environment
// (lowercased), defaulting to "auto" when unset or invalid.
func configuredDiagramBackend() string {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(diagramBackendEnv))); v {
	case diagramBackendSystem, diagramBackendEmbedded, diagramBackendAuto:
		return v
	default:
		return diagramBackendAuto
	}
}

// systemDotPath returns the path to a system `dot` (Graphviz) binary, or "" if
// none is installed on PATH.
func systemDotPath() string {
	p, err := exec.LookPath("dot")
	if err != nil {
		return ""
	}
	return p
}

// resolveDiagramBackend turns "auto" into a concrete backend: system when a
// `dot` binary is installed, otherwise embedded. system/embedded pass through.
func resolveDiagramBackend(requested string) string {
	switch requested {
	case diagramBackendSystem, diagramBackendEmbedded:
		return requested
	default: // auto (or anything unexpected)
		if systemDotPath() != "" {
			return diagramBackendSystem
		}
		return diagramBackendEmbedded
	}
}

// DiagramBackendStatus is a snapshot of how @diagram will render, surfaced by
// `/config diagram` so the operator can see which engine is actually in play.
type DiagramBackendStatus struct {
	Configured string // auto | system | embedded (from CHATCLI_DIAGRAM_BACKEND)
	Effective  string // system | embedded (auto resolved against PATH)
	DotPath    string // path to system `dot`, "" if not installed
	DotVersion string // `dot -V` banner, "" if unavailable
}

// GetDiagramBackendStatus resolves the current @diagram backend for display.
func GetDiagramBackendStatus(ctx context.Context) DiagramBackendStatus {
	cfg := configuredDiagramBackend()
	st := DiagramBackendStatus{Configured: cfg, Effective: resolveDiagramBackend(cfg)}
	if p := systemDotPath(); p != "" {
		st.DotPath = p
		st.DotVersion = systemDotVersion(ctx, p)
	}
	return st
}

// systemDotVersion returns the `dot -V` banner (Graphviz prints it to stderr),
// or "" when the binary can't be run.
func systemDotVersion(ctx context.Context, dotBin string) string {
	out, err := exec.CommandContext(ctx, dotBin, "-V").CombinedOutput() // #nosec G204 -- dotBin came from exec.LookPath; fixed "-V" arg
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// diagramValidFormats is the set of output formats @diagram emits.
var diagramValidFormats = map[string]graphviz.Format{
	"png": graphviz.PNG,
	"svg": graphviz.SVG,
	"jpg": graphviz.JPG,
}

// diagramValidEngines is the set of Graphviz layout engines exposed by the
// tool, mapped to the go-graphviz Layout constant.
var diagramValidEngines = map[string]graphviz.Layout{
	"dot":       graphviz.DOT,
	"neato":     graphviz.NEATO,
	"fdp":       graphviz.FDP,
	"sfdp":      graphviz.SFDP,
	"circo":     graphviz.CIRCO,
	"twopi":     graphviz.TWOPI,
	"osage":     graphviz.OSAGE,
	"patchwork": graphviz.PATCHWORK,
}

// diagramArgs is the typed view of @diagram's JSON input.
type diagramArgs struct {
	Cmd     string // render | gomod (resolved)
	DOT     string // inline DOT source (render)
	File    string // path to a .dot file (render)
	Root    string // module directory (gomod)
	Format  string // png | svg | jpg
	Engine  string // dot | neato | fdp | sfdp | circo | twopi | osage | patchwork
	DPI     int    // raster resolution (png/jpg)
	Output  string // destination file path (empty => temp file)
	Style   string // dark | light | plain (gomod styling)
	Backend string // auto | system | embedded (rendering engine)

	InternalOnly bool // gomod: only edges between packages of THIS module
	Cluster      bool // gomod: group nodes into subgraph clusters by top dir
	DOTOnly      bool // gomod: return the generated DOT source, do not render
}

// BuiltinDiagramPlugin is the @diagram tool.
type BuiltinDiagramPlugin struct{}

// NewBuiltinDiagramPlugin returns a ready-to-register plugin.
func NewBuiltinDiagramPlugin() *BuiltinDiagramPlugin { return &BuiltinDiagramPlugin{} }

// Name returns "@diagram".
func (*BuiltinDiagramPlugin) Name() string { return "@diagram" }

// Description surfaces the tool in the catalog. This is the primary signal the
// model uses to decide it CAN render diagrams natively — keep it explicit.
func (*BuiltinDiagramPlugin) Description() string {
	return i18n.T("plugins.diagram.description")
}

// IsReadOnly reports false: rendering writes an image file to disk.
func (*BuiltinDiagramPlugin) IsReadOnly(_ []string) bool { return false }

// IsConcurrencySafe reports false: it writes a file (and gomod shells out to
// `go list`), so it must not run inside a parallel read-only batch.
func (*BuiltinDiagramPlugin) IsConcurrencySafe(_ []string) bool { return false }

// DescribeCall surfaces what is being rendered in the spinner.
func (*BuiltinDiagramPlugin) DescribeCall(args []string) string {
	cfg, err := parseDiagramArgs(args)
	if err != nil {
		return i18n.T("plugins.diagram.description")
	}
	if cfg.Cmd == "gomod" {
		return i18n.T("plugins.diagram.describe_gomod", cfg.Root)
	}
	target := cfg.Output
	if target == "" {
		target = strings.ToUpper(cfg.Format)
	}
	return i18n.T("plugins.diagram.describe_render", target)
}

// Usage explains the canonical invocation.
func (*BuiltinDiagramPlugin) Usage() string {
	return `<tool_call name="@diagram" args='{"cmd":"render","dot":"digraph{a->b}","output":"/tmp/g.png"}' />

Subcommands (flat JSON or {"cmd":"...","args":{...}} envelope):

render — DOT source to an image. Use this for architecture/flow diagrams; write
         the DOT yourself (you are good at DOT) and let the engine render it.
  dot      inline Graphviz DOT source (one of dot|file)
  file     path to a .dot file to render (one of dot|file)
  format   png | svg | jpg (default: png)
  engine   dot | neato | fdp | sfdp | circo | twopi | osage | patchwork (default: dot)
  dpi      raster resolution for png/jpg (default: 150; use 300 for print)
  backend  auto | system | embedded (default: auto; CHATCLI_DIAGRAM_BACKEND)
  output   destination file (default: a temp file whose path is returned)

gomod — render the REAL import graph of a Go module (no manual enumeration).
  root          module directory to analyze (default: ".")
  internalOnly  only edges between packages of this module (default: true)
  cluster       group packages into clusters by top-level directory (default: true)
  style         dark | light | plain (default: dark)
  dotOnly       return the generated DOT source instead of an image (default: false)
  format/engine/dpi/output  same as render

Graphviz is embedded (WASM) so it works with no install. When a system Graphviz
('dot') is on PATH it is used automatically (backend=auto) for crisper fonts and
layout; set backend=embedded to force the bundled engine, or CHATCLI_DIAGRAM_BACKEND.`
}

// Version is semver. 1.x: initial builtin.
func (*BuiltinDiagramPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinDiagramPlugin) Path() string { return "" }

// Schema describes the tool for the LLM catalog.
func (*BuiltinDiagramPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "flat JSON preferred",
		"subcommands": []map[string]interface{}{
			{
				"name": "render",
				"description": "Render Graphviz DOT (inline via dot, or a .dot file via file) to a PNG/SVG/JPG image. " +
					"Use this whenever the user asks for an architecture, flow, dependency, ER or any node/edge diagram as an image — " +
					"WRITE the DOT yourself and render it here. Text labels come out crisp and exactly correct (never do this by generating a raster image with @image). " +
					"Graphviz is embedded so no install is needed; if a system Graphviz is on PATH it is used automatically (backend=auto) for nicer fonts/layout. " +
					"SVG is infinitely scalable; PNG with dpi=300 is print-grade.",
				"flags": []map[string]interface{}{
					{"name": "dot", "type": "string", "description": "Inline Graphviz DOT source. Exactly one of dot|file."},
					{"name": "file", "type": "string", "description": "Path to a .dot file to render. Exactly one of dot|file."},
					{"name": "format", "type": "string", "description": "png | svg | jpg. Default: png."},
					{"name": "engine", "type": "string", "description": "Layout engine: dot | neato | fdp | sfdp | circo | twopi | osage | patchwork. Default: dot."},
					{"name": "dpi", "type": "integer", "description": "Raster resolution for png/jpg (30-600). Default: 150. Use 300 for print."},
					{"name": "backend", "type": "string", "description": "Rendering engine: auto | system | embedded. auto (default) prefers a system `dot` if installed (crisper) and falls back to the embedded WASM engine. Overrides CHATCLI_DIAGRAM_BACKEND."},
					{"name": "output", "type": "string", "description": "Destination file path. If omitted, a temp file is written and its path returned."},
				},
				"examples": []string{
					`{"dot":"digraph{rankdir=LR; cli->agent; cli->llm}","output":"/tmp/arch.png"}`,
					`{"file":"./arch.dot","format":"svg","output":"/tmp/arch.svg"}`,
					`{"dot":"digraph{a->b->c}","format":"png","dpi":300}`,
				},
			},
			{
				"name": "gomod",
				"description": "Build the REAL import graph of a Go module via `go list` and render it as a clustered diagram — a 1:1, code-faithful dependency graph with no manual package enumeration. " +
					"Point root at the module directory. Labels are the actual package paths, grouped into clusters by top-level directory. Use dotOnly=true to get the DOT source for further editing.",
				"flags": []map[string]interface{}{
					{"name": "root", "type": "string", "description": "Module directory to analyze. Default: '.'."},
					{"name": "internalOnly", "type": "boolean", "description": "Only edges between packages of this module (drop third-party deps). Default: true."},
					{"name": "cluster", "type": "boolean", "description": "Group packages into clusters by top-level directory. Default: true."},
					{"name": "style", "type": "string", "description": "dark | light | plain. Default: dark."},
					{"name": "dotOnly", "type": "boolean", "description": "Return the generated DOT source instead of rendering an image. Default: false."},
					{"name": "format", "type": "string", "description": "png | svg | jpg. Default: png."},
					{"name": "engine", "type": "string", "description": "Layout engine. Default: dot."},
					{"name": "dpi", "type": "integer", "description": "Raster resolution (30-600). Default: 150."},
					{"name": "backend", "type": "string", "description": "Rendering engine: auto | system | embedded. Default: auto (CHATCLI_DIAGRAM_BACKEND)."},
					{"name": "output", "type": "string", "description": "Destination file path. If omitted, a temp file is written and its path returned."},
				},
				"examples": []string{
					`{"cmd":"gomod","root":".","output":"/tmp/imports.svg","format":"svg"}`,
					`{"cmd":"gomod","root":"./cli","dotOnly":true}`,
					`{"cmd":"gomod","root":".","internalOnly":false,"dpi":300}`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinDiagramPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream renders the diagram, streaming progress for the longer
// gomod path.
func (p *BuiltinDiagramPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	cfg, err := parseDiagramArgs(args)
	if err != nil {
		return "", fmt.Errorf("@diagram: %w", err)
	}
	emit := func(line string) {
		if onOutput != nil {
			onOutput(line)
		}
	}

	if cfg.Cmd == "gomod" {
		return diagramRunGomod(ctx, cfg, emit)
	}
	return diagramRunRender(ctx, cfg, emit)
}

// diagramRunRender handles the render subcommand: resolve the DOT source, then
// render to the requested format.
func diagramRunRender(ctx context.Context, cfg diagramArgs, emit func(string)) (string, error) {
	dotSrc := cfg.DOT
	if dotSrc == "" {
		data, err := os.ReadFile(cfg.File) // #nosec G304 -- user-provided .dot path the tool was explicitly asked to render
		if err != nil {
			return "", fmt.Errorf("@diagram: reading dot file: %w", err)
		}
		dotSrc = string(data)
	}
	if strings.TrimSpace(dotSrc) == "" {
		return "", errors.New("@diagram: empty DOT source")
	}
	emit(i18n.T("plugins.diagram.describe_render", strings.ToUpper(cfg.Format)))
	return diagramRenderAndWrite(ctx, dotSrc, cfg)
}

// diagramRunGomod handles the gomod subcommand: build the import-graph DOT and
// either return it (dotOnly) or render it.
func diagramRunGomod(ctx context.Context, cfg diagramArgs, emit func(string)) (string, error) {
	emit(i18n.T("plugins.diagram.describe_gomod", cfg.Root))
	dotSrc, edges, err := buildGoImportDOT(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("@diagram: %w", err)
	}
	if edges == 0 {
		return i18n.T("plugins.diagram.no_edges", cfg.Root), nil
	}
	if cfg.DOTOnly {
		return dotSrc, nil
	}
	return diagramRenderAndWrite(ctx, dotSrc, cfg)
}

// diagramRenderAndWrite renders dotSrc with the configured engine/dpi/format,
// writes the bytes to the output (or a temp file) and returns a summary.
func diagramRenderAndWrite(ctx context.Context, dotSrc string, cfg diagramArgs) (string, error) {
	data, err := renderDiagramDOT(ctx, dotSrc, cfg)
	if err != nil {
		return "", fmt.Errorf("@diagram: %w", err)
	}

	output := cfg.Output
	if output == "" {
		output, err = diagramTempOutput(cfg.Format)
		if err != nil {
			return "", fmt.Errorf("@diagram: %w", err)
		}
	}
	if err := writeDiagramOutput(output, data); err != nil {
		return "", fmt.Errorf("@diagram: %w", err)
	}
	abs, absErr := filepath.Abs(output)
	if absErr == nil {
		output = abs
	}

	summary := i18n.T("plugins.diagram.rendered", output, strings.ToUpper(cfg.Format), humanByteSize(len(data)))
	if dims := imageDimensions(cfg.Format, data); dims != "" {
		summary += "\n" + i18n.T("plugins.diagram.dimensions", dims)
	}
	return summary, nil
}

// renderDiagramDOT renders dotSrc to the configured format using the resolved
// backend. The system `dot` (when present/selected) yields crisper output via
// fontconfig + system fonts + cairo; the embedded WASM engine is the fully
// self-contained fallback. Under "auto" a system render failure transparently
// falls back to embedded so a broken local Graphviz never breaks rendering;
// under an explicit "system" the error is surfaced.
func renderDiagramDOT(ctx context.Context, dotSrc string, cfg diagramArgs) ([]byte, error) {
	if resolveDiagramBackend(cfg.Backend) == diagramBackendSystem {
		data, err := renderViaSystemDot(ctx, dotSrc, cfg)
		if err == nil {
			return data, nil
		}
		if cfg.Backend == diagramBackendSystem {
			return nil, err // explicit request: do not silently fall back
		}
		// auto: fall through to the embedded engine
	}
	return renderViaEmbedded(ctx, dotSrc, cfg)
}

// renderViaSystemDot pipes the DOT through the system `dot` binary. format and
// engine are validated against closed allow-lists and dpi is bounded, so the
// argv is fully constrained.
func renderViaSystemDot(ctx context.Context, dotSrc string, cfg diagramArgs) ([]byte, error) {
	dotBin := systemDotPath()
	if dotBin == "" {
		return nil, errors.New("backend=system requested but no `dot` (Graphviz) found on PATH — install it (e.g. `brew install graphviz`) or use backend=embedded")
	}
	dotArgs := []string{"-T" + cfg.Format, "-K" + cfg.Engine}
	if cfg.Format != "svg" {
		dotArgs = append(dotArgs, fmt.Sprintf("-Gdpi=%d", cfg.DPI))
	}
	cmd := exec.CommandContext(ctx, dotBin, dotArgs...) // #nosec G204 -- dotBin from exec.LookPath; format/engine validated against closed maps; dpi bounded int
	cmd.Stdin = strings.NewReader(dotSrc)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("system dot render failed: %w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("system dot produced no output\n%s", strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// renderViaEmbedded renders with the embedded Graphviz: go-graphviz ships the
// upstream engine compiled to WebAssembly and runs it through the pure-Go
// wazero runtime — no cgo, no external `dot`, no network.
func renderViaEmbedded(ctx context.Context, dotSrc string, cfg diagramArgs) ([]byte, error) {
	g, err := graphviz.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("init graphviz: %w", err)
	}
	defer g.Close()

	graph, err := graphviz.ParseBytes([]byte(dotSrc))
	if err != nil {
		return nil, fmt.Errorf("invalid DOT: %w", err)
	}
	defer func() { _ = graph.Close() }()

	if cfg.Format != "svg" {
		graph.SetDPI(float64(cfg.DPI))
	}
	g.SetLayout(diagramValidEngines[cfg.Engine])

	var buf bytes.Buffer
	if err := g.Render(ctx, graph, diagramValidFormats[cfg.Format], &buf); err != nil {
		return nil, fmt.Errorf("render %s: %w", cfg.Format, err)
	}
	return buf.Bytes(), nil
}

// parseDiagramArgs supports flat JSON, the {"cmd","args"} envelope and --flag
// argv form, applies defaults and validates.
func parseDiagramArgs(args []string) (diagramArgs, error) {
	out := diagramArgs{
		Format:       "png",
		Engine:       "dot",
		DPI:          diagramDefaultDPI,
		Style:        "dark",
		Backend:      configuredDiagramBackend(),
		InternalOnly: true,
		Cluster:      true,
	}
	payload := strings.TrimSpace(strings.Join(args, " "))
	var raw map[string]json.RawMessage
	var verb string

	switch {
	case strings.HasPrefix(payload, "{"):
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return out, fmt.Errorf("malformed JSON args: %w", err)
		}
		// Capture the envelope verb, then merge nested args OVER sibling keys so
		// a partial envelope keeps both (mirrors @docs-flatten's handling).
		if v := jsonString(raw, "cmd"); v != "" {
			verb = strings.ToLower(v)
		}
		if inner, ok := raw["args"]; ok {
			var innerMap map[string]json.RawMessage
			if err := json.Unmarshal(inner, &innerMap); err == nil {
				delete(raw, "args")
				delete(raw, "cmd")
				for k, v := range innerMap {
					raw[k] = v
				}
			}
		}
	default:
		var aerr error
		raw, verb, aerr = diagramArgvToMap(args)
		if aerr != nil {
			return out, aerr
		}
	}

	out.DOT = jsonString(raw, "dot", "source", "dotSource")
	out.File = jsonString(raw, "file", "dotFile", "path")
	out.Root = jsonString(raw, "root", "dir", "module")
	if v := strings.ToLower(jsonString(raw, "format", "fmt")); v != "" {
		out.Format = v
	}
	if v := strings.ToLower(jsonString(raw, "engine", "layout")); v != "" {
		out.Engine = v
	}
	if _, ok := raw["dpi"]; ok {
		out.DPI = jsonInt(raw, "dpi")
	}
	out.Output = jsonString(raw, "output", "out")
	if v := strings.ToLower(jsonString(raw, "style", "theme")); v != "" {
		out.Style = v
	}
	if v := strings.ToLower(jsonString(raw, "backend", "engine_backend")); v != "" {
		out.Backend = v
	}
	if v, present := jsonBoolLookup(raw, "internalOnly", "internal-only"); present {
		out.InternalOnly = v
	}
	if v, present := jsonBoolLookup(raw, "cluster"); present {
		out.Cluster = v
	}
	if v, present := jsonBoolLookup(raw, "dotOnly", "dot-only"); present {
		out.DOTOnly = v
	}

	out.Cmd = resolveDiagramCmd(verb, out)
	return finalizeDiagramArgs(out)
}

// resolveDiagramCmd picks the subcommand: an explicit verb wins; otherwise a
// bare {"root":...} with no DOT source implies gomod, else render.
func resolveDiagramCmd(verb string, out diagramArgs) string {
	switch verb {
	case "render", "gomod":
		return verb
	}
	if out.Root != "" && out.DOT == "" && out.File == "" {
		return "gomod"
	}
	return "render"
}

// finalizeDiagramArgs validates and normalizes the parsed args.
func finalizeDiagramArgs(out diagramArgs) (diagramArgs, error) {
	if _, ok := diagramValidFormats[out.Format]; !ok {
		return out, fmt.Errorf("invalid format %q (valid: png|svg|jpg)", out.Format)
	}
	if _, ok := diagramValidEngines[out.Engine]; !ok {
		return out, fmt.Errorf("invalid engine %q (valid: dot|neato|fdp|sfdp|circo|twopi|osage|patchwork)", out.Engine)
	}
	switch out.Style {
	case "dark", "light", "plain":
	default:
		return out, fmt.Errorf("invalid style %q (valid: dark|light|plain)", out.Style)
	}
	switch out.Backend {
	case diagramBackendAuto, diagramBackendSystem, diagramBackendEmbedded:
	default:
		return out, fmt.Errorf("invalid backend %q (valid: auto|system|embedded)", out.Backend)
	}
	if out.DPI < diagramMinDPI {
		out.DPI = diagramMinDPI
	}
	if out.DPI > diagramMaxDPI {
		out.DPI = diagramMaxDPI
	}

	if out.Cmd == "gomod" {
		if out.Root == "" {
			out.Root = "."
		}
		return out, nil
	}
	// render
	if out.DOT == "" && out.File == "" {
		return out, errors.New(`render requires "dot" (inline DOT) or "file" (a .dot path)`)
	}
	if out.DOT != "" && out.File != "" {
		return out, errors.New(`"dot" and "file" are mutually exclusive — set exactly one`)
	}
	return out, nil
}

// diagramArgvToMap converts `--flag value` argv form into the raw map shared
// with the JSON path. A bare positional is treated as inline DOT. It also
// returns the leading bare verb (render|gomod) if present.
func diagramArgvToMap(args []string) (map[string]json.RawMessage, string, error) {
	raw := make(map[string]json.RawMessage)
	verb := ""
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		if a == "" {
			continue
		}
		if !strings.HasPrefix(a, "--") {
			if verb == "" && (a == "render" || a == "gomod") {
				verb = a
				continue
			}
			if _, ok := raw["dot"]; !ok {
				v, _ := json.Marshal(trimQuotes(a))
				raw["dot"] = v
			}
			continue
		}
		key := strings.TrimPrefix(a, "--")
		val := "true" // bare --flag is a boolean
		if eq := strings.Index(key, "="); eq >= 0 {
			val = key[eq+1:]
			key = key[:eq]
		} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			val = args[i+1]
			i++
		}
		v, _ := json.Marshal(trimQuotes(val))
		raw[key] = v
	}
	if len(raw) == 0 && verb == "" {
		return nil, "", errors.New(`@diagram: provide "dot", "file" or the gomod subcommand`)
	}
	return raw, verb, nil
}

// diagramTempOutput creates a temp file path for the rendered image when the
// caller did not specify an output.
func diagramTempOutput(format string) (string, error) {
	f, err := os.CreateTemp("", "chatcli-diagram-*."+format)
	if err != nil {
		return "", fmt.Errorf("create temp output: %w", err)
	}
	name := f.Name()
	_ = f.Close()
	return name, nil
}

// writeDiagramOutput writes the rendered bytes, creating parent directories.
func writeDiagramOutput(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write output file: %w", err)
	}
	return nil
}

// imageDimensions returns "WxH" for raster output, or "" when the format has no
// intrinsic pixel size (svg) or cannot be decoded.
func imageDimensions(format string, data []byte) string {
	if format == "svg" {
		return ""
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
}

// --- gomod: real Go import graph -> DOT -----------------------------------

// goListPkg is the slice of `go list -json` output @diagram consumes.
type goListPkg struct {
	ImportPath string `json:"ImportPath"`
	Module     *struct {
		Path string `json:"Path"`
	} `json:"Module"`
	Imports []string `json:"Imports"`
}

// buildGoImportDOT runs `go list -json ./...` in the module root and renders the
// intra-module (and optionally third-party) import graph as a clustered DOT.
// Returns the DOT source and the number of edges (0 => nothing to draw).
func buildGoImportDOT(ctx context.Context, cfg diagramArgs) (string, int, error) {
	abs, err := filepath.Abs(cfg.Root)
	if err != nil {
		return "", 0, err
	}
	if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
		return "", 0, fmt.Errorf("root is not a directory: %s", cfg.Root)
	}

	cmd := exec.CommandContext(ctx, "go", "list", "-json", "./...") // #nosec G204 -- fixed `go` binary and fixed args; cfg.Root is validated as an existing directory and passed via cmd.Dir, never interpolated
	cmd.Dir = abs
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return "", 0, fmt.Errorf("go list failed (is %q a Go module?): %w\n%s", cfg.Root, err, strings.TrimSpace(stderr.String()))
	}

	pkgs, modPath := decodeGoList(stdout)
	if modPath == "" {
		return "", 0, errors.New("target is not a Go module (no module path) — run gomod inside a module")
	}

	nodes := map[string]bool{}
	edgeSet := map[string]bool{}
	rel := goImportRelabeler(modPath)
	for _, p := range pkgs {
		from, ok := rel(p.ImportPath)
		if !ok {
			continue
		}
		nodes[from] = true
		for _, imp := range p.Imports {
			to, internal := rel(imp)
			if !internal {
				if cfg.InternalOnly || isStdlibImport(imp) {
					continue
				}
				to = imp // keep third-party deps as full-path nodes
			}
			if to == from {
				continue
			}
			nodes[to] = true
			edgeSet[from+"\x00"+to] = true
		}
	}
	if len(edgeSet) == 0 {
		return "", 0, nil
	}
	return assembleImportDOT(modPath, nodes, edgeSet, cfg), len(edgeSet), nil
}

// decodeGoList streams the concatenated JSON objects `go list -json` emits and
// returns the packages plus the module path (from the first package carrying a
// Module).
func decodeGoList(stdout []byte) ([]goListPkg, string) {
	dec := json.NewDecoder(bytes.NewReader(stdout))
	var pkgs []goListPkg
	modPath := ""
	for dec.More() {
		var p goListPkg
		if err := dec.Decode(&p); err != nil {
			break
		}
		if p.Module != nil && modPath == "" {
			modPath = p.Module.Path
		}
		pkgs = append(pkgs, p)
	}
	return pkgs, modPath
}

// goImportRelabeler returns a function mapping a full import path to a short
// label relative to the module, reporting whether it belongs to the module.
func goImportRelabeler(modPath string) func(string) (string, bool) {
	base := filepath.Base(modPath)
	return func(importPath string) (string, bool) {
		if importPath == modPath {
			return base, true
		}
		if strings.HasPrefix(importPath, modPath+"/") {
			return strings.TrimPrefix(importPath, modPath+"/"), true
		}
		return "", false
	}
}

// isStdlibImport reports whether an import path is part of the standard library
// (its first path segment has no dot, e.g. "fmt", "encoding/json").
func isStdlibImport(importPath string) bool {
	first := importPath
	if i := strings.IndexByte(importPath, '/'); i >= 0 {
		first = importPath[:i]
	}
	return !strings.Contains(first, ".")
}

// assembleImportDOT renders the node/edge sets into a deterministic, styled DOT
// document, grouping nodes into clusters by their top-level directory segment.
func assembleImportDOT(modPath string, nodes map[string]bool, edgeSet map[string]bool, cfg diagramArgs) string {
	graphAttrs, nodeAttrs, edgeAttrs, clusterColor := diagramStyle(cfg.Style)

	var b strings.Builder
	b.WriteString("digraph imports {\n")
	b.WriteString("  rankdir=TB;\n")
	b.WriteString("  " + graphAttrs + "\n")
	fmt.Fprintf(&b, "  label=%q; labelloc=t; fontsize=22;\n", filepath.Base(modPath)+" — module import graph")
	b.WriteString("  node [" + nodeAttrs + "];\n")
	b.WriteString("  edge [" + edgeAttrs + "];\n\n")

	if cfg.Cluster {
		for _, cluster := range groupByTopSegment(nodes) {
			if cluster.name == "" {
				continue // root-level nodes are emitted implicitly by edges
			}
			fmt.Fprintf(&b, "  subgraph cluster_%s {\n", sanitizeClusterID(cluster.name))
			fmt.Fprintf(&b, "    label=%q; color=%q; style=rounded; fontcolor=%q;\n", cluster.name, clusterColor, clusterColor)
			for _, n := range cluster.members {
				fmt.Fprintf(&b, "    %q;\n", n)
			}
			b.WriteString("  }\n")
		}
		b.WriteString("\n")
	}

	for _, e := range sortedEdges(edgeSet) {
		fmt.Fprintf(&b, "  %q -> %q;\n", e[0], e[1])
	}
	b.WriteString("}\n")
	return b.String()
}

// diagramCluster is a named group of node labels.
type diagramCluster struct {
	name    string
	members []string
}

// groupByTopSegment buckets node labels by their first path segment, returning
// clusters sorted by name with sorted members for deterministic output. Nodes
// without a "/" (top-level packages) go to the "" bucket.
func groupByTopSegment(nodes map[string]bool) []diagramCluster {
	buckets := map[string][]string{}
	for n := range nodes {
		seg := ""
		if i := strings.IndexByte(n, '/'); i >= 0 {
			seg = n[:i]
		}
		buckets[seg] = append(buckets[seg], n)
	}
	names := make([]string, 0, len(buckets))
	for name := range buckets {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]diagramCluster, 0, len(names))
	for _, name := range names {
		members := buckets[name]
		sort.Strings(members)
		out = append(out, diagramCluster{name: name, members: members})
	}
	return out
}

// sortedEdges returns the edge pairs sorted for deterministic DOT output.
func sortedEdges(edgeSet map[string]bool) [][2]string {
	out := make([][2]string, 0, len(edgeSet))
	for e := range edgeSet {
		parts := strings.SplitN(e, "\x00", 2)
		out = append(out, [2]string{parts[0], parts[1]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// sanitizeClusterID makes a DOT-safe subgraph id from a path segment.
func sanitizeClusterID(seg string) string {
	var b strings.Builder
	for _, r := range seg {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "root"
	}
	return b.String()
}

// diagramStyle returns the graph/node/edge default attribute strings plus the
// cluster border color for the given style. dark is the IML-like default.
func diagramStyle(style string) (graphAttrs, nodeAttrs, edgeAttrs, clusterColor string) {
	switch style {
	case "light":
		return `bgcolor="#FFFFFF"; fontcolor="#222222"; fontname="Helvetica"; nodesep=0.35; ranksep=0.7;`,
			`shape=box, style="rounded,filled", fillcolor="#F0F0F0", color="#999999", fontcolor="#222222", fontname="Menlo", fontsize=12`,
			`color="#888888", arrowhead=vee`,
			"#5B7FA6"
	case "plain":
		return `fontname="Helvetica"; nodesep=0.35; ranksep=0.7;`,
			`shape=box, fontname="Menlo", fontsize=12`,
			`arrowhead=vee`,
			"#000000"
	default: // dark
		return `bgcolor="#2B2D30"; fontcolor="#BBBBBB"; fontname="Helvetica"; nodesep=0.35; ranksep=0.7;`,
			`shape=box, style="rounded,filled", fillcolor="#3C3F41", color="#555555", fontcolor="#D7D7D7", fontname="Menlo", fontsize=12`,
			`color="#777777", arrowhead=vee`,
			"#64B5F6"
	}
}
