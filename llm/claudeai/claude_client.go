/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package claudeai

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ClaudeClient é uma estrutura que contém o cliente de ClaudeAI com suas configurações
type ClaudeClient struct {
	apiKey      string
	model       string
	logger      *zap.Logger
	client      *http.Client
	maxAttempts int
	backoff     time.Duration
	apiURL      string
}

const (
	oauthUserAgent         = "claude-cli/2.1.2 (external, cli)"
	oauthAnthropicBeta     = "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219,fine-grained-tool-streaming-2025-05-14"
	oauthSonnet1MBeta      = "context-1m-2025-08-07"
	oauthBaseSystemPrompt  = "You are Claude Code, Anthropic's official CLI for Claude."
	oauthTitleModel        = "claude-haiku-4-5"
	oauthTitleMaxTokens    = 32000
	oauthTitleTemperature  = 0.5
	oauthTitleUserPrefix   = "Generate a title for this conversation:\n"
	oauthTitleUserWrapTmpl = "\n              The following is the text to summarize:\n              <text>\n              %s\n              </text>\n            "
	oauthMessagesBetaQuery = "beta=true"
	oauthContentType       = "application/json"
	oauthAcceptHeader      = "*/*"
	oauthAcceptEncoding    = "gzip, deflate, br, zstd"
	oauthConnectionHeader  = "keep-alive"
)

const oauthTitleSystemPrompt = `You are Claude Code, Anthropic's official CLI for Claude.

You are a title generator. You output ONLY a thread title. Nothing else.

<task>
Generate a brief title that would help the user find this conversation later.

Follow all rules in <rules>
Use the <examples> so you know what a good title looks like.
Your output must be:
- A single line
- ≤50 characters
- No explanations
</task>

<rules>
- you MUST use the same language as the user message you are summarizing
- Title must be grammatically correct and read naturally - no word salad
- Never include tool names in the title (e.g. "read tool", "bash tool", "edit tool")
- Focus on the main topic or question the user needs to retrieve
- Vary your phrasing - avoid repetitive patterns like always starting with "Analyzing"
- When a file is mentioned, focus on WHAT the user wants to do WITH the file, not just that they shared it
- Keep exact: technical terms, numbers, filenames, HTTP codes
- Remove: the, this, my, a, an
- Never assume tech stack
- Never use tools
- NEVER respond to questions, just generate a title for the conversation
- The title should NEVER include "summarizing" or "generating" when generating a title
- DO NOT SAY YOU CANNOT GENERATE A TITLE OR COMPLAIN ABOUT THE INPUT
- Always output something meaningful, even if the input is minimal.
- If the user message is short or conversational (e.g. "hello", "lol", "what's up", "hey"):
  → create a title that reflects the user's tone or intent (such as Greeting, Quick check-in, Light chat, Intro message, etc.)
</rules>

<examples>
"debug 500 errors in production" → Debugging production 500 errors
"refactor user service" → Refactoring user service
"why is app.js failing" → app.js failure investigation
"implement rate limiting" → Rate limiting implementation
"how do I connect postgres to my API" → Postgres API connection
"best practices for React hooks" → React hooks best practices
"@src/auth.ts can you add refresh token support" → Auth refresh token support
"@utils/parser.ts this is broken" → Parser bug fix
"look at @config.json" → Config review
"@App.tsx add dark mode toggle" → Dark mode toggle in App
</examples>
`

// NewClaudeClient cria um novo cliente ClaudeAI com configurações personalizáveis.
func NewClaudeClient(apiKey string, model string, logger *zap.Logger, maxAttempts int, backoff time.Duration) *ClaudeClient {
	httpClient := utils.NewHTTPClient(logger, 900*time.Second)
	return &ClaudeClient{
		apiKey:      apiKey,
		model:       model,
		logger:      logger,
		client:      httpClient,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		apiURL:      config.ClaudeAIAPIURL,
	}
}

// GetModelName retorna o nome amigável do modelo ClaudeAI
func (c *ClaudeClient) GetModelName() string {
	return catalog.GetDisplayName(catalog.ProviderClaudeAI, c.model)
}

// getMaxTokens obtém o limite de tokens configurado
func (c *ClaudeClient) getMaxTokens() int {
	if tokenStr := os.Getenv("ANTHROPIC_MAX_TOKENS"); tokenStr != "" {
		if parsedTokens, err := strconv.Atoi(tokenStr); err == nil && parsedTokens > 0 {
			c.logger.Debug("Usando max_tokens personalizado", zap.Int("max_tokens", parsedTokens))
			return parsedTokens
		}
	}
	return catalog.GetMaxTokens(catalog.ProviderClaudeAI, c.model, 0)
}

