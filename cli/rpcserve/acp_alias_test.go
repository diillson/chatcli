/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package rpcserve

import "testing"

// An alias is the command it stands for. Unresolved, /status matched
// nothing advertised and the line was sent to the model as chat text.
func TestACPAliasResolvesToItsCommand(t *testing.T) {
	backend := &mutableCmdBackend{fakeBackend: &fakeBackend{}, cmds: []CommandInfo{{Name: "config"}, {Name: "version"}, {Name: "ratelimit"}}}
	a, _ := newRefreshACP(backend)
	for _, alias := range []string{"/status", "/settings", "/v", "/limits", "/config"} {
		if _, ok := a.commandAdvertised(alias); !ok {
			t.Errorf("%s is not treated as a command", alias)
		}
	}
	if _, ok := a.commandAdvertised("/nope"); ok {
		t.Error("an unknown token is treated as a command")
	}
}
