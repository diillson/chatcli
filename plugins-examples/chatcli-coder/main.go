package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
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
		fatalf("Uso: @coder <read|write|patch|tree|search|exec|execscript|rollback|clean> [opções]")
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
	case "execscript":
		handleExecScript(args)
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
		Description: "Suite de engenharia completa. IMPORTANTE: Sempre envie conteúdo de arquivos em Base64 para preservar indentação e formatação.",
		Usage: `@coder read --file <path>
    @coder write --file <path> --content <base64>  # ⚠️ SEMPRE use Base64
    @coder patch --file <path> --search <base64> --replace <base64>  # ⚠️ SEMPRE use Base64
    @coder tree --dir <path>
    @coder search --term "texto" --dir <path>
    
    @coder exec --cmd <texto_ou_base64> [--dir <path>] [--timeout <seg>] [--heartbeat <seg>] [--non-interactive true|false]
              # ⚠️ Para comandos complexos/multilinhas, use Base64 em --cmd
              # Exemplo (bash): @coder exec --cmd "$(echo 'npm install' | base64)"
    
    @coder execscript --steps <texto_ou_base64_json> [--dir <path>] [--heartbeat <seg>] [--non-interactive true|false]
              # JSON esperado (lista de steps):
              # [
              #   {"cmd":"go mod tidy","timeout_sec":300},
              #   {"cmd":"go test ./...","timeout_sec":900},
              #   {"cmd":"go build ./...","timeout_sec":300}
              # ]
              # Você pode enviar o JSON em Base64 para evitar problemas de escape.
    
    @coder rollback --file <path>
    @coder clean --dir <path>
    
    📌 REGRAS DE OURO:
    1. Sempre codifique conteúdo de arquivos em Base64
    2. Para comandos multilinhas ou com caracteres especiais, use Base64 em --cmd / --steps
    3. Prefira execscript para sequência de comandos, ao invés de usar '&&' em um único exec`,
		Version: "2.3.0",
	}
	if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
		fatalf("Erro ao gerar metadados: %v", err)
	}
}

// ✨ Decodifica Base64 com limpeza inteligente
func decodeBase64(content string) ([]byte, error) {
	clean := strings.TrimSpace(content)
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.ReplaceAll(clean, " ", "")
	clean = strings.ReplaceAll(clean, "\t", "")

	return base64.StdEncoding.DecodeString(clean)
}

// ✨ Valida se parece Base64 válido
func looksLikeBase64(content string) bool {
	clean := strings.TrimSpace(content)
	clean = strings.ReplaceAll(clean, "\n", "")
	clean = strings.ReplaceAll(clean, "\r", "")
	clean = strings.ReplaceAll(clean, " ", "")

	if len(clean) < 4 {
		return false
	}

	validChars := 0
	for _, char := range clean {
		if isBase64Char(rune(char)) {
			validChars++
		}
	}

	ratio := float64(validChars) / float64(len(clean))
	return ratio > 0.95
}

