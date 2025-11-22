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
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Metadata básico para descoberta (comando --metadata)
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// Estruturas para o Schema rico (comando --schema)
type SchemaFlag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default"`
}

type SchemaSubcommand struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Flags       []SchemaFlag `json:"flags"`
}

type PluginSchema struct {
	Subcommands []SchemaSubcommand `json:"subcommands"`
}

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

	_, _ = fmt.Fprint(l.w, msg)
	if f, ok := l.w.(*os.File); ok {
		_ = f.Sync()
	}

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

func (l *logger) GetHistory() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.history, "\n")
}

type FlattenFormat string

const (
	FormatText  FlattenFormat = "text"
	FormatJSONL FlattenFormat = "jsonl"
	FormatJSON  FlattenFormat = "json"
	FormatYAML  FlattenFormat = "yaml"
)

type Chunk struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content"`
	ChunkSize int    `json:"chunkSize"`
	RepoURL   string `json:"repoUrl,omitempty"`
	Commit    string `json:"commit,omitempty"`
}

type frontMatter struct {
	Title string
}

type parsedMarkdown struct {
	FrontMatter frontMatter
	Body        string
	Full        string
	HasFM       bool
}

type config struct {
	RootPath         string
	Format           FlattenFormat
	MaxChars         int
	IncludePatterns  []string
	ExcludePatterns  []string
	StripFrontMatter bool
	OutputPath       string
	RepoURL          string
	Branch           string
	Subdir           string
	KeepClone        bool
}

var (
	tomlFence = regexp.MustCompile(`^\s*\+\+\+\s*$`)
	yamlFence = regexp.MustCompile(`^\s*---\s*$`)
	titleTOML = regexp.MustCompile(`(?i)^\s*title\s*=\s*"(.*)"\s*$`)
	titleYAML = regexp.MustCompile(`(?i)^\s*title\s*:\s*"(.*)"\s*$`)
)

// globMatch usa filepath.Match.
func globMatch(path string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		matchPath := path
		if !strings.Contains(p, "/") {
			matchPath = filepath.Base(path)
		}

		ok, err := filepath.Match(p, matchPath)
		if err == nil && ok {
			return true
		}
	}
	return false
}

func shouldProcessFile(relPath string, cfg config) bool {
	if len(cfg.IncludePatterns) > 0 {
		if !globMatch(relPath, cfg.IncludePatterns) {
			return false
		}
	}
	if len(cfg.ExcludePatterns) > 0 {
		if globMatch(relPath, cfg.ExcludePatterns) {
			return false
		}
	}
	return true
}

func parseFrontMatter(r io.Reader) (parsedMarkdown, error) {
	var pm parsedMarkdown
	var b bytes.Buffer
	if _, err := b.ReadFrom(r); err != nil {
		return pm, err
	}
	data := b.String()
	pm.Full = data

	lines := strings.Split(data, "\n")
	if len(lines) == 0 {
		pm.Body = data
		return pm, nil
	}

	first := strings.TrimSpace(lines[0])
	var fence *regexp.Regexp
	switch {
	case tomlFence.MatchString(first):
		fence = tomlFence
	case yamlFence.MatchString(first):
		fence = yamlFence
	default:
		pm.Body = data
		return pm, nil
	}

	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if fence.MatchString(lines[i]) {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		pm.Body = data
		return pm, nil
	}

	pm.HasFM = true
	fmLines := lines[1:endIdx]
	bodyLines := lines[endIdx+1:]

	for _, line := range fmLines {
		if m := titleTOML.FindStringSubmatch(line); len(m) == 2 {
			pm.FrontMatter.Title = m[1]
			break
		}
		if m := titleYAML.FindStringSubmatch(line); len(m) == 2 {
			pm.FrontMatter.Title = m[1]
			break
		}
	}

	pm.Body = strings.Join(bodyLines, "\n")
	return pm, nil
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

func processFile(absPath, relPath string, cfg config, log *logger, chunkIndex *int, repoURL, commit string) ([]Chunk, error) {
	f, err := os.Open(absPath)
	if err != nil {
		return nil, err
	}
	defer func(f *os.File) {
		if err := f.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "Erro ao fechar o arquivo:", err)
		}
	}(f)

	pm, err := parseFrontMatter(f)
	if err != nil {
		return nil, fmt.Errorf("parse front matter: %w", err)
	}

	title := pm.FrontMatter.Title
	var content string

	if cfg.StripFrontMatter {
		if pm.HasFM && title != "" {
			content = fmt.Sprintf("# %s\n\n%s", title, pm.Body)
		} else {
			content = pm.Body
		}
	} else {
		content = pm.Full
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
			Title:     title,
			Content:   c,
			ChunkSize: len(c),
			RepoURL:   repoURL,
			Commit:    commit,
		})
	}

	log.Infof("Processed %s → %d chunk(s)", relPath, len(chunks))
	return chunks, nil
}

