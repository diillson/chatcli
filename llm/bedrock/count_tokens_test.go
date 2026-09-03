/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"errors"
	"testing"
	"time"

	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestCountTokensInput_ByFamily(t *testing.T) {
	hist := []models.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}

	converse := NewBedrockClient("amazon.nova-pro-v1:0", "us-east-1", "", zap.NewNop(), 1, time.Millisecond)
	in, err := converse.countTokensInput("q", hist)
	if err != nil {
		t.Fatal(err)
	}
	cv, ok := in.(*bedrockruntimetypes.CountTokensInputMemberConverse)
	if !ok || len(cv.Value.Messages) == 0 || len(cv.Value.System) == 0 {
		t.Fatalf("converse family must build a Converse tokens request: %#v", in)
	}

	claude := NewBedrockClient("anthropic.claude-sonnet-5-v1:0", "us-east-1", "", zap.NewNop(), 1, time.Millisecond)
	in, err = claude.countTokensInput("q", hist)
	if err != nil {
		t.Fatal(err)
	}
	iv, ok := in.(*bedrockruntimetypes.CountTokensInputMemberInvokeModel)
	if !ok || len(iv.Value.Body) == 0 {
		t.Fatalf("anthropic family must build an InvokeModel body: %#v", in)
	}

	gpt := NewBedrockClient("openai.gpt-oss-120b-1:0", "us-east-1", "", zap.NewNop(), 1, time.Millisecond)
	if _, err := gpt.countTokensInput("q", hist); !errors.Is(err, ErrCountUnsupported) {
		t.Fatalf("openai family has no CountTokens input: %v", err)
	}
	if n, err := gpt.countTokensLocal("q", hist); err != nil && n == 0 {
		// Loading or unsupported are both acceptable offline; a hard error is not.
		if !errors.Is(err, ErrCountUnsupported) && err.Error() == "" {
			t.Fatal(err)
		}
	}
	if in, err := converse.countTokensInput("", nil); err != nil || in != nil {
		t.Fatalf("empty input counts nothing: %v %v", in, err)
	}
}
