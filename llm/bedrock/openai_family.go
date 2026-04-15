/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// sendPromptOpenAI invokes OpenAI-family models on Bedrock (GPT-OSS, etc.)
// using the OpenAI Chat Completions body schema that Bedrock accepts for
// these models — distinct from the Anthropic Messages schema used for
// Claude. See AWS docs: Bedrock "Model parameters: OpenAI".
func (c *BedrockClient) sendPromptOpenAI(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokensOpenAI()
	}

	messages := buildOpenAIMessages(prompt, history)

	reqBody := map[string]interface{}{
		"messages":              messages,
		"max_completion_tokens": effectiveMaxTokens,
	}
	if t := os.Getenv("BEDROCK_TEMPERATURE"); t != "" {
		if f, err := strconv.ParseFloat(t, 64); err == nil {
			reqBody["temperature"] = f
		}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.error.prepare_request"), err)
	}

	responseText, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		out, err := c.runtime.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
			ModelId:     stringPtr(c.model),
			ContentType: stringPtr("application/json"),
			Accept:      stringPtr("application/json"),
			Body:        payload,
		})
		if err != nil {
			return "", err
		}
		return parseOpenAIBody(out.Body)
	})
	if err != nil {
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "Bedrock"), zap.Error(err))
		return "", err
	}
	return responseText, nil
}

func (c *BedrockClient) getMaxTokensOpenAI() int {
	if tokenStr := os.Getenv("BEDROCK_MAX_TOKENS"); tokenStr != "" {
		if parsed, err := strconv.Atoi(tokenStr); err == nil && parsed > 0 {
			return parsed
		}
	}
	if tokenStr := os.Getenv("OPENAI_MAX_TOKENS"); tokenStr != "" {
		if parsed, err := strconv.Atoi(tokenStr); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 4096
}

// buildOpenAIMessages converts the internal history into the OpenAI
// Chat Completions array expected by Bedrock's OpenAI-family endpoint.
// Role mapping: assistant/system kept as-is, everything else becomes "user".
func buildOpenAIMessages(prompt string, history []models.Message) []map[string]interface{} {
	var messages []map[string]interface{}
	var systemParts []string

	for _, msg := range history {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "assistant":
			messages = append(messages, map[string]interface{}{
				"role":    "assistant",
				"content": msg.Content,
			})
		case "system":
			// SystemParts (with cache_control) have no OpenAI equivalent —
			// concatenate their text content.
			if len(msg.SystemParts) > 0 {
				for _, part := range msg.SystemParts {
					if part.Text != "" {
						systemParts = append(systemParts, part.Text)
					}
				}
			} else if msg.Content != "" {
				systemParts = append(systemParts, msg.Content)
			}
		default:
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})
		}
	}

	// Prepend a single consolidated system message, if any.
	if len(systemParts) > 0 {
		joined := strings.Join(systemParts, "\n\n")
		messages = append([]map[string]interface{}{{
			"role":    "system",
			"content": joined,
		}}, messages...)
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": prompt,
			})
		}
	}
	return messages
}

func parseOpenAIBody(body []byte) (string, error) {
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("bedrock-openai: decode response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("bedrock-openai: %s: %s", result.Error.Type, result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("%s", i18n.T("llm.error.no_response", "Bedrock"))
	}
	text := strings.TrimSpace(result.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("%s", i18n.T("llm.error.no_response", "Bedrock"))
	}
	return text, nil
}
