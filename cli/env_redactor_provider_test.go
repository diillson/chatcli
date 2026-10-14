/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
)

// Provider credentials that match no suffix pattern must still be redacted:
// StackSpot's CLIENT_ID is declared on its descriptor, and a provider
// registered at runtime (an external provider plugin) is covered without
// rebuilding the redactor.
func TestEnvRedactorCoversProviderSecretEnv(t *testing.T) {
	r := NewEnvRedactor()
	if !r.isSensitive("CLIENT_ID", "abc") {
		t.Error("CLIENT_ID (StackSpot credential) must be treated as a secret")
	}
	if r.isSensitive("ACME_TENANT_REF", "abc") {
		t.Fatal("precondition: ACME_TENANT_REF must not match any built-in rule")
	}

	t.Cleanup(func() { catalog.UnregisterProvider("ACME") })
	if err := catalog.RegisterProvider(catalog.ProviderDescriptor{
		Name:        "ACME",
		DisplayName: "Acme Gateway",
		SecretEnv:   []string{"ACME_TENANT_REF"},
	}); err != nil {
		t.Fatal(err)
	}
	if !r.isSensitive("acme_tenant_ref", "abc") {
		t.Error("a runtime provider's declared secret env must be redacted")
	}
}
