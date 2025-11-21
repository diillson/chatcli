package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Metadata define o contrato de descoberta do plugin para o ChatCLI.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// logger aprimorado: escreve no stderr (humano) e guarda histórico (IA).
type logger struct {
	w       io.Writer
	history []string
	mu      sync.Mutex
}

func newLogger(w io.Writer) *logger {
	return &logger{w: w, history: make([]string, 0)}
}

func (l *logger) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	msg := fmt.Sprintf(format, args...)

	// 1. Escreve no Output original (ex: Stderr para o usuário ver)
	_, _ = fmt.Fprint(l.w, msg)
	if f, ok := l.w.(*os.File); ok {
		_ = f.Sync()
	}

	// 2. Guarda no histórico para a IA ver depois (se for útil)
	cleanMsg := strings.TrimRight(msg, "\n")
	l.history = append(l.history, cleanMsg)
}

func (l *logger) Infof(format string, args ...any) {
	l.Logf("ℹ️  "+format+"\n", args...)
}

func (l *logger) Warnf(format string, args ...any) {
	l.Logf("⚠️  "+format+"\n", args...)
}

func (l *logger) Errorf(format string, args ...any) {
	l.Logf("❌ "+format+"\n", args...)
}

func (l *logger) Separator() {
	l.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
}

// GetHistory retorna todo o log acumulado como uma única string.
func (l *logger) GetHistory() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.history, "\n")
}

// FlattenFormat define o formato de saída.
type FlattenFormat string

const (
	FormatText  FlattenFormat = "text"
	FormatJSONL FlattenFormat = "jsonl"
)

// Chunk representa um pedaço de texto pronto para IA.
type Chunk struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	ChunkSize int    `json:"chunkSize"`
}

type frontMatter struct {
	Title string
}

type config struct {
	RootPath         string
	Format           FlattenFormat
	MaxChars         int
	IncludePatterns  []string
	ExcludePatterns  []string
	StripFrontMatter bool
	OutputPath       string
}

var (
	tomlFence = regexp.MustCompile(`^\s*\+\+\+\s*$`)
	yamlFence = regexp.MustCompile(`^\s*---\s*$`)
	titleTOML = regexp.MustCompile(`(?i)^\s*title\s*=\s*"(.*)"\s*$`)
	titleYAML = regexp.MustCompile(`(?i)^\s*title\s*:\s*"(.*)"\s*$`)
)

func globMatch(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		ok, err := filepath.Match(p, path)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func shouldProcessFile(relPath string, cfg config) bool {
	if !globMatch(relPath, cfg.IncludePatterns) {
		return false
	}
	if len(cfg.ExcludePatterns) == 0 {
		return true
	}
	return !globMatch(relPath, cfg.ExcludePatterns)
}

func parseFrontMatter(r io.Reader) (frontMatter, string, error) {
	var fm frontMatter
	var b bytes.Buffer
	if _, err := b.ReadFrom(r); err != nil {
		return fm, "", err
	}
	data := b.String()
	lines := strings.Split(data, "\n")
	if len(lines) == 0 {
		return fm, data, nil
	}

	first := strings.TrimSpace(lines[0])
	var fence *regexp.Regexp
	switch {
	case tomlFence.MatchString(first):
		fence = tomlFence
	case yamlFence.MatchString(first):
		fence = yamlFence
	default:
		return fm, data, nil
	}

	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if fence.MatchString(lines[i]) {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return fm, data, nil
	}

	fmLines := lines[1:endIdx]
	bodyLines := lines[endIdx+1:]

	for _, line := range fmLines {
		if m := titleTOML.FindStringSubmatch(line); len(m) == 2 {
			fm.Title = m[1]
			break
		}
		if m := titleYAML.FindStringSubmatch(line); len(m) == 2 {
			fm.Title = m[1]
			break
		}
	}

	return fm, strings.Join(bodyLines, "\n"), nil
}

func normalizeMarkdown(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	scanner := bufio.NewScanner(strings.NewReader(text))
	var out []string
	blankStreak := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			blankStreak++
			if blankStreak > 1 {
				continue
			}
		} else {
			blankStreak = 0
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func chunkText(text string, maxChars int) []string {
	if maxChars <= 0 || len(text) <= maxChars {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}

	var chunks []string
	var buf strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(text))

	for scanner.Scan() {
		line := scanner.Text()
		if buf.Len()+len(line)+1 > maxChars && buf.Len() > 0 {
			chunks = append(chunks, buf.String())
			buf.Reset()
		}
		if buf.Len() > 0 {
			buf.WriteRune('\n')
		}
		buf.WriteString(line)
	}

	if buf.Len() > 0 {
		chunks = append(chunks, buf.String())
	}

	return chunks
}

func processFile(absPath, relPath string, cfg config, log *logger, chunkIndex *int) ([]Chunk, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "Erro ao fechar o arquivo:", err)
		}
	}(f)

	fm, body, err := parseFrontMatter(f)
	if err != nil {
		return nil, fmt.Errorf("parse front matter: %w", err)
	}

	content := body
	if !cfg.StripFrontMatter {
		if fm.Title != "" {
			content = fmt.Sprintf("# %s\n\n%s", fm.Title, body)
		}
	}

	content = normalizeMarkdown(content)
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}

	rawChunks := chunkText(content, cfg.MaxChars)
	chunks := make([]Chunk, 0, len(rawChunks))

	for _, c := range rawChunks {
		id := fmt.Sprintf("%s#%04d", relPath, *chunkIndex)
		*chunkIndex++
		chunks = append(chunks, Chunk{
			ID:        id,
			Source:    relPath,
			Title:     fm.Title,
			Content:   c,
			ChunkSize: len(c),
		})
	}

	log.Infof("Processed %s → %d chunk(s)", relPath, len(chunks))
	return chunks, nil
}

