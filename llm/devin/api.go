/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package devin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// ErrNotSupported marks operations that exist in only one API generation
// (e.g. archive is v3-only). Surfaces translate it into a friendly message.
var ErrNotSupported = errors.New("devin: operation not supported by this API version")

// API is the version-neutral Devin REST surface shared by the DEVIN provider,
// the @devin builtin/command and the scheduler polling action. Both
// generations (v1 individual/Teams, v3 organizations/enterprise) implement it.
type API interface {
	// Version returns "v1" or "v3".
	Version() string

	CreateSession(ctx context.Context, req CreateSessionRequest) (*Session, error)
	GetSession(ctx context.Context, sessionID string) (*Session, error)
	ListSessions(ctx context.Context, opts ListSessionsOptions) ([]Session, error)
	SendMessage(ctx context.Context, sessionID, message string, attachmentURLs []string) error
	ListMessages(ctx context.Context, sessionID string) ([]Message, error)
	TerminateSession(ctx context.Context, sessionID string) error
	// ArchiveSession is v3-only; v1 returns ErrNotSupported.
	ArchiveSession(ctx context.Context, sessionID string) error
	SetSessionTags(ctx context.Context, sessionID string, tags []string) error

	UploadAttachment(ctx context.Context, filename string, content io.Reader) (*Attachment, error)

	ListSecrets(ctx context.Context) ([]Secret, error)
	CreateSecret(ctx context.Context, req CreateSecretRequest) (*Secret, error)
	DeleteSecret(ctx context.Context, secretID string) error

	ListKnowledge(ctx context.Context) ([]KnowledgeNote, error)
	CreateKnowledge(ctx context.Context, req KnowledgeNoteRequest) (*KnowledgeNote, error)
	UpdateKnowledge(ctx context.Context, noteID string, req KnowledgeNoteRequest) (*KnowledgeNote, error)
	DeleteKnowledge(ctx context.Context, noteID string) error

	ListPlaybooks(ctx context.Context) ([]Playbook, error)
	GetPlaybook(ctx context.Context, playbookID string) (*Playbook, error)
	CreatePlaybook(ctx context.Context, req PlaybookRequest) (*Playbook, error)
	UpdatePlaybook(ctx context.Context, playbookID string, req PlaybookRequest) (*Playbook, error)
	DeletePlaybook(ctx context.Context, playbookID string) error
}

// APIConfig configures NewAPI. Zero values resolve from the environment.
type APIConfig struct {
	// APIKey is the bearer credential: `apk_user_…`/`apk_…` (v1 personal/
	// Teams) or `cog_…` (v3 service user). Falls back to DEVIN_API_KEY.
	APIKey string
	// OrgID is the `org-…` identifier required by v3. Falls back to
	// DEVIN_ORG_ID.
	OrgID string
	// Version forces "v1" or "v3"; empty/"auto" picks by credential shape.
	Version string
	// BaseURL overrides https://api.devin.ai (self-hosted enterprise).
	BaseURL string
	Logger  *zap.Logger
}

// ResolveAPIConfigFromEnv builds an APIConfig from the DEVIN_* environment.
func ResolveAPIConfigFromEnv(logger *zap.Logger) APIConfig {
	return APIConfig{
		APIKey:  os.Getenv(config.DevinAPIKeyEnv),
		OrgID:   os.Getenv(config.DevinOrgIDEnv),
		Version: os.Getenv(config.DevinAPIVersionEnv),
		BaseURL: os.Getenv(config.DevinBaseURLEnv),
		Logger:  logger,
	}
}

// ResolveVersion applies the flavor-detection rule: an explicit version wins;
// otherwise a `cog_…` credential with an org id targets v3 and anything else
// targets v1 (the individual/Teams generation, which also accepts cog_ keys).
func (c APIConfig) ResolveVersion() string {
	switch strings.ToLower(strings.TrimSpace(c.Version)) {
	case "v1":
		return "v1"
	case "v3":
		return "v3"
	}
	if strings.HasPrefix(c.APIKey, "cog_") && strings.TrimSpace(c.OrgID) != "" {
		return "v3"
	}
	return "v1"
}

