/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package conformance

import (
	"os"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// Details are localized; pin English before the first Init so assertions on
// them do not depend on the machine's locale.
func TestMain(m *testing.M) {
	_ = os.Setenv("CHATCLI_LANG", "en")
	i18n.Init()
	os.Exit(m.Run())
}
