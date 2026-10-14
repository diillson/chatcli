/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"

	prompt "github.com/c-bata/go-prompt"
)

// The raw "/model" prefix route used to swallow "/models" and run
// `/switch --model s`, silently switching to a model named "s". /models is
// now an alias with its own word route, so both spellings behave the same.
func TestModelsAliasRoutesToModelHandler(t *testing.T) {
	ch := NewCommandHandler(&ChatCLI{})
	model, ok := ch.lookup("/model")
	if !ok {
		t.Fatal("no route for /model")
	}
	alias, ok := ch.lookup("/models")
	if !ok {
		t.Fatal("no route for /models")
	}
	if model == nil || alias == nil {
		t.Fatal("nil handlers")
	}
	if _, ok := ch.lookup("/modelsx"); ok {
		t.Fatal("/modelsx must not resolve")
	}
}

func TestModelSuggestionsEchoTheTypedAlias(t *testing.T) {
	cli := &ChatCLI{}
	for _, cmd := range []string{"/model", "/models"} {
		buf := prompt.NewBuffer()
		buf.InsertText(cmd, false, true)
		got := cli.getModelSuggestions(*buf.Document())
		if len(got) != 1 || got[0].Text != cmd {
			t.Fatalf("suggestions for %q = %+v, want the command echoed back", cmd, got)
		}
	}
}
