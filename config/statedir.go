/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package config

import (
	"os"

	"github.com/diillson/chatcli/pkg/stateroot"
)

// KeepStateDir snapshots CHATCLI_STATE_DIR before an environment file is
// applied and returns the function that undoes whatever the file did to
// it. The state directory is where the global .env itself is looked up and
// where every store opened at boot lives, so it can only come from the
// process environment and cannot change while the process runs. The
// returned function reports the value it discarded ("" when the file left
// the variable alone).
func KeepStateDir() func() string {
	prev, had := os.LookupEnv(stateroot.Env)
	return func() string {
		cur, has := os.LookupEnv(stateroot.Env)
		if has == had && cur == prev {
			return ""
		}
		if had {
			_ = os.Setenv(stateroot.Env, prev)
		} else {
			_ = os.Unsetenv(stateroot.Env)
		}
		if !has {
			return ""
		}
		return cur
	}
}