func isBase64Char(r rune) bool {
	return (r >= 'A' && r <= 'Z') ||
		(r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') ||
		r == '+' || r == '/' || r == '='
}

// ✨ Função inteligente: Tenta Base64, avisa se for texto puro
func smartDecodeWithWarning(content string, context string) []byte {
	decoded, err := decodeBase64(content)

	if err == nil && looksLikeBase64(content) {
		fmt.Fprintf(os.Stderr, "✅ Base64 detectado e decodificado (%s)\n", context)
		_ = os.Stderr.Sync()
		return decoded
	}

	fmt.Fprintf(os.Stderr, "⚠️  AVISO: Conteúdo recebido como texto puro (%s)\n", context)
	fmt.Fprintf(os.Stderr, "⚠️  Recomendação: Use Base64 para evitar problemas de formatação\n")
	_ = os.Stderr.Sync()

	return []byte(content)
}

// ✨ NOVA: Decodifica comando com suporte a Base64 ou texto puro
func decodeCommand(cmdInput string) (string, bool) {
	trimmed := strings.TrimSpace(cmdInput)

	decoded, err := decodeBase64(trimmed)
	if err == nil && looksLikeBase64(trimmed) {
		return string(decoded), true
	}

	normalized := strings.ReplaceAll(trimmed, "\\\n", " ")
	normalized = strings.ReplaceAll(normalized, "\\\r\n", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")

	return normalized, false
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
		oldLines := strings.Split(oldContent, "\n")
		newLines := strings.Split(newContent, "\n")
		fmt.Printf("Linhas: %d -> %d\n", len(oldLines), len(newLines))
		fmt.Printf("Bytes: %d -> %d\n", len(oldContent), len(newContent))
	}
	fmt.Println("----------------------------------------")
}

// --- COMANDO: READ ---
func handleRead(args []string) {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	file := fs.String("file", "", "Caminho do arquivo")
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
	fmt.Printf("<<< INÍCIO DO ARQUIVO: %s (%d linhas, %d bytes) >>>\n", *file, len(lines), len(content))
	for i, line := range lines {
		fmt.Printf("%4d | %s\n", i+1, line)
	}
	fmt.Printf("<<< FIM DO ARQUIVO: %s >>>\n", *file)
	fmt.Printf("\n💡 Dica: Para modificar, codifique o conteúdo em Base64\n")
}

// --- COMANDO: WRITE ---
func handleWrite(args []string) {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	file := fs.String("file", "", "Caminho")
	content := fs.String("content", "", "Conteúdo (preferencialmente Base64)")
	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	if *file == "" {
		fatalf("--file é obrigatório")
	}

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

	data := smartDecodeWithWarning(rawContent, "write")

	dir := filepath.Dir(*file)
	if dir != "." && dir != "/" {
		_ = os.MkdirAll(dir, 0755)
	}

	if err := createBackup(*file); err != nil {
		fmt.Printf("⚠️  Aviso: Falha no backup: %v\n", err)
	}

	oldBytes, _ := os.ReadFile(*file)
	if err := os.WriteFile(*file, data, 0644); err != nil {
		fatalf("Erro escrita: %v", err)
	}

	showDiff(*file, string(oldBytes), string(data))
	fmt.Printf("✅ Arquivo '%s' escrito com sucesso.\n", *file)
}

// --- COMANDO: PATCH ---
func handlePatch(args []string) {
	fs := flag.NewFlagSet("patch", flag.ExitOnError)
	file := fs.String("file", "", "Arquivo")
	search := fs.String("search", "", "Busca (preferencialmente Base64)")
	replace := fs.String("replace", "", "Substituição (preferencialmente Base64)")
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

	searchBytes := smartDecodeWithWarning(*search, "search")
	searchStr := string(searchBytes)

	replaceStr := ""
	if *replace != "" {
		replaceBytes := smartDecodeWithWarning(*replace, "replace")
		replaceStr = string(replaceBytes)
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	searchStr = strings.ReplaceAll(searchStr, "\r\n", "\n")
	replaceStr = strings.ReplaceAll(replaceStr, "\r\n", "\n")

	occurrences := strings.Count(content, searchStr)
	if occurrences == 0 {
		fatalf("❌ Texto de busca não encontrado no arquivo.")
	}

	fmt.Printf("🔍 Encontradas %d ocorrência(s) do texto de busca\n", occurrences)

	if err := createBackup(*file); err != nil {
		fmt.Printf("⚠️  Aviso: Falha no backup: %v\n", err)
	}

	newContent := strings.Replace(content, searchStr, replaceStr, 1)
	if err := os.WriteFile(*file, []byte(newContent), 0644); err != nil {
		fatalf("Erro escrita: %v", err)
	}

	fmt.Printf("✅ Patch aplicado em '%s' (1 ocorrência substituída).\n", *file)
}

// --- COMANDO: TREE ---
func handleTree(args []string) {
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	dir := fs.String("dir", ".", "Diretório")
	if err := fs.Parse(args); err != nil {
		fatalf("Erro ao analisar flags: %v", err)
	}

	fmt.Printf("📁 Estrutura de '%s':\n", *dir)
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
	count := 0
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
					count++
				}
			}
		}
		return nil
	})
	if err != nil {
		fatalf("Erro busca: %v", err)
	}
	fmt.Printf("✅ Busca concluída: %d ocorrência(s) encontrada(s)\n", count)
}

type ExecResult struct {
	Command    string `json:"command"`
	Dir        string `json:"dir,omitempty"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	Cancelled  bool   `json:"cancelled"`
	DurationMs int64  `json:"duration_ms"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
}

