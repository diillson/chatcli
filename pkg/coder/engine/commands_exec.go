package engine

import (
	"context"
	"flag"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sandboxedCommand resolves the actual (name, args) to exec for a shell
// command line, applying OS-level confinement when CHATCLI_CODER_SANDBOX
// selects it. Off (the default) returns the bare shell invocation unchanged.
// A one-line note is emitted to the error stream the first time confinement
// engages or degrades, so the operator can see it took effect.
func (e *Engine) sandboxedCommand(cmdLine, workDir string) (string, []string) {
	shell, shellFlag := resolveShell()
	mode := resolveSandboxMode()
	if mode == SandboxOff {
		return shell, []string{shellFlag, cmdLine}
	}
	ws := workDir
	if ws == "" {
		ws = e.WorkspaceRoot
	}
	name, args, note := wrapWithSandbox(mode, ws, shell, shellFlag, cmdLine)
	if note != "" {
		e.errorf("[sandbox] %s\n", note)
	}
	return name, args
}

// resolveShell returns the shell binary and its command flag for the current OS.
// On Windows it checks SHELL (set by Git Bash/MSYS2), then COMSPEC, then falls back
// to the absolute path of cmd.exe.
func resolveShell() (shell, flag string) {
	if runtime.GOOS != "windows" {
		return "sh", "-c"
	}

	// Git Bash / MSYS2 set $SHELL to something like /usr/bin/bash
	if s := os.Getenv("SHELL"); s != "" {
		lower := strings.ToLower(s)
		if strings.Contains(lower, "bash") {
			return s, "-c"
		}
	}

	// COMSPEC is the standard Windows way to locate the command interpreter
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		lower := strings.ToLower(comspec)
		if strings.Contains(lower, "powershell") || strings.Contains(lower, "pwsh") {
			return comspec, "-Command"
		}
		return comspec, "/C"
	}

	return `C:\Windows\System32\cmd.exe`, "/C"
}

// registerDirAliases makes the working-directory flag lenient: models keep
// inventing --workingDir/--workdir/--cwd spellings, and a strict parser
// turns each one into a failed call plus a retry loop. All aliases write
// into the same destination; the last one parsed wins.
func registerDirAliases(fs *flag.FlagSet, dir *string) {
	fs.Func("workingDir", "", func(v string) error { *dir = v; return nil })
	fs.Func("workdir", "", func(v string) error { *dir = v; return nil })
	fs.Func("cwd", "", func(v string) error { *dir = v; return nil })
}

func (e *Engine) handleExec(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	cmdStr := fs.String("cmd", "", "")
	dir := fs.String("dir", "", "")
	registerDirAliases(fs, dir)
	timeout := fs.Int("timeout", 600, "")
	registerIgnoredElevationFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *cmdStr == "" {
		return fmt.Errorf("--cmd requerido")
	}

	finalCmd := html.UnescapeString(*cmdStr)
	if err := checkUnsafeCommand(finalCmd); err != nil {
		return err
	}

	e.printf("⚙️ Running: %s\n", finalCmd)

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
	defer cancel()

	workDir := *dir
	if workDir == "" {
		workDir = e.WorkspaceRoot
	}
	name, cmdArgs := e.sandboxedCommand(finalCmd, workDir)
	cmd := exec.CommandContext(execCtx, name, cmdArgs...) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream, optionally OS-sandboxed
	if *dir != "" {
		if err := e.validatePath(*dir); err != nil {
			return err
		}
		cmd.Dir = *dir
	}

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start error: %w", err)
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
		e.printf("❌ Failed: %v\n", err)
		return fmt.Errorf("command failed: %w", err)
	}
	e.println("✅ Success.")
	return nil
}

