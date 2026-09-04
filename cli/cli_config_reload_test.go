package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/joho/godotenv"
)

// TestReloadableEnvVarsCoverCriticalProviderVars trava na lista de reload as
// variáveis cuja ausência já causou estado obsoleto pós-/reload — remover do
// .env não surtia efeito até reiniciar o processo (BEDROCK_PROVIDER foi o
// caso reportado: o override continuava forçando o schema Anthropic no Nova).
func TestReloadableEnvVarsCoverCriticalProviderVars(t *testing.T) {
	required := []string{
		"BEDROCK_PROVIDER", "BEDROCK_MODEL", "BEDROCK_REGION", "BEDROCK_MAX_TOKENS",
		"OPENAI_API_URL", "OPENAI_RESPONSES_API_URL",
		"OPENROUTER_API_KEY", "OPENROUTER_API_URL", "OPENROUTER_MODEL",
		"DEVIN_API_KEY", "ZAI_API_URL", "MINIMAX_API_URL", "GITHUB_MODELS_API_URL",
		"CHATCLI_COMMANDS", "CHATCLI_COMMANDS_AUTOROUTE",
	}
	have := make(map[string]bool, len(reloadableEnvVars))
	for _, v := range reloadableEnvVars {
		if have[v] {
			t.Errorf("duplicated entry in reloadableEnvVars: %s", v)
		}
		have[v] = true
	}
	for _, v := range required {
		if !have[v] {
			t.Errorf("reloadableEnvVars is missing %s", v)
		}
	}
	// As ambientais do SDK entraram na lista quando limpar deixou de
	// significar perder: reloadConfiguration restaura, depois do Overload, o
	// que o shell/cliente entregou no boot e o que o .env do projeto
	// contribuiu. A exclusão anterior existia só para não derrubar a cadeia
	// de credenciais — esse comportamento agora é garantido pelo teste
	// abaixo, e não mais pela ausência delas aqui.
	for _, v := range []string{"AWS_REGION", "AWS_PROFILE", "AWS_DEFAULT_REGION"} {
		if !have[v] {
			t.Errorf("reloadableEnvVars is missing ambient AWS SDK var %s", v)
		}
	}
}

// TestReloadKeepsShellProvidedAWSProfile trava o motivo pelo qual
// AWS_PROFILE ficava fora da lista de reload: um profile vindo do shell (ou
// do bloco env de uma IDE/cliente MCP), que o .env não repete, NÃO pode
// sumir no /reload — era assim que a cadeia de credenciais caía. O ciclo
// aqui é o mesmo de reloadConfiguration: limpa, relê o arquivo, restaura.
func TestReloadKeepsShellProvidedAWSProfile(t *testing.T) {
	config.ResetBootEnvForTest()
	config.ResetProjectDotenvForTest()
	t.Cleanup(func() {
		config.ResetBootEnvForTest()
		config.ResetProjectDotenvForTest()
	})

	dir := t.TempDir()
	dotenv := filepath.Join(dir, ".env")
	if err := os.WriteFile(dotenv, []byte("BEDROCK_MODEL=claude-sonnet-4-6\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_DOTENV", dotenv)
	t.Setenv("AWS_PROFILE", "corp-sso") // do shell, ausente do arquivo
	config.CaptureBootEnv()

	for _, v := range reloadableEnvVars {
		_ = os.Unsetenv(v)
	}
	if err := godotenv.Overload(dotenv); err != nil {
		t.Fatal(err)
	}
	config.RestoreBootEnv(reloadableEnvVars)
	config.ReapplyProjectDotenv()

	if got := os.Getenv("AWS_PROFILE"); got != "corp-sso" {
		t.Fatalf("AWS_PROFILE do shell precisa sobreviver ao /reload, got %q", got)
	}
	if got := os.Getenv("BEDROCK_MODEL"); got != "claude-sonnet-4-6" {
		t.Fatalf("o arquivo precisa continuar valendo, got %q", got)
	}
}

// TestRouteConfigReloadAlias garante que /config --reload (e a forma sem
// hífens) despacha para o mesmo reloadConfiguration do /reload.
func TestRouteConfigReloadAlias(t *testing.T) {
	// Hermeticidade: reloadConfiguration carrega CHATCLI_DOTENV (ou ./.env)
	// via godotenv.Overload — apontado para o .env REAL do dev, ele injetava
	// as variáveis pessoais (ex.: ZAI_USE_CODING_PLAN=true) no processo de
	// teste e quebrava os testes de pricing que rodam depois.
	t.Setenv("CHATCLI_DOTENV", filepath.Join(t.TempDir(), ".env"))
	c := minimalCLI(t)
	config.InitGlobal(c.logger)
	for _, alias := range []string{"--reload", "reload"} {
		out := captureStdout(t, func() { c.routeConfigCommand(context.Background(), []string{alias}) })
		if !strings.Contains(out, i18n.T("status.reloading_config")) {
			t.Errorf("%s: expected reload banner in output, got:\n%s", alias, out)
		}
	}
}

// TestConfigCompleterOffersReload trava a presença do --reload no
// autocomplete de /config.
func TestConfigCompleterOffersReload(t *testing.T) {
	c := minimalCLI(t)
	line := "/config "
	got := suggestTexts(c.getConfigSuggestions(docWithCursor(line, len(line))))
	for _, s := range got {
		if s == "--reload" {
			return
		}
	}
	t.Errorf("expected --reload in /config suggestions, got %v", got)
}
