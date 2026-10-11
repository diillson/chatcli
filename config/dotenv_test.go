package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setHome points os.UserHomeDir at dir on every platform.
func setHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	}
}

// trustWorkspaces installs a workspace trust gate for the test: every
// directory is trusted when trusted is true, none otherwise.
func trustWorkspaces(t *testing.T, trusted bool) {
	t.Helper()
	SetWorkspaceTrustGate(func(string) bool { return trusted })
	t.Cleanup(func() { SetWorkspaceTrustGate(nil) })
}

func writeEnvFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveDotenv_ExplicitEnvVarWins(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	work := t.TempDir()
	t.Chdir(work)
	writeEnvFile(t, filepath.Join(work, ".env"), "A=1\n")
	pinned := filepath.Join(home, "custom.env")
	writeEnvFile(t, pinned, "A=2\n")
	t.Setenv(DotenvEnv, pinned)

	res := ResolveDotenv()
	if res.Path != pinned || res.Origin != DotenvFromEnvVar || !res.Exists {
		t.Fatalf("explicit CHATCLI_DOTENV must win: %+v", res)
	}
}

func TestResolveDotenv_ExplicitMissingFileIsKept(t *testing.T) {
	setHome(t, t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv(DotenvEnv, filepath.Join(t.TempDir(), "nope.env"))

	res := ResolveDotenv()
	if res.Origin != DotenvFromEnvVar || res.Exists {
		t.Fatalf("a pinned but missing file must be reported as missing, not replaced: %+v", res)
	}
}

func TestResolveDotenv_TildeIsExpanded(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Chdir(t.TempDir())
	writeEnvFile(t, filepath.Join(home, "shell.env"), "A=1\n")
	t.Setenv(DotenvEnv, "~/shell.env")

	res := ResolveDotenv()
	if res.Path != filepath.Join(home, "shell.env") || !res.Exists {
		t.Fatalf("~ must expand: %+v", res)
	}
}

func TestResolveDotenv_FallbackOrder(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	work := t.TempDir()
	t.Chdir(work)
	t.Setenv(DotenvEnv, "")

	homeEnv := filepath.Join(home, ".env")
	chatcliEnv := filepath.Join(home, ".chatcli", ".env")
	workEnv := filepath.Join(work, ".env")

	// 1) Only ~/.env exists — this is the case an editor-spawned acp/mcp-server
	// hits: no CHATCLI_DOTENV, project directory without a .env.
	writeEnvFile(t, homeEnv, "A=1\n")
	if res := ResolveDotenv(); res.Path != homeEnv || res.Origin != DotenvFromHome || !res.Exists {
		t.Fatalf("~/.env fallback: %+v", res)
	}

	// 2) ~/.chatcli/.env outranks ~/.env.
	writeEnvFile(t, chatcliEnv, "A=2\n")
	if res := ResolveDotenv(); res.Path != chatcliEnv || res.Origin != DotenvFromChatCLIDir {
		t.Fatalf("~/.chatcli/.env must outrank ~/.env: %+v", res)
	}

	// 3) An untrusted working directory is skipped entirely: the next
	// candidate becomes the first hit. No gate installed fails closed.
	writeEnvFile(t, workEnv, "A=3\n")
	if res := ResolveDotenv(); res.Origin != DotenvFromChatCLIDir || res.Untrusted == "" {
		t.Fatalf("an untrusted ./.env must be skipped: %+v", res)
	}
	trustWorkspaces(t, false)
	if res := ResolveDotenv(); res.Origin != DotenvFromChatCLIDir {
		t.Fatalf("an untrusted ./.env must be skipped: %+v", res)
	}

	// 4) A trusted working directory still wins over both.
	trustWorkspaces(t, true)
	if res := ResolveDotenv(); res.Origin != DotenvFromWorkdir || res.Untrusted != "" {
		t.Fatalf("a trusted ./.env must win: %+v", res)
	}
}

func TestResolveDotenv_NothingFound(t *testing.T) {
	setHome(t, t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv(DotenvEnv, "")

	res := ResolveDotenv()
	if res.Exists || res.Origin != DotenvNotFound || res.Path != ".env" {
		t.Fatalf("empty environment: %+v", res)
	}
	if len(res.Candidates) < 3 {
		t.Fatalf("every candidate must be reported for diagnostics: %+v", res.Candidates)
	}
}

func TestApplyProjectDotenv_FillOnly(t *testing.T) {
	trustWorkspaces(t, true)
	ResetProjectDotenvForTest()
	t.Cleanup(ResetProjectDotenvForTest)
	setHome(t, t.TempDir())
	t.Setenv(ProjectDotenvEnv, "")

	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"), "AWS_PROFILE=project\nLLM_PROVIDER=BEDROCK\n")
	t.Setenv("LLM_PROVIDER", "CLAUDEAI") // already set by the user
	os.Unsetenv("AWS_PROFILE")
	t.Cleanup(func() { os.Unsetenv("AWS_PROFILE") })

	rep := ApplyProjectDotenv(dir)
	if !rep.Present || rep.Err != nil {
		t.Fatalf("overlay should have been read: %+v", rep)
	}
	if got := os.Getenv("AWS_PROFILE"); got != "project" {
		t.Fatalf("unset variable must be filled, got %q", got)
	}
	if got := os.Getenv("LLM_PROVIDER"); got != "CLAUDEAI" {
		t.Fatalf("a variable already in effect must never be overridden, got %q", got)
	}
	if len(rep.Applied) != 1 || rep.Applied[0] != "AWS_PROFILE" {
		t.Fatalf("applied = %v", rep.Applied)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "LLM_PROVIDER" {
		t.Fatalf("skipped = %v", rep.Skipped)
	}
}

func TestApplyProjectDotenv_SafeModeRefusesCredentialsAndEndpoints(t *testing.T) {
	trustWorkspaces(t, true)
	ResetProjectDotenvForTest()
	t.Cleanup(ResetProjectDotenvForTest)
	setHome(t, t.TempDir())
	t.Setenv(ProjectDotenvEnv, "")

	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"),
		"OPENAI_API_KEY=sk-hostile\nANTHROPIC_BASE_URL=https://evil.example\nBEDROCK_REGION=sa-east-1\n")
	for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_BASE_URL", "BEDROCK_REGION"} {
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range []string{"OPENAI_API_KEY", "ANTHROPIC_BASE_URL", "BEDROCK_REGION"} {
			os.Unsetenv(k)
		}
	})

	rep := ApplyProjectDotenv(dir)
	if os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_BASE_URL") != "" {
		t.Fatal("a project directory must not be able to inject credentials or redirect endpoints")
	}
	if os.Getenv("BEDROCK_REGION") != "sa-east-1" {
		t.Fatal("plain per-project configuration must still apply")
	}
	if len(rep.Refused) != 2 {
		t.Fatalf("refused = %v", rep.Refused)
	}
}

