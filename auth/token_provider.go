/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// TokenProvider supplies credentials for HTTP authorization. Implementations
// are responsible for refreshing OAuth tokens transparently, coalescing
// concurrent refresh attempts, and persisting renewed credentials to the
// auth store.
//
// All methods are safe for concurrent use.
type TokenProvider interface {
	// Token returns the current access token. It blocks only when a refresh
	// is required; concurrent callers share the in-flight refresh.
	Token(ctx context.Context) (string, error)

	// Mode reports how the credential is presented (oauth, api-key, token).
	Mode() AuthMode

	// Provider returns the auth provider id this token belongs to.
	Provider() ProviderID

	// ProfileID returns the auth-store profile id, or "" for env/inline sources.
	ProfileID() string

	// Source returns a short label for diagnostics ("auth-store", "env:NAME", "inline").
	Source() string

	// Email returns the account email if known.
	Email() string

	// Invalidate marks the current token as unusable so the next Token call
	// forces a refresh. Called by HTTP clients after a 401/403 to recover
	// mid-session. No-op for non-refreshable providers.
	Invalidate()

	// Close stops any background refresh goroutine. Safe to call multiple times.
	Close()
}

// DoWithRefresh executes buildAndSend, automatically refreshing the token and
// retrying once when the response is 401 or 403 and the provider runs in OAuth
// mode. The callback receives the current access token and must construct a
// fresh *http.Request on each invocation (the previous body is consumed).
//
// The caller owns the returned response and is responsible for closing the body.
func DoWithRefresh(ctx context.Context, p TokenProvider, buildAndSend func(token string) (*http.Response, error)) (*http.Response, error) {
	token, err := p.Token(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := buildAndSend(token)
	if err != nil {
		return resp, err
	}
	if p.Mode() != AuthModeOAuth {
		return resp, nil
	}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		return resp, nil
	}
	// Drain and close so the underlying connection can be reused.
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	p.Invalidate()
	token, err = p.Token(ctx)
	if err != nil {
		return nil, err
	}
	return buildAndSend(token)
}

// staticTokenProvider wraps a non-refreshable credential (API key, env var,
// or inline-forwarded token from a remote client).
type staticTokenProvider struct {
	token     string
	mode      AuthMode
	provider  ProviderID
	profileID string
	source    string
	email     string
}

// NewStaticTokenProvider returns a TokenProvider that always serves the same token.
func NewStaticTokenProvider(token string, mode AuthMode, provider ProviderID) TokenProvider {
	return &staticTokenProvider{
		token:    strings.TrimSpace(token),
		mode:     mode,
		provider: provider,
		source:   "inline",
	}
}

// NewStaticTokenProviderFromResolved wraps a previously resolved key into a
// non-refreshable TokenProvider. Used when callers already hold a prefixed
// string ("oauth:...", "token:...", "apikey:...") and only need passthrough —
// e.g. server-side request handling that received the token from a remote
// client and must not attempt local refresh.
func NewStaticTokenProviderFromResolved(prefixedKey string, provider ProviderID) TokenProvider {
	mode := AuthModeAPIKey
	raw := strings.TrimSpace(prefixedKey)
	switch {
	case strings.HasPrefix(raw, "oauth:"):
		mode = AuthModeOAuth
		raw = strings.TrimPrefix(raw, "oauth:")
	case strings.HasPrefix(raw, "token:"):
		mode = AuthModeToken
		raw = strings.TrimPrefix(raw, "token:")
	case strings.HasPrefix(raw, "apikey:"):
		mode = AuthModeAPIKey
		raw = strings.TrimPrefix(raw, "apikey:")
	}
	return &staticTokenProvider{
		token:    raw,
		mode:     mode,
		provider: provider,
		source:   "inline",
	}
}

func (s *staticTokenProvider) Token(_ context.Context) (string, error) {
	if s.token == "" {
		return "", fmt.Errorf("%s", i18n.T("auth.token_provider.empty_token"))
	}
	return s.token, nil
}

func (s *staticTokenProvider) Mode() AuthMode       { return s.mode }
func (s *staticTokenProvider) Provider() ProviderID { return s.provider }
func (s *staticTokenProvider) ProfileID() string    { return s.profileID }
func (s *staticTokenProvider) Source() string       { return s.source }
func (s *staticTokenProvider) Email() string        { return s.email }
func (s *staticTokenProvider) Invalidate()          {}
func (s *staticTokenProvider) Close()               {}

// Background refresh tunables. The provider captures these into its struct
// at construction time so the background goroutine never races with test
// substitutions of the defaults.
//
// refreshLead is how long before the stored expiry the proactive loop fires.
// The stored expiry already carries a 5-minute safety margin
// (calcExpiresAtMilli), so 1 minute lead leaves ~4 minutes of real-world
// headroom before the upstream token actually dies.
//
// refreshMinWait caps loop frequency when expiry is in the past.
//
// refreshErrorBackoff delays the next attempt after a failure.
const (
	defaultRefreshLead         = 1 * time.Minute
	defaultRefreshMinWait      = 30 * time.Second
	defaultRefreshErrorBackoff = 60 * time.Second
)

// oauthRefreshFn performs the actual OAuth refresh round-trip. Pluggable so
// tests can substitute a fake.
type oauthRefreshFn func(ctx context.Context, cred *AuthProfileCredential, logger *zap.Logger) (*AuthProfileCredential, error)

