/*
 * ChatCLI - tests for the OpenAI-compatible transcription provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package transcription

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestBuildRequest_Multipart(t *testing.T) {
	p, err := NewOpenAICompatible("https://api.example.com/v1", "sk-key", "whisper-1", "openai", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	req, err := p.buildRequest(context.Background(), []byte("RIFFfake"), "audio/ogg", "", "pt")
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if !strings.HasSuffix(req.URL.String(), "/v1/audio/transcriptions") {
		t.Errorf("URL = %s, want .../v1/audio/transcriptions", req.URL.String())
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-key" {
		t.Errorf("Authorization = %q, want Bearer sk-key", got)
	}

	mediaType, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("Content-Type = %q (%v)", req.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(req.Body, params["boundary"])
	fields := map[string]string{}
	var fileName string
	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			t.Fatal(perr)
		}
		data, _ := io.ReadAll(part)
		if part.FormName() == "file" {
			fileName = part.FileName()
		} else {
			fields[part.FormName()] = string(data)
		}
	}
	if fields["model"] != "whisper-1" {
		t.Errorf("model field = %q", fields["model"])
	}
	if fields["language"] != "pt" {
		t.Errorf("language field = %q", fields["language"])
	}
	if fields["response_format"] != "json" {
		t.Errorf("response_format = %q", fields["response_format"])
	}
	if !strings.HasSuffix(fileName, ".ogg") { // derived from audio/ogg
		t.Errorf("file name = %q, want *.ogg", fileName)
	}
}

func TestBuildRequest_NoKeyOmitsAuth(t *testing.T) {
	p, _ := NewOpenAICompatible("http://localhost:9000/v1", "", "", "selfhosted", zap.NewNop())
	req, err := p.buildRequest(context.Background(), []byte("x"), "", "voice.ogg", "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("keyless self-hosted must not send Authorization")
	}
}

func TestParseTranscript(t *testing.T) {
	cases := []struct {
		name, ctype, body, want string
		wantErr                 bool
	}{
		{"json", "application/json", `{"text":"  hello world "}`, "hello world", false},
		{"json by sniff", "", `{"text":"hi"}`, "hi", false},
		{"plain text", "text/plain", "  raw transcript ", "raw transcript", false},
		{"error json", "application/json", `{"error":{"message":"bad model"}}`, "", true},
		{"empty", "application/json", "   ", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseTranscript(c.ctype, []byte(c.body))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestTranscribe_EndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/audio/transcriptions") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"transcribed text"}`)
	}))
	defer srv.Close()

	p, err := NewOpenAICompatible(srv.URL, "", "whisper-1", "selfhosted", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Transcribe(context.Background(), []byte("audio-bytes"), "audio/ogg", "v.ogg", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "transcribed text" {
		t.Errorf("got %q", out)
	}
}

func TestTranscribe_EmptyAudio(t *testing.T) {
	p, _ := NewOpenAICompatible("http://x/v1", "", "", "selfhosted", zap.NewNop())
	if _, err := p.Transcribe(context.Background(), nil, "", "", ""); err == nil {
		t.Error("empty audio must error before any network call")
	}
}

func TestNewOpenAICompatible_BadURL(t *testing.T) {
	if _, err := NewOpenAICompatible("ftp://nope", "", "", "", zap.NewNop()); err == nil {
		t.Error("non-http base URL must be rejected")
	}
	if _, err := NewOpenAICompatible("", "", "", "", zap.NewNop()); err == nil {
		t.Error("empty base URL must be rejected")
	}
}