type ExecScriptStep struct {
	Cmd        string `json:"cmd"`
	TimeoutSec int    `json:"timeout_sec"`
}

type ExecScriptResult struct {
	Ok         bool         `json:"ok"`
	FailedStep int          `json:"failed_step"` // 1-based; 0 = nenhum
	Steps      []ExecResult `json:"steps"`
}

func writeExecResultJSON(res ExecResult) {
	// marcador para o ChatCLI extrair facilmente sem confundir com logs humanos
	fmt.Println("<<<CHATCLI_EXEC_RESULT_JSON>>>")
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(res)
	fmt.Println("<<<END_CHATCLI_EXEC_RESULT_JSON>>>")
	_ = os.Stdout.Sync()
}

func writeExecScriptResultJSON(res ExecScriptResult) {
	fmt.Println("<<<CHATCLI_EXECSCRIPT_RESULT_JSON>>>")
	enc := json.NewEncoder(os.Stdout)
	_ = enc.Encode(res)
	fmt.Println("<<<END_CHATCLI_EXECSCRIPT_RESULT_JSON>>>")
	_ = os.Stdout.Sync()
}

func detectExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if ok := errorsAs(err, &exitErr); ok {
		// Unix/Windows: ExitCode() funciona bem
		return exitErr.ExitCode()
	}
	return 1
}

// errorsAs: wrapper interno para evitar import extra e manter compatibilidade
func errorsAs(err error, target interface{}) bool {
	switch t := target.(type) {
	case **exec.ExitError:
		e, ok := err.(*exec.ExitError)
		if !ok {
			return false
		}
		*t = e
		return true
	default:
		return false
	}
}

func isContextTimeoutOrCancel(err error, ctx context.Context) (timedOut bool, cancelled bool) {
	if ctx == nil {
		return false, false
	}
	if ctx.Err() == context.DeadlineExceeded {
		return true, false
	}
	if ctx.Err() == context.Canceled {
		return false, true
	}
	// fallback: se err mencionar deadline/canceled
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "deadline exceeded") {
			return true, false
		}
		if strings.Contains(msg, "canceled") || strings.Contains(msg, "cancelled") {
			return false, true
		}
	}
	return false, false
}

func runCommand(ctx context.Context, finalCmd string, dir string, heartbeatSec int, nonInteractive bool) ExecResult {
	start := time.Now()

	result := ExecResult{
		Command: finalCmd,
		Dir:     dir,
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/C", finalCmd)
	} else {
		shellPath := "/bin/bash"
		if _, err := os.Stat(shellPath); os.IsNotExist(err) {
			shellPath = "/bin/sh"
		}
		cmd = exec.CommandContext(ctx, shellPath, "-c", finalCmd)

		// process group (Unix) para matar filhos também
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	if dir != "" {
		cmd.Dir = dir
	}

	if nonInteractive {
		env := os.Environ()
		env = append(env,
			"CI=true",
			"TERM=dumb",
			"DEBIAN_FRONTEND=noninteractive",
		)
		cmd.Env = env
		// stdin fechado para evitar travas esperando input
		cmd.Stdin = nil
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		result.ExitCode = 1
		result.Stderr = fmt.Sprintf("erro ao criar stdout pipe: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		result.ExitCode = 1
		result.Stderr = fmt.Sprintf("erro ao criar stderr pipe: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	if err := cmd.Start(); err != nil {
		result.ExitCode = 1
		result.Stderr = fmt.Sprintf("erro ao iniciar comando: %v", err)
		result.DurationMs = time.Since(start).Milliseconds()
		return result
	}

	// Kill em cancel/timeout (garantido)
	killOnce := sync.Once{}
	killTree := func() {
		killOnce.Do(func() {
			if cmd.Process == nil {
				return
			}
			pid := cmd.Process.Pid
			if runtime.GOOS == "windows" {
				// mata a árvore
				_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", pid)).Run()
				return
			}
			// mata o process group inteiro
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		})
	}

	// Observa ctx.Done para matar mesmo se Wait demorar
	doneKill := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killTree()
		case <-doneKill:
			return
		}
	}()

	var hbDone = make(chan struct{})
	if heartbeatSec > 0 {
		go func() {
			ticker := time.NewTicker(time.Duration(heartbeatSec) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-hbDone:
					return
				case <-ticker.C:
					fmt.Fprintf(os.Stderr, "⏳ ainda executando... (%ds)\n", int(time.Since(start).Seconds()))
					_ = os.Stderr.Sync()
				}
			}
		}()
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)

	stream := func(r io.Reader, w *os.File, buf *bytes.Buffer) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		tmp := make([]byte, 0, 64*1024)
		sc.Buffer(tmp, 1024*1024)

		for sc.Scan() {
			line := sc.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')
			fmt.Fprintln(w, line)
			_ = w.Sync()
		}
		if err := sc.Err(); err != nil {
			// registrar erro de scanner no buffer de stderr para depuração
			fmt.Fprintf(os.Stderr, "⚠️  scanner erro: %v\n", err)
			_ = os.Stderr.Sync()
		}
	}

	go stream(stdoutPipe, os.Stdout, &stdoutBuf)
	go stream(stderrPipe, os.Stderr, &stderrBuf)

	waitErr := cmd.Wait()

	close(doneKill)
	close(hbDone)
	wg.Wait()

	// Se ctx expirou/cancelou, tenta matar novamente (idempotente)
	if ctx.Err() != nil {
		killTree()
	}

	result.DurationMs = time.Since(start).Milliseconds()
	result.Stdout = stdoutBuf.String()
	result.Stderr = stderrBuf.String()

	timedOut, cancelled := isContextTimeoutOrCancel(waitErr, ctx)
	result.TimedOut = timedOut
	result.Cancelled = cancelled

	result.ExitCode = detectExitCode(waitErr)

	// se timeout/cancel, garantir exit code != 0
	if result.TimedOut || result.Cancelled {
		if result.ExitCode == 0 {
			result.ExitCode = 124
		}
	}

	return result
}

