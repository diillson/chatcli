/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestIsMostlyText(t *testing.T) {
	if !isMostlyText([]byte("{\"ok\":true}\n")) {
		t.Fatal("json is text")
	}
	if isMostlyText([]byte("\x89PNG\r\n\x1a\n\x00\x00")) {
		t.Fatal("png is binary")
	}
}

func TestDownloadEventsAndRing(t *testing.T) {
	s := &Session{requests: map[string]NetworkEntry{}}
	s.handleEvent("Browser.downloadWillBegin", json.RawMessage(`{"guid":"g1","url":"https://x/report.pdf","suggestedFilename":"report.pdf"}`), "")
	s.handleEvent("Browser.downloadProgress", json.RawMessage(`{"guid":"g1","state":"inProgress","receivedBytes":10,"totalBytes":100}`), "")
	s.handleEvent("Browser.downloadProgress", json.RawMessage(`{"guid":"g1","state":"completed","receivedBytes":100,"totalBytes":100,"filePath":"/dl/report.pdf"}`), "")
	s.handleEvent("Browser.downloadProgress", json.RawMessage(`{"guid":"unknown","state":"completed"}`), "")
	d := s.Downloads()
	if len(d) != 1 || d[0].State != "completed" || d[0].File != "/dl/report.pdf" || d[0].Received != 100 || d[0].URL != "https://x/report.pdf" {
		t.Fatalf("downloads = %+v", d)
	}
	for i := 0; i < maxDownloads+5; i++ {
		s.handleEvent("Browser.downloadWillBegin", json.RawMessage(`{"guid":"n`+string(rune('a'+i%26))+string(rune('a'+i/26))+`","url":"https://x/f","suggestedFilename":"f"}`), "")
	}
	if len(s.Downloads()) != maxDownloads {
		t.Fatalf("ring not bounded: %d", len(s.Downloads()))
	}
}

func TestResponseBody(t *testing.T) {
	s, _ := bodyFakeSession(t)
	ctx := context.Background()
	s.mu.Lock()
	s.appendNetworkLocked(NetworkEntry{Method: "GET", URL: "https://x/api/items", Status: 200, Type: "Fetch"}, "ok1")
	s.appendNetworkLocked(NetworkEntry{Method: "GET", URL: "https://x/old", Status: 200, Type: "Fetch"}, "gone")
	s.appendNetworkLocked(NetworkEntry{Method: "GET", URL: "https://x/logo.png", Status: 200, Type: "Image"}, "bin")
	s.appendNetworkLocked(NetworkEntry{Method: "GET", URL: "https://x/failed", Status: 0, Type: "failed"}, "")
	s.mu.Unlock()

	entry, body, err := s.ResponseBody(ctx, "/api/", 0)
	if err != nil || entry.URL != "https://x/api/items" || !strings.Contains(body, `"items":[1,2,3]`) {
		t.Fatalf("body: %+v %q %v", entry, body, err)
	}
	if _, body, err := s.ResponseBody(ctx, "/api/", 8); err != nil || !strings.Contains(body, "truncated") {
		t.Fatalf("cap: %q %v", body, err)
	}
	if _, _, err := s.ResponseBody(ctx, "/old", 0); err == nil || !strings.Contains(err.Error(), "no longer available") {
		t.Fatalf("gone body: %v", err)
	}
	if _, body, err := s.ResponseBody(ctx, "logo", 0); err != nil || !strings.Contains(body, "binary body") {
		t.Fatalf("binary: %q %v", body, err)
	}
	if _, _, err := s.ResponseBody(ctx, "nothing-like-this", 0); err == nil {
		t.Fatal("no match must error")
	}
	// The newest entry with a body wins when no filter is given; a failed
	// request (no id) is skipped.
	if entry, _, err := s.ResponseBody(ctx, "", 0); err != nil || entry.URL != "https://x/logo.png" {
		t.Fatalf("newest: %+v %v", entry, err)
	}
}
