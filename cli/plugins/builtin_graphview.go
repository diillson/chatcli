/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinGraphViewPlugin — @graphview renders an interactive, Obsidian-style
 * graph view: a force-directed graph you can drag, zoom, pan, search and filter,
 * opened in the browser. Unlike @diagram (which produces a static PNG/SVG), the
 * output is a self-contained HTML file with an embedded, dependency-free physics
 * engine (canvas + JS, no CDN, no network, no API key) — so the result keeps
 * living and moving the way the user wanted.
 *
 * Three node/edge sources, selected by --source:
 *   - json (default): the model supplies nodes/edges inline (or via a JSON file).
 *     This is how "graph what we just discussed" works — the model, which already
 *     has the conversation in context, emits the semantic graph itself.
 *   - knowledge: the in-core knowledge graph (memory/skills/topics), the same
 *     substrate behind /graph, but interactive.
 *   - conversation: a structural graph of the current session (turns, tools,
 *     files), derived deterministically from history.
 *
 * The knowledge/conversation sources need live CLI state, so — like @knowledge
 * and @memory — the plugin reaches it through a package-level adapter supplied
 * via SetGraphSourceProvider. With no provider wired, json still works.
 */
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	_ "embed"

	"github.com/diillson/chatcli/i18n"
	"golang.org/x/term"
)

//go:embed assets/graphview.html
var graphViewTemplate string

// graphViewDataPlaceholder is the placeholder the embedded template carries; it is
// replaced with the JSON-encoded GraphData at render time.
const graphViewDataPlaceholder = "__CHATCLI_GRAPH_DATA__"

// graphViewMaxNodes bounds the rendered graph. The simulation is O(n²) per
// tick; a couple thousand nodes is the comfortable ceiling for a smooth drag.
const graphViewMaxNodes = 2000

// GraphNode is one vertex of a rendered graph. Field names match the JSON the
// embedded template consumes.
type GraphNode struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Kind    string  `json:"kind,omitempty"`
	Summary string  `json:"summary,omitempty"`
	Weight  float64 `json:"weight,omitempty"`
}

// GraphEdge is one undirected link between two node IDs.
type GraphEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Weight float64 `json:"weight,omitempty"`
}

