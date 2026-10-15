/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

// Every channel now keeps a PDF instead of dropping the message.
func TestParsers_KeepPDFDocuments(t *testing.T) {
	tg := `{"ok":true,"result":[{"update_id":7,"message":{"chat":{"id":1},"from":{"id":2,"username":"u"},
		"caption":"resume","document":{"file_id":"F1","mime_type":"application/pdf","file_name":"q3.pdf"}}}]}`
	msgs, _, err := parseTelegramUpdates([]byte(tg))
	if err != nil || len(msgs) != 1 || msgs[0].Document == nil || msgs[0].Document.ref != "F1" || msgs[0].Text != "resume" {
		t.Fatalf("telegram: %+v %v", msgs, err)
	}

	dc := json.RawMessage(`{"channel_id":"c","author":{"id":"a","username":"u"},"content":"",
		"attachments":[{"url":"https://cdn.example/q3.pdf","content_type":"application/pdf","filename":"q3.pdf"}]}`)
	dm, ok := parseDiscordMessage(dc)
	if !ok || dm.Document == nil || dm.Document.FileName != "q3.pdf" {
		t.Fatalf("discord: %+v %v", dm, ok)
	}

	sl := `{"type":"event_callback","event":{"type":"message","channel":"C","user":"U","text":"","subtype":"file_share",
		"files":[{"url_private":"https://files.example/q3.pdf","mimetype":"application/pdf","name":"q3.pdf"}]}}`
	_, sm, has, err := parseSlackEvent([]byte(sl))
	if err != nil || !has || sm.Document == nil {
		t.Fatalf("slack: %+v %v %v", sm, has, err)
	}

	wa := `{"entry":[{"changes":[{"value":{"messages":[{"from":"551199","type":"document",
		"document":{"id":"M1","mime_type":"application/pdf","filename":"contrato.pdf","caption":"revise"}}]}}]}]}`
	wm := parseWhatsAppInbound([]byte(wa))
	if len(wm) != 1 || wm[0].Document == nil || wm[0].Document.ref != "M1" || wm[0].Text != "revise" {
		t.Fatalf("whatsapp: %+v", wm)
	}
	// A WhatsApp document that is not a PDF is still ignored.
	waDoc := `{"entry":[{"changes":[{"value":{"messages":[{"from":"1","type":"document","document":{"id":"M2","mime_type":"application/zip","filename":"a.zip"}}]}}]}]}`
	if got := parseWhatsAppInbound([]byte(waDoc)); len(got) != 0 {
		t.Fatalf("non-PDF documents stay out: %+v", got)
	}

	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\n1 0 obj\n"))
	wh, code := parseWebhookRequest([]byte(`{"chat_id":"c","document_b64":"` + pdf + `","document_name":"a.pdf"}`))
	if code != "" || wh.Document == nil || len(wh.Document.Data) == 0 {
		t.Fatalf("webhook: %+v %q", wh, code)
	}
	if _, code := parseWebhookRequest([]byte(`{"chat_id":"c","document_b64":"` + base64.StdEncoding.EncodeToString([]byte("zip")) + `"}`)); code != whErrDocumentB64 {
		t.Fatalf("webhook non-PDF code = %q", code)
	}
}

func TestHydrateDocument(t *testing.T) {
	msg := InboundMessage{UserID: "u", Document: &InboundDocument{ref: "r"}}
	hydrateDocument(context.Background(), &msg, func(context.Context, string, int64) ([]byte, string, error) {
		return []byte("%PDF-1.7 body"), "application/pdf", nil
	}, nil, "test")
	if msg.Document == nil || string(msg.Document.Data) != "%PDF-1.7 body" || msg.Document.MimeType != "application/pdf" {
		t.Fatalf("hydrated = %+v", msg.Document)
	}
	bad := InboundMessage{Document: &InboundDocument{ref: "r"}}
	hydrateDocument(context.Background(), &bad, func(context.Context, string, int64) ([]byte, string, error) {
		return []byte("<html>login</html>"), "text/html", nil
	}, nil, "test")
	if bad.Document != nil {
		t.Fatal("a download that is not a PDF is dropped")
	}
	failed := InboundMessage{Document: &InboundDocument{ref: "r"}}
	hydrateDocument(context.Background(), &failed, func(context.Context, string, int64) ([]byte, string, error) {
		return nil, "", errors.New("boom")
	}, nil, "test")
	if failed.Document != nil {
		t.Fatal("a failed download clears the document")
	}
	if !isPDFAttachment("", "x.PDF") || isPDFAttachment("image/png", "x.pdf") || !isPDFAttachment("application/pdf", "") {
		t.Fatal("isPDFAttachment")
	}
}