// oauthTokenProvider wraps a refreshable OAuth credential. It coalesces
// concurrent refresh attempts via a single-flight channel and runs a
// background goroutine to renew the token proactively before expiry.
type oauthTokenProvider struct {
	mu          sync.Mutex
	cred        AuthProfileCredential
	profileID   string
	source      string
	logger      *zap.Logger
	refreshFn   oauthRefreshFn
	refreshing  chan struct{}
	lastErr     error
	invalidated bool

	refreshLead         time.Duration
	refreshMinWait      time.Duration
	refreshErrorBackoff time.Duration

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgDone   chan struct{}
	closed   bool
}

// newOAuthTokenProvider builds a refreshable provider from a credential snapshot.
// Starts a background goroutine when the credential has a refresh token and
// a positive expiry. The returned provider must be Close()'d to release the
// goroutine.
func newOAuthTokenProvider(cred *AuthProfileCredential, profileID, source string, logger *zap.Logger) *oauthTokenProvider {
	p := &oauthTokenProvider{
		cred:                *cred,
		profileID:           profileID,
		source:              source,
		logger:              logger,
		refreshFn:           RefreshOAuth,
		refreshLead:         defaultRefreshLead,
		refreshMinWait:      defaultRefreshMinWait,
		refreshErrorBackoff: defaultRefreshErrorBackoff,
	}
	if cred.Refresh != "" && cred.Expires > 0 {
		p.bgCtx, p.bgCancel = context.WithCancel(context.Background())
		p.bgDone = make(chan struct{})
		go p.backgroundLoop()
	}
	return p
}

func (p *oauthTokenProvider) Token(ctx context.Context) (string, error) {
	p.mu.Lock()
	if !p.invalidated && !p.cred.IsExpired() {
		token := p.cred.Access
		p.mu.Unlock()
		return token, nil
	}

	if p.refreshing != nil {
		done := p.refreshing
		p.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		p.mu.Lock()
		token := p.cred.Access
		err := p.lastErr
		p.mu.Unlock()
		if err != nil {
			return "", err
		}
		return token, nil
	}

	done := make(chan struct{})
	p.refreshing = done
	credSnapshot := p.cred
	p.mu.Unlock()

	err := p.runRefresh(ctx, &credSnapshot)

	p.mu.Lock()
	p.refreshing = nil
	p.lastErr = err
	if err == nil {
		p.invalidated = false
	}
	close(done)
	token := p.cred.Access
	p.mu.Unlock()

	if err != nil {
		return "", err
	}
	return token, nil
}

func (p *oauthTokenProvider) runRefresh(ctx context.Context, snapshot *AuthProfileCredential) error {
	refreshed, err := p.refreshFn(ctx, snapshot, p.logger)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.cred.Access = refreshed.Access
	if refreshed.Refresh != "" {
		p.cred.Refresh = refreshed.Refresh
	}
	p.cred.Expires = refreshed.Expires
	p.mu.Unlock()

	p.persistToStore(refreshed)
	return nil
}

func (p *oauthTokenProvider) persistToStore(refreshed *AuthProfileCredential) {
	if p.profileID == "" {
		return
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	store := loadStoreUnlocked(p.logger)
	if store == nil {
		return
	}
	existing := store.Profiles[p.profileID]
	if existing == nil {
		return
	}
	updated := *existing
	updated.Access = refreshed.Access
	if refreshed.Refresh != "" {
		updated.Refresh = refreshed.Refresh
	}
	updated.Expires = refreshed.Expires
	store.Profiles[p.profileID] = &updated
	if err := saveStoreUnlocked(store, p.logger); err != nil && p.logger != nil {
		p.logger.Warn(i18n.T("auth.token_provider.persist_failed"),
			zap.String("provider", string(p.cred.Provider)),
			zap.Error(err))
	}
}

func (p *oauthTokenProvider) backgroundLoop() {
	defer close(p.bgDone)
	for {
		p.mu.Lock()
		expires := p.cred.Expires
		refresh := p.cred.Refresh
		p.mu.Unlock()

		if refresh == "" || expires <= 0 {
			return
		}

		wait := time.Until(time.UnixMilli(expires)) - p.refreshLead
		if wait < p.refreshMinWait {
			wait = p.refreshMinWait
		}

		timer := time.NewTimer(wait)
		select {
		case <-p.bgCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if _, err := p.Token(p.bgCtx); err != nil {
			if p.logger != nil {
				p.logger.Warn(i18n.T("auth.token_provider.background_refresh_failed"),
					zap.String("provider", string(p.cred.Provider)),
					zap.Error(err))
			}
			select {
			case <-p.bgCtx.Done():
				return
			case <-time.After(p.refreshErrorBackoff):
			}
		}
	}
}

func (p *oauthTokenProvider) Mode() AuthMode       { return AuthModeOAuth }
func (p *oauthTokenProvider) Provider() ProviderID { return p.cred.Provider }
func (p *oauthTokenProvider) ProfileID() string    { return p.profileID }
func (p *oauthTokenProvider) Source() string       { return p.source }

func (p *oauthTokenProvider) Email() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cred.Email
}

func (p *oauthTokenProvider) Invalidate() {
	p.mu.Lock()
	p.invalidated = true
	p.mu.Unlock()
}

func (p *oauthTokenProvider) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	cancel := p.bgCancel
	done := p.bgDone
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
