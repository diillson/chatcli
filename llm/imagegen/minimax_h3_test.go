/*
 * ChatCLI - MiniMax H3 Image backend tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package imagegen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// h3Server answers the H3 endpoint with reply and records the request.
func h3Server(t *testing.T, status int, reply interface{}, got *map[string]interface{}, calls *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.URL.Path != "/images/generations" {
			t.Errorf("path = %q, want the H3 endpoint", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer k" {
			t.Errorf("auth = %q", auth)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, got)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMiniMaxH3_Generate(t *testing.T) {
	var got map[string]interface{}
	var calls int32
	srv := h3Server(t, http.StatusOK, map[string]interface{}{
		"created": 1,
		"data": []map[string]interface{}{
			{"b64_json": tinyPNGB64, "size": "2720x1536", "revised_prompt": "a cat"},
			{"error": map[string]interface{}{"code": 1027, "message": "blocked by the safety check"}},
		},
		"output_format": "png",
		"usage":         map[string]interface{}{"generated_images": 1},
	}, &got, &calls)

	// The factory routes on a lower-cased name; the endpoint wants its own
	// spelling.
	m, err := NewMiniMax(srv.URL, "k", "minimax-h3-image", nil)
	if err != nil {
		t.Fatal(err)
	}
	imgs, err := m.Generate(context.Background(), "a cat", Options{Size: "2720x1536", N: 2})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// One image succeeded and one was blocked: the good one is returned.
	if len(imgs) != 1 || imgs[0].Ext != "png" || imgs[0].Mime != "image/png" || len(imgs[0].Data) == 0 {
		t.Fatalf("imgs = %+v", imgs)
	}
	for field, want := range map[string]interface{}{
		"model": "MiniMax-H3-Image", "prompt": "a cat", "size": "2720x1536",
		"response_format": "b64_json", "output_format": "png", "n": float64(2),
	} {
		if got[field] != want {
			t.Errorf("%s = %v, want %v", field, got[field], want)
		}
	}
	// An explicit size may not be combined with aspect_ratio or area.
	for _, field := range []string{"aspect_ratio", "area"} {
		if _, sent := got[field]; sent {
			t.Errorf("%s must not travel with an explicit size", field)
		}
	}
	if calls != 1 {
		t.Errorf("the request was sent %d times; the endpoint bills every one", calls)
	}
}

func TestMiniMaxH3_Payload(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		opts        Options
		want        map[string]interface{}
		absent      []string
	}{
		{"default size", "MiniMax-H3-Image", Options{},
			map[string]interface{}{"model": "MiniMax-H3-Image", "size": "1024x1024", "n": 1, "output_format": "png"},
			[]string{"aspect_ratio", "area"}},
		{"highspeed keeps its id", "MiniMax-H3-Image-Highspeed", Options{Size: "1536x1024"},
			map[string]interface{}{"model": "MiniMax-H3-Image-Highspeed", "size": "1536x1024"}, nil},
		{"lower-cased highspeed", "minimax-h3-image-highspeed", Options{},
			map[string]interface{}{"model": "MiniMax-H3-Image-Highspeed"}, nil},
		// Below 655,360 pixels the endpoint refuses the size: nearest ratio at 1K.
		{"too small", "MiniMax-H3-Image", Options{Size: "512x512"},
			map[string]interface{}{"aspect_ratio": "1:1", "area": "1K"}, []string{"size"}},
		{"too small, wide", "MiniMax-H3-Image", Options{Size: "640x360"},
			map[string]interface{}{"aspect_ratio": "16:9", "area": "1K"}, []string{"size"}},
		// Beyond 3:1 the endpoint refuses the ratio.
		{"too wide", "MiniMax-H3-Image", Options{Size: "4096x1024"},
			map[string]interface{}{"aspect_ratio": "21:9", "area": "1K"}, []string{"size"}},
		{"above 20M pixels", "MiniMax-H3-Image", Options{Size: "6000x4000"},
			map[string]interface{}{"aspect_ratio": "3:2", "area": "1K"}, []string{"size"}},
		{"largest accepted", "MiniMax-H3-Image", Options{Size: "5000x4000"},
			map[string]interface{}{"size": "5000x4000", "output_format": "png"}, nil},
		// n x width x height above 20M pixels cannot come back as PNG.
		{"many large images", "MiniMax-H3-Image", Options{Size: "4096x4096", N: 2},
			map[string]interface{}{"size": "4096x4096", "n": 2, "output_format": "jpeg"}, nil},
	} {
		payload, format := minimaxH3Payload(tc.model, "p", tc.opts)
		for field, want := range tc.want {
			if payload[field] != want {
				t.Errorf("%s: %s = %v, want %v", tc.name, field, payload[field], want)
			}
		}
		for _, field := range tc.absent {
			if _, sent := payload[field]; sent {
				t.Errorf("%s: %s must not be sent (%v)", tc.name, field, payload[field])
			}
		}
		if payload["output_format"] != format {
			t.Errorf("%s: returned format %q differs from the payload's %v", tc.name, format, payload["output_format"])
		}
		if payload["response_format"] != "b64_json" {
			t.Errorf("%s: response_format = %v", tc.name, payload["response_format"])
		}
	}
}

// The endpoint's error envelope reaches the user as its type and message.
func TestMiniMaxH3_ErrorEnvelope(t *testing.T) {
	var got map[string]interface{}
	var calls int32
	srv := h3Server(t, http.StatusPaymentRequired, map[string]interface{}{
		"type":       "error",
		"error":      map[string]interface{}{"type": "insufficient_balance_error", "message": "insufficient balance (1008)", "http_code": "402"},
		"request_id": "r",
	}, &got, &calls)
	m, _ := NewMiniMax(srv.URL, "k", "MiniMax-H3-Image", nil)
	_, err := m.Generate(context.Background(), "a cat", Options{})
	if err == nil || !strings.Contains(err.Error(), "402") || !strings.Contains(err.Error(), "insufficient_balance_error: insufficient balance (1008)") {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("a failed request was retried: %d calls", calls)
	}
}

// When every image fails inside a 200, the first per-image error is the
// error of the call.
func TestMiniMaxH3_AllImagesFailed(t *testing.T) {
	var got map[string]interface{}
	var calls int32
	srv := h3Server(t, http.StatusOK, map[string]interface{}{
		"data": []map[string]interface{}{{"error": map[string]interface{}{"code": 1001, "message": "timed out"}}},
	}, &got, &calls)
	m, _ := NewMiniMax(srv.URL, "k", "MiniMax-H3-Image", nil)
	_, err := m.Generate(context.Background(), "a cat", Options{})
	if err == nil || !strings.Contains(err.Error(), "timed out (code 1001)") {
		t.Fatalf("err = %v", err)
	}
}

// Image-01 keeps its own endpoint and body.
func TestMiniMaxH3_ImageOneUnchanged(t *testing.T) {
	if isMiniMaxH3Image("image-01") || isMiniMaxH3Image("MiniMax-H3") {
		t.Fatal("only the H3 image models use the H3 endpoint")
	}
	if !isMiniMaxH3Image("MiniMax-H3-Image") || !isMiniMaxH3Image(" minimax-h3-image-highspeed ") {
		t.Fatal("both H3 image models use the H3 endpoint")
	}
	for format, ext := range map[string]string{"png": "png", "jpeg": "jpg", "jpg": "jpg", "webp": "webp", "": "png"} {
		if _, got := minimaxH3Format(format); got != ext {
			t.Errorf("format %q ext = %q, want %q", format, got, ext)
		}
	}
}
