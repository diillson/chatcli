/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinJSONPlugin — structured-data reader as an @json ReAct tool.
 *
 * The agent often has large JSON/YAML payloads on disk (an @http response
 * saved to a file, a tool result that overflowed to the overflow dir) and
 * needs one value, the keys at a path or the shape of an unknown document
 * without pulling the whole file into its context. Every verb is read-only,
 * pure Go, bounded in input and output, and workspace-bounded through the
 * engine's path guard.
 */
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/coder/engine"
	"github.com/diillson/chatcli/pkg/textfold"
)

const (
	jsonToolMaxFileBytes = 20 << 20
	jsonToolOutputCap    = 24_000
	jsonToolDefaultLimit = 20
	jsonToolMaxLimit     = 200
	jsonToolDefaultDepth = 3
	jsonToolMaxDepth     = 8
	jsonToolMaxKeys      = 60
	jsonToolSample       = 50
	jsonToolMaxRegex     = 200
	jsonToolCutHint      = "narrow with a deeper path, select or limit"
)

// BuiltinJSONPlugin is the @json tool.
type BuiltinJSONPlugin struct{}

// NewBuiltinJSONPlugin returns a ready-to-register plugin.
func NewBuiltinJSONPlugin() *BuiltinJSONPlugin { return &BuiltinJSONPlugin{} }

// Name returns "@json".
func (*BuiltinJSONPlugin) Name() string { return "@json" }

// Description surfaces the tool in the catalog (i18n).
func (*BuiltinJSONPlugin) Description() string { return i18n.T("plugins.json.description") }

// Usage explains the canonical invocation.
func (*BuiltinJSONPlugin) Usage() string {
	return `<tool_call name="@json" args='{"cmd":"get","args":{"file":"resp.json","path":"data.items[0].name"}}' />

Input is a workspace file (file, JSON or YAML, ≤20 MB) or inline text (text).
Paths: dotted keys, [n] / [-1] indexes, * wildcard over arrays and objects
(data.items[*].id, users.*.email). Output is capped: narrow with path/select/limit.

Subcommands (cmd + args):
  get      {file|text, path}                     value at path, pretty-printed
  keys     {file|text, path?}                    keys (or length) and types one level down
  query    {file|text, path?, where, select?, limit?}
                                                 filter an array of objects. where:
                                                 "field=value", "field!=value", "field~regex",
                                                 joined with " && "; select picks fields
  schema   {file|text, depth?}                   inferred shape: key → type, array element
                                                 types, key frequency (default depth 3)
  validate {file|text}                           parse check with line/column on error
  pretty   {file|text, compact?}                 re-indent (or minify) the document`
}

