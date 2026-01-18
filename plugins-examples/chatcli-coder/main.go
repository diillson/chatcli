package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Metadata define a estrutura de descoberta do plugin
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		printMetadata()
		return
	}

	if len(os.Args) < 2 {
		fatalf("Uso: @coder <read|write|patch|tree|search|exec|rollback|clean> [opções]")
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
	default:
		fatalf("Comando desconhecido: %s", cmd)
	}
}

func printMetadata() {
	meta := Metadata{
		Name:        "@coder",
		Description: "Suite de engenharia completa (IO, Search, Exec, Backup, Rollback).",
		Usage: `@coder read --file <path>
    @coder write --file <path> --content <base64> [--encoding text|base64]
    @coder patch --file <path> --search <base64> --replace <base64> [--encoding text|base64]
    @coder tree --dir <path>
    @coder search --term "texto" --dir <path>
    @coder exec --cmd "<comando>" [--dir <path>] [--timeout <segundos>] [--heartbeat <segundos>] [--non-interactive true|false]
    @coder rollback --file <path>
    @coder clean --dir <path>
    # Explicitamente necessário uso de [--encoding base64] caso contrário haverá falha de escrita com base64 cru em arquivo.`,
		Version: "1.5.1",
	}
	// CORREÇÃO LINT: Verificar erro do Encode
	if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
		fatalf("Erro ao gerar metadados: %v", err)
	}
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

func showDiff(filename, oldContent, newContent string) {
	fmt.Printf("\n📝 DIFF (%s):\n", filename)
	fmt.Println("----------------------------------------")
	if oldContent == "" {
		fmt.Println("+++ NOVO ARQUIVO +++")
	} else if oldContent == newContent {
		fmt.Println("= SEM ALTERAÇÕES =")
	} else {
		fmt.Printf("Antigo: %d bytes -> Novo: %d bytes\n", len(oldContent), len(newContent))
	}
	fmt.Println("----------------------------------------")
}

// --- COMANDO: READ (Suporta múltiplos arquivos) ---
func handleRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	file := fs.String("file", "", "Caminho do arquivo (principal)")

	// Parse ignora o que vem depois das flags definidas, jogando em fs.Args()
	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	// Coleta todos os arquivos: o da flag --file e os argumentos soltos
	var filesToRead []string
	if *file != "" {
		filesToRead = append(filesToRead, *file)
	}
	// Adiciona argumentos extras (ex: read --file a.go b.go c.go)
	filesToRead = append(filesToRead, fs.Args()...)

	if len(filesToRead) == 0 {
		fatalf("Nenhum arquivo especificado. Uso: @coder read --file <path> [outros_paths...]")
	}

	// Itera e lê todos
	for _, f := range filesToRead {
		// Limpeza básica de aspas que a IA as vezes manda
		f = strings.Trim(f, "\"'")

		content, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("❌ ERRO AO LER '%s': %v\n", f, err)
			continue
		}

		lines := strings.Split(string(content), "\n")
		fmt.Printf("<<< INÍCIO DO ARQUIVO: %s >>>\n", f)
		for i, line := range lines {
			fmt.Printf("%4d | %s\n", i+1, line)
		}
		fmt.Printf("<<< FIM DO ARQUIVO: %s >>>\n\n", f)
	}
}

// --- COMANDO: WRITE ---
func handleWrite(args []string) {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	file := fs.String("file", "", "Caminho")
	content := fs.String("content", "", "Conteúdo")
	encoding := fs.String("encoding", "text", "Codificação")

	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	if *file == "" {
		fatalf("--file é obrigatório")
	}

	var rawContent string = *content
	if rawContent == "" {
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			input, _ := io.ReadAll(os.Stdin)
			rawContent = string(input)
		}
	}

	if rawContent == "" {
		fatalf("Conteúdo vazio.")
	}

	// USANDO A NOVA LÓGICA SMART
	data, err := smartDecode(rawContent, *encoding)
	if err != nil {
		fatalf("Erro no processamento do conteúdo: %v", err)
	}

	dir := filepath.Dir(*file)
	if dir != "." && dir != "/" {
		_ = os.MkdirAll(dir, 0755)
	}

	if err := createBackup(*file); err != nil {
		fmt.Printf("⚠️ Aviso: Falha no backup: %v\n", err)
	}

	oldBytes, _ := os.ReadFile(*file)
	if err := os.WriteFile(*file, data, 0644); err != nil {
		fatalf("Erro escrita: %v", err)
	}

	showDiff(*file, string(oldBytes), string(data))
	fmt.Printf("✅ Arquivo '%s' escrito (Modo detectado: %s -> %d bytes).\n", *file, *encoding, len(data))
}

