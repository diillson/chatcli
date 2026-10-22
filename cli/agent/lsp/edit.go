/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * LSP requests behind the @lsp implementation / workspace_symbol / rename
 * verbs, plus the on-disk application of a WorkspaceEdit.
 *
 * Rename is the one request whose answer is not a listing but an edit set:
 * the server returns a WorkspaceEdit in one of two spec-legal shapes
 * (`changes` keyed by URI, or `documentChanges` carrying versioned
 * TextDocumentEdits and, optionally, file create/rename/delete operations).
 * Both decode to the same per-document edit map; resource operations are
 * reported as unsupported rather than silently dropped. Applying the edits
 * is done here too so the whole "server said → disk changed" path is one
 * testable unit: columns are UTF-16 code units (the LSP default encoding),
 * every file is checked against the workspace boundary and re-stat'ed
 * before its atomic write, and nothing is written unless every edit in the
 * set could be applied in memory first.
 */
package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/diillson/chatcli/pkg/fspath"
	"github.com/diillson/chatcli/utils"
)

// Range is a start/end span in a document (0-based LSP positions, UTF-16
// columns).
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// TextEdit is one replacement inside a document.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit is the per-document edit set a rename produces. Keys are
// document URIs. Unsupported describes resource operations (create, rename
// or delete of whole files) the tool does not apply.
type WorkspaceEdit struct {
	Changes     map[string][]TextEdit
	Unsupported []string
}

// WorkspaceSymbol is one workspace/symbol hit: a SymbolInfo that also knows
// which document it lives in.
type WorkspaceSymbol struct {
	Name      string
	Kind      int
	Container string
	URI       string
	Line      int // zero-based
}

// KindLabel renders the numeric LSP SymbolKind.
func (s WorkspaceSymbol) KindLabel() string { return SymbolInfo{Kind: s.Kind}.KindLabel() }

// Implementation resolves the implementation location(s) of the symbol at
// pos (the concrete types behind an interface, the overrides of a method).
func (c *Client) Implementation(uri string, pos Position) ([]Location, error) {
	res, err := c.call("textDocument/implementation", textDocumentPositionParams(uri, pos), navTimeout)
	if err != nil {
		return nil, err
	}
	return decodeLocations(res), nil
}

// WorkspaceSymbols searches the whole workspace for symbols matching query.
func (c *Client) WorkspaceSymbols(query string) ([]WorkspaceSymbol, error) {
	res, err := c.call("workspace/symbol", map[string]interface{}{"query": query}, navTimeout)
	if err != nil {
		return nil, err
	}
	return decodeWorkspaceSymbols(res), nil
}

// Rename asks the server for the edit set that renames the symbol at pos to
// newName. Nothing is applied; see ApplyWorkspaceEdit.
func (c *Client) Rename(uri string, pos Position, newName string) (WorkspaceEdit, error) {
	params := textDocumentPositionParams(uri, pos)
	params["newName"] = newName
	res, err := c.call("textDocument/rename", params, navTimeout)
	if err != nil {
		return WorkspaceEdit{}, err
	}
	return decodeWorkspaceEdit(res), nil
}

