/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// sendPromptConverse routes the request through Bedrock's unified Converse
// API. A single body schema works for Llama, Amazon Nova, Mistral, Cohere,
// AI21 Jamba, DeepSeek, Stability and any future provider Bedrock onboards
// — no per-provider InvokeModel encoder needed.
//
// Anthropic and OpenAI keep their dedicated InvokeModel paths because:
//   - Anthropic: cache_control breakpoints + extended-thinking knobs map
//     onto Converse with a different shape, and we don't want to disturb
//     the working cache planner during this rollout.
//   - OpenAI gpt-oss: stable on InvokeModel today and Converse coverage
//     for these IDs has been uneven across regions.
func (c *BedrockClient) sendPromptConverse(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokensConverse()
	}

	messages, system := buildConverseMessages(prompt, history)
	if len(messages) == 0 {
		return "", fmt.Errorf("%s", i18n.T("llm.error.no_response", "Bedrock"))
	}

	input := &bedrockruntime.ConverseInput{
		ModelId:  stringPtr(c.model),
		Messages: messages,
		System:   system,
		InferenceConfig: &bedrockruntimetypes.InferenceConfiguration{
			MaxTokens: aws.Int32(clampInt32(effectiveMaxTokens)),
		},
	}
	if t := os.Getenv("BEDROCK_TEMPERATURE"); t != "" {
		if f, err := strconv.ParseFloat(t, 32); err == nil {
			input.InferenceConfig.Temperature = aws.Float32(float32(f))
		}
	}
	if t := os.Getenv("BEDROCK_TOP_P"); t != "" {
		if f, err := strconv.ParseFloat(t, 32); err == nil {
			input.InferenceConfig.TopP = aws.Float32(float32(f))
		}
	}

	start := time.Now()
	client.LogRequestStart(c.logger, "BEDROCK", c.model,
		zap.String("family", string(familyConverse)),
		zap.String("region", c.region),
		zap.String("endpoint", RuntimeEndpointURL(c.region)),
		zap.Int("history_len", len(history)),
		zap.Int("max_tokens", effectiveMaxTokens),
	)

	responseText, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		out, err := c.runtime.Converse(ctx, input)
		if err != nil {
			return "", wrapBedrockInferenceProfileError(c.model, err)
		}
		return parseConverseOutput(out)
	})
	if err != nil {
		client.LogRequestFinish(c.logger, "BEDROCK", c.model, "error", time.Since(start),
			zap.String("family", string(familyConverse)),
		)
		c.logger.Error(i18n.T("llm.error.get_response_after_retries", "Bedrock"), zap.Error(err))
		return "", err
	}
	client.LogRequestFinish(c.logger, "BEDROCK", c.model, "success", time.Since(start),
		zap.String("family", string(familyConverse)),
		zap.Int("response_chars", len(responseText)),
	)
	return responseText, nil
}

// clampInt32 narrows an int into the int32 range Bedrock's Converse API
// expects for MaxTokens. Practical maxima for any model are well under
// the int32 ceiling, but the explicit clamp silences gosec G115 and
// keeps the conversion safe even if a misconfigured caller passes a
// truly huge value.
func clampInt32(v int) int32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