// SendPrompt com exponential backoff usando utils.Retry
func (c *ClaudeClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	effectiveMaxTokens := maxTokens
	if effectiveMaxTokens <= 0 {
		effectiveMaxTokens = c.getMaxTokens()
	}

	isOAuth := strings.HasPrefix(c.apiKey, "oauth:")
	var messages interface{}
	var systemObj interface{}
	if isOAuth {
		messages, systemObj = c.buildOAuthMessagesAndSystem(prompt, history)
	} else {
		messages, systemObj = c.buildMessagesAndSystem(prompt, history)
	}

	reqBody := map[string]interface{}{
		"model":      c.model,
		"max_tokens": effectiveMaxTokens,
		"messages":   messages,
	}

	if systemObj != nil {
		reqBody["system"] = systemObj
	}
	if isOAuth {
		reqBody["stream"] = true
	}

	jsonValue, err := json.Marshal(reqBody)
	if err != nil {
		c.logger.Error("Erro ao marshalizar o payload", zap.Error(err))
		return "", fmt.Errorf("erro ao preparar a requisição: %w", err)
	}

	if isOAuth {
		if err := c.sendOAuthTitleRequest(ctx, oauthTitleUserPrefix+prompt); err != nil {
			c.logger.Debug("Falha ao enviar request de titulo pre (OAuth)", zap.Error(err))
		}
	}

	responseText, err := utils.Retry(ctx, c.logger, c.maxAttempts, c.backoff, func(ctx context.Context) (string, error) {
		reqURL := c.apiURL
		if isOAuth {
			reqURL = withBetaQuery(reqURL)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(jsonValue))
		if err != nil {
			return "", fmt.Errorf("erro ao criar a requisição: %w", err)
		}
		req = req.WithContext(context.WithValue(req.Context(), oauthModelKey{}, c.model))

		req.Header.Add("Content-Type", oauthContentType)

		if isOAuth {
			applyOAuthHeaders(req, c.apiKey)
		} else if strings.HasPrefix(c.apiKey, "token:") {
			req.Header.Add("Authorization", "Bearer "+strings.TrimPrefix(c.apiKey, "token:"))
			req.Header.Add("anthropic-version", catalog.GetAnthropicAPIVersion(c.model))
		} else if strings.HasPrefix(c.apiKey, "apikey:") {
			req.Header.Add("x-api-key", strings.TrimPrefix(c.apiKey, "apikey:"))
			req.Header.Add("anthropic-version", catalog.GetAnthropicAPIVersion(c.model))
		} else {
			req.Header.Add("x-api-key", c.apiKey)
			req.Header.Add("anthropic-version", catalog.GetAnthropicAPIVersion(c.model))
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return "", err
		}

		if isOAuth {
			return c.processStreamResponse(resp)
		}
		return c.processResponse(resp)
	})

	if err != nil {
		c.logger.Error("Erro ao obter resposta da Claude AI após retries", zap.Error(err))
		return "", err
	}

	if isOAuth {
		if err := c.sendOAuthTitleRequest(ctx, fmt.Sprintf(oauthTitleUserWrapTmpl, prompt)); err != nil {
			c.logger.Debug("Falha ao enviar request de titulo pos (OAuth)", zap.Error(err))
		}
	}

	return responseText, nil
}

func (c *ClaudeClient) processResponse(resp *http.Response) (string, error) {
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Erro ao ler a resposta da ClaudeAI", zap.Error(err))
		return "", fmt.Errorf("erro ao ler a resposta: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(bodyBytes)}
	}

	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		c.logger.Error("Erro ao decodificar a resposta da ClaudeAI", zap.Error(err))
		return "", fmt.Errorf("erro ao decodificar a resposta: %w", err)
	}

	var responseText string
	for _, content := range result.Content {
		if content.Type == "text" {
			responseText += content.Text
		}
	}

	if responseText == "" {
		c.logger.Error("Nenhum conteúdo de texto encontrado na resposta da ClaudeAI")
		return "", fmt.Errorf("erro ao obter a resposta da ClaudeAI")
	}

	return responseText, nil
}

// helper beta 1m tokens sonnet model
func isClaudeSonnet(model string) bool {
	var claudeSonnetRe = regexp.MustCompile(`^claude-.*sonnet.*$`)
	return claudeSonnetRe.MatchString(model)
}

func (c *ClaudeClient) processStreamResponse(resp *http.Response) (string, error) {
	decodedBody, err := decodeResponseBody(resp)
	if err != nil {
		_ = resp.Body.Close()
		return "", err
	}
	defer func() { _ = decodedBody.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(decodedBody)
		return "", &utils.APIError{StatusCode: resp.StatusCode, Message: string(raw)}
	}

	var out strings.Builder
	reader := bufio.NewReader(decodedBody)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("erro ao ler stream: %w", err)
		}
		if len(line) == 0 && err == io.EOF {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || !strings.HasPrefix(line, "data:") {
			if err == io.EOF {
				break
			}
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			if err == io.EOF {
				break
			}
			continue
		}
		var evt struct {
			Type  string `json:"type"`
			Delta *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if jsonErr := json.Unmarshal([]byte(data), &evt); jsonErr != nil {
			if err == io.EOF {
				break
			}
			continue
		}
		if evt.Type == "content_block_delta" && evt.Delta != nil && evt.Delta.Type == "text_delta" {
			out.WriteString(evt.Delta.Text)
		}
		if evt.Type == "message_stop" && err == io.EOF {
			break
		}
		if err == io.EOF {
			break
		}
	}

	responseText := out.String()
	if responseText == "" {
		c.logger.Error("Nenhum conteúdo de texto encontrado na resposta da ClaudeAI (stream)")
		return "", fmt.Errorf("erro ao obter a resposta da ClaudeAI")
	}

	return responseText, nil
}