// GraphData is the full payload injected into the HTML template.
type GraphData struct {
	Title string      `json:"title"`
	Theme string      `json:"theme"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// GraphSourceProvider lets @graphview pull node/edge data from live CLI state
// for the knowledge and conversation sources. Bound via SetGraphSourceProvider.
type GraphSourceProvider interface {
	// KnowledgeGraph returns the in-core knowledge graph as renderable data.
	KnowledgeGraph() (GraphData, error)
	// ConversationGraph returns a structural graph of the current session.
	ConversationGraph() (GraphData, error)
}

// graphProviderHolder mirrors knowAdapterHolder: a concrete wrapper so
// atomic.Value never sees a bare nil interface.
type graphProviderHolder struct{ p GraphSourceProvider }

var graphProviderAtom atomic.Value // stores graphProviderHolder

// SetGraphSourceProvider wires the live provider. Called from the cli package
// once the session exists. Pass nil to clear it.
func SetGraphSourceProvider(p GraphSourceProvider) {
	graphProviderAtom.Store(graphProviderHolder{p: p})
}

func currentGraphSourceProvider() GraphSourceProvider {
	v := graphProviderAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(graphProviderHolder)
	return h.p
}

// BuiltinGraphViewPlugin is the @graphview tool.
type BuiltinGraphViewPlugin struct{}

// NewBuiltinGraphViewPlugin returns a ready-to-register plugin.
func NewBuiltinGraphViewPlugin() *BuiltinGraphViewPlugin { return &BuiltinGraphViewPlugin{} }

// Name returns "@graphview".
func (*BuiltinGraphViewPlugin) Name() string { return "@graphview" }

// Description surfaces the tool in the catalog.
func (*BuiltinGraphViewPlugin) Description() string {
	return i18n.T("plugins.graphview.description")
}

// IsReadOnly reports true: it visualizes data into a fresh HTML file and opens a
// viewer; it never mutates the user's workspace, so it needs no approval prompt.
func (*BuiltinGraphViewPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports false: it writes an output file and may open a
// browser window — not something to fan out in parallel.
func (*BuiltinGraphViewPlugin) IsConcurrencySafe(_ []string) bool { return false }

// DescribeCall surfaces the source in the spinner.
func (*BuiltinGraphViewPlugin) DescribeCall(args []string) string {
	src := extractStringArg(args, "source", "src")
	if src == "" {
		src = "json"
	}
	return i18n.T("plugins.graphview.describe", src)
}

// Usage explains the canonical invocation.
func (*BuiltinGraphViewPlugin) Usage() string {
	return `<tool_call name="@graphview" args='{"title":"Our chat","nodes":[{"id":"a","label":"Auth","kind":"topic"},{"id":"b","label":"OAuth","kind":"topic"}],"edges":[{"source":"a","target":"b"}]}' />

Flags (flat JSON or {"cmd":"render","args":{...}} envelope):
  source   json (default) | knowledge | conversation
  nodes    [{id,label,kind?,summary?,weight?}]   (source=json)
  edges    [{source,target,weight?}]             (source=json)
  file     path to a JSON file holding {nodes,edges} instead of inline
  title    graph title shown in the toolbar
  theme    dark (default) | light
  output   path to write the .html (default: a temp file)
  open     true (default) | false — open the result in the browser

Renders an interactive force-directed graph (drag/zoom/pan/search/filter) to a
self-contained HTML file and opens it. To graph the conversation, pass the
entities and relations you extracted as nodes/edges with source=json.`
}

// Version is semver.
func (*BuiltinGraphViewPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinGraphViewPlugin) Path() string { return "" }

// Schema describes the tool for the LLM catalog.
func (*BuiltinGraphViewPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "flat JSON preferred",
		"description": "Render an interactive, draggable force-directed graph (Obsidian-style) to a self-contained HTML file and open it in the browser. " +
			"Use it to visualize how concepts, entities, files or topics relate — especially to map out the current conversation/session.",
		"flags": []map[string]interface{}{
			{"name": "source", "type": "string", "description": "json (default): you provide nodes/edges. knowledge: the in-core knowledge graph. conversation: a structural graph of this session."},
			{"name": "nodes", "type": "array", "description": "source=json. Array of {id, label, kind?, summary?, weight?}. kind drives node color (e.g. topic, project, file, entity, person)."},
			{"name": "edges", "type": "array", "description": "source=json. Array of {source, target, weight?} where source/target are node ids."},
			{"name": "file", "type": "string", "description": "Path to a JSON file holding {\"nodes\":[...],\"edges\":[...]} — use for large graphs instead of inline args."},
			{"name": "title", "type": "string", "description": "Graph title shown in the toolbar."},
			{"name": "theme", "type": "string", "description": "dark (default) or light."},
			{"name": "output", "type": "string", "description": "Path to write the .html file (default: a temp file)."},
			{"name": "open", "type": "boolean", "description": "Open the result in the default browser (default true)."},
		},
		"examples": []string{
			`{"title":"What we discussed","nodes":[{"id":"oauth","label":"OAuth login","kind":"topic"},{"id":"token","label":"Token exchange","kind":"topic"},{"id":"cli","label":"ChatCLI","kind":"project"}],"edges":[{"source":"cli","target":"oauth"},{"source":"oauth","target":"token","weight":2}]}`,
			`{"source":"knowledge"}`,
			`{"source":"conversation"}`,
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and renders the graph.
func (p *BuiltinGraphViewPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream resolves the data source, writes the HTML and opens it.
func (p *BuiltinGraphViewPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	cfg, err := parseGraphViewArgs(args)
	if err != nil {
		return "", fmt.Errorf("@graphview: %w", err)
	}

	data, err := resolveGraphData(cfg)
	if err != nil {
		return "", fmt.Errorf("@graphview: %w", err)
	}
	if len(data.Nodes) == 0 {
		return i18n.T("plugins.graphview.empty"), nil
	}
	if len(data.Nodes) > graphViewMaxNodes {
		return "", fmt.Errorf("@graphview: %s", i18n.T("plugins.graphview.too_big", len(data.Nodes), graphViewMaxNodes))
	}

	path, err := renderGraphViewHTML(data, cfg.Output)
	if err != nil {
		return "", fmt.Errorf("@graphview: %w", err)
	}

	opened := false
	if cfg.shouldOpen() {
		if oerr := openInBrowser(path); oerr == nil {
			opened = true
		}
	}

	msg := i18n.T("plugins.graphview.rendered", path, len(data.Nodes), len(data.Edges))
	if opened {
		msg += "\n" + i18n.T("plugins.graphview.opened")
	} else {
		msg += "\n" + i18n.T("plugins.graphview.open_hint", path)
	}
	return msg, nil
}

// graphViewArgs is the typed view of @graphview's input.
type graphViewArgs struct {
	Source string
	Title  string
	Theme  string
	Output string
	File   string
	Open   *bool
	Nodes  []GraphNode
	Edges  []GraphEdge
}

// shouldOpen reports whether the browser should be launched: honor an explicit
// open flag, else the CHATCLI_GRAPHVIEW_OPEN env (default on), but never when
// stdout is not a terminal (e.g. the gateway daemon or a piped run).
func (a graphViewArgs) shouldOpen() bool {
	if a.Open != nil && !*a.Open {
		return false
	}
	if a.Open == nil && !graphViewOpenEnvEnabled() {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

const graphViewOpenEnv = "CHATCLI_GRAPHVIEW_OPEN"
const graphViewThemeEnv = "CHATCLI_GRAPHVIEW_THEME"

func graphViewOpenEnvEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(graphViewOpenEnv))) {
	case "false", "0", "off", "no":
		return false
	default:
		return true
	}
}

func graphViewDefaultTheme() string {
	if strings.ToLower(strings.TrimSpace(os.Getenv(graphViewThemeEnv))) == "light" {
		return "light"
	}
	return "dark"
}

// gvNodeInput / gvEdgeInput accept the common field aliases an LLM might emit,
// so a slightly-off shape still renders instead of failing.
type gvNodeInput struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Title   string  `json:"title"`
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Type    string  `json:"type"`
	Group   string  `json:"group"`
	Summary string  `json:"summary"`
	Desc    string  `json:"description"`
	Weight  float64 `json:"weight"`
}

type gvEdgeInput struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	From   string  `json:"from"`
	To     string  `json:"to"`
	Weight float64 `json:"weight"`
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// parseGraphViewArgs supports flat JSON, the {"cmd","args"} envelope and a
// minimal --flag argv form.
func parseGraphViewArgs(args []string) (graphViewArgs, error) {
	out := graphViewArgs{Source: "json", Theme: graphViewDefaultTheme()}
	payload := strings.TrimSpace(strings.Join(args, " "))

	if !strings.HasPrefix(payload, "{") {
		// Bare argv: only the simple sources/flags are supported here.
		if s := stringFromFlagArgs(args, []string{"source", "src"}); s != "" {
			out.Source = s
		}
		if t := stringFromFlagArgs(args, []string{"theme"}); t != "" {
			out.Theme = t
		}
		if o := stringFromFlagArgs(args, []string{"output", "out"}); o != "" {
			out.Output = o
		}
		if f := stringFromFlagArgs(args, []string{"file"}); f != "" {
			out.File = f
		}
		return finalizeGraphViewArgs(out)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return out, fmt.Errorf("malformed JSON args: %w", err)
	}
	if inner, ok := raw["args"]; ok {
		var innerMap map[string]json.RawMessage
		if err := json.Unmarshal(inner, &innerMap); err == nil {
			raw = innerMap
		}
	}

	if s := firstNonEmpty(jsonString(raw, "source", "src")); s != "" {
		out.Source = s
	}
	out.Title = jsonString(raw, "title")
	if t := jsonString(raw, "theme"); t != "" {
		out.Theme = t
	}
	out.Output = jsonString(raw, "output", "out")
	out.File = jsonString(raw, "file")
	if v, ok := raw["open"]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil {
			out.Open = &b
		}
	}

	if ndata, ok := raw["nodes"]; ok {
		var nin []gvNodeInput
		if err := json.Unmarshal(ndata, &nin); err != nil {
			return out, fmt.Errorf("decoding nodes: %w", err)
		}
		out.Nodes = normalizeNodeInputs(nin)
	}
	if edata, ok := raw["edges"]; ok {
		var ein []gvEdgeInput
		if err := json.Unmarshal(edata, &ein); err != nil {
			return out, fmt.Errorf("decoding edges: %w", err)
		}
		out.Edges = normalizeEdgeInputs(ein)
	}
	return finalizeGraphViewArgs(out)
}

func normalizeNodeInputs(in []gvNodeInput) []GraphNode {
	out := make([]GraphNode, 0, len(in))
	for _, n := range in {
		id := firstNonEmpty(n.ID, n.Label, n.Title, n.Name)
		if id == "" {
			continue
		}
		out = append(out, GraphNode{
			ID:      id,
			Label:   firstNonEmpty(n.Label, n.Title, n.Name, n.ID),
			Kind:    firstNonEmpty(n.Kind, n.Type, n.Group),
			Summary: firstNonEmpty(n.Summary, n.Desc),
			Weight:  n.Weight,
		})
	}
	return out
}

func normalizeEdgeInputs(in []gvEdgeInput) []GraphEdge {
	out := make([]GraphEdge, 0, len(in))
	for _, e := range in {
		s := firstNonEmpty(e.Source, e.From)
		t := firstNonEmpty(e.Target, e.To)
		if s == "" || t == "" {
			continue
		}
		out = append(out, GraphEdge{Source: s, Target: t, Weight: e.Weight})
	}
	return out
}

func finalizeGraphViewArgs(out graphViewArgs) (graphViewArgs, error) {
	out.Source = strings.ToLower(strings.TrimSpace(out.Source))
	switch out.Source {
	case "", "json":
		out.Source = "json"
	case "knowledge", "conversation":
	default:
		return out, fmt.Errorf("unknown source %q (use json, knowledge or conversation)", out.Source)
	}
	if out.Theme != "light" {
		out.Theme = "dark"
	}
	return out, nil
}

// resolveGraphData produces the renderable data for the chosen source.
func resolveGraphData(cfg graphViewArgs) (GraphData, error) {
	switch cfg.Source {
	case "knowledge", "conversation":
		prov := currentGraphSourceProvider()
		if prov == nil {
			return GraphData{}, fmt.Errorf("source %q is unavailable here; pass nodes/edges with source=json instead", cfg.Source)
		}
		var (
			data GraphData
			err  error
		)
		if cfg.Source == "knowledge" {
			data, err = prov.KnowledgeGraph()
		} else {
			data, err = prov.ConversationGraph()
		}
		if err != nil {
			return GraphData{}, err
		}
		applyGraphMeta(&data, cfg)
		return sanitizeGraphData(data), nil
	default: // json
		data := GraphData{Nodes: cfg.Nodes, Edges: cfg.Edges}
		if cfg.File != "" {
			fileData, err := readGraphDataFile(cfg.File)
			if err != nil {
				return GraphData{}, err
			}
			data.Nodes = append(data.Nodes, fileData.Nodes...)
			data.Edges = append(data.Edges, fileData.Edges...)
		}
		applyGraphMeta(&data, cfg)
		return sanitizeGraphData(data), nil
	}
}

func applyGraphMeta(data *GraphData, cfg graphViewArgs) {
	if cfg.Title != "" {
		data.Title = cfg.Title
	}
	if data.Title == "" {
		data.Title = i18n.T("plugins.graphview.default_title")
	}
	data.Theme = cfg.Theme
}

func readGraphDataFile(path string) (GraphData, error) {
	clean := filepath.Clean(path)
	b, err := os.ReadFile(clean) //#nosec G304 -- user/agent-specified graph data file, read-only
	if err != nil {
		return GraphData{}, fmt.Errorf("reading file %q: %w", path, err)
	}
	var in struct {
		Nodes []gvNodeInput `json:"nodes"`
		Edges []gvEdgeInput `json:"edges"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return GraphData{}, fmt.Errorf("parsing %q: %w", path, err)
	}
	return GraphData{Nodes: normalizeNodeInputs(in.Nodes), Edges: normalizeEdgeInputs(in.Edges)}, nil
}