func walkAndFlatten(cfg config, log *logger) ([]Chunk, error) {
	var chunks []Chunk
	chunkIndex := 1

	err := filepath.WalkDir(cfg.RootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			log.Warnf("Cannot access %s: %v", path, walkErr)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}

		rel, err := filepath.Rel(cfg.RootPath, path)
		if err != nil {
			rel = path
		}

		rel = filepath.ToSlash(rel)
		if !shouldProcessFile(rel, cfg) {
			return nil
		}

		fileChunks, err := processFile(path, rel, cfg, log, &chunkIndex)
		if err != nil {
			log.Warnf("Failed to process %s: %v", rel, err)
			return nil
		}
		chunks = append(chunks, fileChunks...)
		return nil
	})

	if err != nil {
		return nil, err
	}
	return chunks, nil
}

func outputText(chunks []Chunk, w io.Writer) error {
	if len(chunks) == 0 {
		return nil
	}

	currentSource := ""
	for _, c := range chunks {
		if c.Source != currentSource {
			if currentSource != "" {
				_, _ = fmt.Fprintln(w)
			}
			currentSource = c.Source
			_, _ = fmt.Fprintf(w, "===== FILE: %s =====\n", c.Source)
			if c.Title != "" {
				_, _ = fmt.Fprintf(w, "TITLE: %s\n\n", c.Title)
			} else {
				_, _ = fmt.Fprintln(w)
			}
		}
		_, _ = fmt.Fprintln(w, c.Content)
		_, _ = fmt.Fprintln(w)
	}
	return nil
}

func outputJSONL(chunks []Chunk, w io.Writer) error {
	enc := json.NewEncoder(w)
	for _, c := range chunks {
		if err := enc.Encode(&c); err != nil {
			return err
		}
	}
	return nil
}

func parseFlags() (config, bool, error) {
	var (
		showMetadata     bool
		root             string
		formatStr        string
		maxChars         int
		includeStr       string
		excludeStr       string
		stripFrontMatter bool
		outputPath       string
	)

	flag.BoolVar(&showMetadata, "metadata", false, "Exibe metadados do plugin em JSON e sai")
	flag.StringVar(&root, "root", "", "Diretório raiz da documentação (obrigatório)")
	flag.StringVar(&formatStr, "format", "text", "Formato de saída: text | jsonl")
	flag.IntVar(&maxChars, "max-chars", 16000, "Tamanho máximo (em caracteres) por chunk (0 = sem divisão)")
	flag.StringVar(&includeStr, "include", "", "Padrões glob incluídos (separados por vírgula), ex: 'docs/**.md,content/**.md'")
	flag.StringVar(&excludeStr, "exclude", "", "Padrões glob excluídos (separados por vírgula), ex: 'node_modules/**,public/**'")
	flag.BoolVar(&stripFrontMatter, "strip-front-matter", true, "Remove front matter dos arquivos Markdown")
	flag.StringVar(&outputPath, "output", "", "Arquivo de saída (se vazio, usa stdout)")

	flag.Parse()

	if showMetadata {
		return config{}, true, nil
	}

	if root == "" {
		return config{}, false, errors.New("--root é obrigatório")
	}

	format := FlattenFormat(strings.ToLower(strings.TrimSpace(formatStr)))
	switch format {
	case FormatText, FormatJSONL:
	default:
		return config{}, false, fmt.Errorf("formato inválido: %s (use text ou jsonl)", formatStr)
	}

	splitCSV := func(s string) []string {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, filepath.ToSlash(p))
			}
		}
		return out
	}

	cfg := config{
		RootPath:         root,
		Format:           format,
		MaxChars:         maxChars,
		IncludePatterns:  splitCSV(includeStr),
		ExcludePatterns:  splitCSV(excludeStr),
		StripFrontMatter: stripFrontMatter,
		OutputPath:       outputPath,
	}

	return cfg, false, nil
}

