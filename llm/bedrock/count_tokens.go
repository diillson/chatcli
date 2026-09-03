/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Bedrock token counting (bedrock-runtime CountTokens). The input is what
 * the adapter would send for the model's family: the Converse messages
 * and system blocks for the Converse family, the InvokeModel body for the
 * Anthropic family (Mantle included — same body). The OpenAI family on
 * Bedrock has no counting surface and reports ErrCountUnsupported so
 * callers fall back to usage calibration.
 */
package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/tokenizer"
	"github.com/diillson/chatcli/models"
)

const countTokensTimeout = 10 * time.Second

// ErrCountUnsupported is returned for model families without a counting
// surface on Bedrock.
var ErrCountUnsupported = errors.New("bedrock: token counting not available for this model family")

var _ client.TokenCounter = (*BedrockClient)(nil)

// CountTokens implements client.TokenCounter.
func (c *BedrockClient) CountTokens(ctx context.Context, prompt string, history []models.Message) (int, error) {
	opCtx, cancel := context.WithTimeout(ctx, countTokensTimeout)
	defer cancel()
	if err := c.ensureRuntime(opCtx); err != nil {
		return 0, err
	}
	c.maybeResolveProfileModel(opCtx)
	if resolveFamily(c.model) == familyOpenAI {
		return c.countTokensLocal(prompt, history)
	}
	input, err := c.countTokensInput(prompt, history)
	if err != nil {
		return 0, err
	}
	if input == nil {
		return 0, nil
	}
	out, err := c.runtime.CountTokens(opCtx, &bedrockruntime.CountTokensInput{
		ModelId: stringPtr(c.model),
		Input:   input,
	})
	if err != nil {
		return 0, err
	}
	if out == nil || out.InputTokens == nil {
		return 0, nil
	}
	return int(*out.InputTokens), nil
}

// countTokensInput builds the family-specific CountTokens input; nil when
// there is nothing to count.
func (c *BedrockClient) countTokensInput(prompt string, history []models.Message) (bedrockruntimetypes.CountTokensInput, error) {
	switch resolveFamily(c.model) {
	case familyConverse:
		messages, system := buildConverseMessages(prompt, history)
		if len(messages) == 0 {
			return nil, nil
		}
		return &bedrockruntimetypes.CountTokensInputMemberConverse{Value: bedrockruntimetypes.ConverseTokensRequest{
			Messages: messages,
			System:   system,
		}}, nil
	case familyAnthropic:
		messages, systemObj := c.buildMessagesAndSystem(prompt, history)
		if len(messages) == 0 {
			return nil, nil
		}
		body := map[string]interface{}{
			"anthropic_version": anthropicBedrockVersion,
			"max_tokens":        1,
			"messages":          messages,
		}
		if systemObj != nil {
			body["system"] = systemObj
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &bedrockruntimetypes.CountTokensInputMemberInvokeModel{Value: bedrockruntimetypes.InvokeModelTokensRequest{
			Body: payload,
		}}, nil
	default:
		return nil, ErrCountUnsupported
	}
}

// countTokensLocal serves the OpenAI family on Bedrock (GPT models have no
// CountTokens surface there): the local GPT tokenizer.
func (c *BedrockClient) countTokensLocal(prompt string, history []models.Message) (int, error) {
	if !tokenizer.IsGPTModel(c.model) {
		return 0, ErrCountUnsupported
	}
	return tokenizer.CountChat(c.model, tokenizer.MessagesFromHistory(prompt, history))
}
