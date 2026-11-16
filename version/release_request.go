/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * release_request.go
 *
 * The release check against the GitHub API. Without a token GitHub allows
 * 60 requests an hour per IP address, shared by everything behind that
 * address, and answers 403 once they are spent. Two things follow:
 *
 *   - the answer says it is the rate limit and when it lifts, so the check
 *     reports that instead of a bare 403 and nobody retries into the limit;
 *   - a GitHub token already in the environment (GH_TOKEN, GITHUB_TOKEN)
 *     raises the allowance to the account's, so it is sent, to the GitHub
 *     API only. A token GitHub rejects never breaks the check: the request
 *     is made again without it.
 */
package version

import (
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// githubAPIHost is the only host the environment's token is ever sent to.
const githubAPIHost = "api.github.com"

// releaseToken is the GitHub token to authenticate the check with, "" when
// there is none or the check goes somewhere else (CHATCLI_LATEST_VERSION_URL
// pointing at a mirror or a test server). GH_TOKEN wins, as it does for gh.
func releaseToken(target string) string {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), githubAPIHost) {
		return ""
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// rateLimitReset reads when a rate-limited answer says requests are accepted
// again: Retry-After (seconds) for the secondary limits, X-RateLimit-Reset
// (Unix time) for the hourly one. Zero when the answer does not say.
func rateLimitReset(h http.Header, now time.Time) time.Time {
	if secs, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && secs > 0 {
		return now.Add(time.Duration(secs) * time.Second)
	}
	if epoch, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && epoch > 0 {
		return time.Unix(epoch, 0)
	}
	return time.Time{}
}

// rateLimited reports whether a refusal is GitHub's rate limit rather than
// a proxy's 403: the allowance is spent, or the answer asks to come back
// later.
func rateLimited(status int, h http.Header) bool {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false
	}
	return strings.TrimSpace(h.Get("X-RateLimit-Remaining")) == "0" || h.Get("Retry-After") != ""
}

// statusErrorFrom builds the check's error for an answer other than 200.
func statusErrorFrom(resp *http.Response, authenticated bool) *ReleaseStatusError {
	e := &ReleaseStatusError{StatusCode: resp.StatusCode}
	if rateLimited(resp.StatusCode, resp.Header) {
		e.RateLimited = true
		e.ResetAt = rateLimitReset(resp.Header, time.Now())
		e.Authenticated = authenticated
	}
	return e
}

// doReleaseRequest sends the check, with the environment's token when there
// is one for this target. A 401 means GitHub does not accept that token
// (expired, revoked, issued by another GitHub): the check does not need
// one, so it is made again without it.
func doReleaseRequest(client *http.Client, req *http.Request) (*http.Response, bool, error) {
	token := releaseToken(req.URL.String())
	if token == "" {
		resp, err := client.Do(req)
		return resp, false, err
	}
	authed := req.Clone(req.Context())
	authed.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(authed)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, true, err
	}
	_ = resp.Body.Close()
	resp, err = client.Do(req)
	return resp, false, err
}