func TestApplyProjectDotenv_ModesOffAndAll(t *testing.T) {
	trustWorkspaces(t, true)
	setHome(t, t.TempDir())
	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"), "OPENAI_API_KEY=sk-project\n")
	os.Unsetenv("OPENAI_API_KEY")
	t.Cleanup(func() { os.Unsetenv("OPENAI_API_KEY") })

	ResetProjectDotenvForTest()
	t.Setenv(ProjectDotenvEnv, "off")
	if rep := ApplyProjectDotenv(dir); rep.Present || len(rep.Applied) != 0 {
		t.Fatalf("off must ignore the file entirely: %+v", rep)
	}

	ResetProjectDotenvForTest()
	t.Setenv(ProjectDotenvEnv, "all")
	if rep := ApplyProjectDotenv(dir); len(rep.Applied) != 1 {
		t.Fatalf("all must apply every key: %+v", rep)
	}
	if os.Getenv("OPENAI_API_KEY") != "sk-project" {
		t.Fatal("all mode must export the key")
	}
	ResetProjectDotenvForTest()
}

func TestApplyProjectDotenv_SkipsTheActiveDotenvAndRepeats(t *testing.T) {
	trustWorkspaces(t, true)
	ResetProjectDotenvForTest()
	t.Cleanup(ResetProjectDotenvForTest)
	setHome(t, t.TempDir())
	t.Setenv(ProjectDotenvEnv, "")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeEnvFile(t, path, "PROJECT_ONLY_KEY=1\n")
	SetActiveDotenv(DotenvResolution{Path: path, Origin: DotenvFromWorkdir, Exists: true})

	if rep := ApplyProjectDotenv(dir); len(rep.Applied) != 0 {
		t.Fatalf("the file already loaded process-wide must not be applied twice: %+v", rep)
	}
	if len(ProjectDotenvOverlays()) != 0 {
		t.Fatal("no overlay should have been recorded")
	}
}

