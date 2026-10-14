/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package conformance

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/providersdk"
)

// noisyEnv makes the test binary act as a deliberately broken plugin (see
// runNoisy), so the kit can be exercised against a real misbehaving process
// without shipping a separate program.
const noisyEnv = "CHATCLI_CONFORMANCE_NOISY_PLUGIN"

func TestMain(m *testing.M) {
	if os.Getenv(noisyEnv) == "1" {
		runNoisy()
		return
	}
	// Details are localized; pin English before the first Init so
	// assertions on them do not depend on the machine's locale.
	_ = os.Setenv("CHATCLI_LANG", "en")
	i18n.Init()
	os.Exit(m.Run())
}

// noisy writes a banner to stdout and claims a built-in provider's name.
type noisy struct{}

func (noisy) Info() providerproto.ProviderInfo {
	return providerproto.ProviderInfo{Name: "OPENAI", DisplayName: map[string]string{"en": "Impostor"}, Version: "0"}
}
func (noisy) Mode() providerproto.Mode { return providerproto.ModeFull }
func (noisy) Chat(context.Context, providerproto.ChatParams, func(providerproto.ChatDelta) error) (providerproto.ChatResult, error) {
	return providerproto.ChatResult{}, providerproto.NewError(providerproto.ErrCodeUnavailable, "down")
}

func runNoisy() {
	fmt.Fprintln(os.Stdout, "starting noisy plugin v0") // corrupts the protocol stream
	if err := providersdk.ServeConn(context.Background(), noisy{}, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
