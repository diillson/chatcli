/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"testing"

	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/diillson/chatcli/models"
)

func TestBedrockImageBlocks(t *testing.T) {
	imgs := []models.ImageContent{
		{MediaType: "image/png", Data: []byte("p")},
		{MediaType: "image/jpeg", Data: []byte("j")},
		{MediaType: "image/gif", Data: []byte("g")},
		{MediaType: "image/webp", Data: []byte("w")},
		{MediaType: "application/pdf", Data: []byte("x")}, // dropped (unsupported)
		{MediaType: "image/png", URL: "http://x/y.png"},   // dropped (no bytes)
	}
	blocks := bedrockImageBlocks(imgs)
	if len(blocks) != 4 {
		t.Fatalf("expected 4 image blocks (4 valid byte images), got %d", len(blocks))
	}
	wantFmts := map[bedrockruntimetypes.ImageFormat]bool{
		bedrockruntimetypes.ImageFormatPng:  false,
		bedrockruntimetypes.ImageFormatJpeg: false,
		bedrockruntimetypes.ImageFormatGif:  false,
		bedrockruntimetypes.ImageFormatWebp: false,
	}
	for _, b := range blocks {
		img, ok := b.(*bedrockruntimetypes.ContentBlockMemberImage)
		if !ok {
			t.Fatalf("block is not an image member: %T", b)
		}
		wantFmts[img.Value.Format] = true
	}
	for f, seen := range wantFmts {
		if !seen {
			t.Errorf("format %q not emitted", f)
		}
	}

	if bedrockImageBlocks(nil) != nil {
		t.Error("nil images should yield nil blocks")
	}
}

func TestBuildConverseMessages_WithImage(t *testing.T) {
	history := []models.Message{
		{Role: "user", Content: "what is this?", Images: []models.ImageContent{
			{MediaType: "image/png", Data: []byte("imgbytes")},
		}},
	}
	msgs, _ := buildConverseMessages("", history)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Content must carry an image block followed by the text block.
	content := msgs[0].Content
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks (image + text), got %d", len(content))
	}
	if _, ok := content[0].(*bedrockruntimetypes.ContentBlockMemberImage); !ok {
		t.Errorf("first block should be the image, got %T", content[0])
	}
	if _, ok := content[1].(*bedrockruntimetypes.ContentBlockMemberText); !ok {
		t.Errorf("second block should be the text, got %T", content[1])
	}
}
