package cli

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

// 1x1 PNG.
var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
	0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
	0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

func testCLI() *ChatCLI {
	return &ChatCLI{logger: zap.NewNop()}
}

func TestLoadImageAttachment_PNG(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(p, testPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	img, ok := testCLI().loadImageAttachment(p)
	if !ok {
		t.Fatal("expected PNG to be recognized as an image attachment")
	}
	if img.MediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png", img.MediaType)
	}
	if len(img.Data) != len(testPNG) {
		t.Fatalf("data len = %d, want %d", len(img.Data), len(testPNG))
	}
	if img.FileName != "pic.png" {
		t.Fatalf("filename = %q, want pic.png", img.FileName)
	}
	if !img.IsValid() {
		t.Fatal("attachment should be valid")
	}
}

func TestLoadImageAttachment_RejectsTextAndDir(t *testing.T) {
	dir := t.TempDir()

	// A non-image file must not be treated as an attachment.
	txt := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txt, []byte("just some text, definitely not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := testCLI().loadImageAttachment(txt); ok {
		t.Fatal("text file must not be detected as an image")
	}

	// A directory must not be an attachment.
	if _, ok := testCLI().loadImageAttachment(dir); ok {
		t.Fatal("directory must not be detected as an image")
	}

	// A missing path must not be an attachment.
	if _, ok := testCLI().loadImageAttachment(filepath.Join(dir, "nope.png")); ok {
		t.Fatal("missing path must not be detected as an image")
	}
}

func TestLoadImageAttachment_ExtensionlessImageSniffed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob") // no extension
	if err := os.WriteFile(p, testPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := testCLI().loadImageAttachment(p); !ok {
		t.Fatal("extensionless image must still be detected by content sniff")
	}
}
