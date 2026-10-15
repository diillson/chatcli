/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package webui

import (
	"encoding/base64"
	"testing"
)

// The page sends PDFs in the attachment list; the server routes them to
// Documents and rejects a "PDF" that is not one.
func TestTurnOptionsFrom_PDF(t *testing.T) {
	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\n1 0 obj"))
	req := turnRequest{}
	req.Images = append(req.Images, struct {
		Name      string `json:"name"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}{Name: "q3.pdf", MediaType: "application/pdf", Data: pdf})
	o, err := turnOptionsFrom(req)
	if err != nil || len(o.Documents) != 1 || len(o.Images) != 0 || o.Documents[0].Name != "q3.pdf" {
		t.Fatalf("o=%+v err=%v", o, err)
	}
	req.Images[0].Data = base64.StdEncoding.EncodeToString([]byte("not a pdf"))
	if _, err := turnOptionsFrom(req); err == nil {
		t.Fatal("a fake PDF must be rejected")
	}
}
