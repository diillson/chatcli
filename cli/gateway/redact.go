/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"errors"
	"net/http"
	"net/url"
)

// doHTTP sends req and, on failure, strips secrets from the URL the error
// carries. http.Client.Do wraps every failure in a *url.Error whose text
// embeds the full request URL; the Telegram Bot API puts the bot token in
// the path and signed media URLs carry credentials in the query, so a plain
// dial error logged with zap.Error would write them to the log. Every
// adapter request goes through here.
func doHTTP(c *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := c.Do(req) // #nosec G704 -- callers build the URL from configuration or a platform API response, and webhook attachment URLs go through the netguard client
	if err != nil {
		return resp, redactURLError(err)
	}
	return resp, nil
}

// redactURLError replaces the URL inside a *url.Error with its redacted
// form. Other errors are returned unchanged.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = redactURL(ue.URL)
	}
	return err
}

// redactURL keeps scheme, host and the path with any bot token masked, and
// drops user info, query and fragment.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[redacted]"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	u.Path = sanitizeAPIPath(u.Path)
	// Keep the mask readable: "*" is valid in a path, and when the rest of
	// the path needs escaping url falls back to its own encoding.
	u.RawPath = u.Path
	return u.String()
}
