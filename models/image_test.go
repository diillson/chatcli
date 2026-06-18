/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package models

import (
	"strings"
	"testing"
)

// 1x1 PNG.
var onePxPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func TestNormalizeImageMediaType(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOk bool
	}{
		{"image/png", "image/png", true},
		{"IMAGE/PNG", "image/png", true},
		{"image/jpeg; charset=binary", "image/jpeg", true},
		{"image/jpg", "image/jpeg", true}, // alias fold
		{"image/webp", "image/webp", true},
		{"image/gif", "image/gif", true},
		{"application/pdf", "application/pdf", false},
		{"text/plain", "text/plain", false},
	}
	for _, c := range cases {
		got, ok := NormalizeImageMediaType(c.in)
		if got != c.want || ok != c.wantOk {
			t.Errorf("NormalizeImageMediaType(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.wantOk)
		}
	}
}

func TestDetectImageMediaType(t *testing.T) {
	if mt, ok := DetectImageMediaType(onePxPNG); !ok || mt != "image/png" {
		t.Errorf("PNG detect = (%q,%v), want image/png,true", mt, ok)
	}
	if _, ok := DetectImageMediaType([]byte("not an image at all")); ok {
		t.Error("plain text must not detect as image")
	}
	if _, ok := DetectImageMediaType(nil); ok {
		t.Error("empty must not detect")
	}
}

func TestSupportedImageMediaTypes(t *testing.T) {
	got := SupportedImageMediaTypes()
	if len(got) != 4 {
		t.Fatalf("expected 4 supported types, got %d: %v", len(got), got)
	}
	joined := strings.Join(got, ",")
	for _, want := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

func TestImageContentIsValid(t *testing.T) {
	if !(ImageContent{MediaType: "image/png", Data: onePxPNG}).IsValid() {
		t.Error("png with data should be valid")
	}
	if !(ImageContent{MediaType: "image/jpeg", URL: "http://x/y.jpg"}).IsValid() {
		t.Error("jpeg with URL should be valid")
	}
	if (ImageContent{MediaType: "image/png"}).IsValid() {
		t.Error("no data and no URL must be invalid")
	}
	if (ImageContent{MediaType: "application/pdf", Data: []byte("x")}).IsValid() {
		t.Error("unsupported media type must be invalid")
	}
}

func TestEstimateImageTokens(t *testing.T) {
	// Decodable PNG → at least the floor.
	if got := EstimateImageTokens(ImageContent{MediaType: "image/png", Data: onePxPNG}); got < 1 {
		t.Errorf("decodable image tokens = %d, want >= 1", got)
	}
	// Undecodable bytes → falls back to a size-based floor.
	big := make([]byte, 8192)
	if got := EstimateImageTokens(ImageContent{MediaType: "image/webp", Data: big}); got < 1 {
		t.Errorf("undecodable image tokens = %d, want >= 1", got)
	}
	// URL-only → conservative floor.
	if got := EstimateImageTokens(ImageContent{MediaType: "image/png", URL: "http://x/y.png"}); got < 1 {
		t.Errorf("url-only tokens = %d, want >= 1", got)
	}
}

func TestMessageIsValidWithImage(t *testing.T) {
	// A user turn carrying only an image (no text) is valid.
	m := Message{Role: "user", Images: []ImageContent{{MediaType: "image/png", Data: onePxPNG}}}
	if !m.IsValid() {
		t.Error("image-only user message should be valid")
	}
}