// decodeWorkspaceSymbols accepts []SymbolInformation or []WorkspaceSymbol
// (whose location may carry only a uri).
func decodeWorkspaceSymbols(raw json.RawMessage) []WorkspaceSymbol {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []struct {
		Name          string `json:"name"`
		Kind          int    `json:"kind"`
		ContainerName string `json:"containerName"`
		Location      struct {
			URI   string `json:"uri"`
			Range *Range `json:"range"`
		} `json:"location"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]WorkspaceSymbol, 0, len(items))
	for _, it := range items {
		if it.Name == "" {
			continue
		}
		s := WorkspaceSymbol{Name: it.Name, Kind: it.Kind, Container: it.ContainerName, URI: it.Location.URI}
		if it.Location.Range != nil {
			s.Line = it.Location.Range.Start.Line
		}
		out = append(out, s)
	}
	return out
}

// decodeWorkspaceEdit accepts {changes:{uri:[edit]}} and/or
// {documentChanges:[TextDocumentEdit | CreateFile | RenameFile | DeleteFile]}.
func decodeWorkspaceEdit(raw json.RawMessage) WorkspaceEdit {
	out := WorkspaceEdit{Changes: map[string][]TextEdit{}}
	if len(raw) == 0 || string(raw) == "null" {
		return out
	}
	var we struct {
		Changes         map[string][]TextEdit `json:"changes"`
		DocumentChanges []struct {
			Kind         string `json:"kind"`
			URI          string `json:"uri"`
			OldURI       string `json:"oldUri"`
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []TextEdit `json:"edits"`
		} `json:"documentChanges"`
	}
	if err := json.Unmarshal(raw, &we); err != nil {
		return out
	}
	for uri, edits := range we.Changes {
		out.Changes[uri] = append(out.Changes[uri], edits...)
	}
	for _, dc := range we.DocumentChanges {
		switch dc.Kind {
		case "":
			if dc.TextDocument.URI != "" {
				out.Changes[dc.TextDocument.URI] = append(out.Changes[dc.TextDocument.URI], dc.Edits...)
			}
		case "rename":
			out.Unsupported = append(out.Unsupported, fmt.Sprintf("%s %s -> %s", dc.Kind, dc.OldURI, dc.URI))
		default:
			out.Unsupported = append(out.Unsupported, dc.Kind+" "+dc.URI)
		}
	}
	return out
}

// URIToPath converts a file:// URI to a filesystem path.
func URIToPath(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.Scheme == "file" {
		return filepath.FromSlash(u.Path)
	}
	return strings.TrimPrefix(uri, "file://")
}

// ApplyTextEdits applies edits to content and returns the new content. Edits
// are applied back to front so earlier offsets stay valid; overlapping edits
// or a range past the end of its line are refused (the usual sign that the
// file changed since the server analyzed it).
func ApplyTextEdits(content string, edits []TextEdit) (string, error) {
	type span struct {
		start, end int
		text       string
	}
	spans := make([]span, 0, len(edits))
	for _, e := range edits {
		start, err := offsetOf(content, e.Range.Start)
		if err != nil {
			return "", err
		}
		end, err := offsetOf(content, e.Range.End)
		if err != nil {
			return "", err
		}
		if end < start {
			return "", fmt.Errorf("edit range ends before it starts (L%d:%d)", e.Range.Start.Line+1, e.Range.Start.Character+1)
		}
		spans = append(spans, span{start, end, e.NewText})
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	out := content
	prevStart := len(content) + 1
	for _, s := range spans {
		if s.end > prevStart {
			return "", errors.New("overlapping edits in the rename result")
		}
		spliced, err := spliceAtRuneBoundaries(out, s.start, s.end, s.text)
		if err != nil {
			return "", err
		}
		out = spliced
		prevStart = s.start
	}
	return out, nil
}

// spliceAtRuneBoundaries replaces content[start:end] with text, refusing
// an offset that falls inside a UTF-8 sequence: a server that miscounts
// UTF-16 units would otherwise corrupt the character it lands on.
func spliceAtRuneBoundaries(content string, start, end int, text string) (string, error) {
	if start < 0 || end > len(content) || end < start {
		return "", fmt.Errorf("edit offsets %d..%d outside the content (%d bytes)", start, end, len(content))
	}
	if !utf8.RuneStart(byteAt(content, start)) || !utf8.RuneStart(byteAt(content, end)) {
		return "", fmt.Errorf("edit offsets %d..%d split a UTF-8 character", start, end)
	}
	var b strings.Builder
	b.Grow(len(content) - (end - start) + len(text))
	b.WriteString(content[:start])
	b.WriteString(text)
	b.WriteString(content[end:])
	return b.String(), nil
}

// byteAt returns content[i], or a rune-start byte at the end of content.
func byteAt(content string, i int) byte {
	if i >= len(content) {
		return 0
	}
	return content[i]
}

// offsetOf maps a 0-based (line, UTF-16 column) position to a byte offset.
func offsetOf(content string, pos Position) (int, error) {
	if pos.Line < 0 || pos.Character < 0 {
		return 0, fmt.Errorf("negative position L%d:%d", pos.Line+1, pos.Character+1)
	}
	off := 0
	for line := 0; line < pos.Line; line++ {
		nl := strings.IndexByte(content[off:], '\n')
		if nl < 0 {
			return 0, fmt.Errorf("line %d is past the end of the file (%d lines)", pos.Line+1, line+1)
		}
		off += nl + 1
	}
	lineEnd := len(content)
	if nl := strings.IndexByte(content[off:], '\n'); nl >= 0 {
		lineEnd = off + nl
	}
	units := 0
	for i, r := range content[off:lineEnd] {
		if units >= pos.Character {
			return off + i, nil
		}
		units += len(utf16.Encode([]rune{r}))
	}
	if units < pos.Character {
		return 0, fmt.Errorf("column %d is past the end of line %d", pos.Character+1, pos.Line+1)
	}
	return lineEnd, nil
}

// ApplyWorkspaceEdit writes edit to disk and returns the files it touched,
// root-relative. Every document must resolve (symlinks included) inside
// root; every edit set must apply in memory before any file is written; and
// each file is re-stat'ed right before its atomic replacement so a change
// that raced the rename refuses instead of being clobbered. Resource
// operations in edit.Unsupported refuse the whole apply.
func ApplyWorkspaceEdit(root string, edit WorkspaceEdit) ([]string, error) {
	if len(edit.Unsupported) > 0 {
		return nil, fmt.Errorf("rename needs file operations this tool does not apply: %s", strings.Join(edit.Unsupported, "; "))
	}
	boundary, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if resolved, rerr := filepath.EvalSymlinks(boundary); rerr == nil {
		boundary = resolved
	}
	type pending struct {
		path    string
		content []byte
		mode    os.FileMode
		modTime int64
		size    int64
	}
	var plan []pending
	uris := make([]string, 0, len(edit.Changes))
	for uri := range edit.Changes {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	for _, uri := range uris {
		path, err := filepath.Abs(URIToPath(uri))
		if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !fspath.WithinBoundary(resolved, boundary) {
			return nil, fmt.Errorf("refusing to edit %s: outside the workspace %s", path, root)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(resolved) //#nosec G304 -- path produced by the language server and checked against the workspace boundary above
		if err != nil {
			return nil, err
		}
		updated, err := ApplyTextEdits(string(data), edit.Changes[uri])
		if err != nil {
			return nil, fmt.Errorf("%s: %w (did the file change since the server analyzed it?)", path, err)
		}
		plan = append(plan, pending{resolved, []byte(updated), info.Mode().Perm(), info.ModTime().UnixNano(), info.Size()})
	}
	var touched []string
	for _, p := range plan {
		info, err := os.Stat(p.path)
		if err != nil {
			return touched, err
		}
		if info.ModTime().UnixNano() != p.modTime || info.Size() != p.size {
			return touched, fmt.Errorf("refusing to write %s: it changed while the rename was being prepared", p.path)
		}
		if err := utils.AtomicWriteFile(p.path, p.content, p.mode); err != nil {
			return touched, err
		}
		if rel, rerr := filepath.Rel(boundary, p.path); rerr == nil {
			touched = append(touched, rel)
		} else {
			touched = append(touched, p.path)
		}
	}
	return touched, nil
}
