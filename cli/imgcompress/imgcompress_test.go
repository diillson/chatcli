/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package imgcompress

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// photoPNG builds a w×h PNG with high-frequency, photo-like content
// (deterministic value noise) so PNG compresses poorly and JPEG re-encoding
// shows a real win — like a real photograph.
func photoPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Deterministic LCG so the test is stable (no time/rand seeding).
	var s uint32 = 0x12345678
	next := func() uint8 {
		s = s*1664525 + 1013904223
		return uint8(s >> 24)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Smooth base + noise: structure JPEG keeps, noise PNG can't pack.
			base := uint8(((x + y) * 255) / (w + h))
			img.Set(x, y, color.RGBA{
				R: base/2 + next()/2,
				G: base/2 + next()/2,
				B: base/2 + next()/2,
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func alphaPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8((x * 255) / w) // varying transparency
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: a})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestCompressDownscalesAndReencodes(t *testing.T) {
	orig := photoPNG(t, 3000, 2000) // longest edge 3000 > 1568
	out, res := Compress(orig, "image/png", DefaultOptions())
	if !res.Changed {
		t.Fatalf("expected compression, got unchanged: %+v", res)
	}
	if res.NewW != DefaultMaxEdge {
		t.Errorf("longest edge should clamp to %d, got %d", DefaultMaxEdge, res.NewW)
	}
	if res.NewH != 1045 { // 2000 * 1568/3000 = 1045.3 -> 1045
		t.Errorf("height should scale proportionally, got %d", res.NewH)
	}
	if res.OutMediaType != "image/jpeg" {
		t.Errorf("opaque photo should re-encode as JPEG, got %s", res.OutMediaType)
	}
	if len(out) >= len(orig) {
		t.Errorf("expected byte reduction, got %d >= %d", len(out), len(orig))
	}
	// Output must be a valid, decodable image of the new size.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output not decodable: %v", err)
	}
	if cfg.Width != res.NewW || cfg.Height != res.NewH {
		t.Errorf("decoded size %dx%d != reported %dx%d", cfg.Width, cfg.Height, res.NewW, res.NewH)
	}
	if res.SavedBytes() == 0 {
		t.Error("SavedBytes should be positive")
	}
}

func TestCompressPreservesAlphaAsPNG(t *testing.T) {
	orig := alphaPNG(t, 2400, 100) // wide, has transparency
	out, res := Compress(orig, "image/png", DefaultOptions())
	if !res.Changed {
		t.Fatalf("expected resize of oversized alpha PNG: %+v", res)
	}
	if res.OutMediaType != "image/png" {
		t.Fatalf("alpha image must stay PNG to preserve transparency, got %s", res.OutMediaType)
	}
	// Transparency must survive: decode and confirm a non-opaque pixel exists.
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	foundAlpha := false
	bnd := img.Bounds()
	for x := bnd.Min.X; x < bnd.Max.X && !foundAlpha; x++ {
		if _, _, _, a := img.At(x, bnd.Min.Y).RGBA(); a < 0xffff {
			foundAlpha = true
		}
	}
	if !foundAlpha {
		t.Error("transparency was lost")
	}
}

func TestCompressSmallImageNoInflate(t *testing.T) {
	// A small JPEG already efficient: re-encode must not inflate it.
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82})
	orig := buf.Bytes()
	out, res := Compress(orig, "image/jpeg", DefaultOptions())
	if len(out) > len(orig) {
		t.Fatalf("must never inflate: %d > %d", len(out), len(orig))
	}
	if res.NewBytes > res.OrigBytes {
		t.Fatalf("reported NewBytes inflated: %+v", res)
	}
}

func TestCompressLeavesUnsupportedFormats(t *testing.T) {
	// Not an image at all.
	out, res := Compress([]byte("not an image"), "application/octet-stream", DefaultOptions())
	if res.Changed {
		t.Fatal("non-image must be returned unchanged")
	}
	if string(out) != "not an image" {
		t.Fatal("bytes must be untouched")
	}
}

func TestCompressUnderCapStillReencodesPhoto(t *testing.T) {
	// A 1000px PNG photo (under the edge cap): no resize, but JPEG re-encode
	// should still shrink it substantially.
	orig := photoPNG(t, 1000, 800)
	out, res := Compress(orig, "image/png", DefaultOptions())
	if !res.Changed || res.OutMediaType != "image/jpeg" {
		t.Fatalf("under-cap photo should re-encode to JPEG: %+v", res)
	}
	if res.NewW != 1000 || res.NewH != 800 {
		t.Errorf("should not resize under-cap image, got %dx%d", res.NewW, res.NewH)
	}
	if len(out) >= len(orig) {
		t.Errorf("JPEG re-encode should shrink a PNG photo, got %d >= %d", len(out), len(orig))
	}
}
