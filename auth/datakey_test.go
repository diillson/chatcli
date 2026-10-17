/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package auth

import (
	"encoding/hex"
	"testing"
)

func TestDataKeySecret_DerivedStableAndSeparate(t *testing.T) {
	t.Setenv("CHATCLI_AUTH_DIR", t.TempDir())
	t.Setenv("CHATCLI_KEYCHAIN_BACKEND", "file")

	a, where, err := DataKeySecret()
	if err != nil {
		t.Fatal(err)
	}
	if where != "file" {
		t.Errorf("where = %q, want file", where)
	}
	if len(a) != 64 {
		t.Fatalf("secret length = %d, want 64 hex chars", len(a))
	}
	b, _, err := DataKeySecret()
	if err != nil || a != b {
		t.Fatal("the data key is not stable across calls")
	}
	root, err := loadOrCreateKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == hex.EncodeToString(root) {
		t.Error("the data key equals the credential key")
	}
}
