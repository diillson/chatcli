/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

func TestConfigTruth_RegistryReloadablesAndHint(t *testing.T) {
	for _, name := range []string{"CHATCLI_AUDIT_LOG_PATH", "CHATCLI_ENCRYPTION_KEY", "CHATCLI_COMPRESSION_CCR_TTL", "CHATCLI_COMPRESSION_CCR_MAX_MB",
		"CHATCLI_MEMORY_RETENTION_DAYS", "CHATCLI_MEMORY_MAX_FACTS", "CHATCLI_MEMORY_MAX_SIZE", "CHATCLI_MEMORY_RETRIEVAL_BUDGET",
		"CHATCLI_MEMORY_AUTORECALL", "CHATCLI_HUB_TTL_HOURS", "OLLAMA_HOST"} {
		if _, ok := envDefaults[name]; !ok {
			t.Errorf("%s missing from the defaults registry", name)
		}
	}
	if !strings.Contains(envDefaults["CHATCLI_CONTEXT_ENGINE"].Source, "provider") {
		t.Error("CHATCLI_CONTEXT_ENGINE must document the provider value")
	}
	have := map[string]bool{}
	for _, v := range reloadableEnvVars {
		have[v] = true
	}
	for _, name := range []string{"CHATCLI_ENCRYPTION_KEY", "CHATCLI_ENV_REDACT_MODE", "CHATCLI_MANAGED_CONFIG", "CHATCLI_SESSION_TTL",
		"CHATCLI_GATEWAY_MAX_TENANTS", "CHATCLI_MEMORY_MODE", "CHATCLI_MEMORY_AUTORECALL", "OLLAMA_HOST", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SERVICE_NAME"} {
		if !have[name] {
			t.Errorf("%s must be reloadable", name)
		}
	}
	if len(configSectionNames) < 25 {
		t.Fatalf("section list too short: %d", len(configSectionNames))
	}
	hint := i18n.T("cfg.panorama.sections_hint", strings.Join(configSectionNames, ", "))
	for _, s := range []string{"managed", "retention", "memory", "hub", "quality"} {
		if !strings.Contains(hint, s) {
			t.Fatalf("hint must list %s: %s", s, hint)
		}
	}
	found := false
	for _, s := range contextSubcommands() {
		if s.Text == "unwatch" {
			found = true
		}
	}
	if !found {
		t.Fatal("/context unwatch must complete")
	}
}