func printMetadata() {
	meta := Metadata{
		Name: "@docs-flatten",
		Description: "Varre documentação em Markdown (Hugo, Docusaurus, mkdocs, etc.), " +
			"extrai o conteúdo e gera texto ou JSONL pronto para IA (RAG/contexto).",
		Usage:   `@docs-flatten --root <dir> [--format text|jsonl] [--max-chars N] [--include globs] [--exclude globs] [--strip-front-matter bool] [--output file]`,
		Version: "1.2.0",
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(meta)
}

// run encapsula a lógica principal.
func run(log *logger) error {
	cfg, onlyMetadata, err := parseFlags()
	if err != nil {
		return err
	}

	if onlyMetadata {
		printMetadata()
		// Sinal especial para o main não imprimir logs extras se for só metadados
		return errors.New("METADATA_ONLY")
	}

	absRoot, err := filepath.Abs(cfg.RootPath)
	if err != nil {
		return fmt.Errorf("falha ao resolver caminho absoluto de %s: %v", cfg.RootPath, err)
	}
	cfg.RootPath = absRoot

	info, err := os.Stat(cfg.RootPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("diretório raiz inválido: %s", cfg.RootPath)
	}

	log.Separator()
	log.Infof("📚 Docs Flatten - root: %s", cfg.RootPath)
	log.Infof("Config: Format=%s, MaxChars=%d, StripFrontMatter=%t", cfg.Format, cfg.MaxChars, cfg.StripFrontMatter)
	if cfg.OutputPath != "" {
		log.Infof("Output file: %s", cfg.OutputPath)
	} else {
		log.Infof("Output: stdout (stream)")
	}
	log.Separator()

	start := time.Now()
	chunks, err := walkAndFlatten(cfg, log)
	if err != nil {
		return fmt.Errorf("falha ao processar documentação: %v", err)
	}

	duration := time.Since(start).Seconds()
	log.Infof("Total: %d chunks gerados em %.2fs", len(chunks), duration)

	// Configurar saída de dados (IA)
	var out io.Writer = os.Stdout
	if cfg.OutputPath != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.OutputPath), 0o755); err != nil {
			return fmt.Errorf("falha ao criar diretório de saída: %v", err)
		}
		f, err := os.Create(cfg.OutputPath)
		if err != nil {
			return fmt.Errorf("falha ao criar arquivo de saída: %v", err)
		}
		defer func(f *os.File) {
			err := f.Close()
			if err != nil {
				fmt.Fprintln(os.Stderr, "Erro ao fechar arquivo de saída:", err)
			}
		}(f)
		out = f
	}

	// Escrever conteúdo real (para IA) – nunca misturar logs aqui
	switch cfg.Format {
	case FormatText:
		if err := outputText(chunks, out); err != nil {
			return fmt.Errorf("falha na saída text: %v", err)
		}
	case FormatJSONL:
		if err := outputJSONL(chunks, out); err != nil {
			return fmt.Errorf("falha na saída jsonl: %v", err)
		}
	default:
		return fmt.Errorf("formato não suportado: %s", cfg.Format)
	}

	if cfg.OutputPath != "" {
		log.Infof("✅ Sucesso! Arquivo salvo em: %s", cfg.OutputPath)
	}

	return nil
}

func main() {
	// Logger escreve no Stderr (para o usuário ver) e guarda buffer (se você quiser inspecionar)
	log := newLogger(os.Stderr)

	err := run(log)

	// Se foi apenas solicitação de metadados, sai limpo sem logs extras
	if err != nil && err.Error() == "METADATA_ONLY" {
		return
	}

	if err != nil {
		log.Errorf("FALHA CRÍTICA: %v", err)
	}

	// Relatório final: SOMENTE em stderr, nunca em stdout
	fmt.Fprintln(os.Stderr, "\n\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintln(os.Stderr, "📋 RELATÓRIO DE EXECUÇÃO (LOGS)")
	fmt.Fprintln(os.Stderr, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintln(os.Stderr, log.GetHistory())

	if err != nil {
		fmt.Fprintln(os.Stderr, "\n❌ A execução terminou com erros. Verifique os logs acima.")
		os.Exit(1)
	}
}
