package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

type FlagDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type SubcommandDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Flags       []FlagDefinition `json:"flags"`
	Examples    []string         `json:"examples,omitempty"`
}

type PluginSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ArgsFormat  string                 `json:"args_format"`
	Subcommands []SubcommandDefinition `json:"subcommands"`
}

const (
	pluginVersion     = "2.0.0"
	defaultMaxBytes   = 200_000
	defaultMaxEntries = 2_000
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		printMetadata()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "--schema" {
		printSchema()
		return
	}
	if len(os.Args) < 2 {
		fatalf("Uso: @coder <subcommand> [flags]")
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "read":
		handleRead(args)
	case "write":
		handleWrite(args)
	case "patch":
		handlePatch(args)
	case "tree":
		handleTree(args)
	case "search":
		handleSearch(args)
	case "exec":
		handleExec(args)
	case "rollback":
		handleRollback(args)
	case "clean":
		handleClean(args)
	case "git-status":
		handleGitStatus(args)
	case "git-diff":
		handleGitDiff(args)
	case "git-log":
		handleGitLog(args)
	case "git-changed":
		handleGitChanged(args)
	case "git-branch":
		handleGitBranch(args)
	case "test":
		handleTest(args)
	default:
		fatalf("Comando desconhecido: %s", cmd)
	}
}

func printMetadata() {
	meta := Metadata{
		Name:        "@coder",
		Description: "Suite de engenharia completa (IO, Search, Exec, Git, Test, Backup, Rollback, Patch).",
		Usage:       `@coder <read|write|patch|tree|search|exec|git-status|git-diff|git-log|git-changed|git-branch|test|rollback|clean> [flags]`,
		Version:     pluginVersion,
	}
	_ = json.NewEncoder(os.Stdout).Encode(meta)
}