// sanitizeGraphData dedupes nodes by ID and drops edges whose endpoints are
// missing, so the rendered graph is always well-formed.
func sanitizeGraphData(data GraphData) GraphData {
	seen := make(map[string]bool, len(data.Nodes))
	nodes := make([]GraphNode, 0, len(data.Nodes))
	for _, n := range data.Nodes {
		n.ID = strings.TrimSpace(n.ID)
		if n.ID == "" || seen[n.ID] {
			continue
		}
		if strings.TrimSpace(n.Label) == "" {
			n.Label = n.ID
		}
		seen[n.ID] = true
		nodes = append(nodes, n)
	}
	edges := make([]GraphEdge, 0, len(data.Edges))
	edgeSeen := make(map[string]bool, len(data.Edges))
	for _, e := range data.Edges {
		if !seen[e.Source] || !seen[e.Target] || e.Source == e.Target {
			continue
		}
		a, b := e.Source, e.Target
		if a > b {
			a, b = b, a
		}
		key := a + "\x00" + b
		if edgeSeen[key] {
			continue
		}
		edgeSeen[key] = true
		edges = append(edges, e)
	}
	data.Nodes = nodes
	data.Edges = edges
	return data
}

// renderGraphViewHTML injects the data into the template and writes the file.
func renderGraphViewHTML(data GraphData, output string) (string, error) {
	payload, err := json.Marshal(data) // HTMLEscape on by default → safe inside <script>
	if err != nil {
		return "", fmt.Errorf("encoding graph data: %w", err)
	}
	html := strings.Replace(graphViewTemplate, graphViewDataPlaceholder, string(payload), 1)

	path := output
	if path == "" {
		f, terr := os.CreateTemp("", "chatcli-graph-*.html")
		if terr != nil {
			return "", fmt.Errorf("creating temp file: %w", terr)
		}
		path = f.Name()
		_ = f.Close()
	} else {
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return "", fmt.Errorf("creating output dir: %w", err)
			}
		}
	}
	if err := os.WriteFile(path, []byte(html), 0o600); err != nil {
		return "", fmt.Errorf("writing %q: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return abs, nil
}

// openInBrowser opens a local file in the OS default browser. Mirrors
// auth.openBrowser but kept package-local to avoid a cli→auth coupling.
func openInBrowser(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Start() //#nosec G204 -- fixed verb + our own generated temp HTML path
	case "linux":
		return exec.Command("xdg-open", path).Start() //#nosec G204 -- fixed verb + our own generated temp HTML path
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start() //#nosec G204 -- fixed verb + our own generated temp HTML path
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}
