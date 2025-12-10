package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
    @coder write --file <path> --content <base64>
    @coder patch --file <path> --search <base64> --replace <base64>
    @coder tree --dir <path>
    @coder search --term "texto" --dir <path>
    @coder exec --cmd "go test"
    @coder rollback --file <path>
    @coder clean --dir <path>`,
		Version: "1.4.0",
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

// --- COMANDO: READ ---
func handleRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	file := fs.String("file", "", "Caminho do arquivo")
	// CORREÇÃO LINT: Verificar erro do Parse
	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	if *file == "" {
		fatalf("--file é obrigatório")
	}

	content, err := os.ReadFile(*file)
	if err != nil {
		fatalf("Erro ao ler arquivo: %v", err)
	}

	lines := strings.Split(string(content), "\n")
	fmt.Printf("<<< INÍCIO DO ARQUIVO: %s >>>\n", *file)
	for i, line := range lines {
		fmt.Printf("%4d | %s\n", i+1, line)
	}
	fmt.Printf("<<< FIM DO ARQUIVO: %s >>>\n", *file)
}

// --- COMANDO: WRITE ---
func handleWrite(args []string) {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	file := fs.String("file", "", "Caminho")
	content := fs.String("content", "", "Conteúdo")
	encoding := fs.String("encoding", "text", "Codificação")
	// CORREÇÃO LINT: Verificar erro do Parse
	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	if *file == "" {
		fatalf("--file é obrigatório")
	}

	var data []byte
	var err error

	rawContent := *content
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

	if *encoding == "base64" {
		cleanBase64 := strings.ReplaceAll(strings.TrimSpace(rawContent), "\n", "")
		cleanBase64 = strings.ReplaceAll(cleanBase64, " ", "")
		data, err = base64.StdEncoding.DecodeString(cleanBase64)
		if err != nil {
			fatalf("Erro Base64: %v", err)
		}
	} else {
		data = []byte(rawContent)
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
	fmt.Printf("✅ Arquivo '%s' escrito.\n", *file)
}

// --- COMANDO: PATCH ---
func handlePatch(args []string) {
	fs := flag.NewFlagSet("patch", flag.ExitOnError)
	file := fs.String("file", "", "Arquivo")
	search := fs.String("search", "", "Busca")
	replace := fs.String("replace", "", "Substituição")
	encoding := fs.String("encoding", "text", "Codificação")
	// CORREÇÃO LINT: Verificar erro do Parse
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

	var searchStr, replaceStr string
	if *encoding == "base64" {
		sBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*search))
		if err != nil {
			fatalf("Erro Base64 search: %v", err)
		}
		searchStr = string(sBytes)
		if *replace != "" {
			rBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*replace))
			if err != nil {
				fatalf("Erro Base64 replace: %v", err)
			}
			replaceStr = string(rBytes)
		}
	} else {
		searchStr = *search
		replaceStr = *replace
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	searchStr = strings.ReplaceAll(searchStr, "\r\n", "\n")
	replaceStr = strings.ReplaceAll(replaceStr, "\r\n", "\n")

	if strings.Count(content, searchStr) == 0 {
		fatalf("❌ Texto não encontrado.")
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
	if err := fs.Parse(args); err != nil {
		fatalf("Erro flags: %v", err)
	}

	if *cmdStr == "" {
		fatalf("--cmd obrigatório")
	}

	fmt.Printf("⚙️ Executando: %s\n----------------\n", *cmdStr)
	cmd := exec.Command("sh", "-c", *cmdStr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("\n❌ Falhou: %v\n", err)
		os.Exit(1)
	} else {
		fmt.Printf("\n✅ Sucesso.\n")
	}
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
