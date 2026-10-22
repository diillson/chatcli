/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinCompressPlugin — the @compress tool. Lets the model (or a workflow)
 * explicitly compress an arbitrary payload on demand: paste a large log,
 * search dump, diff or JSON array — or point it at one or more files on disk
 * — and get back the content-aware reduced form, with the full original
 * offloaded to CCR and recoverable via @recall. This is the on-demand
 * complement to the automatic compression applied to tool output in the
 * agent/coder loop. The reduced form can be written to a file ("out")
 * instead of returned, so a 2 MB log never has to transit the conversation.
 *
 * Reaches the live compression layer through the shared CompressionAdapter
 * (see builtin_recall.go / SetCompressionAdapter).
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/pkg/textfold"
	"github.com/diillson/chatcli/utils"
)

const (
	// compressMaxFileBytes bounds one input file; compressMaxTotalBytes
	// bounds the concatenation of several. Past that the model should
	// @compress the pieces separately or let the coder loop paginate.
	compressMaxFileBytes  = 2 << 20
	compressMaxTotalBytes = 8 << 20
	// compressMaxFiles bounds the files list so a glob-sized request cannot
	// turn into a disk walk.
	compressMaxFiles = 32
	// compressOutPreviewBytes is how much of the compressed text is echoed
	// back when it was written to "out" (enough to carry the CCR key line).
	compressOutPreviewBytes = 400
)

// BuiltinCompressPlugin is the @compress tool.
type BuiltinCompressPlugin struct{}

// NewBuiltinCompressPlugin returns a ready-to-register plugin.
func NewBuiltinCompressPlugin() *BuiltinCompressPlugin { return &BuiltinCompressPlugin{} }

// Name returns "@compress".
func (*BuiltinCompressPlugin) Name() string { return "@compress" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinCompressPlugin) Description() string {
	return "Compress a large payload (logs, grep/search results, diffs, JSON arrays) into a content-aware, much smaller form to save context — the full original is preserved locally and recoverable with @recall. Give it inline content or one or more files from disk; optionally write the compressed form to a file instead of returning it. Use it before pasting bulky output into the conversation, or to shrink something you already received. Also reports session compression stats via the 'stats' subcommand."
}

// Usage explains the canonical invocation forms.
func (*BuiltinCompressPlugin) Usage() string {
	return `<tool_call name="@compress" args='{"content":"<large text>","hint":"auto"}' />
<tool_call name="@compress" args='{"file":"build.log","hint":"log"}' />
<tool_call name="@compress" args='{"files":["a.log","b.log"],"out":"/tmp/summary.txt"}' />

Args (one of content | file | files is required for compression):
  content : the payload to compress, inline
  file    : path of a file to compress (read from disk, max 2 MB)
  files   : several paths (array or comma-separated); each gets a header line,
            max 32 files / 2 MB each / 8 MB total
  hint    : auto | log | search | diff | json | code | prose  (optional; default auto)
  out     : optional path — write the compressed text there (atomic) and return
            a short summary instead of the text

Subcommand:
  {"cmd":"stats"}  -> session compression savings and CCR store footprint`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinCompressPlugin) Version() string { return "1.1.0" }

