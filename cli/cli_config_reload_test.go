package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
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
	for _, v := range []string{"AWS_REGION", "AWS_PROFILE"} {
		if have[v] {
			t.Errorf("reloadableEnvVars must not unset ambient AWS SDK var %s", v)
		}
	}
}

// TestRouteConfigReloadAlias garante que /config --reload (e a forma sem
// hífens) despacha para o mesmo reloadConfiguration do /reload.
func TestRouteConfigReloadAlias(t *testing.T) {
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
