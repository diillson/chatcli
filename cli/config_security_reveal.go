/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config security reveal: read one sealed store file in plaintext without
 * unsealing anything on disk.
 *
 *   /config security reveal                       list the sealed files
 *   /config security reveal <file>                print it, opened
 *   /config security reveal <file> --out <path>   write an opened copy
 *
 * Only files the at-rest walk itself seals are accepted (the same store
 * enumeration as reseal), named relative to the state root
 * (CHATCLI_STATE_DIR, or ~/.chatcli). The original stays sealed. The
 * command runs only in an interactive terminal: over ACP, the server or the
 * web UI the plaintext would leave the machine, so it refuses there.
 */
package cli

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
	"golang.org/x/term"
)

// revealPrintMax bounds what reveal prints to the terminal; a larger file
// is cut with a notice pointing at --out.
const revealPrintMax = 256 << 10

// revealHistoryName is how the prompt history is listed when it lives
// outside the state root.
const revealHistoryName = "history"

// revealFile is one sealed store file reveal can open.
type revealFile struct {
	Rel, Path string
	// LinePrefix is set for line-oriented stores; Bound says whether their
	// lines are bound to the path.
	LinePrefix string
	Bound      bool
}

// revealTerminal reports whether reveal may print plaintext here; tests
// replace it.
var revealTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) } // #nosec G115 -- a file descriptor fits an int

// revealCandidates lists the sealed files under the state root (and the
// prompt history), sorted by their relative name.
func (cli *ChatCLI) revealCandidates() []revealFile {
	root := cli.atRestRoot()
	var out []revealFile
	seen := map[string]bool{}
	add := func(f revealFile) {
		if !seen[f.Path] {
			seen[f.Path] = true
			out = append(out, f)
		}
	}
	for _, st := range cli.atRestStoreDirs() {
		walkStore(st, func(path string, d os.DirEntry) {
			if !sealableIn(st.Kind, d.Name()) {
				return
			}
			f := revealFile{Rel: relToRoot(root, path), Path: path}
			if strings.HasSuffix(d.Name(), ".jsonl") {
				f.LinePrefix, f.Bound = sealedLinePrefix, true
			}
			if hasSealedContent(path, f.LinePrefix) {
				add(f)
			}
		})
	}
	if h := cli.historyPath(); h != "" && hasSealedContent(h, historySealedPrefix) {
		rel := relToRoot(root, h)
		if filepath.IsAbs(rel) {
			rel = revealHistoryName
		}
		add(revealFile{Rel: rel, Path: h, LinePrefix: historySealedPrefix})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out
}

// revealCache keeps the candidate list for the completer, which runs on
// every keystroke; a few seconds is enough to type the name.
var revealCache struct {
	sync.Mutex
	at    time.Time
	files []revealFile
}

const revealCacheTTL = 5 * time.Second

func (cli *ChatCLI) cachedRevealCandidates() []revealFile {
	revealCache.Lock()
	defer revealCache.Unlock()
	if time.Since(revealCache.at) > revealCacheTTL {
		revealCache.files, revealCache.at = cli.revealCandidates(), time.Now()
	}
	return revealCache.files
}

// relToRoot names path relative to root with forward slashes, or returns it
// unchanged when it is not under root.
func relToRoot(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(rel)
}

// hasSealedContent reports, without decrypting and without reading whole
// files, whether a file holds sealed data: the header of a whole-file store,
// the first sealed line of a journal.
func hasSealedContent(path, linePrefix string) bool {
	fh, err := os.Open(path) // #nosec G304 -- store path enumerated from the state root
	if err != nil {
		return false
	}
	defer func() { _ = fh.Close() }()
	if linePrefix == "" {
		head := make([]byte, 64)
		n, _ := io.ReadFull(fh, head)
		return atrest.IsEncrypted(head[:n])
	}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		if bytes.HasPrefix(sc.Bytes(), []byte(linePrefix)) {
			return true
		}
	}
	return false
}

// findReveal resolves a user argument to one candidate: its relative name,
// or its full path.
func findReveal(cands []revealFile, arg string) (revealFile, bool) {
	want := filepath.ToSlash(strings.TrimSpace(arg))
	abs := ""
	if p, err := expandUserPath(arg); err == nil {
		abs = filepath.Clean(p)
	}
	for _, c := range cands {
		if c.Rel == want || (abs != "" && filepath.Clean(c.Path) == abs) {
			return c, true
		}
	}
	return revealFile{}, false
}

// openReveal returns a candidate's plaintext; the file is not modified.
func openReveal(f revealFile) ([]byte, error) {
	if f.LinePrefix == "" {
		return atrest.ReadFile(f.Path)
	}
	data, err := os.ReadFile(f.Path) // #nosec G304 -- store path enumerated from the state root
	if err != nil {
		return nil, err
	}
	return atrest.OpenLines(f.Path, data, f.LinePrefix, f.Bound)
}