func (e *Engine) handleTest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	dir := fs.String("dir", ".", "")
	registerDirAliases(fs, dir)
	cmd := fs.String("cmd", "", "")
	timeout := fs.Int("timeout", 1800, "")
	registerIgnoredElevationFlags(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if err := e.validatePath(*dir); err != nil {
		return err
	}

	finalCmd := strings.TrimSpace(*cmd)
	if finalCmd == "" {
		finalCmd = detectTestCommand(*dir)
		if finalCmd == "" {
			return fmt.Errorf("não foi possível detectar comando de teste. Use --cmd")
		}
	}

	// `test` takes an arbitrary --cmd, exactly like `exec`, and ran it
	// through none of the guards `exec` applies: not the dangerous-pattern
	// check, not the sandbox. A subcommand named after running tests is not
	// a smaller privilege than one named after running commands, and the
	// same operator escape hatches apply here.
	if err := checkUnsafeCommand(finalCmd); err != nil {
		return err
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
	defer cancel()

	e.printf("🧪 Running tests: %s\n", finalCmd)
	out, err := e.runSandboxedCommand(execCtx, *dir, finalCmd)
	return e.printCommandOutput(out, err)
}

// runSandboxedCommand runs a shell line under whatever confinement
// CHATCLI_CODER_SANDBOX selects, collecting combined output.
func (e *Engine) runSandboxedCommand(ctx context.Context, dir, cmdLine string) (string, error) {
	name, cmdArgs := e.sandboxedCommand(cmdLine, dir)
	cmd := exec.CommandContext(ctx, name, cmdArgs...) //#nosec G204 -- agent/CLI tool execution; command validated by IsUnsafeCommand + policy_manager upstream, optionally OS-sandboxed
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
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
		return true, "sudo usage blocked (operator setting " + AllowSudoEnv + "=true allows it)"
	}
	return false, ""
}

// AllowUnsafeEnv is the operator setting that lifts the dangerous-pattern
// block on exec/test. Tool arguments cannot lift it.
const AllowUnsafeEnv = "CHATCLI_CODER_ALLOW_UNSAFE"

// AllowSudoEnv is the operator setting that permits sudo, shared with the
// agent command validator. Tool arguments cannot grant it.
const AllowSudoEnv = "CHATCLI_AGENT_ALLOW_SUDO"

// operatorAllows reads a boolean operator setting; anything unparsable is
// false.
func operatorAllows(env string) bool {
	on, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(env)))
	return err == nil && on
}

// registerIgnoredElevationFlags keeps --allow-unsafe and --allow-sudo
// parseable so older prompts and saved sessions do not fail on an unknown
// flag, but their values are discarded: the caller of a tool (a model, a
// remote client) must never be able to lift the block on its own command.
func registerIgnoredElevationFlags(fs *flag.FlagSet) {
	fs.Var(ignoredBoolValue{}, "allow-unsafe", "ignored; operator setting "+AllowUnsafeEnv)
	fs.Var(ignoredBoolValue{}, "allow-sudo", "ignored; operator setting "+AllowSudoEnv)
}

// ignoredBoolValue is a boolean flag.Value that discards what it is given,
// so "--allow-unsafe" parses without consuming the next argument.
type ignoredBoolValue struct{}

func (ignoredBoolValue) String() string   { return "false" }
func (ignoredBoolValue) Set(string) error { return nil }
func (ignoredBoolValue) IsBoolFlag() bool { return true }

// checkUnsafeCommand refuses a shell line that matches the dangerous-pattern
// list unless the operator set AllowUnsafeEnv; sudo needs AllowSudoEnv.
func checkUnsafeCommand(cmdLine string) error {
	if operatorAllows(AllowUnsafeEnv) {
		return nil
	}
	if unsafe, reason := IsUnsafeCommand(cmdLine, operatorAllows(AllowSudoEnv)); unsafe {
		return fmt.Errorf("comando bloqueado: %s; tool arguments cannot lift this block, only the operator setting %s=true", reason, AllowUnsafeEnv)
	}
	return nil
}

func runCommandCtx(ctx context.Context, dir, cmd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	command := exec.CommandContext(ctx, cmd, args...) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	if dir != "" {
		command.Dir = dir
	}
	out, err := command.CombinedOutput()
	return string(out), err
}
