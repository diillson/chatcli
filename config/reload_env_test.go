package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
)

// reloadCycle reproduces reloadConfiguration's sequence: clear the
// reloadable variables, re-read the environment file, then restore what the
// shell / client provided at boot and re-apply the project overlay.
func reloadCycle(t *testing.T, reloadable []string) {
	t.Helper()
	for _, key := range reloadable {
		_ = os.Unsetenv(key)
	}
	if err := godotenv.Overload(ResolveDotenv().Path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("overload: %v", err)
	}
	RestoreBootEnv(reloadable)
	ReapplyProjectDotenv()
}

func TestReload_KeepsClientProvidedVariables(t *testing.T) {
	ResetBootEnvForTest()
	ResetProjectDotenvForTest()
	t.Cleanup(func() { ResetBootEnvForTest(); ResetProjectDotenvForTest() })

	home := t.TempDir()
	setHome(t, home)
	t.Chdir(t.TempDir())
	t.Setenv(DotenvEnv, "")
	writeEnvFile(t, filepath.Join(home, ".env"), "BEDROCK_MODEL=claude-sonnet-4-6\n")

	// What an editor's `env` block passes in — absent from the file.
	t.Setenv("LLM_PROVIDER", "BEDROCK")
	t.Setenv("BEDROCK_PROFILE", "work-sso")
	CaptureBootEnv() // as the entrypoint does, before loading the file

	reloadCycle(t, []string{"LLM_PROVIDER", "BEDROCK_PROFILE", "BEDROCK_MODEL"})

	if got := os.Getenv("LLM_PROVIDER"); got != "BEDROCK" {
		t.Errorf("a client-provided variable must survive /reload, got %q", got)
	}
	if got := os.Getenv("BEDROCK_PROFILE"); got != "work-sso" {
		t.Errorf("the client's AWS profile must survive /reload, got %q", got)
	}
	if got := os.Getenv("BEDROCK_MODEL"); got != "claude-sonnet-4-6" {
		t.Errorf("the file must still define its own variables, got %q", got)
	}
}

func TestReload_FileStillWinsAndRemovalStillApplies(t *testing.T) {
	ResetBootEnvForTest()
	ResetProjectDotenvForTest()
	t.Cleanup(func() { ResetBootEnvForTest(); ResetProjectDotenvForTest() })

	home := t.TempDir()
	setHome(t, home)
	t.Chdir(t.TempDir())
	t.Setenv(DotenvEnv, "")
	dotenv := filepath.Join(home, ".env")
	writeEnvFile(t, dotenv, "AWS_PROFILE=from-file\nBEDROCK_REGION=us-east-1\n")

	// Boot: the file is loaded WITHOUT overriding the process, so the
	// snapshot must be taken first and must not contain file values.
	CaptureBootEnv()
	if err := godotenv.Load(dotenv); err != nil {
		t.Fatal(err)
	}
	if _, ok := BootEnv("AWS_PROFILE"); ok {
		t.Fatal("a value coming from the file must never enter the boot snapshot")
	}

	// The user edits the file: profile changed, region removed.
	writeEnvFile(t, dotenv, "AWS_PROFILE=edited\n")
	reloadCycle(t, []string{"AWS_PROFILE", "BEDROCK_REGION"})

	if got := os.Getenv("AWS_PROFILE"); got != "edited" {
		t.Errorf("the edited file must win on /reload, got %q", got)
	}
	if got := os.Getenv("BEDROCK_REGION"); got != "" {
		t.Errorf("a variable removed from the file must stop applying, got %q", got)
	}
}

func TestReload_ReappliesProjectOverlay(t *testing.T) {
	ResetBootEnvForTest()
	ResetProjectDotenvForTest()
	t.Cleanup(func() { ResetBootEnvForTest(); ResetProjectDotenvForTest() })

	home := t.TempDir()
	setHome(t, home)
	t.Chdir(t.TempDir())
	t.Setenv(DotenvEnv, "")
	t.Setenv(ProjectDotenvEnv, "")
	writeEnvFile(t, filepath.Join(home, ".env"), "LLM_PROVIDER=CLAUDEAI\n")
	CaptureBootEnv()

	project := t.TempDir()
	writeEnvFile(t, filepath.Join(project, ".env"), "BEDROCK_PROFILE=project-sso\n")
	os.Unsetenv("BEDROCK_PROFILE")
	t.Cleanup(func() { os.Unsetenv("BEDROCK_PROFILE") })

	if rep := ApplyProjectDotenv(project); len(rep.Applied) != 1 {
		t.Fatalf("overlay should have contributed one variable: %+v", rep)
	}

	// The overlay runs once per process: without the re-apply step a single
	// reload would drop the project's contribution for good.
	reloadCycle(t, []string{"LLM_PROVIDER", "BEDROCK_PROFILE"})

	if got := os.Getenv("BEDROCK_PROFILE"); got != "project-sso" {
		t.Errorf("the project overlay must survive /reload, got %q", got)
	}
	if got := os.Getenv("LLM_PROVIDER"); got != "CLAUDEAI" {
		t.Errorf("the environment file must still apply, got %q", got)
	}
}

func TestRestoreBootEnv_NeverOverridesWhatIsAlreadySet(t *testing.T) {
	ResetBootEnvForTest()
	t.Cleanup(ResetBootEnvForTest)

	t.Setenv("CHATCLI_TEST_KEY", "boot")
	CaptureBootEnv()
	t.Setenv("CHATCLI_TEST_KEY", "current")

	if restored := RestoreBootEnv([]string{"CHATCLI_TEST_KEY"}); len(restored) != 0 {
		t.Fatalf("a variable already in effect must not be rewritten: %v", restored)
	}
	if got := os.Getenv("CHATCLI_TEST_KEY"); got != "current" {
		t.Fatalf("value changed to %q", got)
	}
}