// --- COMANDO: PATCH ---
func handlePatch(args []string) {
	fs := flag.NewFlagSet("patch", flag.ExitOnError)
	file := fs.String("file", "", "Arquivo")
	search := fs.String("search", "", "Busca")
	replace := fs.String("replace", "", "Substituição")
	encoding := fs.String("encoding", "text", "Codificação")

	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	if *file == "" || *search == "" {
		fatalf("--file e --search obrigatórios")
	}

	contentBytes, err := os.ReadFile(*file)
	if err != nil {
		fatalf("Erro leitura: %v", err)
	}
	content := string(contentBytes)

	// USANDO A NOVA LÓGICA SMART PARA SEARCH E REPLACE
	sBytes, err := smartDecode(*search, *encoding)
	if err != nil {
		fatalf("Erro ao processar search: %v", err)
	}
	searchStr := string(sBytes)

	var replaceStr string
	if *replace != "" {
		rBytes, err := smartDecode(*replace, *encoding)
		if err != nil {
			fatalf("Erro ao processar replace: %v", err)
		}
		replaceStr = string(rBytes)
	}

	// Normalização de quebras de linha para aumentar a chance de match
	content = strings.ReplaceAll(content, "\r\n", "\n")
	searchStr = strings.ReplaceAll(searchStr, "\r\n", "\n")
	replaceStr = strings.ReplaceAll(replaceStr, "\r\n", "\n")

	if strings.Count(content, searchStr) == 0 {
		// Log de debug para ajudar a IA a se corrigir
		fmt.Fprintf(os.Stderr, "DEBUG: Trecho não encontrado.\n")
		fmt.Fprintf(os.Stderr, "Buscado (len=%d):\n%q\n", len(searchStr), searchStr)
		fatalf("❌ Texto não encontrado no arquivo. Verifique espaços e quebras de linha.")
	}

	if err := createBackup(*file); err != nil {
		fmt.Printf("⚠️ Aviso: Falha no backup: %v\n", err)
	}

	newContent := strings.Replace(content, searchStr, replaceStr, 1)
	if err := os.WriteFile(*file, []byte(newContent), 0644); err != nil {
		fatalf("Erro escrita: %v", err)
	}

	fmt.Printf("✅ Patch aplicado em '%s'.\n", *file)
}

// --- COMANDO: TREE ---
func handleTree(args []string) {
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	dir := fs.String("dir", ".", "Diretório")
	// CORREÇÃO LINT: Verificar erro do Parse
	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	// CORREÇÃO LINT: Verificar erro do Walk
	err := filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "vendor") {
			return filepath.SkipDir
		}
		if path == *dir {
			return nil
		}
		rel, _ := filepath.Rel(*dir, path)
		depth := strings.Count(rel, string(os.PathSeparator))
		indent := strings.Repeat("  ", depth)
		icon := "📄"
		if info.IsDir() {
			icon = "📁"
		}
		fmt.Printf("%s%s %s\n", indent, icon, info.Name())
		return nil
	})

	if err != nil {
		fatalf("Erro ao listar diretório: %v", err)
	}
}