func printSchema() {
	schema := PluginSchema{
		Name:        "@coder",
		Description: "Ferramentas de engenharia para leitura, escrita, patch, busca, execução e Git.",
		ArgsFormat:  "Aceita argumentos estilo CLI ou JSON (ex.: args=\"{\\\"cmd\\\":\\\"read\\\",\\\"args\\\":{\\\"file\\\":\\\"main.go\\\"}}\")",
		Subcommands: []SubcommandDefinition{
			{
				Name:        "read",
				Description: "Lê arquivos com range, head/tail e limite de bytes.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo.", Required: true},
					{Name: "--start", Type: "int", Description: "Linha inicial (1-based)."},
					{Name: "--end", Type: "int", Description: "Linha final (1-based)."},
					{Name: "--head", Type: "int", Description: "Primeiras N linhas (incompatível com --tail)."},
					{Name: "--tail", Type: "int", Description: "Últimas N linhas (incompatível com --head)."},
					{Name: "--max-bytes", Type: "int", Default: strconv.Itoa(defaultMaxBytes), Description: "Limite de bytes lidos."},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64"},
				},
				Examples: []string{
					"read --file main.go --start 1 --end 120",
				},
			},
			{
				Name:        "write",
				Description: "Escreve arquivo (com backup) com suporte a base64 e append.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo.", Required: true},
					{Name: "--content", Type: "string", Description: "Conteúdo a escrever.", Required: true},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64"},
					{Name: "--append", Type: "bool", Description: "Anexa ao final do arquivo."},
				},
			},
			{
				Name:        "patch",
				Description: "Aplica patch por search/replace ou unified diff.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo (opcional se diff tiver arquivos)."},
					{Name: "--search", Type: "string", Description: "Trecho a substituir (text/base64)."},
					{Name: "--replace", Type: "string", Description: "Substituição (text/base64)."},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64 (para search/replace)"},
					{Name: "--diff", Type: "string", Description: "Unified diff (text/base64)."},
					{Name: "--diff-encoding", Type: "string", Default: "text", Description: "text|base64"},
				},
			},
			{
				Name:        "search",
				Description: "Busca por termo/regex com contexto e limites.",
				Flags: []FlagDefinition{
					{Name: "--term", Type: "string", Description: "Texto ou regex.", Required: true},
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--regex", Type: "bool", Description: "Interpreta --term como regex."},
					{Name: "--case-sensitive", Type: "bool", Description: "Busca case-sensitive (default: false)."},
					{Name: "--context", Type: "int", Description: "Linhas de contexto."},
					{Name: "--max-results", Type: "int", Description: "Limite de resultados."},
					{Name: "--glob", Type: "string", Description: "Filtro glob (ex: *.go,*.md)."},
					{Name: "--max-bytes", Type: "int", Default: "1048576", Description: "Ignora arquivos maiores que N bytes (fallback sem rg)."},
				},
			},
			{
				Name:        "tree",
				Description: "Lista árvore de diretórios com limites.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--max-depth", Type: "int", Default: "6", Description: "Profundidade máxima."},
					{Name: "--max-entries", Type: "int", Default: strconv.Itoa(defaultMaxEntries), Description: "Limite de itens."},
					{Name: "--include-hidden", Type: "bool", Description: "Inclui arquivos ocultos."},
					{Name: "--ignore", Type: "string", Description: "Nomes/padrões separados por vírgula."},
				},
			},
			{
				Name:        "exec",
				Description: "Executa comando shell (com proteção a comandos perigosos).",
				Flags: []FlagDefinition{
					{Name: "--cmd", Type: "string", Description: "Comando a executar.", Required: true},
					{Name: "--dir", Type: "string", Description: "Diretório de execução."},
					{Name: "--timeout", Type: "int", Default: "600", Description: "Timeout em segundos."},
					{Name: "--allow-unsafe", Type: "bool", Description: "Permite comandos perigosos."},
					{Name: "--allow-sudo", Type: "bool", Description: "Permite sudo (ainda bloqueia comandos perigosos)."},
				},
			},
			{
				Name:        "git-status",
				Description: "Status git resumido.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
				},
			},
			{
				Name:        "git-diff",
				Description: "Diff git com opções.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
					{Name: "--staged", Type: "bool", Description: "Diff staged."},
					{Name: "--name-only", Type: "bool", Description: "Somente nomes de arquivos."},
					{Name: "--stat", Type: "bool", Description: "Resumo estatístico."},
					{Name: "--path", Type: "string", Description: "Filtra por caminho."},
					{Name: "--context", Type: "int", Default: "3", Description: "Linhas de contexto."},
				},
			},
			{
				Name:        "git-log",
				Description: "Log git simplificado.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
					{Name: "--limit", Type: "int", Default: "20", Description: "Quantidade de commits."},
					{Name: "--path", Type: "string", Description: "Filtra por caminho."},
				},
			},
			{
				Name:        "git-changed",
				Description: "Lista arquivos alterados (status porcelain).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
				},
			},
			{
				Name:        "git-branch",
				Description: "Branch atual.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório do repo."},
				},
			},
			{
				Name:        "test",
				Description: "Roda testes detectando stack ou via --cmd.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--cmd", Type: "string", Description: "Comando de teste customizado."},
					{Name: "--timeout", Type: "int", Default: "1800", Description: "Timeout em segundos."},
				},
			},
			{
				Name:        "rollback",
				Description: "Restaura arquivo via backup .bak.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "Caminho do arquivo.", Required: true},
				},
			},
			{
				Name:        "clean",
				Description: "Limpa backups .bak (dry-run por padrão).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Diretório base."},
					{Name: "--force", Type: "bool", Description: "Aplica a limpeza (remove arquivos)."},
					{Name: "--pattern", Type: "string", Default: "*.bak", Description: "Padrão de arquivos."},
				},
			},
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(schema)
}

func createBackup(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	input, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".bak", input, 0644)
}

func handleRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ContinueOnError)
	file := fs.String("file", "", "Arquivo")
	start := fs.Int("start", 0, "Linha inicial (1-based)")
	end := fs.Int("end", 0, "Linha final (1-based)")
	head := fs.Int("head", 0, "Primeiras N linhas")
	tail := fs.Int("tail", 0, "Últimas N linhas")
	maxBytes := fs.Int("max-bytes", defaultMaxBytes, "Limite de bytes")
	encoding := fs.String("encoding", "text", "text|base64")
	parseFlagsOrDie(fs, args)

	files := collectFiles(*file, fs.Args())
	if len(files) == 0 {
		fatalf("--file requerido")
	}
	if *head > 0 && *tail > 0 {
		fatalf("--head e --tail são incompatíveis")
	}

	for _, f := range files {
		content, truncated, err := readFileWithLimit(f, *maxBytes)
		if err != nil {
			fmt.Printf("❌ ERRO AO LER '%s': %v\n", f, err)
			continue
		}

		if strings.EqualFold(*encoding, "base64") {
			encoded := base64.StdEncoding.EncodeToString([]byte(content))
			fmt.Printf("<<< INÍCIO DO ARQUIVO (base64): %s >>>\n", f)
			fmt.Println(encoded)
			fmt.Printf("<<< FIM DO ARQUIVO: %s >>>\n\n", f)
			continue
		}

		lines := strings.Split(content, "\n")
		startIdx, endIdx := computeLineRange(len(lines), *start, *end, *head, *tail)
		if startIdx < 0 || endIdx < 0 {
			fmt.Printf("❌ Range inválido para '%s'\n", f)
			continue
		}

		fmt.Printf("<<< INÍCIO DO ARQUIVO: %s >>>\n", f)
		for i := startIdx; i < endIdx; i++ {
			fmt.Printf("%4d | %s\n", i+1, lines[i])
		}
		if truncated {
			fmt.Printf("... [TRUNCADO EM %d BYTES] ...\n", *maxBytes)
		}
		fmt.Printf("<<< FIM DO ARQUIVO: %s >>>\n\n", f)
	}
}