func walkAndFlatten(cfg config, log *logger, repoURL, commit string) ([]Chunk, error) {
	var chunks []Chunk
	chunkIndex := 1

	err := filepath.WalkDir(cfg.RootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			log.Warnf("Cannot access %s: %v", path, walkErr)
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
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

		fileChunks, err := processFile(path, rel, cfg, log, &chunkIndex, repoURL, commit)
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

func outputJSON(chunks []Chunk, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(chunks); err != nil {
		return err
	}
	return nil
}

func outputYAML(chunks []Chunk, w io.Writer) error {
	var b bytes.Buffer
	for i, c := range chunks {
		if i == 0 {
			b.WriteString("- ")
		} else {
			b.WriteString("\n- ")
		}
		id := strings.ReplaceAll(c.ID, "\n", " ")
		src := strings.ReplaceAll(c.Source, "\n", " ")
		title := strings.ReplaceAll(c.Title, "\n", " ")
		repo := strings.ReplaceAll(c.RepoURL, "\n", " ")
		commit := strings.ReplaceAll(c.Commit, "\n", " ")
		fmt.Fprintf(&b, "id: %q\n  source: %q\n  chunkSize: %d\n", id, src, c.ChunkSize)
		if c.Title != "" {
			fmt.Fprintf(&b, "  title: %q\n", title)
		}
		if c.RepoURL != "" {
			fmt.Fprintf(&b, "  repoUrl: %q\n", repo)
		}
		if c.Commit != "" {
			fmt.Fprintf(&b, "  commit: %q\n", commit)
		}
		content := c.Content
		if strings.Contains(content, "\n") {
			b.WriteString("  content: |\n")
			for _, line := range strings.Split(content, "\n") {
				b.WriteString("    ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		} else {
			fmt.Fprintf(&b, "  content: %q\n", content)
		}
	}
	_, err := w.Write(b.Bytes())
	return err
}

func parseFlags() (config, bool, error) {
	var (
		showMetadata        bool
		showSchema          bool
		root                string
		formatStr           string
		maxChars            int
		includeStr          string
		excludeStr          string
		stripFrontMatterStr string
		keepCloneStr        string
		outputPath          string
		repoURL             string
		branch              string
		subdir              string
	)

	flag.BoolVar(&showMetadata, "metadata", false, "Exibe metadados do plugin em JSON e sai")
	flag.BoolVar(&showSchema, "schema", false, "Exibe o schema JSON da ferramenta e sai")
	flag.StringVar(&root, "root", "", "Diretório raiz da documentação")
	flag.StringVar(&formatStr, "format", "text", "Formato de saída: text | jsonl | json | yaml")
	flag.IntVar(&maxChars, "max-chars", 16000, "Tamanho máximo (em caracteres) por chunk (0 = sem divisão)")
	flag.StringVar(&includeStr, "include", "", "Padrões glob incluídos (separados por vírgula), ex: docs/**.md,content/**.md")
	flag.StringVar(&excludeStr, "exclude", "", "Padrões glob excluídos (separados por vírgula), ex: node_modules/**,public/**")

	flag.StringVar(&stripFrontMatterStr, "strip-front-matter", "true", "Remove front matter dos arquivos Markdown (true/false)")
	flag.StringVar(&keepCloneStr, "keep-clone", "false", "Não apagar o clone temporário após o processamento (true/false)")

	flag.StringVar(&outputPath, "output", "", "Arquivo de saída (se vazio, usa stdout)")
	flag.StringVar(&repoURL, "repo", "", "URL do repositório Git com a documentação")
	flag.StringVar(&branch, "branch", "main", "Branch a ser usada ao clonar o repositório")
	flag.StringVar(&subdir, "subdir", "", "Subdiretório dentro do repositório que contém os .md (ex: docs)")

	flag.Parse()

	if showMetadata {
		printMetadata()
		return config{}, true, nil
	}

	if showSchema {
		printSchema()
		return config{}, true, nil
	}

	if root == "" && repoURL == "" {
		return config{}, false, errors.New("é obrigatório usar --root ou --repo")
	}

	format := FlattenFormat(strings.ToLower(strings.TrimSpace(formatStr)))
	switch format {
	case FormatText, FormatJSONL, FormatJSON, FormatYAML:
	default:
		return config{}, false, fmt.Errorf("formato inválido: %s (use text, jsonl, json ou yaml)", formatStr)
	}

	stripFrontMatter, err := strconv.ParseBool(stripFrontMatterStr)
	if err != nil {
		return config{}, false, fmt.Errorf("valor inválido para --strip-front-matter: %v (use true ou false)", err)
	}

	keepClone, err := strconv.ParseBool(keepCloneStr)
	if err != nil {
		return config{}, false, fmt.Errorf("valor inválido para --keep-clone: %v (use true ou false)", err)
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
				p = strings.TrimPrefix(p, "'")
				p = strings.TrimSuffix(p, "'")
				p = strings.TrimPrefix(p, "\"")
				p = strings.TrimSuffix(p, "\"")
				out = append(out, filepath.ToSlash(p))
			}
		}
		return out
	}

	root = strings.TrimSpace(root)
	repoURL = strings.TrimSpace(repoURL)
	outputPath = strings.TrimSpace(outputPath)

	cfg := config{
		RootPath:         root,
		Format:           format,
		MaxChars:         maxChars,
		IncludePatterns:  splitCSV(includeStr),
		ExcludePatterns:  splitCSV(excludeStr),
		StripFrontMatter: stripFrontMatter,
		OutputPath:       outputPath,
		RepoURL:          repoURL,
		Branch:           branch,
		Subdir:           subdir,
		KeepClone:        keepClone,
	}

	return cfg, false, nil
}

func printMetadata() {
	meta := Metadata{
		Name: "@docs-flatten",
		Description: "Varre documentação em Markdown (Hugo, Docusaurus, mkdocs, etc.), " +
			"extrai o conteúdo e gera texto, JSON, JSONL ou YAML pronto para IA (RAG/contexto). " +
			"PREFIRA SEMPRE usar --output para salvar em arquivo em vez de imprimir no chat.",
		Usage: `@docs-flatten --root <dir> [--format text|jsonl|json|yaml] [--strip-front-matter true|false] [--include globs] [--exclude globs] --output <file>
            @docs-flatten --repo <git-url> [--subdir docs] [--branch main] [--keep-clone true|false] --output <file>`,
		Version: "1.4.4",
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(meta)
}

func printSchema() {
	schema := PluginSchema{
		Subcommands: []SchemaSubcommand{
			{
				Name:        "@docs-flatten",
				Description: "Ferramenta para processar e 'achatar' documentações em Markdown. ATENÇÃO: Para evitar inundar o chat, você DEVE usar a flag --output para salvar o resultado em um arquivo quando o conteúdo for extenso.",
				Flags: []SchemaFlag{
					{
						Name:        "root",
						Description: "Caminho do diretório local contendo os arquivos .md. Use ESTE ou --repo (exclusivos).",
						Type:        "string",
						Default:     "",
					},
					{
						Name:        "repo",
						Description: "URL completa do repositório Git (https://...). Use ESTE ou --root (exclusivos).",
						Type:        "string",
						Default:     "",
					},
					{
						Name:        "branch",
						Description: "Branch do repositório Git a ser clonada (apenas se usar --repo).",
						Type:        "string",
						Default:     "main",
					},
					{
						Name:        "subdir",
						Description: "Subdiretório dentro do repositório onde está a documentação (apenas se usar --repo). Ex: 'docs' ou 'content'.",
						Type:        "string",
						Default:     "",
					},
					{
						Name:        "format",
						Description: "Formato de saída dos dados. Opções: text, jsonl, json, yaml.",
						Type:        "string",
						Default:     "text",
					},
					{
						Name:        "strip-front-matter",
						Description: "Remove o cabeçalho de metadados (front-matter) dos arquivos Markdown. Valores aceitos: 'true', 'false'.",
						Type:        "string",
						Default:     "true",
					},
					{
						Name:        "keep-clone",
						Description: "Mantém o diretório temporário do git após o processamento. Útil para debug. Valores aceitos: 'true', 'false'.",
						Type:        "string",
						Default:     "false",
					},
					{
						Name:        "output",
						Description: "Caminho absoluto do arquivo para salvar o resultado. OBRIGATÓRIO se o usuário pedir para 'salvar', 'gerar' ou 'exportar'. Ex: '/tmp/docs.json'.",
						Type:        "string",
						Default:     "",
					},
					{
						Name:        "max-chars",
						Description: "Tamanho máximo de caracteres por chunk. Use 0 para não dividir.",
						Type:        "int",
						Default:     "16000",
					},
					{
						Name:        "include",
						Description: "Padrões para incluir arquivos (separados por vírgula). Ex: 'README.md'. Se vazio, inclui todos .md.",
						Type:        "string",
						Default:     "",
					},
					{
						Name:        "exclude",
						Description: "Padrões para excluir arquivos (separados por vírgula). Ex: 'node_modules'.",
						Type:        "string",
						Default:     "",
					},
				},
			},
		},
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(schema)
}

func gitClone(repoURL, branch, dest string, log *logger) error {
	args := []string{"clone", "--depth", "1", "--branch", branch, repoURL, dest}
	cmd := exec.Command("git", args...)

	var errBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("falha ao clonar repositório: %v\nDetalhes: %s", err, errBuf.String())
	}
	return nil
}

func gitGetCommit(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("falha ao obter commit HEAD: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func prepareRootPath(cfg *config, log *logger) (string, string, func(), error) {
	cleanup := func() {}
	repoCommit := ""

	if cfg.RepoURL == "" {
		if cfg.RootPath == "" {
			return "", "", cleanup, errors.New("RootPath vazio e RepoURL vazio")
		}
		absRoot, err := filepath.Abs(cfg.RootPath)
		if err != nil {
			return "", "", cleanup, fmt.Errorf("falha ao resolver caminho absoluto de %s: %v", cfg.RootPath, err)
		}
		return absRoot, repoCommit, cleanup, nil
	}

	tmpDir, err := os.MkdirTemp("", "docs-flatten-*")
	if err != nil {
		return "", "", cleanup, fmt.Errorf("falha ao criar diretório temporário: %v", err)
	}

	if !cfg.KeepClone {
		cleanup = func() {
			_ = os.RemoveAll(tmpDir)
		}
	} else {
		cleanup = func() {}
	}

	log.Infof("Clonando repositório %s (branch=%s) em %s", cfg.RepoURL, cfg.Branch, tmpDir)
	if err := gitClone(cfg.RepoURL, cfg.Branch, tmpDir, log); err != nil {
		cleanup()
		return "", "", nil, err
	}

	commit, err := gitGetCommit(tmpDir)
	if err == nil {
		repoCommit = commit
	}

	finalRoot := tmpDir
	if cfg.Subdir != "" {
		finalRoot = filepath.Join(tmpDir, cfg.Subdir)
	}

	absRoot, err := filepath.Abs(finalRoot)
	if err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("falha ao resolver caminho absoluto de %s: %v", finalRoot, err)
	}

	return absRoot, repoCommit, cleanup, nil
}

func run(log *logger) error {
	cfg, stop, err := parseFlags()
	if err != nil {
		return err
	}

	if stop {
		return errors.New("METADATA_ONLY")
	}

	rootPath, repoCommit, cleanup, err := prepareRootPath(&cfg, log)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	cfg.RootPath = rootPath

	info, err := os.Stat(cfg.RootPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("diretório raiz inválido ou não encontrado: %s", cfg.RootPath)
	}

	log.Separator()
	if cfg.RepoURL != "" {
		log.Infof("📚 Docs Flatten - repo: %s (branch=%s, commit=%s)", cfg.RepoURL, cfg.Branch, repoCommit)
		log.Infof("Root (clonado): %s", cfg.RootPath)
	} else {
		log.Infof("📚 Docs Flatten - root: %s", cfg.RootPath)
	}
	log.Infof("Config: Format=%s, MaxChars=%d, StripFrontMatter=%t", cfg.Format, cfg.MaxChars, cfg.StripFrontMatter)
	if cfg.OutputPath != "" {
		log.Infof("Output file: %s (nenhum chunk será enviado para stdout)", cfg.OutputPath)
	} else {
		log.Infof("Output: stdout (stream)")
	}
	log.Separator()

	start := time.Now()
	chunks, err := walkAndFlatten(cfg, log, cfg.RepoURL, repoCommit)
	if err != nil {
		return fmt.Errorf("falha ao processar documentação: %v", err)
	}

	duration := time.Since(start).Seconds()
	log.Infof("Total: %d chunks gerados em %.2fs", len(chunks), duration)

	if len(chunks) == 0 {
		log.Warnf("Nenhum arquivo Markdown encontrado em: %s", cfg.RootPath)
		if cfg.Subdir != "" {
			log.Warnf("Verifique se o subdiretório '%s' existe no repositório.", cfg.Subdir)
		}
	}

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
			if err := f.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "Erro ao fechar arquivo de saída:", err)
			}
		}(f)
		out = f
	}

	switch cfg.Format {
	case FormatText:
		if err := outputText(chunks, out); err != nil {
			return fmt.Errorf("falha na saída text: %v", err)
		}
	case FormatJSONL:
		if err := outputJSONL(chunks, out); err != nil {
			return fmt.Errorf("falha na saída jsonl: %v", err)
		}
	case FormatJSON:
		if err := outputJSON(chunks, out); err != nil {
			return fmt.Errorf("falha na saída json: %v", err)
		}
	case FormatYAML:
		if err := outputYAML(chunks, out); err != nil {
			return fmt.Errorf("falha na saída yaml: %v", err)
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
	log := newLogger(os.Stderr)

	err := run(log)

	if err != nil && err.Error() == "METADATA_ONLY" {
		return
	}

	if err != nil {
		log.Errorf("FALHA CRÍTICA: %v", err)
	}

	fmt.Fprintln(os.Stderr, "\n\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintln(os.Stderr, "📋 RELATÓRIO DE EXECUÇÃO (LOGS)")
	fmt.Fprintln(os.Stderr, "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Fprintln(os.Stderr, log.GetHistory())

	if err != nil {
		fmt.Fprintln(os.Stderr, "\n❌ A execução terminou com erros. Verifique os logs acima.")
		os.Exit(1)
	}
}