// NewAPI builds the concrete client for the resolved generation. It fails
// fast on missing credentials so surfaces can show setup guidance instead of
// a mid-flight 401.
func NewAPI(cfg APIConfig) (API, error) {
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("devin: missing API key (set %s)", config.DevinAPIKeyEnv)
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = config.DevinDefaultBaseURL
	}
	core := &apiCore{
		apiKey:  cfg.APIKey,
		baseURL: base,
		logger:  cfg.Logger,
		// Session polling and CRUD calls are short; uploads get a dedicated
		// H1 client because Go's h2 POST-with-body intermittently fails with
		// "unexpected EOF" against Cloudflare-fronted hosts.
		httpClient:   utils.NewHTTPClient(cfg.Logger, 120*time.Second),
		uploadClient: utils.NewHTTPClientH1(cfg.Logger, 300*time.Second),
	}
	if cfg.ResolveVersion() == "v3" {
		orgID := strings.TrimSpace(cfg.OrgID)
		if orgID == "" {
			return nil, fmt.Errorf("devin: v3 API requires an organization id (set %s)", config.DevinOrgIDEnv)
		}
		return &v3Client{core: core, orgID: orgID}, nil
	}
	return &v1Client{core: core}, nil
}

// apiCore holds the transport shared by both generations.
type apiCore struct {
	apiKey       string
	baseURL      string
	logger       *zap.Logger
	httpClient   *http.Client
	uploadClient *http.Client
}

// devinDefaultUserAgent identifies the client to Devin. Two edges care about
// this value and pull it in opposite directions:
//
//   - The CloudFront/AWS-WAF edge in front of api.devin.ai 403s ("The request
//     could not be satisfied") requests carrying Go's default
//     "Go-http-client/…", which bot-reputation rules flag — so we must send a
//     custom UA.
//   - A TLS-intercepting corporate proxy (the kind that needs CHATCLI_CA_BUNDLE)
//     inspects header VALUES; a URL embedded in the UA can trip DLP/URL-category
//     filtering and get the request blocked at the proxy.
//
// So the default is a plain token with NO embedded URL: identifiable enough
// for the WAF, innocuous enough for a header-scanning proxy. Override with
// DEVIN_USER_AGENT when a deployment's WAF requires a browser-like string.
const devinDefaultUserAgent = "ChatCLI/1.0"

// setCommonHeaders applies the bearer credential and the User-Agent shared by
// every Devin request.
func (c *apiCore) setCommonHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	ua := strings.TrimSpace(os.Getenv("DEVIN_USER_AGENT"))
	if ua == "" {
		ua = devinDefaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
}

// doJSON performs an authenticated JSON round-trip. A nil `in` sends no body;
// a nil `out` discards the response body. Non-2xx statuses map to
// utils.APIError with a sanitized body so callers can branch on status codes.
func (c *apiCore) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("devin: encode request: %w", err)
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("devin: create request: %w", err)
	}
	c.setCommonHeaders(req)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("devin: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("devin: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(raw))}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("devin: decode response: %w", err)
	}
	return nil
}

// uploadMultipart POSTs a file as multipart/form-data field "file" and
// decodes the JSON (v3) or raw string (v1) response into out via the caller.
func (c *apiCore) uploadMultipart(ctx context.Context, path, filename string, content io.Reader) ([]byte, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return nil, fmt.Errorf("devin: build multipart: %w", err)
	}
	if _, err := io.Copy(part, content); err != nil {
		return nil, fmt.Errorf("devin: read attachment: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("devin: finish multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &buf)
	if err != nil {
		return nil, fmt.Errorf("devin: create request: %w", err)
	}
	c.setCommonHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.uploadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devin: upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("devin: read upload response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &utils.APIError{StatusCode: resp.StatusCode, Message: utils.SanitizeSensitiveText(string(raw))}
	}
	return raw, nil
}
