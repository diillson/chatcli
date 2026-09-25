/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * llm_route.go
 *
 * Request-path LLM routing for the gRPC server. Every prompt RPC (SendPrompt,
 * StreamPrompt, InteractiveSession, AnalyzeIssue, AgenticStep) resolves its
 * client here, so the whole surface shares one behavior:
 *
 *   - caller credentials (client_api_key / provider_config) and explicit
 *     provider/model overrides get a dedicated client, exactly as before;
 *   - a request that names nothing goes through the configured fallback
 *     chain when there is one, otherwise the server default client;
 *   - max_tokens comes from the request, else the provider's env override,
 *     else the catalog ceiling for the model (never the provider's implicit
 *     default);
 *   - an expired server credential (401/403) triggers ONE throttled
 *     provider refresh and a retry, never for caller-forwarded credentials;
 *   - a reply stopped by a safety classifier (stop_reason refusal) is resent
 *     ONCE on the sibling model of the same provider, like the agent loop;
 *   - responses carry the provider and model that actually answered plus
 *     the usage the provider reported.
 *
 * Nothing here serializes requests: every RPC keeps running concurrently on
 * its own client instance.
 */
package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/pricing"
	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// authRefreshMinInterval bounds how often expired server credentials are
// refreshed: under load, every in-flight request would otherwise trigger its
// own provider rebuild on the same 401.
const authRefreshMinInterval = 30 * time.Second

// llmRoute is the client chosen for one request and how to attribute it.
type llmRoute struct {
	client   client.LLMClient
	provider string
	model    string
	// chain is non-nil when the shared fallback chain serves the request;
	// attribution then comes from the entry that answered.
	chain fallbackChainClient
	// callerCreds is true when the caller forwarded its own credential. The
	// server never refreshes those (the remote client owns their lifecycle).
	callerCreds bool
	// request keeps what is needed to rebuild the route after a refresh or
	// for a refusal retry on a sibling model.
	request routeRequest
}

// routeRequest is the caller's routing input, kept for retries.
type routeRequest struct {
	provider       string
	model          string
	clientAPIKey   string
	providerConfig map[string]string
}

// fallbackChainClient is the slice of *fallback.Chain the router uses. An
// interface so tests can stand in a chain without building providers.
type fallbackChainClient interface {
	client.LLMClient
	LastServedEntry() (provider, model string, ok bool)
}

// turnResult is one completed reply with its attribution and usage.
type turnResult struct {
	text       string
	usage      *models.UsageInfo
	stopReason string
	provider   string
	model      string
}

// resolveRoute picks the client for a request. See the file header for the
// precedence. The route is resolved once per request; retries rebuild it
// through the same rules.
func (h *Handler) resolveRoute(provider, model, clientAPIKey string, providerConfig map[string]string) (llmRoute, error) {
	req := routeRequest{provider: provider, model: model, clientAPIKey: clientAPIKey, providerConfig: providerConfig}
	callerCreds := clientAPIKey != "" || len(providerConfig) > 0
	explicit := strings.TrimSpace(provider) != "" || strings.TrimSpace(model) != ""

	if !callerCreds && !explicit && h.fallbackChain != nil {
		return llmRoute{
			client:   h.fallbackChain,
			provider: h.defaultProvider,
			model:    h.fallbackChain.GetModelName(),
			chain:    h.fallbackChain,
			request:  req,
		}, nil
	}

	c, err := h.getClient(provider, model, clientAPIKey, providerConfig)
	if err != nil {
		return llmRoute{}, err
	}
	resolvedProvider := provider
	if resolvedProvider == "" {
		resolvedProvider = h.defaultProvider
	}
	return llmRoute{
		client:      c,
		provider:    resolvedProvider,
		model:       c.GetModelName(),
		callerCreds: callerCreds,
		request:     req,
	}, nil
}

// effectiveMaxTokens resolves max_tokens for the route: the request value
// when positive, else the provider env override, else the catalog ceiling.
func (h *Handler) effectiveMaxTokens(requested int32, r llmRoute) int {
	return catalog.EffectiveMaxTokens(r.provider, r.model, int(requested))
}

// complete runs one non-streaming request through the route with the
// recovery rules of the file header and returns the attributed result.
func (h *Handler) complete(ctx context.Context, r llmRoute, prompt string, history []models.Message, maxTokens int) (turnResult, error) {
	text, err := r.client.SendPrompt(ctx, prompt, history, maxTokens)
	if err != nil {
		if retry, ok := h.routeAfterAuthError(err, r); ok {
			r = retry
			text, err = r.client.SendPrompt(ctx, prompt, history, maxTokens)
		}
	}
	if err != nil {
		if retry, ok := h.routeAfterRefusal(err, r); ok {
			r = retry
			text, err = r.client.SendPrompt(ctx, prompt, history, maxTokens)
		}
	}
	if err != nil {
		return turnResult{}, err
	}
	res := h.attribute(r, prompt, history, text)
	if sr, ok := client.AsStopReasonAware(r.client); ok {
		res.stopReason = sr.LastStopReason()
	}
	return res, nil
}