func (c *ClaudeClient) buildMessagesAndSystem(prompt string, history []models.Message) ([]map[string]string, interface{}) {
	var messages []map[string]string
	var systemParts []string

	for _, msg := range history {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			messages = append(messages, map[string]string{"role": "assistant", "content": msg.Content})
		case "system":
			systemParts = append(systemParts, msg.Content)
		default:
			messages = append(messages, map[string]string{"role": "user", "content": msg.Content})
		}
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]string{"role": "user", "content": prompt})
		}
	}

	if len(systemParts) > 0 {
		return messages, strings.Join(systemParts, "\n\n")
	}

	return messages, nil
}

func (c *ClaudeClient) buildOAuthMessagesAndSystem(prompt string, history []models.Message) ([]map[string]interface{}, []interface{}) {
	var messages []map[string]interface{}
	var systemParts []string

	for _, msg := range history {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant":
			messages = append(messages, map[string]interface{}{
				"role":    "assistant",
				"content": []interface{}{oauthTextBlock(msg.Content)},
			})
		case "system":
			systemParts = append(systemParts, msg.Content)
		default:
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": []interface{}{oauthTextBlock(msg.Content)},
			})
		}
	}

	if len(history) == 0 || history[len(history)-1].Role != "user" || history[len(history)-1].Content != prompt {
		if strings.TrimSpace(prompt) != "" {
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": []interface{}{oauthTextBlock(prompt)},
			})
		}
	}

	systemObjs := []interface{}{
		oauthTextBlock(oauthBaseSystemPrompt),
	}
	if len(systemParts) > 0 {
		joined := strings.Join(systemParts, "\n\n")
		systemObjs = append(systemObjs, oauthTextBlock(joined))
	}

	return messages, systemObjs
}

func (c *ClaudeClient) sendOAuthTitleRequest(ctx context.Context, userText string) error {
	if strings.TrimSpace(userText) == "" {
		return nil
	}
	body := map[string]interface{}{
		"model":       oauthTitleModel,
		"max_tokens":  oauthTitleMaxTokens,
		"temperature": oauthTitleTemperature,
		"system": []interface{}{
			oauthTextBlock(oauthBaseSystemPrompt),
			oauthTextBlock(oauthTitleSystemPrompt),
		},
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{oauthTextBlock(userText)},
			},
		},
		"stream": true,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	reqURL := withBetaQuery(c.apiURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req = req.WithContext(context.WithValue(req.Context(), oauthModelKey{}, oauthTitleModel))
	req.Header.Add("Content-Type", oauthContentType)
	applyOAuthHeaders(req, c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	_, err = c.processStreamResponse(resp)
	return err
}

func oauthTextBlock(text string) map[string]interface{} {
	return map[string]interface{}{
		"type": "text",
		"text": text,
		//"cache_control": map[string]string{
		//	"type": "ephemeral",
		//},
	}
}

func applyOAuthHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Accept", oauthAcceptHeader)
	req.Header.Set("Accept-Encoding", oauthAcceptEncoding)
	req.Header.Set("Connection", oauthConnectionHeader)
	req.Header.Set("User-Agent", oauthUserAgent)
	betas := oauthAnthropicBeta
	if os.Getenv("ANTHROPIC_1MTOKENS_SONNET") == "true" {
		if m, ok := req.Context().Value(oauthModelKey{}).(string); ok && isClaudeSonnet(m) {
			betas = betas + "," + oauthSonnet1MBeta
		}
	}
	req.Header.Set("anthropic-beta", betas)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(apiKey, "oauth:"))
}

type oauthModelKey struct{}

func withBetaQuery(baseURL string) string {
	if strings.Contains(baseURL, "?") {
		return baseURL + "&" + oauthMessagesBetaQuery
	}
	return baseURL + "?" + oauthMessagesBetaQuery
}

type multiCloser struct {
	reader io.Reader
	close  func() error
}

func (m *multiCloser) Read(p []byte) (int, error) { return m.reader.Read(p) }
func (m *multiCloser) Close() error               { return m.close() }

func decodeResponseBody(resp *http.Response) (io.ReadCloser, error) {
	encoding := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Encoding")))
	switch encoding {
	case "", "identity":
		return resp.Body, nil
	case "gzip":
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		return &multiCloser{
			reader: gr,
			close: func() error {
				_ = gr.Close()
				return resp.Body.Close()
			},
		}, nil
	case "deflate":
		fr := flate.NewReader(resp.Body)
		return &multiCloser{
			reader: fr,
			close: func() error {
				_ = fr.Close()
				return resp.Body.Close()
			},
		}, nil
	default:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("content-encoding nao suportado: %s", encoding)
	}
}
