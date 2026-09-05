/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/jwtkey"
)

// jwtAlgorithmInEffect reports which JWT algorithm a server started from
// this environment would verify with.
//
// It is derived rather than configured, because two variables can select it
// and one of them (CHATCLI_JWT_SECRET) selects RS256 or HS256 depending on
// whether its value is key material. An operator should be able to read the
// answer off `/config server` instead of inferring it — and it comes from
// the same package the verifier itself uses, so the screen cannot drift
// from the behavior.
func jwtAlgorithmInEffect() string {
	alg := jwtkey.Algorithm(os.Getenv("CHATCLI_JWT_PUBLIC_KEY"), os.Getenv("CHATCLI_JWT_SECRET"))
	if alg == jwtkey.AlgNone {
		return i18n.T("cfg.value.jwt_disabled")
	}
	return alg
}