// stream runs one streaming request through the route. onChunk receives
// every text fragment as it arrives; the returned result carries the final
// attribution and usage. When the route cannot stream, the reply is
// completed in one call and delivered as a single chunk, so callers never
// need a second code path.
func (h *Handler) stream(ctx context.Context, r llmRoute, prompt string, history []models.Message, maxTokens int, onChunk func(string) error) (turnResult, error) {
	sc, ok := client.AsStreamingClient(r.client)
	if !ok {
		res, err := h.complete(ctx, r, prompt, history, maxTokens)
		if err != nil {
			return turnResult{}, err
		}
		if err := onChunk(res.text); err != nil {
			return turnResult{}, err
		}
		return res, nil
	}

	chunks, err := sc.SendPromptStream(ctx, prompt, history, maxTokens)
	if err != nil {
		if retry, ok := h.routeAfterAuthError(err, r); ok {
			r = retry
			if rsc, ok := client.AsStreamingClient(r.client); ok {
				sc = rsc
				chunks, err = sc.SendPromptStream(ctx, prompt, history, maxTokens)
			} else {
				return h.streamViaComplete(ctx, r, prompt, history, maxTokens, onChunk)
			}
		}
	}
	if err != nil {
		if retry, ok := h.routeAfterRefusal(err, r); ok {
			return h.streamViaComplete(ctx, retry, prompt, history, maxTokens, onChunk)
		}
		return turnResult{}, err
	}

	var (
		text       strings.Builder
		usage      *models.UsageInfo
		stopReason string
	)
	for chunk := range chunks {
		if chunk.Error != nil {
			// A refusal surfaces as the stream's terminal error before any
			// text; a sibling retry is only safe while nothing was shown.
			if text.Len() == 0 {
				if retry, ok := h.routeAfterRefusal(chunk.Error, r); ok {
					return h.streamViaComplete(ctx, retry, prompt, history, maxTokens, onChunk)
				}
			}
			return turnResult{}, chunk.Error
		}
		if chunk.Text != "" {
			text.WriteString(chunk.Text)
			if err := onChunk(chunk.Text); err != nil {
				return turnResult{}, err
			}
		}
		if chunk.Done {
			usage = chunk.Usage
			stopReason = chunk.StopReason
		}
	}
	res := h.attribute(r, prompt, history, text.String())
	if usage != nil {
		res.usage = usage
	}
	if stopReason != "" {
		res.stopReason = stopReason
	} else if sr, ok := client.AsStopReasonAware(r.client); ok {
		res.stopReason = sr.LastStopReason()
	}
	return res, nil
}

// streamViaComplete finishes a retried request without streaming and hands
// the whole reply to onChunk once.
func (h *Handler) streamViaComplete(ctx context.Context, r llmRoute, prompt string, history []models.Message, maxTokens int, onChunk func(string) error) (turnResult, error) {
	res, err := h.complete(ctx, r, prompt, history, maxTokens)
	if err != nil {
		return turnResult{}, err
	}
	if err := onChunk(res.text); err != nil {
		return turnResult{}, err
	}
	return res, nil
}

// attribute fills the result with the provider/model that answered and the
// usage the provider reported (estimated from characters when it did not).
func (h *Handler) attribute(r llmRoute, prompt string, history []models.Message, text string) turnResult {
	res := turnResult{text: text, provider: r.provider, model: r.model}
	if r.chain != nil {
		if p, m, ok := r.chain.LastServedEntry(); ok {
			res.provider, res.model = p, m
		}
	} else if name := r.client.GetModelName(); name != "" {
		res.model = name
	}
	inputChars := len(prompt)
	for _, m := range history {
		inputChars += len(m.Content)
	}
	res.usage = client.GetUsageOrEstimate(r.client, inputChars, len(text))
	return res
}

