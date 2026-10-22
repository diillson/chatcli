package engine

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (e *Engine) handleRead(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	file := fs.String("file", "", "Arquivo")
	start := fs.Int("start", 0, "Linha inicial (1-based)")
	end := fs.Int("end", 0, "Linha final (1-based)")
	head := fs.Int("head", 0, "Primeiras N linhas")
	tail := fs.Int("tail", 0, "Últimas N linhas")
	maxBytes := fs.Int("max-bytes", DefaultMaxBytes, "Limite de bytes")
	encoding := fs.String("encoding", "text", "text|base64")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	files := collectFiles(*file, fs.Args())
	if len(files) == 0 {
		return fmt.Errorf("--file requerido")
	}
	if *head > 0 && *tail > 0 {
		return fmt.Errorf("--head e --tail são incompatíveis")
	}

	for _, f := range files {
		if err := e.validatePath(f); err != nil {
			e.printf("❌ BLOCKED %s: %v\n", f, err)
			continue
		}
		var content string
		var truncated bool
		var err error
		if strings.EqualFold(*encoding, "base64") {
			content, truncated, err = readFileWithLimit(f, *maxBytes)
		} else {
			// A PDF reads as its extracted text, page by page: the raw
			// bytes are unreadable to a model.
			content, truncated, err = readTextFileWithLimit(ctx, f, *maxBytes)
		}
		if err != nil {
			e.printf("❌ ERROR READING '%s': %v\n", f, err)
			continue
		}

		if strings.EqualFold(*encoding, "base64") {
			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			e.printf("<<< BEGIN FILE (base64): %s >>>\n", f)
			e.println(encoded)
			e.printf("<<< END FILE: %s >>>\n\n", f)
			continue
		}

		lines := strings.Split(content, "\n")
		startIdx, endIdx := computeLineRange(len(lines), *start, *end, *head, *tail)
		if startIdx < 0 || endIdx < 0 {
			e.printf("❌ Invalid range for '%s'\n", f)
			continue
		}
		// No range asked: a whole-file read is capped at DefaultMaxLines
		// (the byte cap stays the ceiling), the way Claude Code pages
		// reads — 200 KB in one read was ~50K tokens of a 200K window.
		lineCapped := false
		if *start == 0 && *end == 0 && *head == 0 && *tail == 0 && endIdx-startIdx > DefaultMaxLines {
			endIdx = startIdx + DefaultMaxLines
			lineCapped = true
		}

		e.printf("<<< BEGIN FILE: %s >>>\n", f)
		for i := startIdx; i < endIdx; i++ {
			e.printf("%4d | %s\n", i+1, lines[i])
		}
		if lineCapped {
			e.printf("... [%d of %d lines; continue with --start %d] ...\n", DefaultMaxLines, len(lines), endIdx+1)
		}
		if truncated {
			e.printf("... [TRUNCATED AT %d BYTES] ...\n", *maxBytes)
		}
		e.printf("<<< END FILE: %s >>>\n\n", f)
	}
	return nil
}

