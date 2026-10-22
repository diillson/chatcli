/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * BuiltinArchivePlugin — safe archive handling as the @archive ReAct tool
 * for agents that download bundles: list, read one entry, extract with
 * zip-slip protection and hard limits, and create deterministic zip /
 * tar.gz bundles from workspace paths. Pure Go (archive/zip, archive/tar,
 * compress/gzip, compress/bzip2 for reading), bounded to the workspace by
 * the engine's path policy.
 */
package plugins

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/coder/engine"
	"github.com/diillson/chatcli/pkg/fspath"
	"github.com/diillson/chatcli/pkg/textfold"
)

const (
	archiveDefaultListLimit  = 500
	archiveDefaultMaxEntries = 10000
	archiveDefaultMaxTotal   = int64(1 << 30)
	archiveDefaultMaxEntry   = int64(256 << 20)
	archiveDefaultReadBytes  = 64 << 10
)

// archiveEntry is the format-independent view of one member. body is only
// valid inside the archiveEach callback that delivered the entry.
type archiveEntry struct {
	name, link string
	size       int64
	mode       fs.FileMode // type bits + permissions
	body       io.Reader
}

// errArchiveStop ends an archiveEach walk early without an error.
var errArchiveStop = errors.New("archive: stop")

// BuiltinArchivePlugin is the @archive tool.
type BuiltinArchivePlugin struct{}

// NewBuiltinArchivePlugin returns a ready-to-register plugin.
func NewBuiltinArchivePlugin() *BuiltinArchivePlugin { return &BuiltinArchivePlugin{} }

// Name returns "@archive".
func (*BuiltinArchivePlugin) Name() string { return "@archive" }

// Description is i18n-resolved for the catalog.
func (*BuiltinArchivePlugin) Description() string { return i18n.T("plugins.archive.description") }

// Usage explains the verbs.
func (*BuiltinArchivePlugin) Usage() string {
	return `<tool_call name="@archive" args='{"cmd":"list","args":{"file":"bundle.tar.gz"}}' />

Subcommands (cmd + args):
  list {file, limit?}                     entries with type and size (zip, tar, tar.gz/tgz, tar.bz2)
  read {file, entry, max_bytes?}          print one entry's text without extracting (default 64 KiB)
  extract {file, to, overwrite?, max_entries?, max_total_bytes?, max_entry_bytes?}
        extract under "to" with zip-slip protection: absolute entries, ".."
        escapes and symlinks/hard links pointing outside "to" are refused;
        existing files are kept unless overwrite:true; limits default to
        10000 entries / 1 GiB total / 256 MiB per entry.
  create {file, paths, base?, overwrite?} build a .zip or .tar.gz/.tgz (by
        extension) from workspace paths, sorted deterministically, .git
        excluded, entry names relative to base (default ".")`
}

