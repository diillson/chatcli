/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinDiffPlugin — unified diffs as an @diff ReAct tool.
 *
 * files/text/dirs compare without touching anything; apply takes a unified
 * diff and either previews it (dry_run, the default) or writes it with a
 * .bak backup next to the file. The line diff is Myers' O(ND) algorithm in
 * pure Go, bounded by an edit budget: when two files differ in more lines
 * than that, the middle is reported as one block replacement instead of
 * burning memory on a minimal script nobody will read.
 */
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/coder/engine"
)

const (
	diffToolMaxFileBytes   = 4 << 20
	diffToolMaxEdits       = 1500
	diffToolOutputCap      = 30_000
	diffToolMaxDirEntries  = 2000
	diffToolDefaultContext = 3
	diffToolMaxContext     = 50
	diffToolCutHint        = "diff a narrower file or directory, or lower context"
)

// BuiltinDiffPlugin is the @diff tool.
type BuiltinDiffPlugin struct{}

// NewBuiltinDiffPlugin returns a ready-to-register plugin.
func NewBuiltinDiffPlugin() *BuiltinDiffPlugin { return &BuiltinDiffPlugin{} }

// Name returns "@diff".
func (*BuiltinDiffPlugin) Name() string { return "@diff" }

// Description surfaces the tool in the catalog (i18n).
func (*BuiltinDiffPlugin) Description() string { return i18n.T("plugins.diff.description") }

// Usage explains the canonical invocation.
func (*BuiltinDiffPlugin) Usage() string {
	return `<tool_call name="@diff" args='{"cmd":"files","args":{"a":"old.go","b":"new.go"}}' />

Subcommands (cmd + args):
  files {a, b, context?}          unified diff of two workspace files (≤4 MB each)
  text  {a, b, context?}          unified diff of two inline strings
  dirs  {a, b, glob?}             added / removed / changed files between two
                                  directories (names only, ≤2000 entries; skips
                                  .git, node_modules, vendor; glob matches basenames)
  apply {file, patch, dry_run?}   apply a unified diff to one file. Context is
                                  verified hunk by hunk; a mismatch refuses the
                                  whole patch and names the first failing line.
                                  dry_run defaults to true (stats only);
                                  dry_run:false writes the file and keeps file.bak`
}

