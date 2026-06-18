/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Shared media download for the channel adapters. Each platform resolves a
 * voice/audio message to a URL (directly or after an API lookup) and then
 * fetches the bytes through fetchAudioBytes, which enforces a single,
 * configurable size cap so a hostile or accidental large upload can't exhaust
 * memory.
 */
package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// defaultMaxAudioBytes bounds a downloaded voice clip. 20MB comfortably covers
// several minutes of Opus/MP3 voice while refusing pathological uploads.
const defaultMaxAudioBytes int64 = 20 << 20

// maxAudioBytes returns the configured audio size cap. CHATCLI_GATEWAY_MAX_AUDIO_BYTES
// accepts a plain byte count; non-positive or unparseable values keep the default.
func maxAudioBytes() int64 {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_GATEWAY_MAX_AUDIO_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxAudioBytes
}

// fetchAudioBytes GETs url (optionally bearer-authenticated) and returns the
// body and its Content-Type, refusing anything larger than limit. It reads at
// most limit+1 bytes so an oversized payload is detected without buffering it all.
func fetchAudioBytes(ctx context.Context, client *http.Client, url, bearer string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", fmt.Errorf("audio exceeds %d-byte limit", limit)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// isAudioMime reports whether a MIME type denotes audio. Used by adapters whose
// attachments are heterogeneous (Discord, Slack) to pick voice/audio parts.
func isAudioMime(mime string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mime)), "audio/")
}

// defaultMaxImageBytes bounds a downloaded image attachment. Vision providers
// reject very large images; 20MB covers any realistic photo.
const defaultMaxImageBytes int64 = 20 << 20

// maxImageBytes returns the configured image size cap.
// CHATCLI_GATEWAY_MAX_IMAGE_BYTES accepts a plain byte count; non-positive or
// unparseable values keep the default.
func maxImageBytes() int64 {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_GATEWAY_MAX_IMAGE_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxImageBytes
}

// isImageMime reports whether a MIME type denotes a supported image. Used by
// adapters whose attachments are heterogeneous (Discord, Slack) to pick image
// parts, and to gate which inbound files become vision attachments.
func isImageMime(mime string) bool {
	m := strings.ToLower(strings.TrimSpace(mime))
	switch {
	case strings.HasPrefix(m, "image/jpeg"), strings.HasPrefix(m, "image/jpg"),
		strings.HasPrefix(m, "image/png"), strings.HasPrefix(m, "image/gif"),
		strings.HasPrefix(m, "image/webp"):
		return true
	}
	return false
}