func handleWrite(args []string) {
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	file := fs.String("file", "", "")
	content := fs.String("content", "", "")
	encoding := fs.String("encoding", "text", "")
	appendMode := fs.Bool("append", false, "")
	parseFlagsOrDie(fs, args)

	if *file == "" {
		fatalf("--file requerido")
	}
	if *content == "" {
		fatalf("--content vazio")
	}

	data, err := smartDecode(*content, *encoding)
	if err != nil {
		fatalf("Erro decode: %v", err)
	}

	_ = os.MkdirAll(filepath.Dir(*file), 0755)
	_ = createBackup(*file)

	if *appendMode {
		f, err := os.OpenFile(*file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fatalf("Erro escrita: %v", err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write(data); err != nil {
			fatalf("Erro escrita: %v", err)
		}
	} else {
		if err := os.WriteFile(*file, data, 0644); err != nil {
			fatalf("Erro escrita: %v", err)
		}
	}

	fmt.Printf("✅ Arquivo '%s' escrito (%d bytes).\n", *file, len(data))
}

func handlePatch(args []string) {
	fs := flag.NewFlagSet("patch", flag.ContinueOnError)
	file := fs.String("file", "", "")
	search := fs.String("search", "", "")
	replace := fs.String("replace", "", "")
	encoding := fs.String("encoding", "text", "")
	diff := fs.String("diff", "", "")
	diffEncoding := fs.String("diff-encoding", "text", "")
	parseFlagsOrDie(fs, args)

	if *diff != "" {
		applyUnifiedDiff(*file, *diff, *diffEncoding)
		return
	}

	if *file == "" || *search == "" {
		fatalf("--file e --search requeridos")
	}

	c, err := os.ReadFile(*file)
	if err != nil {
		fatalf("Erro leitura: %v", err)
	}
	content := string(c)

	sBytes, err := smartDecode(*search, *encoding)
	if err != nil {
		fatalf("Search decode error: %v", err)
	}

	rBytes, err := smartDecode(*replace, *encoding)
	if err != nil {
		fatalf("Replace decode error: %v", err)
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	searchStr := strings.ReplaceAll(string(sBytes), "\r\n", "\n")
	replaceStr := strings.ReplaceAll(string(rBytes), "\r\n", "\n")

	if strings.Count(content, searchStr) == 0 {
		fmt.Fprintf(os.Stderr, "DEBUG: Trecho não encontrado.\nBuscado (len=%d):\n%q\n", len(searchStr), searchStr)
		fatalf("❌ Texto não encontrado.")
	}
	_ = createBackup(*file)
	newContent := strings.Replace(content, searchStr, replaceStr, 1)
	if err := os.WriteFile(*file, []byte(newContent), 0644); err != nil {
		fatalf("Erro escrita: %v", err)
	}
	fmt.Printf("✅ Patch aplicado em '%s'.\n", *file)
}

func handleTree(args []string) {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	maxDepth := fs.Int("max-depth", 6, "")
	maxEntries := fs.Int("max-entries", defaultMaxEntries, "")
	includeHidden := fs.Bool("include-hidden", false, "")
	ignore := fs.String("ignore", "", "")
	parseFlagsOrDie(fs, args)

	ignoreSet := parseCSVSet(*ignore)
	defaultIgnore := map[string]bool{".git": true, "node_modules": true, "vendor": true}

	count := 0
	err := filepath.Walk(*dir, func(p string, i os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if count >= *maxEntries {
			return errors.New("limit reached")
		}

		name := i.Name()
		if !*includeHidden && strings.HasPrefix(name, ".") {
			if i.IsDir() && p != *dir {
				return filepath.SkipDir
			}
			return nil
		}
		if defaultIgnore[name] || ignoreSet[name] {
			if i.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if p != *dir {
			rel, _ := filepath.Rel(*dir, p)
			depth := strings.Count(rel, string(os.PathSeparator))
			if depth >= *maxDepth {
				if i.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			fmt.Printf("%s%s\n", strings.Repeat("  ", depth), name)
			count++
		}
		return nil
	})
	if err != nil && !strings.Contains(err.Error(), "limit reached") {
		fmt.Printf("Erro ao gerar árvore: %v\n", err)
	}

	if count >= *maxEntries {
		fmt.Printf("... [LIMITADO EM %d ENTRADAS] ...\n", *maxEntries)
	}
}

func handleSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	term := fs.String("term", "", "")
	dir := fs.String("dir", ".", "")
	useRegex := fs.Bool("regex", false, "")
	caseSensitive := fs.Bool("case-sensitive", false, "")
	contextLines := fs.Int("context", 0, "")
	maxResults := fs.Int("max-results", 0, "")
	glob := fs.String("glob", "", "")
	maxBytes := fs.Int("max-bytes", 1_048_576, "")
	parseFlagsOrDie(fs, args)

	if *term == "" {
		fatalf("--term requerido")
	}

	if rgPath, err := exec.LookPath("rg"); err == nil {
		runRipgrep(rgPath, *term, *dir, *useRegex, *caseSensitive, *contextLines, *maxResults, *glob)
		return
	}

	fallbackSearch(*term, *dir, *useRegex, *caseSensitive, *contextLines, *maxResults, *glob, *maxBytes)
}

func handleExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	cmdStr := fs.String("cmd", "", "")
	dir := fs.String("dir", "", "")
	timeout := fs.Int("timeout", 600, "")
	allowUnsafe := fs.Bool("allow-unsafe", false, "")
	allowSudo := fs.Bool("allow-sudo", false, "")
	parseFlagsOrDie(fs, args)
	if *cmdStr == "" {
		fatalf("--cmd requerido")
	}

	finalCmd := html.UnescapeString(*cmdStr)
	if !*allowUnsafe {
		if unsafe, reason := isUnsafeCommand(finalCmd, *allowSudo); unsafe {
			fatalf("Comando bloqueado: %s", reason)
		}
	}

	fmt.Printf("⚙️ Executando: %s\n", finalCmd)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", finalCmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", finalCmd)
	}
	if *dir != "" {
		cmd.Dir = *dir
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		fatalf("Start error: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	stream := func(r io.Reader, w *os.File) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				_ = w.Sync()
			}
			if err != nil {
				if err == io.EOF {
					return
				}
				return
			}
		}
	}
	go stream(stdout, os.Stdout)
	go stream(stderr, os.Stderr)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		fmt.Printf("❌ Falhou: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Sucesso.")
}

func handleRollback(args []string) {
	fs := flag.NewFlagSet("rb", flag.ContinueOnError)
	file := fs.String("file", "", "")
	parseFlagsOrDie(fs, args)
	if *file == "" {
		fatalf("--file requerido")
	}
	c, err := os.ReadFile(*file + ".bak")
	if err != nil {
		fatalf("Backup error: %v", err)
	}
	_ = os.WriteFile(*file, c, 0644)
	fmt.Println("✅ Rollback ok.")
}

func handleClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	force := fs.Bool("force", false, "")
	pattern := fs.String("pattern", "*.bak", "")
	parseFlagsOrDie(fs, args)

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
		fmt.Println("Nenhum arquivo para limpar.")
		return
	}

	if !*force {
		fmt.Println("🧹 Dry-run: arquivos que seriam removidos:")
		for _, m := range matches {
			fmt.Println(m)
		}
		fmt.Println("Use --force para remover.")
		return
	}

	for _, m := range matches {
		_ = os.Remove(m)
	}
	fmt.Printf("✅ Removidos %d arquivos.\n", len(matches))
}

func handleGitStatus(args []string) {
	fs := flag.NewFlagSet("git-status", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	parseFlagsOrDie(fs, args)

	out, err := runCommand(*dir, "git", "status", "-sb")
	printCommandOutput(out, err)
}

func handleGitDiff(args []string) {
	fs := flag.NewFlagSet("git-diff", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	staged := fs.Bool("staged", false, "")
	nameOnly := fs.Bool("name-only", false, "")
	stat := fs.Bool("stat", false, "")
	path := fs.String("path", "", "")
	context := fs.Int("context", 3, "")
	parseFlagsOrDie(fs, args)

	cmdArgs := []string{"diff", fmt.Sprintf("--unified=%d", *context)}
	if *staged {
		cmdArgs = append(cmdArgs, "--staged")
	}
	if *nameOnly {
		cmdArgs = append(cmdArgs, "--name-only")
	}
	if *stat {
		cmdArgs = append(cmdArgs, "--stat")
	}
	if *path != "" {
		cmdArgs = append(cmdArgs, "--", *path)
	}

	out, err := runCommand(*dir, "git", cmdArgs...)
	printCommandOutput(out, err)
}

func handleGitLog(args []string) {
	fs := flag.NewFlagSet("git-log", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	limit := fs.Int("limit", 20, "")
	path := fs.String("path", "", "")
	parseFlagsOrDie(fs, args)

	cmdArgs := []string{"log", "--oneline", fmt.Sprintf("-n%d", *limit)}
	if *path != "" {
		cmdArgs = append(cmdArgs, "--", *path)
	}

	out, err := runCommand(*dir, "git", cmdArgs...)
	printCommandOutput(out, err)
}

func handleGitChanged(args []string) {
	fs := flag.NewFlagSet("git-changed", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	parseFlagsOrDie(fs, args)

	out, err := runCommand(*dir, "git", "status", "--porcelain")
	if err != nil {
		printCommandOutput(out, err)
		return
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var files []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if len(l) > 3 {
			files = append(files, strings.TrimSpace(l[3:]))
		}
	}

	for _, f := range files {
		fmt.Println(f)
	}
}

func handleGitBranch(args []string) {
	fs := flag.NewFlagSet("git-branch", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	parseFlagsOrDie(fs, args)

	out, err := runCommand(*dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	printCommandOutput(out, err)
}

func handleTest(args []string) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	cmd := fs.String("cmd", "", "")
	timeout := fs.Int("timeout", 1800, "")
	parseFlagsOrDie(fs, args)

	finalCmd := strings.TrimSpace(*cmd)
	if finalCmd == "" {
		finalCmd = detectTestCommand(*dir)
		if finalCmd == "" {
			fatalf("Não foi possível detectar comando de teste. Use --cmd.")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()

	fmt.Printf("🧪 Rodando testes: %s\n", finalCmd)
	out, err := runCommandWithContext(ctx, *dir, finalCmd)
	printCommandOutput(out, err)
}

func fatalf(msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERROR: "+msg+"\n", args...)
	os.Exit(1)
}

// parseFlagsOrDie parses flags with ContinueOnError and provides helpful error messages
// including the available flags for the subcommand.
func parseFlagsOrDie(fs *flag.FlagSet, args []string) {
	// Redirect default error output to discard (we handle errors ourselves)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		// Collect available flags for help message
		var flags []string
		fs.VisitAll(func(f *flag.Flag) {
			flags = append(flags, fmt.Sprintf("--%s (%s, default: %q)", f.Name, f.Usage, f.DefValue))
		})
		fatalf("Flag parse error in '%s': %v\nAvailable flags:\n  %s",
			fs.Name(), err, strings.Join(flags, "\n  "))
	}
}

func smartDecode(content, enc string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "base64":
		// Explicit base64: clean non-base64 chars and decode
		reg := regexp.MustCompile("[^a-zA-Z0-9+/=]")
		clean := reg.ReplaceAllString(content, "")
		decoded, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			// Try URL-safe base64 as fallback
			decoded, err = base64.URLEncoding.DecodeString(clean)
		}
		if err != nil {
			// Try without padding
			decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(clean, "="))
		}
		return decoded, err
	case "auto":
		// Explicit auto-detect mode: try base64 only if it really looks like it
		if !strings.Contains(content, " ") && !strings.Contains(content, "\n") && len(content) >= 4 && len(content)%4 == 0 {
			reg := regexp.MustCompile("[^a-zA-Z0-9+/=]")
			clean := reg.ReplaceAllString(content, "")
			if d, err := base64.StdEncoding.DecodeString(clean); err == nil && utf8.Valid(d) {
				return d, nil
			}
		}
		return []byte(content), nil
	default:
		// "text" or anything else: return as-is, never auto-detect
		return []byte(content), nil
	}
}

func collectFiles(primary string, extras []string) []string {
	var files []string
	if primary != "" {
		files = append(files, strings.Trim(primary, "\"'"))
	}
	for _, f := range extras {
		files = append(files, strings.Trim(f, "\"'"))
	}
	return files
}

func readFileWithLimit(path string, maxBytes int) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return string(data[:maxBytes]), true, nil
	}
	return string(data), false, nil
}

func computeLineRange(total, start, end, head, tail int) (int, int) {
	if total <= 0 {
		return 0, 0
	}

	if head > 0 {
		if head > total {
			head = total
		}
		return 0, head
	}
	if tail > 0 {
		if tail > total {
			tail = total
		}
		return total - tail, total
	}

	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > total {
		end = total
	}

	startIdx := start - 1
	endIdx := end
	if startIdx < 0 || startIdx >= total || endIdx < start {
		return -1, -1
	}
	return startIdx, endIdx
}

func parseCSVSet(input string) map[string]bool {
	set := make(map[string]bool)
	for _, item := range strings.Split(input, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		set[item] = true
	}
	return set
}

func runRipgrep(rgPath, term, dir string, useRegex, caseSensitive bool, contextLines, maxResults int, glob string) {
	args := []string{"--line-number", "--column", "--color", "never"}
	if !caseSensitive {
		args = append(args, "-i")
	}
	if !useRegex {
		args = append(args, "-F")
	}
	if contextLines > 0 {
		args = append(args, "-C", strconv.Itoa(contextLines))
	}
	if maxResults > 0 {
		args = append(args, "--max-count", strconv.Itoa(maxResults))
	}
	for _, g := range splitCSV(glob) {
		args = append(args, "--glob", g)
	}
	args = append(args, term, dir)

	out, err := runCommand("", rgPath, args...)
	printCommandOutput(out, err)
}

func fallbackSearch(term, dir string, useRegex, caseSensitive bool, contextLines, maxResults int, glob string, maxBytes int) {
	var re *regexp.Regexp
	var err error
	if useRegex {
		if !caseSensitive {
			term = "(?i)" + term
		}
		re, err = regexp.Compile(term)
		if err != nil {
			fatalf("Regex inválida: %v", err)
		}
	}

	globSet := splitCSV(glob)
	count := 0

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}

		if maxResults > 0 && count >= maxResults {
			return errors.New("limit reached")
		}

		if len(globSet) > 0 {
			matched := false
			for _, g := range globSet {
				ok, _ := filepath.Match(g, filepath.Base(path))
				if ok {
					matched = true
					break
				}
			}
			if !matched {
				return nil
			}
		}

		if maxBytes > 0 && info.Size() > int64(maxBytes) {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		for i, line := range lines {
			match := false
			if useRegex {
				match = re.MatchString(line)
			} else {
				hay := line
				needle := term
				if !caseSensitive {
					hay = strings.ToLower(hay)
					needle = strings.ToLower(needle)
				}
				match = strings.Contains(hay, needle)
			}

			if match {
				start := i - contextLines
				if start < 0 {
					start = 0
				}
				end := i + contextLines + 1
				if end > len(lines) {
					end = len(lines)
				}
				for j := start; j < end; j++ {
					prefix := " "
					if j == i {
						prefix = ">"
					}
					fmt.Printf("%s %s:%d:%s\n", prefix, path, j+1, lines[j])
				}
				count++
				if maxResults > 0 && count >= maxResults {
					return errors.New("limit reached")
				}
			}
		}
		return nil
	})
}