func (c *BedrockClient) getMaxTokensConverse() int {
	if tokenStr := os.Getenv("BEDROCK_MAX_TOKENS"); tokenStr != "" {
		if parsed, err := strconv.Atoi(tokenStr); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 4096
}

// buildConverseMessages projects the internal history onto Converse's
// strict alternating user/assistant shape. System messages are pulled out
// into a separate slice (Converse requires this — they cannot live inside
// the messages array). Cache markers from SystemParts are intentionally
// dropped here: Converse's cachePoint block has a different placement
// model and we deliberately keep the Anthropic cache planner scoped to
// the dedicated familyAnthropic path.
// bedrockImageBlocks converts the provider-agnostic image attachments into
// Bedrock Converse image content blocks. Bedrock requires the raw bytes
// (there is no URL source on the Converse ImageBlock), so URL-only images
// are skipped — the caller's describe-fallback handles those. Unsupported
// media types are dropped rather than erroring the whole turn.
func bedrockImageBlocks(images []models.ImageContent) []bedrockruntimetypes.ContentBlock {
	if len(images) == 0 {
		return nil
	}
	var blocks []bedrockruntimetypes.ContentBlock
	for _, ic := range images {
		if !ic.IsValid() || len(ic.Data) == 0 {
			continue
		}
		mt, _ := models.NormalizeImageMediaType(ic.MediaType)
		var format bedrockruntimetypes.ImageFormat
		switch mt {
		case "image/png":
			format = bedrockruntimetypes.ImageFormatPng
		case "image/jpeg":
			format = bedrockruntimetypes.ImageFormatJpeg
		case "image/gif":
			format = bedrockruntimetypes.ImageFormatGif
		case "image/webp":
			format = bedrockruntimetypes.ImageFormatWebp
		default:
			continue
		}
		blocks = append(blocks, &bedrockruntimetypes.ContentBlockMemberImage{
			Value: bedrockruntimetypes.ImageBlock{
				Format: format,
				Source: &bedrockruntimetypes.ImageSourceMemberBytes{Value: ic.Data},
			},
		})
	}
	return blocks
}

func buildConverseMessages(prompt string, history []models.Message) ([]bedrockruntimetypes.Message, []bedrockruntimetypes.SystemContentBlock) {
	var messages []bedrockruntimetypes.Message
	var systemBlocks []bedrockruntimetypes.SystemContentBlock

	flushSystem := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		systemBlocks = append(systemBlocks, &bedrockruntimetypes.SystemContentBlockMemberText{Value: text})
	}

	appendMessage := func(role bedrockruntimetypes.ConversationRole, text string, images []models.ImageContent) {
		imageBlocks := bedrockImageBlocks(images)
		if strings.TrimSpace(text) == "" && len(imageBlocks) == 0 {
			return
		}
		// Image blocks first (better grounding), then the text block.
		blocks := imageBlocks
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, &bedrockruntimetypes.ContentBlockMemberText{Value: text})
		}
		// Coalesce consecutive same-role messages — Bedrock Converse
		// rejects two user (or two assistant) messages in a row.
		if n := len(messages); n > 0 && messages[n-1].Role == role {
			messages[n-1].Content = append(messages[n-1].Content, blocks...)
			return
		}
		messages = append(messages, bedrockruntimetypes.Message{
			Role:    role,
			Content: blocks,
		})
	}

	for _, msg := range history {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "system":
			if len(msg.SystemParts) > 0 {
				for _, part := range msg.SystemParts {
					flushSystem(part.Text)
				}
			} else {
				flushSystem(msg.Content)
			}
		case "assistant":
			appendMessage(bedrockruntimetypes.ConversationRoleAssistant, msg.Content, msg.Images)
		default:
			appendMessage(bedrockruntimetypes.ConversationRoleUser, msg.Content, msg.Images)
		}
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		appendMessage(bedrockruntimetypes.ConversationRoleUser, prompt, nil)
	}

	// Converse rejects requests that start with an assistant message.
	// Drop the leading assistant turn(s) so we always start with user —
	// this matches what Anthropic's path tolerates implicitly.
	for len(messages) > 0 && messages[0].Role != bedrockruntimetypes.ConversationRoleUser {
		messages = messages[1:]
	}
	return messages, systemBlocks
}

func parseConverseOutput(out *bedrockruntime.ConverseOutput) (string, error) {
	if out == nil || out.Output == nil {
		return "", fmt.Errorf("%s", i18n.T("llm.error.no_response", "Bedrock"))
	}
	msg, ok := out.Output.(*bedrockruntimetypes.ConverseOutputMemberMessage)
	if !ok {
		return "", fmt.Errorf("bedrock-converse: unexpected output type %T", out.Output)
	}
	var b strings.Builder
	for _, block := range msg.Value.Content {
		if text, ok := block.(*bedrockruntimetypes.ContentBlockMemberText); ok {
			b.WriteString(text.Value)
		}
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("%s", i18n.T("llm.error.no_response", "Bedrock"))
	}
	return b.String(), nil
}

// wrapBedrockInferenceProfileError annotates the common ValidationException
// users hit when they pick a foundation ID that requires an inference
// profile. The raw AWS message tells you the model isn't on-demand
// invokable but doesn't suggest the fix; this wrapper points at the
// correct prefix family ("global." / "us." / "eu." / "apac.") so /switch
// users have a path forward without reading AWS docs.
func wrapBedrockInferenceProfileError(model string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		msg = apiErr.ErrorMessage()
	}
	low := strings.ToLower(msg)
	if !strings.Contains(low, "inference profile") &&
		!strings.Contains(low, "on-demand throughput isn't supported") &&
		!strings.Contains(low, "on-demand throughput is not supported") {
		return err
	}
	hint := suggestInferenceProfilePrefix(model)
	return fmt.Errorf(
		"bedrock: model %q requires an inference profile (the bare foundation ID is not invokable on-demand). "+
			"Try selecting %s instead, or run `/switch --model` to see profiles your account has access to. Original: %w",
		model, hint, err)
}

// suggestInferenceProfilePrefix builds a short, copy-pasteable hint based
// on the bare model ID the user picked. We stay deliberately vague on
// region — the right profile depends on the account's region and the
// model's availability — and just nudge them toward the prefix family.
func suggestInferenceProfilePrefix(model string) string {
	m := strings.TrimSpace(model)
	if m == "" {
		return "an inference profile (e.g. \"global.<provider>.<model>\")"
	}
	// Already prefixed — nothing useful to suggest.
	if strings.HasPrefix(m, "global.") ||
		strings.HasPrefix(m, "us.") ||
		strings.HasPrefix(m, "eu.") ||
		strings.HasPrefix(m, "apac.") {
		return fmt.Sprintf("a different inference profile for %q", m)
	}
	return fmt.Sprintf("\"global.%s\", \"us.%s\", \"eu.%s\", or \"apac.%s\"", m, m, m, m)
}