// Version is semver.
func (*BuiltinJSONPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinJSONPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinJSONPlugin) Schema() string {
	src := []map[string]interface{}{
		{"name": "file", "type": "string", "required": false, "description": "Workspace path of a .json/.yaml/.yml file (≤20 MB). Either file or text is required."},
		{"name": "text", "type": "string", "required": false, "description": "Inline JSON or YAML document."},
	}
	pathFlag := func(req bool, desc string) map[string]interface{} {
		return map[string]interface{}{"name": "path", "type": "string", "required": req, "description": desc}
	}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; flat {file,path} defaults to get",
		"subcommands": []map[string]interface{}{
			{"name": "get", "description": "Value at a path, pretty-printed (array when the path has a wildcard).",
				"flags":    append(append([]map[string]interface{}{}, src...), pathFlag(true, "Dotted path: data.items[3].name, users.*.email, items[-1]")),
				"examples": []string{`{"cmd":"get","args":{"file":"resp.json","path":"data.items[0].name"}}`, `{"cmd":"get","args":{"text":"{\"a\":{\"b\":1}}","path":"a.b"}}`}},
			{"name": "keys", "description": "Keys and value types at a path (one level); length and element types for arrays.",
				"flags":    append(append([]map[string]interface{}{}, src...), pathFlag(false, "Path to inspect; default: document root")),
				"examples": []string{`{"cmd":"keys","args":{"file":"resp.json"}}`, `{"cmd":"keys","args":{"file":"resp.json","path":"data"}}`}},
			{"name": "query", "description": "Filter an array of objects (or the values of an object) and project fields.",
				"flags": append(append([]map[string]interface{}{}, src...),
					pathFlag(false, "Path of the array to filter; default: root"),
					map[string]interface{}{"name": "where", "type": "string", "required": true, "description": `Condition: "status=active", "name~^prod", "id!=0"; several joined with " && "`},
					map[string]interface{}{"name": "select", "type": "array", "required": false, "description": "Fields to keep in each match, e.g. [\"id\",\"name\"]"},
					map[string]interface{}{"name": "limit", "type": "integer", "required": false, "description": "Max matches returned (default 20, max 200)"}),
				"examples": []string{`{"cmd":"query","args":{"file":"resp.json","path":"data.items","where":"status=failed","select":["id","name"],"limit":10}}`}},
			{"name": "schema", "description": "Inferred shape of an unknown payload: key → type, array element types, key frequency.",
				"flags":    append(append([]map[string]interface{}{}, src...), map[string]interface{}{"name": "depth", "type": "integer", "required": false, "description": "Nesting depth to describe (default 3, max 8)"}),
				"examples": []string{`{"cmd":"schema","args":{"file":"resp.json"}}`, `{"cmd":"schema","args":{"file":"values.yaml","depth":5}}`}},
			{"name": "validate", "description": "Parse check; reports line and column of the first error.",
				"flags":    src,
				"examples": []string{`{"cmd":"validate","args":{"file":"config.json"}}`}},
			{"name": "pretty", "description": "Re-indent the document, or minify it with compact:true.",
				"flags":    append(append([]map[string]interface{}{}, src...), map[string]interface{}{"name": "compact", "type": "boolean", "required": false, "description": "Emit a single-line document"}),
				"examples": []string{`{"cmd":"pretty","args":{"text":"{\"a\":1}"}}`, `{"cmd":"pretty","args":{"file":"big.json","compact":true}}`}},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinJSONPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback: every verb answers at once.
func (p *BuiltinJSONPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	cmd, raw, err := parseJSONToolInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@json: %w", err)
	}
	a := jsonToolArgsFrom(raw)
	out, err := runJSONVerb(cmd, a)
	if err != nil {
		return "", fmt.Errorf("@json %s: %w", cmd, err)
	}
	return capToolOutput(out, jsonToolOutputCap, jsonToolCutHint), nil
}

func runJSONVerb(cmd string, a jsonToolArgs) (string, error) {
	data, src, err := readStructuredInput(a)
	if err != nil {
		return "", err
	}
	root, format, perr := parseStructured(data, strings.ToLower(filepath.Ext(a.file)))
	if cmd == "validate" {
		if perr != nil {
			return fmt.Sprintf("invalid: %s — %v", src, perr), nil
		}
		return fmt.Sprintf("valid %s: %s, %d bytes (%s)", format, typeSummary(root), len(data), src), nil
	}
	if perr != nil {
		return "", perr
	}
	switch cmd {
	case "get":
		if a.path == "" {
			return "", errors.New(`"path" is required (use keys or schema to explore the document)`)
		}
		hits, err := jsonPathSelectRoot(root, a.path)
		if err != nil {
			return "", err
		}
		if len(hits) == 1 {
			return renderJSON(hits[0], false), nil
		}
		return fmt.Sprintf("%d matches for %s:\n%s", len(hits), a.path, renderJSON(hits, false)), nil
	case "keys":
		node, err := jsonPathOne(root, a.path)
		if err != nil {
			return "", err
		}
		return jsonKeysReport(node, pathLabel(a.path)), nil
	case "query":
		node, err := jsonPathOne(root, a.path)
		if err != nil {
			return "", err
		}
		return jsonQuery(node, a)
	case "schema":
		var b strings.Builder
		inferShape(&b, root, clampInt(a.depth, jsonToolDefaultDepth, jsonToolMaxDepth), 0)
		return fmt.Sprintf("shape of %s (%s, depth %d):\n%s", src, format, clampInt(a.depth, jsonToolDefaultDepth, jsonToolMaxDepth), b.String()), nil
	case "pretty":
		return renderJSON(root, a.compact), nil
	}
	return "", fmt.Errorf("unknown cmd %q (valid: get|keys|query|schema|validate|pretty)", cmd)
}

// --- args ---------------------------------------------------------------

type jsonToolArgs struct {
	file, text, path, where string
	sel                     []string
	limit, depth            int
	compact                 bool
}

func jsonToolArgsFrom(raw map[string]json.RawMessage) jsonToolArgs {
	return jsonToolArgs{
		file:    jsonString(raw, "file", "filepath", "filename"),
		text:    jsonString(raw, "text", "json", "yaml", "content", "input"),
		path:    jsonString(raw, "path", "at", "pointer"),
		where:   jsonString(raw, "where", "filter"),
		sel:     contextStringSlice(raw, "select", "fields"),
		limit:   jsonInt(raw, "limit", "max"),
		depth:   jsonInt(raw, "depth"),
		compact: jsonBool(raw, "compact", "minify"),
	}
}

func parseJSONToolInvocation(args []string) (string, map[string]json.RawMessage, error) {
	return parseVerbInvocation(args, canonicalJSONCmd, inferJSONCmd, "file",
		map[string]bool{"select": true}, map[string]bool{"limit": true, "depth": true})
}

func canonicalJSONCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "get", "read", "value":
		return "get"
	case "keys", "ls", "list":
		return "keys"
	case "query", "filter", "find", "where":
		return "query"
	case "schema", "shape", "describe":
		return "schema"
	case "validate", "check", "lint":
		return "validate"
	case "pretty", "format", "fmt":
		return "pretty"
	}
	return ""
}