// Version is semver.
func (*BuiltinDiffPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinDiffPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinDiffPlugin) Schema() string {
	ctxFlag := map[string]interface{}{"name": "context", "type": "integer", "required": false, "description": "Context lines around each change (default 3, max 50)"}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; flat {a,b} compares files, flat {file,patch} applies",
		"subcommands": []map[string]interface{}{
			{"name": "files", "description": "Unified diff of two workspace files; binary files are compared byte-wise.",
				"flags": []map[string]interface{}{
					{"name": "a", "type": "string", "required": true, "description": "Original file path"},
					{"name": "b", "type": "string", "required": true, "description": "Modified file path"},
					ctxFlag},
				"examples": []string{`{"cmd":"files","args":{"a":"config.old.yaml","b":"config.yaml"}}`, `{"cmd":"files","args":{"a":"a.go","b":"b.go","context":1}}`}},
			{"name": "text", "description": "Unified diff of two inline strings.",
				"flags": []map[string]interface{}{
					{"name": "a", "type": "string", "required": true, "description": "Original text"},
					{"name": "b", "type": "string", "required": true, "description": "Modified text"},
					ctxFlag},
				"examples": []string{`{"cmd":"text","args":{"a":"one\ntwo\n","b":"one\n2\n"}}`}},
			{"name": "dirs", "description": "Files added, removed or changed between two directories (names only).",
				"flags": []map[string]interface{}{
					{"name": "a", "type": "string", "required": true, "description": "Original directory"},
					{"name": "b", "type": "string", "required": true, "description": "Modified directory"},
					{"name": "glob", "type": "string", "required": false, "description": "Only basenames matching this pattern, e.g. *.go"}},
				"examples": []string{`{"cmd":"dirs","args":{"a":"build/v1","b":"build/v2","glob":"*.js"}}`}},
			{"name": "apply", "description": "Apply a unified diff to one file; dry_run (default true) only reports what would change.",
				"flags": []map[string]interface{}{
					{"name": "file", "type": "string", "required": true, "description": "Target file path"},
					{"name": "patch", "type": "string", "required": true, "description": "Unified diff text (--- / +++ headers optional, @@ hunks required)"},
					{"name": "dry_run", "type": "boolean", "required": false, "description": "true (default): preview only; false: write the file and keep <file>.bak"}},
				"examples": []string{`{"cmd":"apply","args":{"file":"main.go","patch":"@@ -1,2 +1,2 @@\n-old\n+new\n same\n"}}`, `{"cmd":"apply","args":{"file":"main.go","patch":"...","dry_run":false}}`}},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinDiffPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback: every verb answers at once.
func (p *BuiltinDiffPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	cmd, raw, err := parseDiffToolInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@diff: %w", err)
	}
	a := diffToolArgsFrom(raw)
	var out string
	switch cmd {
	case "files":
		out, err = diffFiles(a)
	case "text":
		out = diffTexts("a", "b", a.a, a.b, a.context)
	case "dirs":
		out, err = diffDirs(a.a, a.b, a.glob)
	case "apply":
		out, err = diffApply(a)
	}
	if err != nil {
		return "", fmt.Errorf("@diff %s: %w", cmd, err)
	}
	return capToolOutput(out, diffToolOutputCap, diffToolCutHint), nil
}

// --- args ---------------------------------------------------------------

type diffToolArgs struct {
	a, b, file, patch, glob string
	context                 int
	dryRun                  bool
}

func diffToolArgsFrom(raw map[string]json.RawMessage) diffToolArgs {
	d := diffToolArgs{
		a:       jsonString(raw, "a", "from", "old", "left"),
		b:       jsonString(raw, "b", "to", "new", "right"),
		file:    jsonString(raw, "file", "path", "target"),
		patch:   jsonString(raw, "patch", "diff"),
		glob:    jsonString(raw, "glob", "pattern"),
		context: clampInt(jsonInt(raw, "context", "lines"), diffToolDefaultContext, diffToolMaxContext),
		dryRun:  true,
	}
	if jsonHas(raw, "dry_run", "dryRun", "dry") {
		d.dryRun = jsonBool(raw, "dry_run", "dryRun", "dry")
	}
	return d
}

func parseDiffToolInvocation(args []string) (string, map[string]json.RawMessage, error) {
	return parseVerbInvocation(args, canonicalDiffCmd, inferDiffCmd, "a", nil, map[string]bool{"context": true})
}

func canonicalDiffCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "files", "file", "compare":
		return "files"
	case "text", "strings", "inline":
		return "text"
	case "dirs", "dir", "directories", "tree":
		return "dirs"
	case "apply", "patch":
		return "apply"
	}
	return ""
}

// inferDiffCmd picks the verb for flat native args: a patch means apply, a
// glob or two directories mean dirs, two existing paths mean files, and two
// strings that are not paths mean text.
func inferDiffCmd(raw map[string]json.RawMessage) string {
	switch {
	case jsonHas(raw, "patch", "diff"):
		return "apply"
	case jsonHas(raw, "glob", "pattern"):
		return "dirs"
	}
	a, b := jsonString(raw, "a", "from", "old", "left"), jsonString(raw, "b", "to", "new", "right")
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	switch {
	case errA == nil && errB == nil && ia.IsDir() && ib.IsDir():
		return "dirs"
	case errA == nil && errB == nil:
		return "files"
	}
	return "text"
}

// --- files / text ---------------------------------------------------------

func readDiffFile(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New(`"a" and "b" are required`)
	}
	if err := engine.ValidateReadPath(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory (use dirs)", path)
	}
	if info.Size() > diffToolMaxFileBytes {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d", path, info.Size(), diffToolMaxFileBytes)
	}
	return os.ReadFile(path) // #nosec G304 -- validated against the workspace boundary by engine.ValidateReadPath
}

func isBinaryData(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}

func diffFiles(a diffToolArgs) (string, error) {
	da, err := readDiffFile(a.a)
	if err != nil {
		return "", err
	}
	db, err := readDiffFile(a.b)
	if err != nil {
		return "", err
	}
	if bytes.Equal(da, db) {
		return fmt.Sprintf("identical: %s and %s (%d bytes)", a.a, a.b, len(da)), nil
	}
	if isBinaryData(da) || isBinaryData(db) {
		return fmt.Sprintf("binary files differ: %s (%d bytes) vs %s (%d bytes)", a.a, len(da), a.b, len(db)), nil
	}
	return diffTexts(a.a, a.b, string(da), string(db), a.context), nil
}

// splitDiffLines splits text into lines without a trailing empty element
// and reports whether the text ended without a newline.
func splitDiffLines(s string) ([]string, bool) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if s == "" {
		return nil, false
	}
	noNL := !strings.HasSuffix(s, "\n")
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	return lines, noNL
}