func splitCSV(input string) []string {
	var out []string
	for _, item := range strings.Split(input, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func isUnsafeCommand(cmd string, allowSudo bool) (bool, string) {
	lower := strings.ToLower(cmd)
	dangerPatterns := []string{
		`\brm\s+-rf\s+/`,
		`\brm\s+-rf\s+~`,
		`\bmkfs\b`,
		`\bdd\b`,
		`\bshutdown\b`,
		`\breboot\b`,
		`\bpoweroff\b`,
		`\bkill\s+-9\s+1\b`,
		`\b:>\s*/`,
	}
	for _, p := range dangerPatterns {
		re := regexp.MustCompile(p)
		if re.MatchString(lower) {
			return true, fmt.Sprintf("Padrão perigoso detectado (%s)", p)
		}
	}
	if strings.Contains(lower, "sudo ") && !allowSudo {
		return true, "Uso de sudo bloqueado (use --allow-sudo)"
	}
	return false, ""
}

func runCommand(dir, cmd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, cmd, args...)
	if dir != "" {
		command.Dir = dir
	}
	out, err := command.CombinedOutput()
	return string(out), err
}

func runCommandWithContext(ctx context.Context, dir, cmdLine string) (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", cmdLine)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", cmdLine)
	}
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func printCommandOutput(out string, err error) {
	if strings.TrimSpace(out) != "" {
		fmt.Println(strings.TrimRight(out, "\n"))
	}
	if err != nil {
		fmt.Printf("❌ Falhou: %v\n", err)
		os.Exit(1)
	}
}