// inferJSONCmd picks the verb for flat native args: a filter means query,
// a path means get, anything else is the safe exploratory default.
func inferJSONCmd(raw map[string]json.RawMessage) string {
	switch {
	case jsonHas(raw, "where", "filter", "select"):
		return "query"
	case jsonHas(raw, "path", "at", "pointer"):
		return "get"
	}
	return "keys"
}

// parseVerbInvocation is the shared envelope/argv parser for verb-style
// builtins: a JSON object (envelope {cmd,args} or flat native args) or the
// flattened "verb --key value" argv. Unknown cmd values are reported, never
// silently remapped.
func parseVerbInvocation(args []string, canon func(string) string, infer func(map[string]json.RawMessage) string,
	primary string, arrayKeys, intKeys map[string]bool) (string, map[string]json.RawMessage, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if payload == "" {
		return "", nil, errors.New("empty args")
	}
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", nil, fmt.Errorf("parse envelope: %w", err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		inner := raw
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(rargs, &m); err != nil {
				return "", nil, fmt.Errorf("parse args: %w", err)
			}
			inner = m
		} else {
			delete(inner, "cmd")
		}
		cmd := canon(cmdStr)
		if cmd == "" {
			if cmdStr != "" {
				return "", nil, fmt.Errorf("unknown cmd %q", cmdStr)
			}
			cmd = infer(inner)
		}
		return cmd, inner, nil
	}
	cmd := canon(args[0])
	if cmd == "" {
		return "", nil, fmt.Errorf("expected JSON envelope or verb; got %q", args[0])
	}
	inner := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(argvInner(args[1:], primary, arrayKeys, intKeys)), &inner); err != nil {
		return "", nil, fmt.Errorf("parse argv: %w", err)
	}
	return cmd, inner, nil
}

// jsonHas reports whether any of keys is present in raw.
func jsonHas(raw map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		if _, ok := raw[k]; ok {
			return true
		}
	}
	return false
}

func clampInt(v, def, maxV int) int {
	if v <= 0 {
		return def
	}
	if v > maxV {
		return maxV
	}
	return v
}