func diffTexts(nameA, nameB, a, b string, context int) string {
	la, aNoNL := splitDiffLines(a)
	lb, bNoNL := splitDiffLines(b)
	ops, minimal := diffLines(la, lb)
	out := renderUnified(nameA, nameB, la, lb, aNoNL, bNoNL, ops, context)
	if out == "" {
		return fmt.Sprintf("identical: %s and %s", nameA, nameB)
	}
	if !minimal {
		out += fmt.Sprintf("\n[%d+ differing lines: shown as one block replacement, not a minimal diff]", diffToolMaxEdits)
	}
	return out
}

// diffOp is one step of the edit script: ' ' keep a[ai]==b[bi], '-' drop
// a[ai], '+' insert b[bi].
type diffOp struct {
	kind   byte
	ai, bi int
}

// diffLines returns the edit script a→b. minimal is false when the two
// sides differ in more than diffToolMaxEdits lines; the script then replaces
// the whole differing middle in one block.
func diffLines(a, b []string) (ops []diffOp, minimal bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	mid, ok := myersOps(ma, mb)
	if !ok {
		mid = mid[:0]
		for i := range ma {
			mid = append(mid, diffOp{'-', i, 0})
		}
		for j := range mb {
			mid = append(mid, diffOp{'+', len(ma), j})
		}
	}
	ops = make([]diffOp, 0, pre+len(mid)+suf)
	for i := 0; i < pre; i++ {
		ops = append(ops, diffOp{' ', i, i})
	}
	for _, o := range mid {
		o.ai += pre
		o.bi += pre
		ops = append(ops, o)
	}
	for i := 0; i < suf; i++ {
		ops = append(ops, diffOp{' ', len(a) - suf + i, len(b) - suf + i})
	}
	return ops, ok
}

// myersOps is Myers' greedy O(ND) diff with a per-step trace for the
// backtrack. ok is false once d exceeds diffToolMaxEdits.
func myersOps(a, b []string) ([]diffOp, bool) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, true
	}
	maxD := n + m
	if maxD > diffToolMaxEdits {
		maxD = diffToolMaxEdits
	}
	off := maxD + 1
	v := make([]int, 2*off+1)
	trace := make([][]int, 0, maxD+1)
	for d := 0; d <= maxD; d++ {
		snap := make([]int, 2*d+3)
		copy(snap, v[off-d-1:off+d+2])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return myersBacktrack(trace, n, m, d), true
			}
		}
	}
	return nil, false
}

func myersBacktrack(trace [][]int, x, y, d int) []diffOp {
	var rev []diffOp
	for ; d > 0; d-- {
		snap := trace[d]
		get := func(k int) int { return snap[k+d+1] }
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && get(k-1) < get(k+1)) {
			prevK = k + 1
		}
		prevX := get(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			rev = append(rev, diffOp{' ', x, y})
		}
		if x == prevX {
			y--
			rev = append(rev, diffOp{'+', x, y})
		} else {
			x--
			rev = append(rev, diffOp{'-', x, y})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, diffOp{' ', x, y})
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

const diffNoNewlineMarker = "\\ No newline at end of file"

// renderUnified groups the edit script into hunks with context lines and
// prints them in unified format. Empty when there is no change.
func renderUnified(nameA, nameB string, a, b []string, aNoNL, bNoNL bool, ops []diffOp, context int) string {
	var out strings.Builder
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := i - context
		if start < 0 {
			start = 0
		}
		end := i + 1
		for j := end; j < len(ops); {
			if ops[j].kind != ' ' {
				end = j + 1
				j++
				continue
			}
			k := j
			for k < len(ops) && ops[k].kind == ' ' {
				k++
			}
			if k == len(ops) || k-j > 2*context {
				break
			}
			j = k
		}
		if end+context < len(ops) {
			end += context
		} else {
			end = len(ops)
		}
		if out.Len() == 0 {
			fmt.Fprintf(&out, "--- %s\n+++ %s\n", nameA, nameB)
		}
		writeHunk(&out, a, b, aNoNL, bNoNL, ops[start:end])
		i = end
	}
	return strings.TrimRight(out.String(), "\n")
}

