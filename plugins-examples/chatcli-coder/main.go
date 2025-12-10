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
		fatalf("Uso: @coder <read|write|patch|tree> [opções]")
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
		Description: "Suite de engenharia de software (IO, Search, Exec) com Auto-Backup e Diff.",
		Usage: `@coder read --file <path>
    @coder write --file <path> --content <base64> --encoding base64
    @coder patch --file <path> --search <base64> --replace <base64> --encoding base64
    @coder tree --dir <path>
    @coder search --term "texto" --dir <path>
    @coder exec --cmd "go test ./..."
    @coder rollback --file <path>
    @coder clean --dir <path>`,
		Version: "1.3.0",
	}
	json.NewEncoder(os.Stdout).Encode(meta)
}

func createBackup(path string) error {
	// Se o arquivo não existe (criação), não há backup
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	input, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Backup simples: nomearquivo.go.bak
	backupPath := path + ".bak"

	return os.WriteFile(backupPath, input, 0644)
}

func showDiff(filename, oldContent, newContent string) {
	fmt.Printf("\n📝 DIFF (%s):\n", filename)
	fmt.Println("----------------------------------------")

	// Se for arquivo novo
	if oldContent == "" {
		fmt.Println("+++ NOVO ARQUIVO +++")
		// Mostra as primeiras 5 linhas para não poluir
		lines := strings.Split(newContent, "\n")
		for i := 0; i < len(lines) && i < 5; i++ {
			fmt.Printf("+ %s\n", lines[i])
		}
		if len(lines) > 5 {
			fmt.Printf("... (+%d linhas)\n", len(lines)-5)
		}
		fmt.Println("----------------------------------------")
		return
	}

	// Diff simples baseado em tamanho e preview
	// (Um diff real linha a linha em Go puro seria grande para este exemplo,
	// então focamos em mostrar que houve mudança)
	if oldContent == newContent {
		fmt.Println("= SEM ALTERAÇÕES =")
	} else {
		fmt.Printf("Antigo: %d bytes -> Novo: %d bytes\n", len(oldContent), len(newContent))
		// Se foi um patch, tentamos mostrar o contexto
		// Mas como o patch já substitui blocos, o agente sabe o que fez.
		// Vamos apenas confirmar a alteração.
	}
	fmt.Println("----------------------------------------")
}

// --- COMANDO: READ ---
func handleRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	file := fs.String("file", "", "Caminho do arquivo")
	fs.Parse(args)

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
	file := fs.String("file", "", "Caminho do arquivo")
	content := fs.String("content", "", "Conteúdo do arquivo")
	encoding := fs.String("encoding", "text", "Codificação do conteúdo (text|base64)")
	fs.Parse(args)

	if *file == "" {
		fatalf("--file é obrigatório")
	}

	var data []byte
	var err error

	// 1. Obter dados brutos
	rawContent := *content
	if rawContent == "" {
		// Tentar ler do stdin se flag estiver vazia
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) == 0 {
			input, _ := io.ReadAll(os.Stdin)
			rawContent = string(input)
		}
	}

	if rawContent == "" {
		fatalf("Conteúdo vazio.")
	}

	// 2. Decodificar se necessário
	if *encoding == "base64" {
		// Remove quebras de linha que a IA possa ter colocado no base64
		cleanBase64 := strings.ReplaceAll(strings.TrimSpace(rawContent), "\n", "")
		cleanBase64 = strings.ReplaceAll(cleanBase64, " ", "") // Remove espaços

		data, err = base64.StdEncoding.DecodeString(cleanBase64)
		if err != nil {
			fatalf("Erro ao decodificar Base64: %v. Certifique-se que o conteúdo é um Base64 válido.", err)
		}
	} else {
		data = []byte(rawContent)
	}

	// 3. Criar diretórios
	dir := filepath.Dir(*file)
	if dir != "." && dir != "/" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			fatalf("Erro ao criar diretórios: %v", err)
		}
	}

	// 4. Backup
	if err := createBackup(*file); err != nil {
		fmt.Printf("⚠️ Aviso: Falha ao criar backup: %v\n", err)
	}

	// Ler conteúdo antigo para Diff (se existir)
	oldContentBytes, _ := os.ReadFile(*file)
	oldContent := string(oldContentBytes)

	// 5. Escrever
	if err := os.WriteFile(*file, data, 0644); err != nil {
		fatalf("Erro ao escrever arquivo: %v", err)
	}

	// 6. Feedback
	showDiff(*file, oldContent, string(data))
	fmt.Printf("✅ Arquivo '%s' escrito com sucesso. (Backup salvo em %s.bak)\n", *file, *file)
}

