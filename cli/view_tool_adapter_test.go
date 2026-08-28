/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * view_tool_adapter_test.go
 *
 * @view adapter: image loading/rejection through the vision pipeline and the
 * agent-loop staging drained at the turn boundary.
 */
package cli

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// tinyPNG is a valid 1x1 transparent PNG.
var tinyPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestStageAndDrainViewedImages(t *testing.T) {
	a := &AgentMode{}
	img := models.ImageContent{MediaType: "image/png", Data: tinyPNG, FileName: "s.png"}
	a.stageViewedImage(img, "s.png")
	a.stageViewedImage(img, "t.png")

	imgs, names := a.drainViewedImages()
	if len(imgs) != 2 || len(names) != 2 || names[1] != "t.png" {
		t.Fatalf("stage/drain mismatch: %d imgs, names %v", len(imgs), names)
	}
	if imgs, names := a.drainViewedImages(); len(imgs) != 0 || len(names) != 0 {
		t.Fatal("drain must clear the staging area")
	}
}

func TestViewImage_RejectsPDFAndNonImage(t *testing.T) {
	c := minimalCLI(t)
	adapter := &viewToolAdapter{cli: c}

	if _, err := adapter.ViewImage(context.Background(), "doc.pdf"); err == nil || !strings.Contains(err.Error(), "PDF") {
		t.Fatalf("pdf must be rejected with guidance, got %v", err)
	}

	txt := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(txt, []byte("just text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ViewImage(context.Background(), txt); err == nil {
		t.Fatal("non-image file must be rejected")
	}
	if _, err := adapter.ViewImage(context.Background(), filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Fatal("missing file must be rejected")
	}
}

func TestViewImage_StagesOnAgentLoop(t *testing.T) {
	c := minimalCLI(t)
	// Force the native-vision path so gateImagesForModel passes the image
	// through regardless of the fixture's provider/model.
	t.Setenv("CHATCLI_VISION_INPUT", "native")
	c.agentMode = NewAgentMode(c, c.logger)
	adapter := &viewToolAdapter{cli: c}

	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(shot, tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := adapter.ViewImage(context.Background(), shot)
	if err != nil {
		t.Fatalf("ViewImage: %v", err)
	}
	if !strings.Contains(out, "staged") {
		t.Fatalf("expected staging confirmation, got %q", out)
	}
	imgs, names := c.agentMode.drainViewedImages()
	if len(imgs) != 1 || imgs[0].MediaType != "image/png" || len(names) != 1 {
		t.Fatalf("image not staged: %d imgs, names %v", len(imgs), names)
	}
}

func TestViewImage_NoAgentLoop(t *testing.T) {
	c := minimalCLI(t)
	t.Setenv("CHATCLI_VISION_INPUT", "native")
	c.agentMode = nil
	adapter := &viewToolAdapter{cli: c}

	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(shot, tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ViewImage(context.Background(), shot); err == nil || !strings.Contains(err.Error(), "agent loop") {
		t.Fatalf("no agent loop must error with guidance, got %v", err)
	}
}