// ===== Unified Diff Apply =====

type diffHunk struct {
	oldStart int
	oldLines int
	newStart int
	newLines int
	lines    []diffLine
}

type diffLine struct {
	kind byte
	text string
}

func applyUnifiedDiff(fileArg, diffText, enc string) {
	decoded, err := smartDecode(diffText, enc)
	if err != nil {
		fatalf("Erro decode diff: %v", err)
	}

	files, err := parseUnifiedDiff(string(decoded))
	if err != nil {
		fatalf("Diff inválido: %v", err)
	}

	if len(files) == 0 {
		fatalf("Diff vazio")
	}

	if fileArg != "" {
		hunks, ok := files[fileArg]
		if !ok {
			clean := filepath.Clean(fileArg)
			for k, h := range files {
				if filepath.Clean(k) == clean {
					hunks = h
					ok = true
					break
				}
			}
			if len(files) == 1 {
				for _, h := range files {
					hunks = h
					ok = true
					break
				}
			}
		}
		if !ok {
			fatalf("Arquivo não encontrado no diff e --file informado não confere")
		}
		applyHunksToFile(fileArg, hunks)
		return
	}

	if _, ok := files["__single__"]; ok {
		fatalf("Diff sem headers de arquivo: informe --file")
	}
	for path, hunks := range files {
		applyHunksToFile(path, hunks)
	}
}

