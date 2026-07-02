/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * LSP navigation requests — the queries behind the @lsp agent tool.
 *
 * Servers answer these requests in structurally different (but spec-legal)
 * shapes: definition may be a single Location, a Location array or a
 * LocationLink array; document symbols come hierarchical (DocumentSymbol) or
 * flat (SymbolInformation); hover contents may be a bare string, a
 * MarkupContent object, or an array mixing both. Every decoder here is
 * therefore lenient by construction — the tool must work the same against
 * gopls, pyright, typescript-language-server, rust-analyzer and clangd.
 */
package lsp

import (
	"encoding/json"
	"strings"
	"time"
)

// navTimeout bounds one navigation request. Generous enough for a language
// server still indexing a large module; short enough that a hung server
// degrades one tool call, not the agent turn.
const navTimeout = 20 * time.Second

// Location is a resolved source location returned by navigation requests.
type Location struct {
	URI   string `json:"uri"`
	Range struct {
		Start Position `json:"start"`
		End   Position `json:"end"`
	} `json:"range"`
}

// SymbolInfo is the flattened symbol shape the tool renders, unified across
// the hierarchical (DocumentSymbol) and flat (SymbolInformation) responses.
type SymbolInfo struct {
	Name      string
	Kind      int
	Container string // parent symbol path ("Type.Method") when hierarchical
	Line      int    // zero-based, converted by the caller for display
}

// KindLabel renders the numeric LSP SymbolKind.
func (s SymbolInfo) KindLabel() string {
	labels := map[int]string{
		1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
		6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
		11: "interface", 12: "function", 13: "variable", 14: "constant",
		15: "string", 16: "number", 17: "boolean", 18: "array", 19: "object",
		20: "key", 21: "null", 22: "enum-member", 23: "struct", 24: "event",
		25: "operator", 26: "type-parameter",
	}
	if l, ok := labels[s.Kind]; ok {
		return l
	}
	return "symbol"
}

// Definition resolves the definition location(s) of the symbol at pos.
func (c *Client) Definition(uri string, pos Position) ([]Location, error) {
	res, err := c.call("textDocument/definition", textDocumentPositionParams(uri, pos), navTimeout)
	if err != nil {
		return nil, err
	}
	return decodeLocations(res), nil
}

// References resolves every reference to the symbol at pos.
func (c *Client) References(uri string, pos Position, includeDeclaration bool) ([]Location, error) {
	params := textDocumentPositionParams(uri, pos)
	params["context"] = map[string]interface{}{"includeDeclaration": includeDeclaration}
	res, err := c.call("textDocument/references", params, navTimeout)
	if err != nil {
		return nil, err
	}
	return decodeLocations(res), nil
}

// DocumentSymbols lists the symbols of a document, flattened to SymbolInfo in
// source order regardless of which response shape the server chose.
func (c *Client) DocumentSymbols(uri string) ([]SymbolInfo, error) {
	res, err := c.call("textDocument/documentSymbol", map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
	}, navTimeout)
	if err != nil {
		return nil, err
	}
	return decodeSymbols(res), nil
}

// Hover returns the hover text (signature/type/docs) for the symbol at pos.
// An empty string means the server had nothing to show.
func (c *Client) Hover(uri string, pos Position) (string, error) {
	res, err := c.call("textDocument/hover", textDocumentPositionParams(uri, pos), navTimeout)
	if err != nil {
		return "", err
	}
	return decodeHover(res), nil
}

// DidChange notifies the server of a full-content document update. version
// must strictly increase per uri (the pool tracks it).
func (c *Client) DidChange(uri, text string, version int) error {
	return c.notify("textDocument/didChange", map[string]interface{}{
		"textDocument":   map[string]interface{}{"uri": uri, "version": version},
		"contentChanges": []map[string]interface{}{{"text": text}},
	})
}

// ClearDiagnostics forgets the cached diagnostics for uri so the next
// Diagnostics call waits for a FRESH publish (used after DidChange — the
// sticky received flag would otherwise return the stale pre-edit set).
func (c *Client) ClearDiagnostics(uri string) {
	c.diagsMu.Lock()
	delete(c.diags, uri)
	delete(c.received, uri)
	c.diagsMu.Unlock()
}