func (e *Engine) handleWrite(args []string) error {
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	file := fs.String("file", "", "")
	content := fs.String("content", "", "")
	encoding := fs.String("encoding", "text", "")
	appendMode := fs.Bool("append", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *file == "" {
		return fmt.Errorf("--file requerido")
	}
	if *content == "" {
		return fmt.Errorf("--content vazio")
	}
	if err := e.validateWritePath(*file); err != nil {
		return err
	}

	data, err := smartDecode(*content, *encoding)
	if err != nil {
		return fmt.Errorf("erro decode: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(*file), 0700); err != nil {
		return fmt.Errorf("erro ao criar diretório: %w", err)
	}
	if err := createBackup(*file); err != nil {
		e.errorf("WARN: backup falhou para %s: %v\n", *file, err)
	}

	if *appendMode {
		f, err := os.OpenFile(*file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("erro escrita: %w", err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("erro escrita: %w", err)
		}
	} else {
		if err := os.WriteFile(*file, data, 0600); err != nil {
			return fmt.Errorf("erro escrita: %w", err)
		}
	}

	e.printf("✅ File '%s' written (%d bytes).\n", *file, len(data))
	return nil
}

func (e *Engine) handlePatch(args []string) error {
	fs := flag.NewFlagSet("patch", flag.ContinueOnError)
	file := fs.String("file", "", "")
	search := fs.String("search", "", "")
	replace := fs.String("replace", "", "")
	encoding := fs.String("encoding", "text", "")
	diff := fs.String("diff", "", "")
	diffEncoding := fs.String("diff-encoding", "text", "")
	all := fs.Bool("all", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *diff != "" {
		return e.applyUnifiedDiff(*file, *diff, *diffEncoding)
	}

	if *file == "" || *search == "" {
		return fmt.Errorf("patch needs --file and --search (plus --replace; or --diff for a unified diff)")
	}
	if err := e.validateWritePath(*file); err != nil {
		return err
	}

	c, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("erro leitura: %w", err)
	}
	content := string(c)

	sBytes, err := smartDecode(*search, *encoding)
	if err != nil {
		return fmt.Errorf("search decode error: %w", err)
	}

	rBytes, err := smartDecode(*replace, *encoding)
	if err != nil {
		return fmt.Errorf("replace decode error: %w", err)
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	searchStr := strings.ReplaceAll(string(sBytes), "\r\n", "\n")
	replaceStr := strings.ReplaceAll(string(rBytes), "\r\n", "\n")

	matches, err := locatePatch(content, searchStr, *file, *all)
	if err != nil {
		return fmt.Errorf("❌ %w", err)
	}
	if err := createBackup(*file); err != nil {
		e.errorf("WARN: backup failed for %s: %v\n", *file, err)
	}
	newContent := applyPatches(content, searchStr, replaceStr, matches)
	if err := os.WriteFile(*file, []byte(newContent), 0600); err != nil { //#nosec G703 -- path validated by engine.validatePath / SensitiveReadPaths.IsReadAllowed
		return fmt.Errorf("erro escrita: %w", err)
	}
	e.printf("✅ Patch applied to '%s' (%s).\n", *file, describePatch(matches))
	return nil
}

// describePatch says where a patch landed: the line of a single match,
// the count and lines of several, and whether whitespace was tolerated.
func describePatch(matches []patchMatch) string {
	if len(matches) == 1 {
		s := fmt.Sprintf("line %d", matches[0].line)
		if matches[0].tolerant {
			s += ", matched ignoring indentation and re-indented to the file"
		}
		return s
	}
	return fmt.Sprintf("%d occurrences at lines %s", len(matches), matchLines(matches))
}

func (e *Engine) handleRollback(args []string) error {
	fs := flag.NewFlagSet("rb", flag.ContinueOnError)
	file := fs.String("file", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("--file requerido")
	}
	if err := e.validateWritePath(*file); err != nil {
		return err
	}
	c, err := os.ReadFile(*file + ".bak")
	if err != nil {
		return fmt.Errorf("backup error: %w", err)
	}
	if err := os.WriteFile(*file, c, 0600); err != nil { //#nosec G703 -- path validated by engine.validatePath / SensitiveReadPaths.IsReadAllowed
		return fmt.Errorf("erro ao restaurar arquivo: %w", err)
	}
	e.println("✅ Rollback done.")
	return nil
}

func (e *Engine) handleClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	force := fs.Bool("force", false, "")
	pattern := fs.String("pattern", "*.bak", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := e.validatePath(*dir); err != nil {
		return err
	}

	var matches []string
	_ = filepath.Walk(*dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ok, _ := filepath.Match(*pattern, filepath.Base(p))
		if ok {
			matches = append(matches, p)
		}
		return nil
	})

	if len(matches) == 0 {
		e.println("No files to clean.")
		return nil
	}

	if !*force {
		e.println("🧹 Dry-run: files that would be removed:")
		for _, m := range matches {
			e.println(m)
		}
		e.println("Use --force to remove them.")
		return nil
	}

	for _, m := range matches {
		_ = os.Remove(m)
	}
	e.printf("✅ Removed %d files.\n", len(matches))
	return nil
}
