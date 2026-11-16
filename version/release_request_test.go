/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package version

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTrip func(*http.Request) *http.Response

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }

func answer(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func noTokens(t *testing.T) {
	t.Helper()
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
}

func TestReleaseToken(t *testing.T) {
	noTokens(t)
	assert.Empty(t, releaseToken(), "no token in the environment")
	t.Setenv("GITHUB_TOKEN", "from-github-token")
	assert.Equal(t, "from-github-token", releaseToken())
	t.Setenv("GH_TOKEN", " from-gh-token ")
	assert.Equal(t, "from-gh-token", releaseToken(), "GH_TOKEN wins, as it does for gh")
}

func TestRateLimited(t *testing.T) {
	spent := http.Header{"X-Ratelimit-Remaining": {"0"}}
	later := http.Header{"Retry-After": {"30"}}
	left := http.Header{"X-Ratelimit-Remaining": {"12"}}
	assert.True(t, rateLimited(http.StatusForbidden, spent))
	assert.True(t, rateLimited(http.StatusTooManyRequests, later))
	assert.False(t, rateLimited(http.StatusForbidden, left), "a 403 with allowance left is not the limit")
	assert.False(t, rateLimited(http.StatusForbidden, http.Header{}), "a proxy's 403 carries no rate limit headers")
	assert.False(t, rateLimited(http.StatusNotFound, spent))
}

func TestRateLimitReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	assert.Equal(t, now.Add(30*time.Second), rateLimitReset(http.Header{"Retry-After": {"30"}}, now))
	at := now.Add(17 * time.Minute)
	assert.Equal(t, at, rateLimitReset(http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(at.Unix(), 10)}}, now))
	assert.True(t, rateLimitReset(http.Header{"X-Ratelimit-Reset": {"soon"}}, now).IsZero())
	assert.True(t, rateLimitReset(http.Header{}, now).IsZero())
}

// closeBody closes a response the code under test handed back, when it did.
func closeBody(resp *http.Response) {
	if resp != nil {
		_ = resp.Body.Close()
	}
}

// githubStub answers the requests the check makes to GitHub and records the
// Authorization and URL of each.
type githubStub struct {
	auth, urls []string
	answer     func(auth string) *http.Response
}

func (g *githubStub) client() *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) *http.Response {
		g.auth = append(g.auth, r.Header.Get("Authorization"))
		g.urls = append(g.urls, r.URL.String())
		return g.answer(r.Header.Get("Authorization"))
	})}
}

// With a token the request to GitHub is authenticated, and it addresses the
// fixed GitHub endpoint whatever the environment says about endpoints.
func TestAuthenticatedRelease_SendsTheTokenToGitHubOnly(t *testing.T) {
	noTokens(t)
	gh := &githubStub{answer: func(string) *http.Response { return answer(http.StatusOK, `{}`) }}

	resp, ok, err := authenticatedRelease(context.Background(), gh.client())
	closeBody(resp)
	require.NoError(t, err)
	assert.False(t, ok, "no token: the caller asks anonymously")
	assert.Nil(t, resp)
	assert.Empty(t, gh.urls, "and nothing was sent")

	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("CHATCLI_LATEST_VERSION_URL", "https://mirror.example/latest")
	resp, ok, err = authenticatedRelease(context.Background(), gh.client())
	closeBody(resp)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []string{"Bearer tok"}, gh.auth)
	assert.Equal(t, []string{LatestVersionURL}, gh.urls, "the token has one destination")
}

// A token GitHub rejects must not break a check that needs none.
func TestAuthenticatedRelease_RejectedTokenFallsBack(t *testing.T) {
	noTokens(t)
	t.Setenv("GITHUB_TOKEN", "expired")
	gh := &githubStub{answer: func(string) *http.Response {
		return answer(http.StatusUnauthorized, `{"message":"Bad credentials"}`)
	}}
	resp, ok, err := authenticatedRelease(context.Background(), gh.client())
	closeBody(resp)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, resp)
	assert.Equal(t, []string{"Bearer expired"}, gh.auth)
}

// A rate-limited answer to the authenticated request is the account's.
func TestStatusErrorFrom_Authenticated(t *testing.T) {
	limited := answer(http.StatusForbidden, "")
	defer func() { _ = limited.Body.Close() }()
	limited.Header.Set("X-RateLimit-Remaining", "0")
	e := statusErrorFrom(limited, true)
	assert.True(t, e.RateLimited)
	assert.True(t, e.Authenticated)

	missing := answer(http.StatusNotFound, "")
	defer func() { _ = missing.Body.Close() }()
	assert.False(t, statusErrorFrom(missing, true).Authenticated, "only a rate limit is qualified")
}

// A release check endpoint set in the environment is asked without the
// token, even with one present: the real fetch against a server standing
// in for a mirror never sees an Authorization header.
func TestFetchLatestRelease_ConfiguredEndpointNeverGetsTheToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "must-not-leave")
	t.Setenv("GITHUB_TOKEN", "must-not-leave-either")
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	rel, err := FetchLatestReleaseImpl(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v9.9.9", rel.TagName)
	assert.Equal(t, []string{""}, auth)
}

// The real fetch against a server that answers like a spent allowance: the
// error says it is the rate limit and when it lifts, and a plain 403 stays
// a plain 403.
func TestFetchLatestRelease_RateLimitIsTyped(t *testing.T) {
	noTokens(t)
	reset := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	limited := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if limited {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", server.URL)

	_, err := FetchLatestReleaseImpl(context.Background())
	var status *ReleaseStatusError
	require.True(t, errors.As(err, &status))
	assert.True(t, status.RateLimited)
	assert.False(t, status.Authenticated)
	assert.True(t, status.ResetAt.Equal(reset), "reset %v, want %v", status.ResetAt, reset)
	assert.Contains(t, err.Error(), "status 403")
	assert.Contains(t, err.Error(), reset.Local().Format("15:04"))

	limited = false
	_, err = FetchLatestReleaseImpl(context.Background())
	require.True(t, errors.As(err, &status))
	assert.False(t, status.RateLimited)
	assert.Equal(t, "erro ao verificar versão: status 403", err.Error(), "the message callers already match on is unchanged")
}

func TestReleaseStatusError_RateLimitWithoutReset(t *testing.T) {
	e := &ReleaseStatusError{StatusCode: 429, RateLimited: true}
	assert.Equal(t, "erro ao verificar versão: status 429 (limite de requisições da API do GitHub)", e.Error())
}

// The whole default path, with the built-in endpoint pointed at a local
// server: the token goes with the first request, and when it is refused the
// check still answers, from a second request without it.
func TestFetchLatestRelease_DefaultPathAuthenticatesAndFallsBack(t *testing.T) {
	t.Setenv("CHATCLI_LATEST_VERSION_URL", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "tok")
	reject := false
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		auth = append(auth, a)
		if reject && a != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	}))
	defer server.Close()
	orig := LatestVersionURL
	LatestVersionURL = server.URL
	defer func() { LatestVersionURL = orig }()

	rel, err := FetchLatestReleaseImpl(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", rel.TagName)
	assert.Equal(t, []string{"Bearer tok"}, auth, "one authenticated request")

	reject, auth = true, nil
	rel, err = FetchLatestReleaseImpl(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v2.0.0", rel.TagName)
	assert.Equal(t, []string{"Bearer tok", ""}, auth, "the refused token is dropped and the check asks again")
}