// Path is empty for builtin plugins.
func (*BuiltinCompressPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinCompressPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON object {content|file|files, hint?, out?} or {cmd:\"stats\"}",
		"flags": []map[string]interface{}{
			{"name": "content", "type": "string", "required": false, "description": "Payload to compress, inline (one of content|file|files)."},
			{"name": "file", "type": "string", "required": false, "description": "Path of a file to compress from disk (max 2 MB)."},
			{"name": "files", "type": "array", "required": false, "description": "Several file paths (array or comma-separated); concatenated with a header per file. Max 32 files, 2 MB each, 8 MB total."},
			{"name": "hint", "type": "string", "required": false, "description": "Content type: auto|log|search|diff|json|code|prose (default auto)."},
			{"name": "out", "type": "string", "required": false, "description": "Write the compressed text to this path (atomic) and return a summary instead of the text."},
			{"name": "cmd", "type": "string", "required": false, "description": "Use \"stats\" to report session compression savings instead of compressing."},
		},
		"examples": []string{
			`{"content":"<paste a long build log here>","hint":"log"}`,
			`{"file":"build.log","hint":"log"}`,
			`{"files":["api.log","worker.log"],"out":"/tmp/logs-compressed.txt"}`,
			`{"cmd":"stats"}`,
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// compressArgs is the parsed, lenient view of one @compress call.
type compressArgs struct {
	Cmd     string
	Content string
	Hint    string
	Files   []string
	Out     string
}

// parseCompressArgs accepts the flat JSON object, the {cmd,args} envelope,
// the flattened "--flag value" argv the agent loop produces, the bare
// "stats" token, and — for anything else — the whole payload as content.
func parseCompressArgs(args []string) (compressArgs, error) {
	var in compressArgs
	payload := strings.TrimSpace(strings.Join(args, " "))
	var raw map[string]json.RawMessage
	switch {
	case strings.HasPrefix(payload, "{"):
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return in, fmt.Errorf(`parse args: %w. Expected {"content":"...","hint":"auto"}`, err)
		}
		if inner, ok := raw["args"]; ok {
			var innerMap map[string]json.RawMessage
			if json.Unmarshal(inner, &innerMap) == nil {
				cmd := raw["cmd"]
				raw = innerMap
				if _, has := raw["cmd"]; !has && cmd != nil {
					raw["cmd"] = cmd
				}
			}
		}
	case strings.EqualFold(payload, "stats"):
		// The agent flattener reduces {"cmd":"stats"} to the bare argv token.
		in.Cmd = "stats"
		return in, nil
	case len(args) > 0 && strings.HasPrefix(strings.TrimSpace(args[0]), "--"):
		_, inner := argvToInnerJSON(args, map[string]bool{"files": true, "paths": true}, nil)
		_ = json.Unmarshal([]byte(inner), &raw)
	default:
		// Bare text: treat the whole payload as content with auto hint.
		in.Content = payload
		return in, nil
	}
	in.Cmd = strings.TrimSpace(jsonString(raw, "cmd"))
	in.Content = jsonString(raw, "content", "text", "payload")
	in.Hint = strings.TrimSpace(jsonString(raw, "hint", "type"))
	in.Out = strings.TrimSpace(jsonString(raw, "out", "output", "to"))
	if f := strings.TrimSpace(jsonString(raw, "file", "path")); f != "" {
		in.Files = append(in.Files, f)
	}
	in.Files = append(in.Files, trimNonEmpty(jsonStringList(raw, "files", "paths"))...)
	return in, nil
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinCompressPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinCompressPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentCompressionAdapter()
	if adapter == nil {
		return "", errors.New("@compress: no compression layer wired in this session")
	}
	if strings.TrimSpace(strings.Join(args, " ")) == "" {
		return "", errors.New(`@compress: empty args. Example: {"content":"...","hint":"auto"} or {"file":"build.log"}`)
	}
	in, err := parseCompressArgs(args)
	if err != nil {
		return "", fmt.Errorf("@compress: %w", err)
	}
	if strings.EqualFold(in.Cmd, "stats") {
		return adapter.Stats(), nil
	}

	content := in.Content
	if len(in.Files) > 0 {
		fromDisk, err := readCompressInputs(in.Files)
		if err != nil {
			return "", fmt.Errorf("@compress: %w", err)
		}
		if strings.TrimSpace(content) != "" {
			content += "\n"
		}
		content += fromDisk
	}
	if strings.TrimSpace(content) == "" {
		return "", errors.New(`@compress: "content", "file" or "files" is required (or use {"cmd":"stats"})`)
	}

	hint := in.Hint
	if hint == "" || strings.EqualFold(hint, "auto") {
		hint = ""
	}
	compressed, err := adapter.Compress(hint, content)
	if err != nil {
		return "", err
	}
	if in.Out == "" {
		return compressed, nil
	}
	outPath, err := writeCompressOut(in.Out, compressed)
	if err != nil {
		return "", fmt.Errorf("@compress: %w", err)
	}
	preview := textfold.Cut(compressed, compressOutPreviewBytes)
	if len(preview) < len(compressed) {
		preview += "…"
	}
	return fmt.Sprintf("compressed %d bytes to %d bytes and wrote the result to %s\n--- preview ---\n%s",
		len(content), len(compressed), outPath, preview), nil
}

// readCompressInputs reads every file (bounded per file, in total and in
// count) and concatenates them with a header line each so the compressor —
// and the model reading its output — can tell them apart.
func readCompressInputs(files []string) (string, error) {
	if len(files) > compressMaxFiles {
		return "", fmt.Errorf("too many files (%d, max %d)", len(files), compressMaxFiles)
	}
	var b strings.Builder
	total := 0
	for _, f := range files {
		path, err := compressResolvePath(f)
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory — pass files, not directories", f)
		}
		if info.Size() > compressMaxFileBytes {
			return "", fmt.Errorf("%s is %d bytes, over the %d-byte (2 MB) per-file limit — split it or compress a slice", f, info.Size(), compressMaxFileBytes)
		}
		total += int(info.Size())
		if total > compressMaxTotalBytes {
			return "", fmt.Errorf("the files total more than %d bytes (8 MB) — compress them in smaller batches", compressMaxTotalBytes)
		}
		data, err := os.ReadFile(path) // #nosec G304 -- agent-requested input file, same trust boundary as @read; size-capped above
		if err != nil {
			return "", err
		}
		if len(files) > 1 || b.Len() > 0 {
			fmt.Fprintf(&b, "===== %s (%d bytes) =====\n", f, len(data))
		}
		b.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// compressResolvePath expands ~ and makes the path absolute.
func compressResolvePath(p string) (string, error) {
	expanded, err := utils.ExpandPath(strings.TrimSpace(p))
	if err != nil {
		expanded = strings.TrimSpace(p)
	}
	return filepath.Abs(expanded)
}

// writeCompressOut writes text atomically to the requested path (creating
// parent directories) and returns the absolute path written.
func writeCompressOut(out, text string) (string, error) {
	path, err := compressResolvePath(out)
	if err != nil {
		return "", err
	}
	if info, serr := os.Stat(path); serr == nil && info.IsDir() {
		return "", fmt.Errorf("out %s is a directory — give a file path", out)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	if err := utils.AtomicWriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