// capToolOutput bounds a model-facing result on a rune boundary and says
// what was cut and how to ask for less.
func capToolOutput(s string, capBytes int, hint string) string {
	if len(s) <= capBytes {
		return s
	}
	return textfold.Cut(s, capBytes) + fmt.Sprintf("\n… [output cut: %d of %d bytes shown — %s]", capBytes, len(s), hint)
}

// --- loading --------------------------------------------------------------

func readStructuredInput(a jsonToolArgs) ([]byte, string, error) {
	switch {
	case a.file != "" && a.text != "":
		return nil, "", errors.New(`give either "file" or "text", not both`)
	case a.file != "":
		if err := engine.ValidateReadPath(a.file); err != nil {
			return nil, "", err
		}
		info, err := os.Stat(a.file)
		if err != nil {
			return nil, "", err
		}
		if info.IsDir() {
			return nil, "", fmt.Errorf("%s is a directory", a.file)
		}
		if info.Size() > jsonToolMaxFileBytes {
			return nil, "", fmt.Errorf("%s is %d bytes; the limit is %d", a.file, info.Size(), jsonToolMaxFileBytes)
		}
		data, err := os.ReadFile(a.file) // #nosec G304 -- validated against the workspace boundary by engine.ValidateReadPath
		return data, a.file, err
	case a.text != "":
		return []byte(a.text), "inline text", nil
	}
	return nil, "", errors.New(`"file" or "text" is required`)
}

// parseStructured decodes JSON first (numbers kept verbatim), then YAML when
// the extension says so or the text is not JSON but is a YAML mapping/list.
// Text that opens with { or [ is judged as JSON only: YAML's flow syntax
// would accept a broken JSON document and hide the error the caller wants.
func parseStructured(data []byte, ext string) (interface{}, string, error) {
	yamlExt := ext == ".yaml" || ext == ".yml"
	jsonErr := errors.New("not a JSON document")
	if !yamlExt {
		v, format, err := parseJSONOnly(data)
		if t := bytes.TrimSpace(data); err == nil || (len(t) > 0 && (t[0] == '{' || t[0] == '[')) {
			return v, format, err
		}
		jsonErr = err
	}
	var y interface{}
	if yerr := yaml.Unmarshal(data, &y); yerr != nil {
		if yamlExt {
			return nil, "", fmt.Errorf("invalid YAML: %w", yerr)
		}
	} else if yamlExt || isContainer(y) {
		return yamlNormalize(y), "yaml", nil
	}
	return nil, "", jsonErr
}

func parseJSONOnly(data []byte) (interface{}, string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, "", describeJSONError(data, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, "", fmt.Errorf("invalid JSON at %s: trailing data after the document", lineCol(data, int(dec.InputOffset())))
	}
	return v, "json", nil
}

func isContainer(v interface{}) bool {
	switch v.(type) {
	case map[string]interface{}, map[interface{}]interface{}, []interface{}:
		return true
	}
	return false
}

// yamlNormalize turns yaml.v3's non-string-keyed maps into JSON-shaped ones.
func yamlNormalize(v interface{}) interface{} {
	switch n := v.(type) {
	case map[string]interface{}:
		for k, e := range n {
			n[k] = yamlNormalize(e)
		}
		return n
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(n))
		for k, e := range n {
			out[fmt.Sprint(k)] = yamlNormalize(e)
		}
		return out
	case []interface{}:
		for i, e := range n {
			n[i] = yamlNormalize(e)
		}
	}
	return v
}