// routeAfterAuthError rebuilds the route after an expired server credential.
// Caller-forwarded credentials are never refreshed: the remote client owns
// them. Refreshes are throttled process-wide so a burst of 401s under load
// rebuilds the providers once, not once per request.
func (h *Handler) routeAfterAuthError(err error, r llmRoute) (llmRoute, bool) {
	if r.callerCreds || !isAuthError(err) {
		return llmRoute{}, false
	}
	if h.refreshServerCredentials() {
		h.logger.Info(i18n.T("server.route.auth_refreshed"), zap.String("provider", r.provider))
	}
	retry, rerr := h.resolveRoute(r.request.provider, r.request.model, r.request.clientAPIKey, r.request.providerConfig)
	if rerr != nil {
		h.logger.Warn(i18n.T("server.route.auth_reroute_failed"), zap.String("provider", r.provider), zap.Error(rerr))
		return llmRoute{}, false
	}
	return retry, true
}

// refreshServerCredentials rebuilds the providers at most once per
// authRefreshMinInterval. Returns true when this call did the refresh; a
// caller that finds a recent refresh simply rebuilds its route on the
// already-refreshed manager.
func (h *Handler) refreshServerCredentials() bool {
	h.authRefreshMu.Lock()
	defer h.authRefreshMu.Unlock()
	if time.Since(h.authRefreshAt) < authRefreshMinInterval {
		return false
	}
	h.llmManager.RefreshProviders()
	h.authRefreshAt = time.Now()
	return true
}

// routeAfterRefusal picks the sibling model of the same provider when the
// reply was stopped by a safety classifier. Resending on the same model
// does not clear the classifier, so the retry always moves. Caller
// credentials and provider config are carried over unchanged. Chain routes
// are left alone: the chain already owns its own failover.
func (h *Handler) routeAfterRefusal(err error, r llmRoute) (llmRoute, bool) {
	if r.chain != nil || !client.IsRefusal(err) {
		return llmRoute{}, false
	}
	handle := catalog.RefusalSibling(r.provider, r.model)
	provider, model, ok := catalog.SplitRouteHandle(handle)
	if !ok {
		h.logger.Warn(i18n.T("server.route.refusal_no_sibling"), zap.String("provider", r.provider), zap.String("model", r.model))
		return llmRoute{}, false
	}
	c, cerr := h.getClient(provider, model, r.request.clientAPIKey, r.request.providerConfig)
	if cerr != nil {
		h.logger.Warn(i18n.T("server.route.refusal_reroute_failed"), zap.String("provider", provider), zap.String("model", model), zap.Error(cerr))
		return llmRoute{}, false
	}
	h.logger.Info(i18n.T("server.route.refusal_retry"), zap.String("from", r.model), zap.String("to", model))
	req := r.request
	req.provider, req.model = provider, model
	return llmRoute{client: c, provider: provider, model: model, callerCreds: r.callerCreds, request: req}, true
}

// isAuthError reports whether err is a provider 401/403.
func isAuthError(err error) bool {
	var apiErr *utils.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 401 || apiErr.StatusCode == 403
	}
	return false
}

// usageToProto converts the provider usage into the wire message and
// prices it with the shared engine, so a client without a pricing table
// (the operator, a thin client) sees the same USD the CLI's /cost shows.
func usageToProto(provider, model string, u *models.UsageInfo) *pb.TokenUsage {
	if u == nil {
		return nil
	}
	cost := pricing.CostOf(provider, model, u)
	return &pb.TokenUsage{
		PromptTokens:     int32(u.PromptTokens),             //#nosec G115 -- token counts are bounded by provider context windows
		CompletionTokens: int32(u.CompletionTokens),         //#nosec G115 -- see above
		CacheReadTokens:  int32(u.CacheReadInputTokens),     //#nosec G115 -- see above
		CacheWriteTokens: int32(u.CacheCreationInputTokens), //#nosec G115 -- see above
		ReasoningTokens:  int32(u.ReasoningTokens),          //#nosec G115 -- see above
		Estimated:        !u.IsReal,
		CostUsd:          cost.TotalUSD,
		CostKnown:        cost.Known,
	}
}

// protoToUsage is the inverse, used by the remote client.
func protoToUsage(u *pb.TokenUsage) *models.UsageInfo {
	if u == nil {
		return nil
	}
	return &models.UsageInfo{
		PromptTokens:             int(u.PromptTokens),
		CompletionTokens:         int(u.CompletionTokens),
		TotalTokens:              int(u.PromptTokens + u.CompletionTokens),
		CacheReadInputTokens:     int(u.CacheReadTokens),
		CacheCreationInputTokens: int(u.CacheWriteTokens),
		ReasoningTokens:          int(u.ReasoningTokens),
		IsReal:                   !u.Estimated,
	}
}

// handlerRouteState holds the throttle for server credential refreshes.
// Embedded in Handler so the zero value is ready.
type handlerRouteState struct {
	authRefreshMu sync.Mutex
	authRefreshAt time.Time
}
