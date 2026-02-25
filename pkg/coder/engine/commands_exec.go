package engine

import (
	"context"
	"flag"
	"fmt"
	"html"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

func (e *Engine) handleExec(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	cmdStr := fs.String("cmd", "", "")
	dir := fs.String("dir", "", "")
	timeout := fs.Int("timeout", 600, "")
	allowUnsafe := fs.Bool("allow-unsafe", false, "")
	allowSudo := fs.Bool("allow-sudo", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *cmdStr == "" {
		return fmt.Errorf("--cmd requerido")
	}

	finalCmd := html.UnescapeString(*cmdStr)
	if !*allowUnsafe {
		if unsafe, reason := IsUnsafeCommand(finalCmd, *allowSudo); unsafe {
			return fmt.Errorf("comando bloqueado: %s", reason)
		}
	}

	e.printf("⚙️ Executando: %s\n", finalCmd)

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(execCtx, "cmd.exe", "/C", finalCmd)
	} else {
		cmd = exec.CommandContext(execCtx, "sh", "-c", finalCmd)
	}
	if *dir != "" {
		cmd.Dir = *dir
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start error: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	stream := func(r io.Reader, w io.Writer) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}
	go stream(stdout, e.Out)
	go stream(stderr, e.Err)
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		e.printf("❌ Falhou: %v\n", err)
		return fmt.Errorf("command failed: %v", err)
	}
	e.println("✅ Sucesso.")
	return nil
}

func (e *Engine) handleTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	cmd := fs.String("cmd", "", "")
	timeout := fs.Int("timeout", 1800, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	finalCmd := strings.TrimSpace(*cmd)
	if finalCmd == "" {
		finalCmd = detectTestCommand(*dir)
		if finalCmd == "" {
			return fmt.Errorf("não foi possível detectar comando de teste. Use --cmd")
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
	defer cancel()

	e.printf("🧪 Rodando testes: %s\n", finalCmd)
	out, err := runCommandWithContext(execCtx, *dir, finalCmd)
	return e.printCommandOutput(out, err)
}

// IsUnsafeCommand checks if a shell command matches known dangerous patterns.
func IsUnsafeCommand(cmd string, allowSudo bool) (bool, string) {
	dangerPatterns := []string{
		`(?i)rm\s+-rf\s+`,
		`(?i)rm\s+--no-preserve-root`,
		`(?i)dd\s+if=`,
		`(?i)mkfs\w*\s+`,
		`(?i)shutdown(\s+|$)`,
		`(?i)reboot(\s+|$)`,
		`(?i)init\s+0`,
		`(?i)\bpoweroff\b`,
		`(?i)curl\s+[^\|;]*\|\s*sh`,
		`(?i)wget\s+[^\|;]*\|\s*sh`,
		`(?i)curl\s+[^\|;]*\|\s*bash`,
		`(?i)wget\s+[^\|;]*\|\s*bash`,
		`(?i)\bdrop\s+database\b`,
		`(?i)\buserdel\b`,
		`(?i)\bchmod\s+777\s+/.*`,
		`(?i)\bchown\s+-R\s+.*\s+/`,
		`(?i)\bbase64\b.*\|\s*(sh|bash|zsh|dash)`,
		`(?i)\bpython[23]?\s+-c\b`,
		`(?i)\bperl\s+-e\b`,
		`(?i)\bruby\s+-e\b`,
		`(?i)\bnode\s+-e\b`,
		`(?i)\bphp\s+-r\b`,
		`(?i)\beval\s+`,
		`(?i)\$\(\s*curl`,
		`(?i)\$\(\s*wget`,
		"(?i)`\\s*curl",
		"(?i)`\\s*wget",
		`(?i)>\s*/etc/`,
		`(?i)>\s*/dev/[sh]d`,
		`(?i)>\s*/proc/`,
		`(?i)\btee\s+/etc/`,
		`(?i)\bsource\s+/dev/tcp`,
		`(?i)/dev/tcp/`,
		`(?i)\bexport\s+.*PATH\s*=`,
		`(?i)\bnc\b.*-[el]`,
		`(?i)\bncat\b.*-[el]`,
		`(?i)\bxargs\b.*\b(rm|del|shutdown|reboot|mkfs)\b`,
		`(?i)\bfind\b.*-exec\b.*(rm|del|shutdown|reboot)\b`,
		`(?i)\bcrontab\s+-r\b`,
		`(?i)\biptables\s+-F\b`,
		`(?i)\bsysctl\s+-w\b`,
		`(?i)\bkillall\b`,
		`(?i)\bpkill\s+-9\b`,
		`(?i)\bexec\s+\d*[<>]`,
		`(?i)\binsmod\b`,
		`(?i)\bmodprobe\b`,
		`(?i)\brmmod\b`,
		`(?i)\bumount\s+-[lf]`,
		`(?i)\bsource\s+/dev/`,
		`(?i)\benv\b.*\|\s*(sh|bash)`,
		`\$\{[^}]*[;|&][^}]*\}`,
		`(?i)\$\(\s*(bash|sh|zsh|dash)\b`,
		`<\(`,
		`>\(`,
		`(?i)\b[A-Z_]+=.*;\s*(sh|bash|zsh)\b`,
		`\bkill\s+-9\s+1\b`,
		`\b:>\s*/`,
	}
	for _, p := range dangerPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		if re.MatchString(cmd) {
			return true, fmt.Sprintf("Dangerous pattern detected (%s)", p)
		}
	}
	if !allowSudo && regexp.MustCompile(`(?i)\bsudo\b`).MatchString(cmd) {
		return true, "sudo usage blocked (use --allow-sudo)"
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