// describeJSONError turns a decoder error into a line/column message.
func describeJSONError(data []byte, err error) error {
	var syn *json.SyntaxError
	if !errors.As(err, &syn) {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	// Offset is one past the offending byte.
	off := int(syn.Offset)
	if off > 0 {
		off--
	}
	if off > len(data) {
		off = len(data)
	}
	start, end := off-20, off+20
	if start < 0 {
		start = 0
	}
	if end > len(data) {
		end = len(data)
	}
	return fmt.Errorf("invalid JSON at %s: %s (near %q)", lineCol(data, off), syn.Error(), strings.TrimSpace(string(data[start:end])))
}

// lineCol renders a byte offset as "line L, column C" (1-based).
func lineCol(data []byte, off int) string {
	if off > len(data) {
		off = len(data)
	}
	line := bytes.Count(data[:off], []byte("\n")) + 1
	col := off - bytes.LastIndexByte(data[:off], '\n')
	return fmt.Sprintf("line %d, column %d", line, col)
}

// --- paths ----------------------------------------------------------------

type jsonSeg struct {
	key   string
	idx   int
	isIdx bool
	wild  bool
}

func (s jsonSeg) String() string {
	switch {
	case s.wild:
		return "*"
	case s.isIdx:
		return "[" + s.key + "]"
	}
	return s.key
}

func parseJSONPath(p string) ([]jsonSeg, error) {
	p = strings.TrimPrefix(strings.TrimSpace(p), "$")
	var segs []jsonSeg
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			segs = append(segs, segFromToken(cur.String()))
			cur.Reset()
		}
	}
	for i := 0; i < len(p); i++ {
		switch c := p[i]; c {
		case '.':
			flush()
		case '[':
			flush()
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unclosed '[' in path %q", p)
			}
			segs = append(segs, segFromToken(strings.Trim(p[i+1:i+end], `"' `)))
			i += end
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return segs, nil
}

func segFromToken(t string) jsonSeg {
	if t == "*" {
		return jsonSeg{wild: true}
	}
	if n, err := strconv.Atoi(t); err == nil && !strings.HasPrefix(t, "+") {
		return jsonSeg{key: t, idx: n, isIdx: true}
	}
	return jsonSeg{key: t}
}

func pathLabel(p string) string {
	if strings.TrimSpace(p) == "" {
		return "root"
	}
	return p
}

func jsonPathSelectRoot(root interface{}, path string) ([]interface{}, error) {
	segs, err := parseJSONPath(path)
	if err != nil {
		return nil, err
	}
	hits, err := jsonPathSelect(root, segs, "root")
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, fmt.Errorf("no match for %s", path)
	}
	return hits, nil
}

// jsonPathOne resolves a path to a single node (root when empty).
func jsonPathOne(root interface{}, path string) (interface{}, error) {
	if strings.TrimSpace(path) == "" {
		return root, nil
	}
	hits, err := jsonPathSelectRoot(root, path)
	if err != nil {
		return nil, err
	}
	if len(hits) > 1 {
		return hits, nil
	}
	return hits[0], nil
}

// jsonPathSelect walks segs from node, fanning out on wildcards. The error
// names the segment that did not resolve and what was there instead.
func jsonPathSelect(node interface{}, segs []jsonSeg, at string) ([]interface{}, error) {
	if len(segs) == 0 {
		return []interface{}{node}, nil
	}
	s := segs[0]
	switch n := node.(type) {
	case map[string]interface{}:
		if s.wild {
			return fanOut(sortedObjKeys(n), func(k string) interface{} { return n[k] }, segs[1:], at)
		}
		v, ok := n[s.key]
		if !ok {
			return nil, fmt.Errorf("key %q not found at %s (keys: %s)", s.key, at, strings.Join(headKeys(n, 15), ", "))
		}
		return jsonPathSelect(v, segs[1:], at+"."+s.key)
	case []interface{}:
		if s.wild {
			idx := make([]string, len(n))
			for i := range n {
				idx[i] = strconv.Itoa(i)
			}
			return fanOut(idx, func(k string) interface{} { i, _ := strconv.Atoi(k); return n[i] }, segs[1:], at)
		}
		if !s.isIdx {
			return nil, fmt.Errorf("%s is an array of %d; use an index like [0] or * instead of %q", at, len(n), s.key)
		}
		i := s.idx
		if i < 0 {
			i += len(n)
		}
		if i < 0 || i >= len(n) {
			return nil, fmt.Errorf("index %d out of range at %s (length %d)", s.idx, at, len(n))
		}
		return jsonPathSelect(n[i], segs[1:], at+"["+s.key+"]")
	}
	return nil, fmt.Errorf("cannot descend into %s at %s with %s", typeSummary(node), at, s)
}