func writeHunk(out *strings.Builder, a, b []string, aNoNL, bNoNL bool, hunk []diffOp) {
	oldN, newN := 0, 0
	for _, o := range hunk {
		if o.kind != '+' {
			oldN++
		}
		if o.kind != '-' {
			newN++
		}
	}
	oldStart, newStart := hunk[0].ai+1, hunk[0].bi+1
	if oldN == 0 {
		oldStart--
	}
	if newN == 0 {
		newStart--
	}
	fmt.Fprintf(out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldN, newStart, newN)
	for _, o := range hunk {
		switch o.kind {
		case '-':
			out.WriteString("-" + a[o.ai] + "\n")
			if aNoNL && o.ai == len(a)-1 {
				out.WriteString(diffNoNewlineMarker + "\n")
			}
		case '+':
			out.WriteString("+" + b[o.bi] + "\n")
			if bNoNL && o.bi == len(b)-1 {
				out.WriteString(diffNoNewlineMarker + "\n")
			}
		default:
			out.WriteString(" " + a[o.ai] + "\n")
			if aNoNL && bNoNL && o.ai == len(a)-1 {
				out.WriteString(diffNoNewlineMarker + "\n")
			}
		}
	}
}

// --- dirs -----------------------------------------------------------------

var diffSkipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true}

type dirEntryInfo struct {
	path string
	size int64
}

func walkDiffDir(root, glob string, budget *int) (map[string]dirEntryInfo, bool, error) {
	if strings.TrimSpace(root) == "" {
		return nil, false, errors.New(`"a" and "b" are required`)
	}
	if err := engine.ValidateReadPath(root); err != nil {
		return nil, false, err
	}
	if _, err := filepath.Match(glob, "x"); glob != "" && err != nil {
		return nil, false, fmt.Errorf("bad glob %q: %w", glob, err)
	}
	out := map[string]dirEntryInfo{}
	truncated := false
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && diffSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, d.Name()); !ok {
				return nil
			}
		}
		if *budget <= 0 {
			truncated = true
			return filepath.SkipAll
		}
		*budget--
		rel, _ := filepath.Rel(root, p)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		out[filepath.ToSlash(rel)] = dirEntryInfo{path: p, size: info.Size()}
		return nil
	})
	return out, truncated, err
}