// --- COMANDO: EXEC (MELHORADO) ---
func handleExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)

	cmdStr := fs.String("cmd", "", "Comando (texto ou Base64)")
	timeoutSec := fs.Int("timeout", 600, "Timeout em segundos")
	dir := fs.String("dir", "", "Diretório de trabalho")
	heartbeatSec := fs.Int("heartbeat", 15, "Heartbeat em segundos")
	nonInteractive := fs.Bool("non-interactive", true, "Modo não-interativo")

	if err := fs.Parse(args); err != nil {
		fatalf("Erro flags: %v", err)
	}

	if *cmdStr == "" {
		fatalf("--cmd é obrigatório")
	}

	finalCmd, wasBase64 := decodeCommand(*cmdStr)

	if wasBase64 {
		fmt.Fprintf(os.Stderr, "✅ Comando Base64 detectado e decodificado\n")
	} else {
		fmt.Fprintf(os.Stderr, "📝 Comando em texto puro (normalizado)\n")
	}
	_ = os.Stderr.Sync()

	fmt.Fprintf(os.Stderr, "⚙️  Executando:\n")
	fmt.Fprintln(os.Stderr, "----------------------------------------")
	displayCmd := finalCmd
	if len(displayCmd) > 200 {
		displayCmd = displayCmd[:200] + "..."
	}
	fmt.Fprintln(os.Stderr, displayCmd)
	fmt.Fprintln(os.Stderr, "----------------------------------------")
	if *dir != "" {
		fmt.Fprintf(os.Stderr, "📂 Diretório: %s\n", *dir)
	}
	_ = os.Stderr.Sync()

	baseCtx := context.Background()
	ctx := baseCtx
	var cancel context.CancelFunc
	if *timeoutSec > 0 {
		ctx, cancel = context.WithTimeout(baseCtx, time.Duration(*timeoutSec)*time.Second)
		defer cancel()
	}

	res := runCommand(ctx, finalCmd, *dir, *heartbeatSec, *nonInteractive)

	// SEMPRE escrever o JSON final (mesmo em erro/timeout)
	writeExecResultJSON(res)

	if res.ExitCode != 0 {
		os.Exit(res.ExitCode)
	}
}