// fanOut applies the rest of the path to every child, skipping the ones
// where it does not resolve (heterogeneous arrays are the common case).
func fanOut(keys []string, get func(string) interface{}, rest []jsonSeg, at string) ([]interface{}, error) {
	var out []interface{}
	for _, k := range keys {
		hits, err := jsonPathSelect(get(k), rest, at+"."+k)
		if err == nil {
			out = append(out, hits...)
		}
	}
	return out, nil
}

func sortedObjKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func headKeys(m map[string]interface{}, n int) []string {
	keys := sortedObjKeys(m)
	if len(keys) > n {
		keys = append(keys[:n], fmt.Sprintf("… +%d", len(keys)-n))
	}
	return keys
}

// --- rendering ------------------------------------------------------------

func renderJSON(v interface{}, compact bool) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if !compact {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(buf.String(), "\n")
}

func jsonTypeName(v interface{}) string {
	switch v.(type) {
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	case string:
		return "string"
	case bool:
		return "bool"
	case nil:
		return "null"
	}
	return "number"
}

// typeSummary is the one-token description used in listings: object{3},
// array[12], string(40), number, bool, null.
func typeSummary(v interface{}) string {
	switch n := v.(type) {
	case map[string]interface{}:
		return fmt.Sprintf("object{%d}", len(n))
	case []interface{}:
		return fmt.Sprintf("array[%d]", len(n))
	case string:
		return fmt.Sprintf("string(%d)", len(n))
	}
	return jsonTypeName(v)
}

func jsonKeysReport(v interface{}, label string) string {
	var b strings.Builder
	switch n := v.(type) {
	case map[string]interface{}:
		fmt.Fprintf(&b, "%s: object with %d keys\n", label, len(n))
		for i, k := range sortedObjKeys(n) {
			if i == jsonToolMaxKeys {
				fmt.Fprintf(&b, "  … +%d more keys (use path to go deeper)\n", len(n)-i)
				break
			}
			fmt.Fprintf(&b, "  %s: %s\n", k, typeSummary(n[k]))
		}
	case []interface{}:
		fmt.Fprintf(&b, "%s: array of %d; element types: %s\n", label, len(n), elementTypes(n))
	default:
		fmt.Fprintf(&b, "%s: %s = %s\n", label, jsonTypeName(v), renderJSON(v, true))
	}
	return strings.TrimRight(b.String(), "\n")
}