// Version is semver.
func (*BuiltinArchivePlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinArchivePlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinArchivePlugin) Schema() string {
	fileFlag := map[string]interface{}{"name": "file", "type": "string", "required": true, "description": "Archive path inside the workspace (.zip, .tar, .tar.gz, .tgz, .tar.bz2)."}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "list",
				"description": "List the entries of an archive with type, size and link target.",
				"flags": []map[string]interface{}{fileFlag,
					{"name": "limit", "type": "integer", "required": false, "description": "Maximum entries printed. Default 500."}},
				"examples": []string{`{"cmd":"list","args":{"file":"release.zip"}}`},
			},
			{
				"name":        "read",
				"description": "Read one entry's text without extracting the archive.",
				"flags": []map[string]interface{}{fileFlag,
					{"name": "entry", "type": "string", "required": true, "description": "Entry name exactly as listed."},
					{"name": "max_bytes", "type": "integer", "required": false, "description": "Truncate after this many bytes. Default 65536."}},
				"examples": []string{`{"cmd":"read","args":{"file":"release.zip","entry":"README.md"}}`},
			},
			{
				"name":        "extract",
				"description": "Extract safely under a destination directory (zip-slip, symlink escape, absolute paths refused; size and count limits enforced).",
				"flags": []map[string]interface{}{fileFlag,
					{"name": "to", "type": "string", "required": true, "description": "Destination directory inside the workspace (created if missing)."},
					{"name": "overwrite", "type": "boolean", "required": false, "description": "Replace existing files. Default false."},
					{"name": "max_entries", "type": "integer", "required": false, "description": "Default 10000."},
					{"name": "max_total_bytes", "type": "integer", "required": false, "description": "Default 1073741824 (1 GiB)."},
					{"name": "max_entry_bytes", "type": "integer", "required": false, "description": "Default 268435456 (256 MiB)."}},
				"examples": []string{`{"cmd":"extract","args":{"file":"release.tar.gz","to":"vendor/release"}}`, `{"cmd":"extract","args":{"file":"a.zip","to":"out","overwrite":true}}`},
			},
			{
				"name":        "create",
				"description": "Create a .zip or .tar.gz/.tgz from workspace paths (deterministic order, .git excluded).",
				"flags": []map[string]interface{}{
					{"name": "file", "type": "string", "required": true, "description": "Output archive path; the extension picks the format."},
					{"name": "paths", "type": "array", "required": true, "description": "Files or directories to include."},
					{"name": "base", "type": "string", "required": false, "description": "Directory entry names are relative to. Default \".\"."},
					{"name": "overwrite", "type": "boolean", "required": false, "description": "Replace an existing output file. Default false."}},
				"examples": []string{`{"cmd":"create","args":{"file":"dist/site.zip","paths":["public","README.md"]}}`, `{"cmd":"create","args":{"file":"src.tgz","paths":["src"],"base":"src"}}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinArchivePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinArchivePlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	cmd, in, err := parseArchiveInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@archive: %w", err)
	}
	file := jsonString(in, "file", "path", "archive")
	if file == "" {
		return "", fmt.Errorf(`@archive %s: "file" is required`, cmd)
	}
	var out string
	switch cmd {
	case "create":
		out, err = archiveCreate(ctx, file, in)
	case "extract":
		out, err = archiveExtract(ctx, file, in)
	case "read":
		out, err = archiveRead(file, jsonString(in, "entry", "name", "member"), jsonInt(in, "max_bytes", "maxBytes"))
	default:
		out, err = archiveList(file, jsonInt(in, "limit", "max"))
	}
	if err != nil {
		return out, fmt.Errorf("@archive %s: %w", cmd, err)
	}
	return out, nil
}

func archiveList(file string, limit int) (string, error) {
	if err := engine.ValidateReadPath(file); err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = archiveDefaultListLimit
	}
	var lines []string
	var total int64
	count := 0
	err := archiveEach(file, func(e archiveEntry) error {
		total += e.size
		count++
		if count > limit {
			return nil
		}
		kind := "file"
		switch {
		case e.mode&fs.ModeSymlink != 0:
			kind = "symlink"
		case e.mode.IsDir():
			kind = "dir"
		case e.link != "":
			kind = "hardlink"
		}
		line := fmt.Sprintf("%-8s %10d %s", kind, e.size, e.name)
		if e.link != "" {
			line += " -> " + e.link
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d entries, %d bytes uncompressed\n%s\n", count, total, strings.Join(lines, "\n"))
	if count > limit {
		fmt.Fprintf(&b, "… and %d more (raise limit)\n", count-limit)
	}
	return b.String(), nil
}

func archiveRead(file, name string, maxBytes int) (string, error) {
	if name == "" {
		return "", errors.New(`"entry" is required`)
	}
	if err := engine.ValidateReadPath(file); err != nil {
		return "", err
	}
	if maxBytes <= 0 {
		maxBytes = archiveDefaultReadBytes
	}
	var out string
	found := false
	err := archiveEach(file, func(e archiveEntry) error {
		if strings.TrimSuffix(e.name, "/") != strings.TrimSuffix(name, "/") {
			return nil
		}
		found = true
		if !e.mode.IsRegular() {
			return fmt.Errorf("%q is not a regular file", name)
		}
		data, err := io.ReadAll(io.LimitReader(e.body, int64(maxBytes)+1))
		if err != nil {
			return err
		}
		switch {
		case bytes.IndexByte(data, 0) >= 0:
			out = fmt.Sprintf("%s: binary content (%d bytes), not printed\n", name, e.size)
		case len(data) > maxBytes:
			out = textfold.Cut(string(data), maxBytes) + fmt.Sprintf("\n… truncated at %d of %d bytes (raise max_bytes)\n", maxBytes, e.size)
		default:
			out = string(data)
		}
		return errArchiveStop
	})
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("entry %q not found", name)
	}
	return out, nil
}

// archiveEach streams the members of file (format by extension) to fn in
// archive order. Returning errArchiveStop from fn ends the walk cleanly.
func archiveEach(file string, fn func(archiveEntry) error) error {
	lower := strings.ToLower(file)
	if strings.HasSuffix(lower, ".zip") {
		zr, err := zip.OpenReader(file)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("entry %q: %w", f.Name, err)
			}
			e := archiveEntry{name: f.Name, size: int64(f.UncompressedSize64), mode: f.Mode(), body: rc}
			if e.mode&fs.ModeSymlink != 0 {
				b, _ := io.ReadAll(io.LimitReader(rc, 4096))
				e.link = string(b)
			}
			err = fn(e)
			rc.Close()
			if errors.Is(err, errArchiveStop) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	decompress, ok := archiveTarDecoder(lower)
	if !ok {
		return fmt.Errorf("unsupported archive %q (zip, tar, tar.gz, tgz, tar.bz2)", filepath.Base(file))
	}
	f, err := os.Open(file) // #nosec G304 -- path validated by engine.ValidateReadPath / ValidateWritePath upstream
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := decompress(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		err = fn(archiveEntry{name: h.Name, size: h.Size, mode: h.FileInfo().Mode(), link: h.Linkname, body: tr})
		if errors.Is(err, errArchiveStop) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// archiveTarDecoder picks the decompressor for a tar-family extension.
func archiveTarDecoder(lower string) (func(io.Reader) (io.Reader, error), bool) {
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }, true
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return func(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil }, true
	case strings.HasSuffix(lower, ".tar"):
		return func(r io.Reader) (io.Reader, error) { return r, nil }, true
	}
	return nil, false
}

// archiveExtract writes every entry under "to", refusing anything that
// would land outside it. Partial progress is reported with the error.
func archiveExtract(ctx context.Context, file string, in map[string]json.RawMessage) (string, error) {
	to := jsonString(in, "to", "dest", "dir")
	if to == "" {
		return "", errors.New(`"to" is required`)
	}
	if err := engine.ValidateReadPath(file); err != nil {
		return "", err
	}
	if err := engine.ValidateWritePath(to); err != nil {
		return "", err
	}
	maxEntries := jsonInt(in, "max_entries", "maxEntries")
	if maxEntries <= 0 {
		maxEntries = archiveDefaultMaxEntries
	}
	maxTotal := int64(jsonInt(in, "max_total_bytes", "maxTotalBytes"))
	if maxTotal <= 0 {
		maxTotal = archiveDefaultMaxTotal
	}
	maxEntry := int64(jsonInt(in, "max_entry_bytes", "maxEntryBytes"))
	if maxEntry <= 0 {
		maxEntry = archiveDefaultMaxEntry
	}
	overwrite := jsonBool(in, "overwrite", "force")
	if err := os.MkdirAll(to, 0o750); err != nil {
		return "", err
	}
	root, err := filepath.Abs(to)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return "", err
	}
	var total int64
	done := 0
	err = archiveEach(file, func(e archiveEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if done >= maxEntries {
			return fmt.Errorf("more than %d entries (raise max_entries)", maxEntries)
		}
		dest, err := archiveSafeDest(root, e.name)
		if err != nil {
			return err
		}
		if e.size > maxEntry {
			return fmt.Errorf("entry %q is %d bytes, limit is %d (raise max_entry_bytes)", e.name, e.size, maxEntry)
		}
		if total+e.size > maxTotal {
			return fmt.Errorf("total size exceeds %d bytes at entry %q (raise max_total_bytes)", maxTotal, e.name)
		}
		if _, err := os.Lstat(dest); err == nil && !e.mode.IsDir() {
			if !overwrite {
				return fmt.Errorf("%q already exists (pass overwrite:true)", e.name)
			}
			if err := os.RemoveAll(dest); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		n, err := archiveWriteEntry(root, dest, e, maxEntry)
		if err != nil {
			return fmt.Errorf("entry %q: %w", e.name, err)
		}
		total += n
		done++
		return nil
	})
	return fmt.Sprintf("extracted %d entries (%d bytes) to %s\n", done, total, to), err
}

// archiveSafeDest maps an entry name to a path under root: absolute names,
// drive letters, ".." components and any symlinked ancestor that resolves
// outside root are refused.
func archiveSafeDest(root, name string) (string, error) {
	slash := strings.ReplaceAll(name, `\`, "/")
	if path.IsAbs(slash) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("entry %q has an absolute path", name)
	}
	clean := path.Clean(slash)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("entry %q escapes the destination (zip-slip)", name)
	}
	dest := filepath.Join(root, filepath.FromSlash(clean))
	if !fspath.WithinBoundary(archiveResolveExisting(dest), root) {
		return "", fmt.Errorf("entry %q resolves outside the destination", name)
	}
	return dest, nil
}

// archiveResolveExisting resolves symlinks through the deepest ancestor
// that already exists, so a symlinked directory written by an earlier
// entry cannot redirect a later one.
func archiveResolveExisting(abs string) string {
	rest := ""
	for dir := abs; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if filepath.Dir(dir) == dir {
			return abs
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// archiveWriteEntry materializes one entry; links must stay under root.
func archiveWriteEntry(root, dest string, e archiveEntry, maxEntry int64) (int64, error) {
	switch {
	case e.mode.IsDir():
		return 0, os.MkdirAll(dest, 0o750)
	case e.mode&fs.ModeSymlink != 0:
		target := filepath.FromSlash(e.link)
		if filepath.IsAbs(target) || !fspath.WithinBoundary(archiveResolveExisting(filepath.Join(filepath.Dir(dest), target)), root) {
			return 0, fmt.Errorf("symlink to %q points outside the destination", e.link)
		}
		return 0, os.Symlink(target, dest)
	case e.link != "":
		src, err := archiveSafeDest(root, e.link)
		if err != nil {
			return 0, fmt.Errorf("hard link: %w", err)
		}
		return 0, os.Link(src, dest)
	case !e.mode.IsRegular():
		return 0, fmt.Errorf("unsupported entry type %v", e.mode.Type())
	}
	perm := fs.FileMode(0o600)
	if e.mode&0o111 != 0 {
		perm = 0o700
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm) // #nosec G304 -- dest confined under root by archiveSafeDest
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(e.body, maxEntry+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxEntry {
		err = fmt.Errorf("content exceeds the declared size and the %d byte limit", maxEntry)
	}
	if err != nil {
		os.Remove(dest)
		return n, err
	}
	return n, nil
}

// archiveCreate builds a zip or tar.gz from workspace paths.
func archiveCreate(ctx context.Context, file string, in map[string]json.RawMessage) (string, error) {
	paths := contextStringSlice(in, "paths", "path", "sources", "src")
	if len(paths) == 0 {
		return "", errors.New(`"paths" is required`)
	}
	base := jsonString(in, "base", "root")
	if base == "" {
		base = "."
	}
	lower := strings.ToLower(file)
	isZip := strings.HasSuffix(lower, ".zip")
	if !isZip && !strings.HasSuffix(lower, ".tar.gz") && !strings.HasSuffix(lower, ".tgz") {
		return "", fmt.Errorf("output %q must end in .zip, .tar.gz or .tgz", filepath.Base(file))
	}
	if err := engine.ValidateWritePath(file); err != nil {
		return "", err
	}
	if _, err := os.Lstat(file); err == nil && !jsonBool(in, "overwrite", "force") {
		return "", fmt.Errorf("%q already exists (pass overwrite:true)", file)
	}
	names, members, err := archiveCollectMembers(ctx, paths, base, file)
	if err != nil {
		return "", err
	}
	out, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 -- path validated by engine.ValidateWritePath
	if err != nil {
		return "", err
	}
	write, closers := newArchiveWriter(out, isZip)
	var total int64
	for _, name := range names {
		var fi fs.FileInfo
		if fi, err = os.Stat(members[name]); err != nil {
			break
		}
		if total += fi.Size(); total > archiveDefaultMaxTotal {
			err = fmt.Errorf("total size exceeds %d bytes at %q", archiveDefaultMaxTotal, name)
			break
		}
		var f *os.File
		if f, err = os.Open(members[name]); err != nil { // #nosec G304 -- walked under source paths validated by engine.ValidateWritePath
			break
		}
		err = write(name, fi, f)
		f.Close()
		if err != nil {
			break
		}
	}
	for _, c := range append(closers, out) {
		if cerr := c.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		os.Remove(file)
		return "", err
	}
	return fmt.Sprintf("created %s: %d entries, %d bytes\n", file, len(names), total), nil
}

// archiveCollectMembers walks the source paths and returns the sorted entry
// names with their source files: .git is skipped, only regular files are
// taken, the output file itself is left out and every name is relative to
// base. Sources must satisfy the strict (write) workspace policy — never
// the system binary directories a read may touch.
func archiveCollectMembers(ctx context.Context, paths []string, base, outFile string) ([]string, map[string]string, error) {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return nil, nil, err
	}
	absOut, _ := filepath.Abs(outFile)
	members := map[string]string{}
	for _, p := range paths {
		if err := engine.ValidateWritePath(p); err != nil {
			return nil, nil, err
		}
		err := filepath.WalkDir(p, func(src string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			abs, _ := filepath.Abs(src)
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			if !d.Type().IsRegular() || abs == absOut {
				return nil
			}
			rel, err := filepath.Rel(absBase, abs)
			if err != nil || strings.HasPrefix(rel, "..") {
				return fmt.Errorf("%q is outside base %q", src, base)
			}
			members[filepath.ToSlash(rel)] = src
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, members, nil
}

// newArchiveWriter returns the per-file writer for a zip or tar.gz stream
// and the closers to flush it, innermost first.
func newArchiveWriter(out io.Writer, isZip bool) (func(name string, fi fs.FileInfo, r io.Reader) error, []io.Closer) {
	if isZip {
		zw := zip.NewWriter(out)
		return func(name string, fi fs.FileInfo, r io.Reader) error {
			h, err := zip.FileInfoHeader(fi)
			if err != nil {
				return err
			}
			h.Name, h.Method = name, zip.Deflate
			w, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, r)
			return err
		}, []io.Closer{zw}
	}
	gw := gzip.NewWriter(out)
	tw := tar.NewWriter(gw)
	return func(name string, fi fs.FileInfo, r io.Reader) error {
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		h.Name = name
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		_, err = io.Copy(tw, r)
		return err
	}, []io.Closer{tw, gw}
}

// parseArchiveInvocation accepts the {cmd,args} envelope, a flat native
// object and the flattened argv form.
func parseArchiveInvocation(args []string) (string, map[string]json.RawMessage, error) {
	return parseVerbEnvelope(args, canonicalArchiveCmd, func(raw map[string]json.RawMessage) string {
		switch {
		case raw["entry"] != nil:
			return "read"
		case raw["to"] != nil:
			return "extract"
		case raw["paths"] != nil:
			return "create"
		}
		return "list"
	}, func(string) string { return "file" }, map[string]bool{"paths": true}, nil)
}

func canonicalArchiveCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "list", "ls", "entries":
		return "list"
	case "read", "cat", "show":
		return "read"
	case "extract", "unpack", "unzip", "untar":
		return "extract"
	case "create", "pack", "zip", "tar":
		return "create"
	}
	return ""
}