// Every key below can redirect traffic, weaken TLS, spawn a process, move
// state or relax a security switch. The safe overlay must refuse each one,
// including those a suffix denylist used to miss.
func TestApplyProjectDotenv_SafeAllowlistRefusesDangerousKeys(t *testing.T) {
	trustWorkspaces(t, true)
	ResetProjectDotenvForTest()
	t.Cleanup(ResetProjectDotenvForTest)
	setHome(t, t.TempDir())
	t.Setenv(ProjectDotenvEnv, "")

	dangerous := []string{
		"CHATCLI_TLS_INSECURE_SKIP_VERIFY", "CHATCLI_BEDROCK_INSECURE_SKIP_VERIFY",
		"ALL_PROXY", "CHATCLI_PROXY_AUTH", "CHATCLI_BROWSER_CDP_URL", "CHATCLI_MCP_CONFIG",
		"CHATCLI_AUTH_DIR", "CHATCLI_ENV_REDACT_MODE", "CHATCLI_ALLOW_UNSIGNED_PLUGINS",
		"CHATCLI_LSP_GO_CMD", "CHATCLI_LATEST_VERSION_URL", "SSL_CERT_FILE", "SSL_CERT_DIR",
		"GOPROXY", "HTTPS_PROXY", "ANTHROPIC_BASE_URL", "OPENAI_API_URL", "OPENAI_API_KEY",
		"CHATCLI_CODER_POLICY_OVERLAY", "CHATCLI_AGENT_SECURITY_MODE", "CHATCLI_PROJECT_ENV",
		"CHATCLI_HOOKS_ENABLED", "CHATCLI_CA_BUNDLE", "PATH", "LD_PRELOAD",
	}
	body := "LLM_PROVIDER=BEDROCK\nAWS_PROFILE=project\n"
	for _, k := range dangerous {
		body += k + "=hostile\n"
	}
	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"), body)
	for _, k := range append([]string{"LLM_PROVIDER", "AWS_PROFILE"}, dangerous...) {
		if k == "PATH" {
			continue
		}
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	rep := ApplyProjectDotenv(dir)
	refused := map[string]bool{}
	for _, k := range rep.Refused {
		refused[k] = true
	}
	for _, k := range dangerous {
		if !refused[k] {
			t.Errorf("safe mode applied %s", k)
		}
		if os.Getenv(k) == "hostile" {
			t.Errorf("%s reached the environment", k)
		}
	}
	if os.Getenv("LLM_PROVIDER") != "BEDROCK" || os.Getenv("AWS_PROFILE") != "project" {
		t.Errorf("allowlisted selection keys must apply: %+v", rep)
	}
}

// An untrusted project is not read at all, whatever the mode.
func TestApplyProjectDotenv_UntrustedProjectIsIgnored(t *testing.T) {
	setHome(t, t.TempDir())
	dir := t.TempDir()
	writeEnvFile(t, filepath.Join(dir, ".env"), "AWS_PROFILE=project\n")
	t.Setenv("AWS_PROFILE", "")
	_ = os.Unsetenv("AWS_PROFILE")
	for _, mode := range []string{"", "all"} {
		ResetProjectDotenvForTest()
		t.Setenv(ProjectDotenvEnv, mode)
		rep := ApplyProjectDotenv(dir)
		if !rep.Present || !rep.Untrusted || len(rep.Applied) != 0 || os.Getenv("AWS_PROFILE") != "" {
			t.Fatalf("mode %q: an untrusted project must not apply: %+v", mode, rep)
		}
	}
	ResetProjectDotenvForTest()
}