func elementTypes(arr []interface{}) string {
	counts := map[string]int{}
	for _, e := range arr {
		counts[jsonTypeName(e)]++
	}
	parts := make([]string, 0, len(counts))
	for t, c := range counts {
		parts = append(parts, fmt.Sprintf("%s×%d", t, c))
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// inferShape writes the merged shape of v: objects list their keys, arrays
// merge the keys of up to jsonToolSample elements with how often each occurs.
// The first line continues the caller's "key: " prefix; children are
// indented one level deeper than indent.
func inferShape(b *strings.Builder, v interface{}, depth, indent int) {
	pad := strings.Repeat("  ", indent+1)
	switch n := v.(type) {
	case map[string]interface{}:
		fmt.Fprintf(b, "object {%d keys}\n", len(n))
		if depth == 0 {
			return
		}
		for i, k := range sortedObjKeys(n) {
			if i == jsonToolMaxKeys {
				fmt.Fprintf(b, "%s… +%d keys\n", pad, len(n)-i)
				return
			}
			fmt.Fprintf(b, "%s%s: ", pad, k)
			inferShape(b, n[k], depth-1, indent+1)
		}
	case []interface{}:
		fmt.Fprintf(b, "array [%d] of %s\n", len(n), elementTypes(n))
		if depth == 0 || len(n) == 0 {
			return
		}
		sample := n
		if len(sample) > jsonToolSample {
			sample = sample[:jsonToolSample]
		}
		seen := map[string]int{}
		first := map[string]interface{}{}
		for _, e := range sample {
			obj, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			for k, val := range obj {
				if seen[k] == 0 {
					first[k] = val
				}
				seen[k]++
			}
		}
		for i, k := range sortedObjKeys(first) {
			if i == jsonToolMaxKeys {
				fmt.Fprintf(b, "%s… +%d keys\n", pad, len(first)-i)
				return
			}
			fmt.Fprintf(b, "%s%s (%d/%d): ", pad, k, seen[k], len(sample))
			inferShape(b, first[k], depth-1, indent+1)
		}
	default:
		b.WriteString(jsonTypeName(v) + "\n")
	}
}

// --- query ----------------------------------------------------------------

type jsonCond struct {
	field, op, value string
	re               *regexp.Regexp
}

func parseJSONWhere(where string) ([]jsonCond, error) {
	var conds []jsonCond
	for _, part := range strings.Split(where, "&&") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		op := ""
		at := -1
		for _, cand := range []string{"!=", "~", "="} {
			if i := strings.Index(part, cand); i > 0 && (at < 0 || i < at) {
				op, at = cand, i
			}
		}
		if op == "" {
			return nil, fmt.Errorf(`condition %q must be "field=value", "field!=value" or "field~regex"`, part)
		}
		c := jsonCond{field: strings.TrimSpace(part[:at]), op: op, value: strings.TrimSpace(part[at+len(op):])}
		if op == "~" {
			if len(c.value) > jsonToolMaxRegex {
				return nil, fmt.Errorf("regex in %q is longer than %d chars", part, jsonToolMaxRegex)
			}
			re, err := regexp.Compile(c.value)
			if err != nil {
				return nil, fmt.Errorf("bad regex in %q: %w", part, err)
			}
			c.re = re
		}
		conds = append(conds, c)
	}
	if len(conds) == 0 {
		return nil, errors.New(`"where" is required, e.g. "status=active"`)
	}
	return conds, nil
}

func (c jsonCond) matches(elem interface{}) bool {
	segs, err := parseJSONPath(c.field)
	if err != nil {
		return false
	}
	hits, err := jsonPathSelect(elem, segs, "item")
	if err != nil || len(hits) == 0 {
		return c.op == "!="
	}
	got := scalarText(hits[0])
	switch c.op {
	case "=":
		return got == c.value
	case "!=":
		return got != c.value
	}
	return c.re.MatchString(got)
}

func scalarText(v interface{}) string {
	switch n := v.(type) {
	case string:
		return n
	case map[string]interface{}, []interface{}:
		return renderJSON(v, true)
	}
	return fmt.Sprint(v)
}

func jsonQuery(node interface{}, a jsonToolArgs) (string, error) {
	conds, err := parseJSONWhere(a.where)
	if err != nil {
		return "", err
	}
	var items []interface{}
	switch n := node.(type) {
	case []interface{}:
		items = n
	case map[string]interface{}:
		for _, k := range sortedObjKeys(n) {
			items = append(items, n[k])
		}
	default:
		return "", fmt.Errorf("%s is %s; query needs an array or object (set path)", pathLabel(a.path), typeSummary(node))
	}
	limit := clampInt(a.limit, jsonToolDefaultLimit, jsonToolMaxLimit)
	var out []interface{}
	total := 0
	for _, it := range items {
		ok := true
		for _, c := range conds {
			if !c.matches(it) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		total++
		if len(out) < limit {
			out = append(out, projectFields(it, a.sel))
		}
	}
	if total == 0 {
		return fmt.Sprintf("0 of %d items match %q at %s", len(items), a.where, pathLabel(a.path)), nil
	}
	return fmt.Sprintf("%d of %d items match %q at %s (showing %d):\n%s", total, len(items), a.where, pathLabel(a.path), len(out), renderJSON(out, false)), nil
}

func projectFields(item interface{}, fields []string) interface{} {
	obj, ok := item.(map[string]interface{})
	if !ok || len(fields) == 0 {
		return item
	}
	out := make(map[string]interface{}, len(fields))
	for _, f := range fields {
		if segs, err := parseJSONPath(f); err == nil {
			if hits, err := jsonPathSelect(obj, segs, "item"); err == nil && len(hits) == 1 {
				out[f] = hits[0]
			}
		}
	}
	return out
}