// parseRevealArgs splits "<file> [--out <path>] [--force]".
func parseRevealArgs(args []string) (file, out string, force bool, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--force" || a == "-f":
			force = true
		case a == "--out" || a == "-o":
			if i+1 >= len(args) {
				return "", "", false, fmt.Errorf("%s", i18n.T("sec.reveal.out_missing"))
			}
			i++
			out = args[i]
		case strings.HasPrefix(a, "--out="):
			out = strings.TrimPrefix(a, "--out=")
		case file == "":
			file = a
		default:
			return "", "", false, fmt.Errorf("%s", i18n.T("sec.reveal.extra_arg", a))
		}
	}
	return file, out, force, nil
}

// configSecurityReveal is /config security reveal [file] [--out path] [--force].
func (cli *ChatCLI) configSecurityReveal(args []string) {
	if !revealTerminal() {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.terminal_only"), ColorYellow))
		return
	}
	file, out, force, err := parseRevealArgs(args)
	if err != nil {
		fmt.Println(colorize("  "+err.Error(), ColorYellow))
		return
	}
	cands := cli.revealCandidates()
	if file == "" {
		cli.printRevealList(cands)
		return
	}
	f, ok := findReveal(cands, file)
	if !ok {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.not_found", file), ColorYellow))
		fmt.Println(colorize("  "+i18n.T("sec.reveal.list_hint"), ColorGray))
		return
	}
	plain, err := openReveal(f)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.open_failed", f.Rel, err), ColorRed))
		return
	}
	if cli.logger != nil {
		cli.logger.Info("encryption at rest: file revealed", zap.String("file", f.Rel), zap.Bool("to_file", out != ""))
	}
	if out != "" {
		cli.writeReveal(f, plain, out, force)
		return
	}
	printReveal(f, plain)
}

// printRevealList shows the sealed files and how to open one.
func (cli *ChatCLI) printRevealList(cands []revealFile) {
	root := cli.atRestRoot()
	if len(cands) == 0 {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.none", root), ColorGray))
		return
	}
	fmt.Println(colorize("  "+i18n.T("sec.reveal.list_header", len(cands), root), ColorCyan+ColorBold))
	for _, c := range cands {
		size := ""
		if info, err := os.Stat(c.Path); err == nil {
			size = formatSize(info.Size())
		}
		fmt.Printf("    %s  %s\n", c.Rel, colorize(size, ColorGray))
	}
	fmt.Println(colorize("  "+i18n.T("sec.reveal.usage_hint"), ColorGray))
}

// printReveal prints the plaintext, refusing binary content and cutting
// very large files.
func printReveal(f revealFile, plain []byte) {
	if !utf8.Valid(plain) || bytes.IndexByte(plain, 0) >= 0 {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.binary", f.Rel, formatSize(int64(len(plain)))), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("sec.reveal.header", f.Rel), ColorCyan+ColorBold))
	body := plain
	cut := len(body) > revealPrintMax
	if cut {
		body = body[:revealPrintMax]
		for !utf8.Valid(body) && len(body) > 0 {
			body = body[:len(body)-1]
		}
	}
	fmt.Println(string(body))
	if cut {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.truncated", formatSize(int64(revealPrintMax)), formatSize(int64(len(plain))), f.Rel), ColorYellow))
	}
}

// writeReveal writes an opened copy outside the state root, never over an
// existing file unless forced.
func (cli *ChatCLI) writeReveal(f revealFile, plain []byte, out string, force bool) {
	dest, err := expandUserPath(out)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.write_failed", out, err), ColorRed))
		return
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.write_failed", out, err), ColorRed))
		return
	}
	// Resolve symlinks on both sides: a directory linked into the state root
	// must not smuggle the copy in.
	root := cli.atRestRoot()
	if r, rerr := filepath.EvalSymlinks(root); rerr == nil {
		root = r
	}
	check := dest
	if parent, perr := filepath.EvalSymlinks(filepath.Dir(dest)); perr == nil {
		check = filepath.Join(parent, filepath.Base(dest))
	}
	if !filepath.IsAbs(relToRoot(root, check)) {
		// Inside the state root a plaintext copy would sit among the stores
		// (and a store directory's walk would seal it again).
		fmt.Println(colorize("  "+i18n.T("sec.reveal.out_in_root", cli.atRestRoot()), ColorYellow))
		return
	}
	if _, err := os.Lstat(dest); err == nil && !force {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.out_exists", dest), ColorYellow))
		return
	}
	if err := utils.AtomicWriteFile(dest, plain, 0o600); err != nil {
		fmt.Println(colorize("  "+i18n.T("sec.reveal.write_failed", dest, err), ColorRed))
		return
	}
	fmt.Println(colorize("  "+i18n.T("sec.reveal.written", f.Rel, dest, formatSize(int64(len(plain)))), ColorGreen))
	fmt.Println(colorize("  "+i18n.T("sec.reveal.written_warn"), ColorYellow))
}
