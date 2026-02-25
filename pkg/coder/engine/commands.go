package engine

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (e *Engine) handleRead(args []string) error {
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
		content, truncated, err := readFileWithLimit(f, *maxBytes)
		if err != nil {
			e.printf("❌ ERRO AO LER '%s': %v\n", f, err)
			continue
		}

		if strings.EqualFold(*encoding, "base64") {
			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			e.printf("<<< INÍCIO DO ARQUIVO (base64): %s >>>\n", f)
			e.println(encoded)
			e.printf("<<< FIM DO ARQUIVO: %s >>>\n\n", f)
			continue
		}

		lines := strings.Split(content, "\n")
		startIdx, endIdx := computeLineRange(len(lines), *start, *end, *head, *tail)
		if startIdx < 0 || endIdx < 0 {
			e.printf("❌ Range inválido para '%s'\n", f)
			continue
		}

		e.printf("<<< INÍCIO DO ARQUIVO: %s >>>\n", f)
		for i := startIdx; i < endIdx; i++ {
			e.printf("%4d | %s\n", i+1, lines[i])
		}
		if truncated {
			e.printf("... [TRUNCADO EM %d BYTES] ...\n", *maxBytes)
		}
		e.printf("<<< FIM DO ARQUIVO: %s >>>\n\n", f)
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

	data, err := smartDecode(*content, *encoding)
	if err != nil {
		return fmt.Errorf("erro decode: %v", err)
	}

	_ = os.MkdirAll(filepath.Dir(*file), 0700)
	_ = createBackup(*file)

	if *appendMode {
		f, err := os.OpenFile(*file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("erro escrita: %v", err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("erro escrita: %v", err)
		}
	} else {
		if err := os.WriteFile(*file, data, 0600); err != nil {
			return fmt.Errorf("erro escrita: %v", err)
		}
	}

	e.printf("✅ Arquivo '%s' escrito (%d bytes).\n", *file, len(data))
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
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *diff != "" {
		return e.applyUnifiedDiff(*file, *diff, *diffEncoding)
	}

	if *file == "" || *search == "" {
		return fmt.Errorf("--file e --search requeridos")
	}

	c, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("erro leitura: %v", err)
	}
	content := string(c)

	sBytes, err := smartDecode(*search, *encoding)
	if err != nil {
		return fmt.Errorf("search decode error: %v", err)
	}

	rBytes, err := smartDecode(*replace, *encoding)
	if err != nil {
		return fmt.Errorf("replace decode error: %v", err)
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	searchStr := strings.ReplaceAll(string(sBytes), "\r\n", "\n")
	replaceStr := strings.ReplaceAll(string(rBytes), "\r\n", "\n")

	if strings.Count(content, searchStr) == 0 {
		e.errorf("DEBUG: Trecho não encontrado.\nBuscado (len=%d):\n%q\n", len(searchStr), searchStr)
		return fmt.Errorf("❌ Texto não encontrado")
	}
	_ = createBackup(*file)
	newContent := strings.Replace(content, searchStr, replaceStr, 1)
	if err := os.WriteFile(*file, []byte(newContent), 0600); err != nil {
		return fmt.Errorf("erro escrita: %v", err)
	}
	e.printf("✅ Patch aplicado em '%s'.\n", *file)
	return nil
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
	c, err := os.ReadFile(*file + ".bak")
	if err != nil {
		return fmt.Errorf("backup error: %v", err)
	}
	_ = os.WriteFile(*file, c, 0600)
	e.println("✅ Rollback ok.")
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
		e.println("Nenhum arquivo para limpar.")
		return nil
	}

	if !*force {
		e.println("🧹 Dry-run: arquivos que seriam removidos:")
		for _, m := range matches {
			e.println(m)
		}
		e.println("Use --force para remover.")
		return nil
	}

	for _, m := range matches {
		_ = os.Remove(m)
	}
	e.printf("✅ Removidos %d arquivos.\n", len(matches))
	return nil
}