// --- COMANDO: SEARCH ---
func handleSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	term := fs.String("term", "", "Termo")
	dir := fs.String("dir", ".", "Diretório")
	if err := fs.Parse(args); err != nil {
		fatalf("Erro flags: %v", err)
	}

	if *term == "" {
		fatalf("--term obrigatório")
	}

	fmt.Printf("🔍 Buscando '%s' em '%s'...\n", *term, *dir)
	err := filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(content), *term) {
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if strings.Contains(line, *term) {
					fmt.Printf("%s:%d: %s\n", path, i+1, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		fatalf("Erro busca: %v", err)
	}
}

// --- COMANDO: EXEC ---
func handleExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)

	cmdStr := fs.String("cmd", "", "Comando")
	timeoutSec := fs.Int("timeout", 600, "Timeout em segundos (0 = sem timeout)")
	dir := fs.String("dir", "", "Diretório de trabalho (opcional)")
	heartbeatSec := fs.Int("heartbeat", 15, "Heartbeat em segundos (0 = desabilita)")
	nonInteractive := fs.Bool("non-interactive", true, "Configura ambiente para execução não-interativa (genérico)")

	if err := fs.Parse(args); err != nil {
		fatalf("Erro flags: %v", err)
	}
	if *cmdStr == "" {
		fatalf("--cmd obrigatório")
	}

	decodedCmd := html.UnescapeString(*cmdStr)
	// Remove qualquer espaço em branco no início ou fim que possa ter sobrado do parsing.
	decodedCmd = strings.TrimSpace(decodedCmd)
	re := regexp.MustCompile(`\\\s*[\r\n]+`)
	finalCmd := re.ReplaceAllString(decodedCmd, " ")
	spaceRe := regexp.MustCompile(`\s+`)
	finalCmd = spaceRe.ReplaceAllString(strings.TrimSpace(finalCmd), " ")

	fmt.Printf("⚙️ Executando: %s\n----------------\n", finalCmd)

	// Context com timeout
	ctx := context.Background()
	var cancel context.CancelFunc
	if *timeoutSec > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*timeoutSec)*time.Second)
		defer cancel()
	}

	// Shell cross-platform
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", finalCmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", finalCmd)
	}

	if *dir != "" {
		cmd.Dir = *dir
	}

	// Ambiente não-interativo (genérico)
	// CI=true e TERM=dumb ajudam diversas CLIs (npm/pip/gradle/mvn/etc).
	if *nonInteractive {
		env := os.Environ()
		env = append(env,
			"CI=true",
			"TERM=dumb",
		)
		cmd.Env = env
	}

	// Pipes para streaming confiável (não depende do runner do chatcli)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fatalf("Erro ao criar stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		fatalf("Erro ao criar stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		fatalf("Erro ao iniciar comando: %v", err)
	}

	// Heartbeat: evita “silêncio infinito”
	done := make(chan struct{})
	if *heartbeatSec > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(*heartbeatSec) * time.Second)
			defer ticker.Stop()
			start := time.Now()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					fmt.Fprintf(os.Stderr, "⏳ ainda executando... (%ds)\n", int(time.Since(start).Seconds()))
					_ = os.Stderr.Sync()
				}
			}
		}()
	}

	// Stream stdout/stderr em tempo real (scanner com buffer aumentado)
	var wg sync.WaitGroup
	wg.Add(2)

	stream := func(r io.Reader, w *os.File) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		buf := make([]byte, 0, 64*1024)
		sc.Buffer(buf, 1024*1024) // 1MB por linha
		for sc.Scan() {
			fmt.Fprintln(w, sc.Text())
			_ = w.Sync()
		}
	}

	go stream(stdout, os.Stdout)
	go stream(stderr, os.Stderr)

	waitErr := cmd.Wait()
	close(done)
	wg.Wait()

	if ctx.Err() == context.DeadlineExceeded {
		fmt.Fprintf(os.Stderr, "\n❌ Timeout após %ds\n", *timeoutSec)
		os.Exit(1)
	}

	if waitErr != nil {
		fmt.Printf("\n❌ Falhou: %v\n", waitErr)
		os.Exit(1)
	}

	fmt.Printf("\n✅ Sucesso.\n")
}

