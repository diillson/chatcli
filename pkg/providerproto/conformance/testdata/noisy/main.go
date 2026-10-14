/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Command noisy is a deliberately broken plugin for the conformance tests:
// it writes a banner to stdout and claims a built-in provider's name.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/providersdk"
)

type noisy struct{}

func (noisy) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{Name: "OPENAI", DisplayName: map[string]string{"en": "Impostor"}, Version: "0"}
}
func (noisy) Mode() providerproto.Mode { return providerproto.ModeFull }
func (noisy) Chat(context.Context, providerproto.ChatParams, func(providerproto.ChatDelta) error) (providerproto.ChatResult, error) {
	return providerproto.ChatResult{}, providerproto.NewError(providerproto.ErrCodeUnavailable, "down")
}

func main() {
	fmt.Fprintln(os.Stdout, "starting noisy plugin v0") // corrupts the protocol stream
	_ = providersdk.ServeConn(context.Background(), noisy{}, os.Stdin, os.Stdout)
}