func diffDirs(a, b, glob string) (string, error) {
	budget := diffToolMaxDirEntries
	ea, truncA, err := walkDiffDir(a, glob, &budget)
	if err != nil {
		return "", err
	}
	eb, truncB, err := walkDiffDir(b, glob, &budget)
	if err != nil {
		return "", err
	}
	var added, removed, changed []string
	for rel := range ea {
		if _, ok := eb[rel]; !ok {
			removed = append(removed, rel)
		}
	}
	for rel, ib := range eb {
		ia, ok := ea[rel]
		if !ok {
			added = append(added, rel)
			continue
		}
		if ia.size != ib.size || !sameFileContent(ia, ib) {
			changed = append(changed, rel)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	var out strings.Builder
	fmt.Fprintf(&out, "%s → %s: %d added, %d removed, %d changed (%d files scanned)\n", a, b, len(added), len(removed), len(changed), len(ea)+len(eb))
	for _, sec := range []struct {
		mark  string
		items []string
	}{{"+", added}, {"-", removed}, {"~", changed}} {
		for _, it := range sec.items {
			fmt.Fprintf(&out, "%s %s\n", sec.mark, it)
		}
	}
	if truncA || truncB {
		fmt.Fprintf(&out, "[stopped after %d entries; use glob or a subdirectory to narrow]\n", diffToolMaxDirEntries)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// sameFileContent compares two same-sized files byte by byte; files past the
// size limit are judged by size alone.
func sameFileContent(a, b dirEntryInfo) bool {
	if a.size > diffToolMaxFileBytes {
		return true
	}
	da, errA := os.ReadFile(a.path) // #nosec G304 -- entry from a walk rooted under a validated directory
	db, errB := os.ReadFile(b.path) // #nosec G304 -- entry from a walk rooted under a validated directory
	return errA == nil && errB == nil && bytes.Equal(da, db)
}

// --- apply ----------------------------------------------------------------

type patchLine struct {
	kind byte
	text string
	noNL bool
}

type patchHunk struct {
	oldStart, oldN, newStart, newN int
	lines                          []patchLine
	header                         string
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseUnifiedPatch reads the hunks of a single-file unified diff. File
// headers (---/+++/diff/index) are tolerated and ignored.
func parseUnifiedPatch(text string) ([]patchHunk, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var hunks []patchHunk
	var cur *patchHunk
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@ "):
			m := hunkHeaderRe.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("line %d: bad hunk header %q", i+1, line)
			}
			h := patchHunk{header: line, oldN: 1, newN: 1}
			h.oldStart, _ = strconv.Atoi(m[1])
			h.newStart, _ = strconv.Atoi(m[3])
			if m[2] != "" {
				h.oldN, _ = strconv.Atoi(m[2])
			}
			if m[4] != "" {
				h.newN, _ = strconv.Atoi(m[4])
			}
			hunks = append(hunks, h)
			cur = &hunks[len(hunks)-1]
		case cur == nil:
			continue // preamble / file headers
		case strings.HasPrefix(line, "\\"):
			if len(cur.lines) > 0 {
				cur.lines[len(cur.lines)-1].noNL = true
			}
		case line == "" && i == len(lines)-1:
			continue // trailing newline of the patch text
		case line == "":
			cur.lines = append(cur.lines, patchLine{kind: ' '})
		case line[0] == ' ' || line[0] == '+' || line[0] == '-':
			cur.lines = append(cur.lines, patchLine{kind: line[0], text: line[1:]})
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "diff "):
			cur = nil
		default:
			return nil, fmt.Errorf("line %d: unexpected diff line %q", i+1, line)
		}
	}
	if len(hunks) == 0 {
		return nil, errors.New("no @@ hunks found in patch")
	}
	return hunks, nil
}

// applyPatchHunks applies hunks to lines, verifying every context and
// removed line at its stated position. The error names the first mismatch.
func applyPatchHunks(lines []string, hunks []patchHunk) (out []string, added, removed int, noNL bool, err error) {
	cur := 0
	for hi, h := range hunks {
		pos := h.oldStart - 1
		if h.oldN == 0 {
			pos = h.oldStart
		}
		if pos < cur || pos > len(lines) {
			return nil, 0, 0, false, fmt.Errorf("hunk %d (%s) starts at line %d, which is past the end or overlaps the previous hunk", hi+1, h.header, pos+1)
		}
		out = append(out, lines[cur:pos]...)
		cur = pos
		for _, pl := range h.lines {
			switch pl.kind {
			case '+':
				out = append(out, pl.text)
				added++
				noNL = pl.noNL
			default:
				if cur >= len(lines) {
					return nil, 0, 0, false, fmt.Errorf("hunk %d (%s): expected %q at line %d but the file ends", hi+1, h.header, pl.text, cur+1)
				}
				if lines[cur] != pl.text {
					return nil, 0, 0, false, fmt.Errorf("hunk %d (%s): line %d is %q, patch expects %q", hi+1, h.header, cur+1, lines[cur], pl.text)
				}
				if pl.kind == ' ' {
					out = append(out, lines[cur])
					noNL = pl.noNL
				} else {
					removed++
				}
				cur++
			}
		}
	}
	out = append(out, lines[cur:]...)
	return out, added, removed, noNL, nil
}

func diffApply(a diffToolArgs) (string, error) {
	if strings.TrimSpace(a.file) == "" || strings.TrimSpace(a.patch) == "" {
		return "", errors.New(`"file" and "patch" are required`)
	}
	hunks, err := parseUnifiedPatch(a.patch)
	if err != nil {
		return "", err
	}
	data, err := readDiffFile(a.file)
	if err != nil {
		return "", err
	}
	if isBinaryData(data) {
		return "", fmt.Errorf("%s is binary", a.file)
	}
	lines, hadNoNL := splitDiffLines(string(data))
	out, added, removed, noNL, err := applyPatchHunks(lines, hunks)
	if err != nil {
		return "", err
	}
	// Keep the file's own line ending when the patch says nothing about it.
	lastTouched := false
	for _, h := range hunks {
		if h.oldStart+h.oldN-1 >= len(lines) {
			lastTouched = true
		}
	}
	if !lastTouched {
		noNL = hadNoNL
	}
	newText := strings.Join(out, "\n")
	if len(out) > 0 && !noNL {
		newText += "\n"
	}
	stats := fmt.Sprintf("%d hunks, +%d -%d lines, %d → %d lines", len(hunks), added, removed, len(lines), len(out))
	if a.dryRun {
		return fmt.Sprintf("dry run: patch applies cleanly to %s (%s). Re-run with dry_run:false to write.", a.file, stats), nil
	}
	if err := engine.ValidateWritePath(a.file); err != nil {
		return "", err
	}
	if err := os.WriteFile(a.file+".bak", data, 0o600); err != nil { // #nosec G703 -- path validated by engine.ValidateWritePath
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := os.WriteFile(a.file, []byte(newText), 0o600); err != nil { // #nosec G703 -- path validated by engine.ValidateWritePath
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("applied to %s (%s); backup at %s.bak", a.file, stats, a.file), nil
}
