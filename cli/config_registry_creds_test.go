/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"reflect"
	"testing"
)

func TestRegistryCredentialEnvNamesListsPerHostOnly(t *testing.T) {
	environ := []string{
		"CHATCLI_REGISTRY_TOKEN=generic",
		"CHATCLI_REGISTRY_USERNAME=u",
		"CHATCLI_REGISTRY_GHCR_IO_TOKEN=x",
		"CHATCLI_REGISTRY_QUAY_IO_USERNAME=robot",
		"CHATCLI_REGISTRY_QUAY_IO_PASSWORD=pw",
		"CHATCLI_REGISTRY_URLS=https://example.com",
		"CHATCLI_REGISTRY_DISABLE=true",
		"OTHER_TOKEN=y",
	}
	got := registryCredentialEnvNames(environ)
	want := []string{"CHATCLI_REGISTRY_GHCR_IO_TOKEN", "CHATCLI_REGISTRY_QUAY_IO_PASSWORD", "CHATCLI_REGISTRY_QUAY_IO_USERNAME"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := registryCredentialEnvNames(nil); len(got) != 0 {
		t.Fatalf("empty environ: %v", got)
	}
}