// --- COMANDO: PATCH ---
func handlePatch(args []string) {
	fs := flag.NewFlagSet("patch", flag.ExitOnError)
	file := fs.String("file", "", "Caminho do arquivo")
	search := fs.String("search", "", "Texto para procurar")
	replace := fs.String("replace", "", "Texto para substituir")
	encoding := fs.String("encoding", "text", "Codificação (text|base64)")
	fs.Parse(args)

	if *file == "" || *search == "" {
		fatalf("--file e --search são obrigatórios")
	}

	// Ler arquivo original
	contentBytes, err := os.ReadFile(*file)
	if err != nil {
		fatalf("Erro ao ler arquivo: %v", err)
	}
	content := string(contentBytes)

	var searchStr, replaceStr string

	if *encoding == "base64" {
		sBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*search))
		if err != nil {
			fatalf("Erro no base64 do --search: %v", err)
		}
		searchStr = string(sBytes)

		if *replace != "" {
			rBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*replace))
			if err != nil {
				fatalf("Erro no base64 do --replace: %v", err)
			}
			replaceStr = string(rBytes)
		}
	} else {
		searchStr = *search
		replaceStr = *replace
	}

	// Normalização de quebras de linha
	content = strings.ReplaceAll(content, "\r\n", "\n")
	searchStr = strings.ReplaceAll(searchStr, "\r\n", "\n")
	replaceStr = strings.ReplaceAll(replaceStr, "\r\n", "\n")

	if strings.Count(content, searchStr) == 0 {
		fatalf("❌ Texto não encontrado no arquivo.")
	}

	// 1. Backup
	if err := createBackup(*file); err != nil {
		fmt.Printf("⚠️ Aviso: Falha ao criar backup: %v\n", err)
	}

	// Aplica patch
	newContent := strings.Replace(content, searchStr, replaceStr, 1)

	// 2. Salvar
	if err := os.WriteFile(*file, []byte(newContent), 0644); err != nil {
		fatalf("Erro ao salvar arquivo patcheado: %v", err)
	}

	// 3. Feedback Visual do Patch
	fmt.Printf("\n📝 PATCH APLICADO EM: %s\n", *file)
	fmt.Println("----------------------------------------")
	fmt.Println("🔴 REMOVIDO:")
	fmt.Println(searchStr)
	fmt.Println("\n🟢 ADICIONADO:")
	fmt.Println(replaceStr)
	fmt.Println("----------------------------------------")
	fmt.Printf("✅ Sucesso. Backup salvo em %s.bak\n", *file)
}

// --- COMANDO: TREE (Mantido igual) ---
func handleTree(args []string) {
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	dir := fs.String("dir", ".", "Diretório")
	fs.Parse(args)

	filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules") {
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
}

// --- COMANDO: SEARCH ---
func handleSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	term := fs.String("term", "", "Termo para buscar")
	dir := fs.String("dir", ".", "Diretório raiz")
	fs.Parse(args)

	if *term == "" {
		fatalf("Flag --term é obrigatória")
	}

	fmt.Printf("🔍 Buscando por '%s' em '%s'...\n", *term, *dir)

	err := filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Ignorar diretórios e arquivos binários/irrelevantes
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == "vendor" || info.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		// Extensões de imagem/binário básicas para pular
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".png" || ext == ".jpg" || ext == ".exe" || ext == ".bin" {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// Verifica se contém o termo
		if strings.Contains(string(content), *term) {
			lines := strings.Split(string(content), "\n")
			for i, line := range lines {
				if strings.Contains(line, *term) {
					// Imprime: ARQUIVO:LINHA: CONTEÚDO (trimado)
					fmt.Printf("%s:%d: %s\n", path, i+1, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})

	if err != nil {
		fatalf("Erro na busca: %v", err)
	}
}

// --- COMANDO: EXEC ---
func handleExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	cmdStr := fs.String("cmd", "", "Comando para executar")
	fs.Parse(args)

	if *cmdStr == "" {
		fatalf("Flag --cmd é obrigatória")
	}

	// Timeout de segurança para não travar o agente
	// ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	// defer cancel()

	fmt.Printf("⚙️ Executando: %s\n", *cmdStr)
	fmt.Println("----------------------------------------")

	// Usa 'sh -c' para permitir pipes e argumentos complexos
	cmd := exec.Command("sh", "-c", *cmdStr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	fmt.Println("\n----------------------------------------")
	if err != nil {
		// Não usamos fatalf aqui porque queremos que o agente veja o erro de compilação/teste
		// e tente corrigir, em vez de o plugin crashar.
		fmt.Printf("❌ Comando falhou: %v\n", err)
		os.Exit(1) // Exit code 1 para o agente saber que falhou
	} else {
		fmt.Printf("✅ Comando executado com sucesso.\n")
	}
}

// --- COMANDO: ROLLBACK ---
func handleRollback(args []string) {
	fs := flag.NewFlagSet("rollback", flag.ExitOnError)
	file := fs.String("file", "", "Arquivo para restaurar")
	fs.Parse(args)

	if *file == "" {
		fatalf("Flag --file é obrigatória")
	}

	backupPath := *file + ".bak"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		fatalf("❌ Backup não encontrado para '%s'. Não é possível reverter.", *file)
	}

	// Ler backup
	content, err := os.ReadFile(backupPath)
	if err != nil {
		fatalf("Erro ao ler backup: %v", err)
	}

	// Restaurar arquivo original
	if err := os.WriteFile(*file, content, 0644); err != nil {
		fatalf("Erro ao restaurar arquivo: %v", err)
	}

	fmt.Printf("✅ ROLLBACK SUCESSO: '%s' restaurado para a versão anterior.\n", *file)
}

// --- COMANDO: CLEAN ---
func handleClean(args []string) {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	dir := fs.String("dir", ".", "Diretório para limpar backups")
	fs.Parse(args)

	fmt.Printf("🧹 Limpando arquivos .bak em '%s'...\n", *dir)
	count := 0

	err := filepath.Walk(*dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Se for diretório irrelevante, pular
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules") {
			return filepath.SkipDir
		}

		if !info.IsDir() && strings.HasSuffix(info.Name(), ".bak") {
			if err := os.Remove(path); err == nil {
				fmt.Printf("   🗑️ Removido: %s\n", path)
				count++
			}
		}
		return nil
	})

	if err != nil {
		fatalf("Erro na limpeza: %v", err)
	}

	if count == 0 {
		fmt.Println("✨ Nenhum arquivo de backup encontrado. Diretório limpo.")
	} else {
		fmt.Printf("✅ Limpeza concluída. %d arquivos removidos.\n", count)
	}
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERRO: "+format+"\n", args...)
	os.Exit(1)
}