func parseUnifiedDiff(diffText string) (map[string][]diffHunk, error) {
	text := strings.ReplaceAll(diffText, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	files := make(map[string][]diffHunk)

	var currentFile string
	var i int
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "--- ") {
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ ") {
				currentFile = normalizeDiffPath(strings.TrimPrefix(lines[i+1], "+++ "))
				i += 2
				continue
			}
		}

		if strings.HasPrefix(line, "@@ ") {
			hunk, next, err := parseHunk(lines, i)
			if err != nil {
				return nil, err
			}
			if currentFile == "" {
				currentFile = "__single__"
			}
			files[currentFile] = append(files[currentFile], hunk)
			i = next
			continue
		}
		i++
	}

	// Se veio sem headers de arquivo, usamos __single__
	if _, ok := files["__single__"]; ok {
		only := files["__single__"]
		delete(files, "__single__")
		files["__single__"] = only
	}

	return files, nil
}

func parseHunk(lines []string, start int) (diffHunk, int, error) {
	header := lines[start]
	re := regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
	m := re.FindStringSubmatch(header)
	if m == nil {
		return diffHunk{}, start + 1, fmt.Errorf("header de hunk inválido: %s", header)
	}
	oldStart, _ := strconv.Atoi(m[1])
	oldLines := 1
	if m[2] != "" {
		oldLines, _ = strconv.Atoi(m[2])
	}
	newStart, _ := strconv.Atoi(m[3])
	newLines := 1
	if m[4] != "" {
		newLines, _ = strconv.Atoi(m[4])
	}

	hunk := diffHunk{oldStart: oldStart, oldLines: oldLines, newStart: newStart, newLines: newLines}
	i := start + 1
	for i < len(lines) {
		line := lines[i]
		if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, "--- ") {
			break
		}
		if strings.HasPrefix(line, "\\ No newline at end of file") {
			i++
			continue
		}
		if len(line) == 0 {
			hunk.lines = append(hunk.lines, diffLine{kind: ' ', text: ""})
			i++
			continue
		}
		kind := line[0]
		if kind != ' ' && kind != '+' && kind != '-' {
			return diffHunk{}, start + 1, fmt.Errorf("linha inválida no diff: %s", line)
		}
		hunk.lines = append(hunk.lines, diffLine{kind: kind, text: line[1:]})
		i++
	}

	return hunk, i, nil
}

func normalizeDiffPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}

func applyHunksToFile(path string, hunks []diffHunk) {
	content, err := os.ReadFile(path)
	if err != nil {
		fatalf("Erro leitura: %v", err)
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	trailingNewline := strings.HasSuffix(text, "\n")

	offset := 0
	for _, h := range hunks {
		idx := h.oldStart - 1 + offset
		if idx < 0 || idx > len(lines) {
			fatalf("Hunk fora do range do arquivo: %s", path)
		}

		cur := idx
		newChunk := make([]string, 0, len(h.lines))
		for _, dl := range h.lines {
			switch dl.kind {
			case ' ':
				if cur >= len(lines) || lines[cur] != dl.text {
					fatalf("Hunk mismatch no arquivo %s", path)
				}
				newChunk = append(newChunk, lines[cur])
				cur++
			case '-':
				if cur >= len(lines) || lines[cur] != dl.text {
					fatalf("Hunk mismatch no arquivo %s", path)
				}
				cur++
			case '+':
				newChunk = append(newChunk, dl.text)
			}
		}

		lines = append(lines[:idx], append(newChunk, lines[cur:]...)...)
		offset += len(newChunk) - (cur - idx)
	}

	newText := strings.Join(lines, "\n")
	if trailingNewline && !strings.HasSuffix(newText, "\n") {
		newText += "\n"
	}

	_ = createBackup(path)
	if err := os.WriteFile(path, []byte(newText), 0644); err != nil {
		fatalf("Erro escrita: %v", err)
	}
	fmt.Printf("✅ Diff aplicado em '%s'.\n", path)
}

func detectTestCommand(dir string) string {
	if fileExists(filepath.Join(dir, "go.mod")) {
		return "go test ./..."
	}
	if fileExists(filepath.Join(dir, "package.json")) {
		return "npm test"
	}
	if fileExists(filepath.Join(dir, "pyproject.toml")) || fileExists(filepath.Join(dir, "pytest.ini")) {
		return "pytest -q"
	}
	if fileExists(filepath.Join(dir, "Cargo.toml")) {
		return "cargo test"
	}
	if fileExists(filepath.Join(dir, "pom.xml")) {
		return "mvn test"
	}
	if fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts")) {
		return "gradle test"
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
