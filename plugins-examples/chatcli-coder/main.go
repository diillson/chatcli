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
	default:
		fatalf("Comando desconhecido: %s", cmd)
	}
}

func printMetadata() {
	meta := Metadata{
		Name:        "@coder",
		Description: "Suite de engenharia completa (IO, Search, Exec, Backup, Rollback).",
		Usage:       `@coder read --file <path>`,
		Version:     "1.6.0-fix-stream",
	}
	_ = json.NewEncoder(os.Stdout).Encode(meta)
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
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	file := fs.String("file", "", "Arquivo")
	_ = fs.Parse(args)
	var files []string
	if *file != "" {
		files = append(files, *file)
	}
	files = append(files, fs.Args()...)

	for _, f := range files {
		f = strings.Trim(f, "\"'")
		c, err := os.ReadFile(f)
		if err != nil {
			fmt.Printf("❌ ERRO AO LER '%s': %v\n", f, err)
			continue
		}
		lines := strings.Split(string(c), "\n")
		fmt.Printf("<<< INÍCIO DO ARQUIVO: %s >>>\n", f)
		for i, l := range lines {
			fmt.Printf("%4d | %s\n", i+1, l)
		}
		fmt.Printf("<<< FIM DO ARQUIVO: %s >>>\n\n", f)
	}
}

func handleWrite(args []string) {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	file := fs.String("file", "", "")
	content := fs.String("content", "", "")
	encoding := fs.String("encoding", "text", "")
	_ = fs.Parse(args)

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
	if err := os.WriteFile(*file, data, 0644); err != nil {
		fatalf("Erro escrita: %v", err)
	}
	fmt.Printf("✅ Arquivo '%s' escrito (%d bytes).\n", *file, len(data))
}

func handlePatch(args []string) {
	fs := flag.NewFlagSet("patch", flag.ExitOnError)
	file := fs.String("file", "", "")
	search := fs.String("search", "", "")
	replace := fs.String("replace", "", "")
	encoding := fs.String("encoding", "text", "")
	_ = fs.Parse(args)

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
	fs := flag.NewFlagSet("tree", flag.ExitOnError)
	dir := fs.String("dir", ".", "")
	_ = fs.Parse(args)
	_ = filepath.Walk(*dir, func(p string, i os.FileInfo, err error) error {
		if err == nil && i.IsDir() && (i.Name() == ".git" || i.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if err == nil && p != *dir {
			rel, _ := filepath.Rel(*dir, p)
			depth := strings.Count(rel, string(os.PathSeparator))
			fmt.Printf("%s%s\n", strings.Repeat("  ", depth), i.Name())
		}
		return nil
	})
}

func handleSearch(args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	term := fs.String("term", "", "")
	dir := fs.String("dir", ".", "")
	_ = fs.Parse(args)
	if *term == "" {
		fatalf("--term requerido")
	}
	_ = filepath.Walk(*dir, func(p string, i os.FileInfo, err error) error {
		if i.IsDir() && (i.Name() == ".git" || i.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !i.IsDir() {
			c, _ := os.ReadFile(p)
			if strings.Contains(string(c), *term) {
				fmt.Printf("Encontrado em: %s\n", p)
			}
		}
		return nil
	})
}

func handleExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	cmdStr := fs.String("cmd", "", "")
	dir := fs.String("dir", "", "")
	timeout := fs.Int("timeout", 600, "")
	_ = fs.Parse(args)
	if *cmdStr == "" {
		fatalf("--cmd requerido")
	}

	finalCmd := html.UnescapeString(*cmdStr)
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
	stream := func(r io.Reader, w io.Writer) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 1024*1024), 5*1024*1024)
		for s.Scan() {
			fmt.Fprintln(w, s.Text())
			if f, ok := w.(*os.File); ok {
				err := f.Sync()
				if err != nil {
					return
				}
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
	fs := flag.NewFlagSet("rb", flag.ExitOnError)
	file := fs.String("file", "", "")
	_ = fs.Parse(args)
	c, err := os.ReadFile(*file + ".bak")
	if err != nil {
		fatalf("Backup error: %v", err)
	}
	_ = os.WriteFile(*file, c, 0644)
	fmt.Println("✅ Rollback ok.")
}

func handleClean(args []string) {
	fmt.Println("🧹 Clean...")
}

func fatalf(msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "ERRO: "+msg+"\n", args...)
	os.Exit(1)
}

func smartDecode(content, enc string) ([]byte, error) {
	if enc == "base64" {
		// BLINDAGEM: Remove TUDO que não for caractere Base64 válido
		// Isso evita erros com quebras de linha (\n), aspas soltas, etc.
		reg := regexp.MustCompile("[^a-zA-Z0-9+/=]")
		clean := reg.ReplaceAllString(content, "")
		return base64.StdEncoding.DecodeString(clean)
	}
	// Fallback e detecção automática
	if !strings.Contains(content, " ") && len(content)%4 == 0 {
		reg := regexp.MustCompile("[^a-zA-Z0-9+/=]")
		clean := reg.ReplaceAllString(content, "")
		if d, err := base64.StdEncoding.DecodeString(clean); err == nil && utf8.Valid(d) {
			return d, nil
		}
	}
	// Senão, retorna o texto puro
	return []byte(content), nil
}