// --- lenient decoders ---

func textDocumentPositionParams(uri string, pos Position) map[string]interface{} {
	return map[string]interface{}{
		"textDocument": map[string]interface{}{"uri": uri},
		"position":     map[string]interface{}{"line": pos.Line, "character": pos.Character},
	}
}

// decodeLocations accepts Location | []Location | []LocationLink | null.
func decodeLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var one Location
	if err := json.Unmarshal(raw, &one); err == nil && one.URI != "" {
		return []Location{one}
	}
	var many []Location
	if err := json.Unmarshal(raw, &many); err == nil && len(many) > 0 && many[0].URI != "" {
		return many
	}
	// LocationLink: {targetUri, targetSelectionRange, targetRange}
	var links []struct {
		TargetURI            string `json:"targetUri"`
		TargetSelectionRange struct {
			Start Position `json:"start"`
			End   Position `json:"end"`
		} `json:"targetSelectionRange"`
	}
	if err := json.Unmarshal(raw, &links); err == nil {
		out := make([]Location, 0, len(links))
		for _, l := range links {
			if l.TargetURI == "" {
				continue
			}
			loc := Location{URI: l.TargetURI}
			loc.Range.Start = l.TargetSelectionRange.Start
			loc.Range.End = l.TargetSelectionRange.End
			out = append(out, loc)
		}
		return out
	}
	return nil
}

// decodeSymbols accepts []DocumentSymbol (hierarchical) or
// []SymbolInformation (flat).
func decodeSymbols(raw json.RawMessage) []SymbolInfo {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	type docSymbol struct {
		Name           string `json:"name"`
		Kind           int    `json:"kind"`
		SelectionRange struct {
			Start Position `json:"start"`
		} `json:"selectionRange"`
		Children []docSymbol `json:"children"`
	}
	var hierarchical []docSymbol
	if err := json.Unmarshal(raw, &hierarchical); err == nil && len(hierarchical) > 0 && hierarchical[0].Name != "" {
		// Distinguish from SymbolInformation: DocumentSymbol has selectionRange,
		// SymbolInformation has location. A flat response decodes here too (the
		// extra fields are just zero), so probe for the flat marker first.
		var probe []struct {
			Location *struct {
				URI string `json:"uri"`
			} `json:"location"`
		}
		flat := false
		if err := json.Unmarshal(raw, &probe); err == nil && len(probe) > 0 && probe[0].Location != nil && probe[0].Location.URI != "" {
			flat = true
		}
		if !flat {
			var out []SymbolInfo
			var walk func(parent string, syms []docSymbol)
			walk = func(parent string, syms []docSymbol) {
				for _, s := range syms {
					out = append(out, SymbolInfo{Name: s.Name, Kind: s.Kind, Container: parent, Line: s.SelectionRange.Start.Line})
					next := s.Name
					if parent != "" {
						next = parent + "." + s.Name
					}
					walk(next, s.Children)
				}
			}
			walk("", hierarchical)
			return out
		}
	}
	var flat []struct {
		Name          string `json:"name"`
		Kind          int    `json:"kind"`
		ContainerName string `json:"containerName"`
		Location      struct {
			Range struct {
				Start Position `json:"start"`
			} `json:"range"`
		} `json:"location"`
	}
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil
	}
	out := make([]SymbolInfo, 0, len(flat))
	for _, s := range flat {
		out = append(out, SymbolInfo{Name: s.Name, Kind: s.Kind, Container: s.ContainerName, Line: s.Location.Range.Start.Line})
	}
	return out
}

// decodeHover accepts {contents: string | MarkupContent | (string|MarkedString)[]}.
func decodeHover(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(raw, &h); err != nil || len(h.Contents) == 0 {
		return ""
	}
	return strings.TrimSpace(decodeHoverContents(h.Contents))
}

func decodeHoverContents(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var markup struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &markup); err == nil && markup.Value != "" {
		return markup.Value
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if t := decodeHoverContents(p); t != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(t)
			}
		}
		return b.String()
	}
	return ""
}