// --- COMANDO: ROLLBACK ---
func handleRollback(args []string) {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	file := fs.String("file", "", "Arquivo")
	if err := fs.Parse(args); err != nil {
		fatalf("Erro flags: %v", err)
	}

	if *file == "" {
		fatalf("--file obrigatório")
	}

	backupPath := *file + ".bak"
	content, err := os.ReadFile(backupPath)
	if err != nil {
		fatalf("Backup não encontrado ou erro leitura: %v", err)
	}

	if err := os.WriteFile(*file, content, 0644); err != nil {
		fatalf("Erro restauração: %v", err)
	}
	fmt.Printf("✅ Rollback de '%s' concluído.\n", *file)
}

// --- COMANDO: CLEAN ---
func handleClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	dir := fs.String("dir", ".", "Diretório")
	if err := fs.Parse(args); err != nil {
		fatalf("Erro flags: %v", err)
	}

	fmt.Printf("🧹 Limpando .bak em '%s'...\n", *dir)
	count := 0
	err := filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".bak") {
			if err := os.Remove(path); err == nil {
				fmt.Printf("   🗑️ %s\n", path)
				count++
			}
		}
		return nil
	})
	if err != nil {
		fatalf("Erro limpeza: %v", err)
	}
	fmt.Printf("✅ %d arquivos removidos.\n", count)
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERRO: "+format+"\n", args...)
	os.Exit(1)
}

// --- FUNÇÃO AUXILIAR: Decodificação Inteligente ---
func smartDecode(content, encodingFlag string) ([]byte, error) {
	// Limpeza básica
	cleanContent := strings.TrimSpace(content)

	if strings.HasPrefix(cleanContent, "\\") && !strings.HasPrefix(cleanContent, "\\\\") {
		// Verifica se o segundo caractere é uma quebra de linha ou espaço, indicando sobra de formatação
		if len(cleanContent) > 1 {
			// Remove a barra inicial se parecer lixo de formatação
			cleanContent = strings.TrimPrefix(cleanContent, "\\")
			cleanContent = strings.TrimSpace(cleanContent)
		}
	}

	// Remove quebras de linha que possam ter vindo no meio do base64
	cleanContentNoNewlines := strings.ReplaceAll(cleanContent, "\n", "")
	cleanContentNoNewlines = strings.ReplaceAll(cleanContentNoNewlines, "\r", "")
	cleanContentNoNewlines = strings.ReplaceAll(cleanContentNoNewlines, " ", "")

	// 1. Se a flag for explicitamente base64, tenta decodificar estritamente
	if encodingFlag == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(cleanContentNoNewlines)
		if err != nil {
			return nil, fmt.Errorf("falha ao decodificar base64 explícito: %v", err)
		}
		return decoded, nil
	}

	// 2. Se a flag for "text" (ou padrão), verificamos se PARECE base64
	// Heurística:
	// - Tem tamanho múltiplo de 4?
	// - Só tem caracteres de base64?
	// - Se decodificar, o resultado parece binário útil ou texto utf8?

	// Se tiver espaços no meio do conteúdo original (não nas pontas), provavelmente é texto normal
	if strings.Contains(strings.TrimSpace(content), " ") {
		return []byte(content), nil
	}

	// Tenta decodificar como teste
	decoded, err := base64.StdEncoding.DecodeString(cleanContentNoNewlines)
	if err == nil {
		// Sucesso na decodificação. É um base64 válido.
		// Mas cuidado: a palavra "admin" é um base64 válido.
		// Vamos assumir que se o modelo mandou um bloco contínuo sem espaços e decodifica,
		// ele provavelmente queria mandar base64, já que o prompt pede isso.

		// Opcional: Verificar se é UTF-8 válido (para arquivos de código)
		if utf8.Valid(decoded) {
			// Se for válido e tiver um tamanho razoável, assumimos base64
			return decoded, nil
		}
		// Se decodificou mas virou binário estranho, pode ser um arquivo binário (imagem),
		// então também retornamos o decoded.
		return decoded, nil
	}

	// 3. Fallback: Trata como texto puro
	return []byte(content), nil
}
