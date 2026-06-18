/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"testing"
)

func TestIsImageMime(t *testing.T) {
	yes := []string{"image/png", "image/jpeg", "image/jpg", "image/gif", "image/webp", "IMAGE/PNG", "image/jpeg; x=y"}
	no := []string{"audio/ogg", "text/plain", "application/pdf", "video/mp4", "", "image/tiff"}
	for _, m := range yes {
		if !isImageMime(m) {
			t.Errorf("%q should be an image mime", m)
		}
	}
	for _, m := range no {
		if isImageMime(m) {
			t.Errorf("%q should NOT be an image mime", m)
		}
	}
}

func TestMaxImageBytes(t *testing.T) {
	t.Setenv("CHATCLI_GATEWAY_MAX_IMAGE_BYTES", "")
	if got := maxImageBytes(); got != defaultMaxImageBytes {
		t.Errorf("default = %d, want %d", got, defaultMaxImageBytes)
	}
	t.Setenv("CHATCLI_GATEWAY_MAX_IMAGE_BYTES", "12345")
	if got := maxImageBytes(); got != 12345 {
		t.Errorf("override = %d, want 12345", got)
	}
	t.Setenv("CHATCLI_GATEWAY_MAX_IMAGE_BYTES", "garbage")
	if got := maxImageBytes(); got != defaultMaxImageBytes {
		t.Errorf("unparseable should keep default, got %d", got)
	}
}

func TestRunnerSetImageProvider(t *testing.T) {
	r := &Runner{}
	want := &OutboundImage{Data: []byte("x"), Mime: "image/png"}
	r.SetImageProvider(func(_ context.Context, session string) *OutboundImage {
		if session == "telegram:1" {
			return want
		}
		return nil
	})
	if r.image == nil {
		t.Fatal("SetImageProvider should install the hook")
	}
	if got := r.image(context.Background(), "telegram:1"); got != want {
		t.Error("installed hook should return the provided image")
	}
}