// --- COMANDO: EXECSCRIPT (NOVO) ---
// Executa vários passos com timeout individual. Evita "&&" e melhora diagnóstico.
func handleExecScript(args []string) {
	fs := flag.NewFlagSet("execscript", flag.ExitOnError)

	stepsStr := fs.String("steps", "", "JSON (texto) ou Base64(JSON) de steps")
	dir := fs.String("dir", "", "Diretório de trabalho (aplicado a todos os steps, a menos que o comando use cd)")
	heartbeatSec := fs.Int("heartbeat", 15, "Heartbeat em segundos")
	nonInteractive := fs.Bool("non-interactive", true, "Modo não-interativo")

	if err := fs.Parse(args); err != nil {
		fatalf("Erro flags: %v", err)
	}

	if *stepsStr == "" {
		fatalf("--steps é obrigatório")
	}

	raw := strings.TrimSpace(*stepsStr)

	var decodedJSON []byte
	if looksLikeBase64(raw) {
		b, err := decodeBase64(raw)
		if err == nil {
			decodedJSON = b
			fmt.Fprintf(os.Stderr, "✅ Steps em Base64 detectado e decodificado\n")
			_ = os.Stderr.Sync()
		}
	}
	if decodedJSON == nil {
		decodedJSON = []byte(raw)
		fmt.Fprintf(os.Stderr, "📝 Steps em texto puro (JSON)\n")
		_ = os.Stderr.Sync()
	}

	var steps []ExecScriptStep
	if err := json.Unmarshal(decodedJSON, &steps); err != nil {
		// Emite resultado de script com erro “estruturado”
		r := ExecScriptResult{
			Ok:         false,
			FailedStep: 0,
			Steps: []ExecResult{{
				Command:    "",
				Dir:        *dir,
				ExitCode:   2,
				TimedOut:   false,
				Cancelled:  false,
				DurationMs: 0,
				Stdout:     "",
				Stderr:     fmt.Sprintf("erro ao parsear JSON de steps: %v", err),
			}},
		}
		writeExecScriptResultJSON(r)
		os.Exit(2)
	}

	if len(steps) == 0 {
		r := ExecScriptResult{
			Ok:         false,
			FailedStep: 0,
			Steps: []ExecResult{{
				Command:    "",
				Dir:        *dir,
				ExitCode:   2,
				TimedOut:   false,
				Cancelled:  false,
				DurationMs: 0,
				Stdout:     "",
				Stderr:     "lista de steps vazia",
			}},
		}
		writeExecScriptResultJSON(r)
		os.Exit(2)
	}

	results := make([]ExecResult, 0, len(steps))
	scriptOk := true
	failedStep := 0

	for i, st := range steps {
		stepN := i + 1

		finalCmd, wasBase64 := decodeCommand(st.Cmd)
		if wasBase64 {
			fmt.Fprintf(os.Stderr, "\n🔷 STEP %d/%d: (cmd em Base64)\n", stepN, len(steps))
		} else {
			fmt.Fprintf(os.Stderr, "\n🔷 STEP %d/%d: (cmd em texto)\n", stepN, len(steps))
		}
		fmt.Fprintln(os.Stderr, "----------------------------------------")
		displayCmd := finalCmd
		if len(displayCmd) > 200 {
			displayCmd = displayCmd[:200] + "..."
		}
		fmt.Fprintln(os.Stderr, displayCmd)
		fmt.Fprintln(os.Stderr, "----------------------------------------")
		_ = os.Stderr.Sync()

		to := st.TimeoutSec
		if to <= 0 {
			to = 600
		}

		baseCtx := context.Background()
		ctx, cancel := context.WithTimeout(baseCtx, time.Duration(to)*time.Second)

		res := runCommand(ctx, finalCmd, *dir, *heartbeatSec, *nonInteractive)
		cancel()

		results = append(results, res)

		if res.ExitCode != 0 {
			scriptOk = false
			failedStep = stepN
			break
		}
	}

	sr := ExecScriptResult{
		Ok:         scriptOk,
		FailedStep: failedStep,
		Steps:      results,
	}

	writeExecScriptResultJSON(sr)

	if !scriptOk {
		// exit code do step que falhou
		os.Exit(results[len(results)-1].ExitCode)
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
		fatalf("Backup não encontrado: %v", err)
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
				fmt.Printf("   🗑️  %s\n", path)
				count++
			}
		}
		return nil
	})
	if err != nil {
		fatalf("Erro limpeza: %v", err)
	}
	fmt.Printf("✅ %d arquivo(s) removido(s).\n", count)
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERRO: "+format+"\n", args...)
	_ = os.Stderr.Sync()
	os.Exit(1)
}
