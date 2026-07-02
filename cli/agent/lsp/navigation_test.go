package lsp

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// navFakeServer answers the navigation methods with deliberately varied
// response shapes — LocationLink for definition, plain locations for
// references, hierarchical symbols, MarkupContent hover — so the lenient
// decoders are exercised against the diversity real servers produce.
func navFakeServer(t *testing.T, serverIn io.Reader, serverOut io.Writer, flatSymbols bool) {
	t.Helper()
	r := bufio.NewReader(serverIn)
	reply := func(id *json.RawMessage, result interface{}) {
		_ = writeMessage(serverOut, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": result})
	}
	rng := func(line, ch int) map[string]interface{} {
		return map[string]interface{}{
			"start": map[string]int{"line": line, "character": ch},
			"end":   map[string]int{"line": line, "character": ch + 3},
		}
	}
	for {
		body, err := readMessage(r)
		if err != nil {
			return
		}
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			return
		}
		switch m.Method {
		case "initialize":
			reply(m.ID, map[string]interface{}{"capabilities": map[string]interface{}{}})
		case "textDocument/definition":
			// LocationLink shape (typescript-language-server style).
			reply(m.ID, []map[string]interface{}{{
				"targetUri":            "file:///src/def.go",
				"targetRange":          rng(9, 0),
				"targetSelectionRange": rng(9, 5),
			}})
		case "textDocument/references":
			reply(m.ID, []map[string]interface{}{
				{"uri": "file:///src/a.go", "range": rng(3, 1)},
				{"uri": "file:///src/b.go", "range": rng(7, 2)},
			})
		case "textDocument/documentSymbol":
			if flatSymbols {
				reply(m.ID, []map[string]interface{}{
					{"name": "Manager", "kind": 23, "containerName": "ctxmgr",
						"location": map[string]interface{}{"uri": "file:///src/a.go", "range": rng(10, 0)}},
				})
			} else {
				reply(m.ID, []map[string]interface{}{{
					"name": "Manager", "kind": 23, "range": rng(10, 0), "selectionRange": rng(10, 5),
					"children": []map[string]interface{}{
						{"name": "Attach", "kind": 6, "range": rng(20, 0), "selectionRange": rng(20, 5)},
					},
				}})
			}
		case "textDocument/hover":
			reply(m.ID, map[string]interface{}{
				"contents": map[string]interface{}{"kind": "markdown", "value": "```go\nfunc Attach(id string) error\n```"},
			})
		case "shutdown":
			reply(m.ID, nil)
		}
	}
}

func newNavClient(t *testing.T, flatSymbols bool) *Client {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	go navFakeServer(t, c2sR, s2cW, flatSymbols)
	c := New(c2sW, s2cR, zap.NewNop())
	if err := c.Initialize("file:///src"); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return c
}

func TestDefinitionDecodesLocationLinks(t *testing.T) {
	c := newNavClient(t, false)
	locs, err := c.Definition("file:///src/a.go", Position{Line: 3, Character: 1})
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	if len(locs) != 1 || locs[0].URI != "file:///src/def.go" || locs[0].Range.Start.Line != 9 || locs[0].Range.Start.Character != 5 {
		t.Fatalf("LocationLink not decoded to selection range: %+v", locs)
	}
}

func TestReferencesDecodesLocations(t *testing.T) {
	c := newNavClient(t, false)
	locs, err := c.References("file:///src/a.go", Position{Line: 3, Character: 1}, true)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(locs) != 2 || locs[1].URI != "file:///src/b.go" || locs[1].Range.Start.Line != 7 {
		t.Fatalf("references wrong: %+v", locs)
	}
}

func TestDocumentSymbolsHierarchicalFlattens(t *testing.T) {
	c := newNavClient(t, false)
	syms, err := c.DocumentSymbols("file:///src/a.go")
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("want 2 flattened symbols, got %+v", syms)
	}
	if syms[0].Name != "Manager" || syms[0].KindLabel() != "struct" || syms[0].Line != 10 {
		t.Fatalf("parent symbol wrong: %+v", syms[0])
	}
	if syms[1].Name != "Attach" || syms[1].Container != "Manager" || syms[1].KindLabel() != "method" || syms[1].Line != 20 {
		t.Fatalf("child symbol wrong: %+v", syms[1])
	}
}

func TestDocumentSymbolsFlatShape(t *testing.T) {
	c := newNavClient(t, true)
	syms, err := c.DocumentSymbols("file:///src/a.go")
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	if len(syms) != 1 || syms[0].Name != "Manager" || syms[0].Container != "ctxmgr" || syms[0].Line != 10 {
		t.Fatalf("flat SymbolInformation not decoded: %+v", syms)
	}
}

func TestHoverDecodesMarkup(t *testing.T) {
	c := newNavClient(t, false)
	text, err := c.Hover("file:///src/a.go", Position{Line: 20, Character: 5})
	if err != nil {
		t.Fatalf("Hover: %v", err)
	}
	if !strings.Contains(text, "func Attach(id string) error") {
		t.Fatalf("hover markup not decoded: %q", text)
	}
}

func TestClearDiagnosticsForcesFreshWait(t *testing.T) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	uri := "file:///tmp/main.go"
	go fakeServer(t, c2sR, s2cW, uri)

	c := New(c2sW, s2cR, zap.NewNop())
	if err := c.Initialize("file:///tmp"); err != nil {
		t.Fatal(err)
	}
	if err := c.DidOpen(uri, "go", "package main\n"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Diagnostics(uri, 2*time.Second); !ok {
		t.Fatal("expected initial diagnostics")
	}
	c.ClearDiagnostics(uri)
	// With the cache cleared and no new publish, the wait must time out
	// instead of serving the stale pre-edit set.
	if _, ok := c.Diagnostics(uri, 200*time.Millisecond); ok {
		t.Fatal("stale diagnostics served after ClearDiagnostics")
	}
}
