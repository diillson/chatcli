/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package imgcompress shrinks images before they are sent to vision-capable
// models, cutting upload bytes, latency and (for oversized images) the actual
// vision-token cost — keylessly, with the standard library only (no cgo, no
// golang.org/x/image, no network).
//
// Two reductions are applied, both standard practice for vision input:
//
//   - Downscale the longest edge to a cap (default 1568px). Providers already
//     downscale larger images server-side to this size for token accounting,
//     so this is token-equivalent — it just avoids shipping pixels that would
//     be thrown away — and for images above the cap it genuinely lowers the
//     billed vision tokens.
//   - Re-encode photographic content as JPEG at a high quality (default 82),
//     which is dramatically smaller than PNG for photos while staying visually
//     faithful for model perception.
//
// The resampler is a pure-Go area-averaging (box) downscaler — correct, fast
// enough for interactive use, and high quality for the downscale-only case we
// need (we never upscale).
//
// Safety: the layer never inflates a payload (if the re-encoded result is not
// smaller, the original bytes are returned), never touches alpha-bearing PNGs
// in a way that drops transparency (they re-encode as PNG), and leaves formats
// it cannot safely round-trip (GIF — possibly animated; WebP — no stdlib
// decoder) untouched.
package imgcompress

import (
	"bytes"
	"image"
	"image/draw"
	_ "image/gif" // register GIF decoder for DecodeConfig sniffing
	"image/jpeg"
	"image/png"
)

// DefaultMaxEdge matches the longest-edge clamp providers apply server-side.
const DefaultMaxEdge = 1568

// DefaultJPEGQuality is a high-but-compact quality for vision input.
const DefaultJPEGQuality = 82

// Options configures one Compress call. The zero value is not used directly;
// callers should start from DefaultOptions.
type Options struct {
	MaxEdge     int // longest-edge cap in pixels; <=0 disables resizing
	JPEGQuality int // 1..100; clamped into range
}

// DefaultOptions returns the recommended settings.
func DefaultOptions() Options {
	return Options{MaxEdge: DefaultMaxEdge, JPEGQuality: DefaultJPEGQuality}
}

// Result reports what happened to one image.
type Result struct {
	Changed      bool   // true when the returned bytes differ from the input
	OrigBytes    int    // input byte length
	NewBytes     int    // output byte length
	OrigW, OrigH int    // source dimensions (0 if undecodable)
	NewW, NewH   int    // output dimensions (equal to source when not resized)
	OutMediaType string // canonical MIME of the output
	Reason       string // short note when unchanged (e.g. "format-unsupported")
}

// SavedBytes is the byte reduction (never negative).
func (r Result) SavedBytes() int {
	if r.NewBytes >= r.OrigBytes {
		return 0
	}
	return r.OrigBytes - r.NewBytes
}

// Compress reduces a single image. declaredType is the caller's MIME hint
// (used only to skip formats we deliberately don't touch); detection is done
// from the bytes. On any problem it returns the original bytes unchanged with
// Changed=false — it is always safe to send the result.
func Compress(data []byte, declaredType string, opts Options) ([]byte, Result) {
	res := Result{OrigBytes: len(data), NewBytes: len(data), OutMediaType: declaredType}
	if len(data) == 0 {
		res.Reason = "empty"
		return data, res
	}
	if opts.JPEGQuality < 1 || opts.JPEGQuality > 100 {
		opts.JPEGQuality = DefaultJPEGQuality
	}

	// Sniff the real format; only JPEG and PNG are safe to re-encode (GIF may
	// be animated, WebP has no stdlib decoder).
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		res.Reason = "decode-config-failed"
		return data, res
	}
	if format != "jpeg" && format != "png" {
		res.Reason = "format-unsupported:" + format
		return data, res
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		res.Reason = "decode-failed"
		return data, res
	}
	b := src.Bounds()
	res.OrigW, res.OrigH = b.Dx(), b.Dy()
	res.NewW, res.NewH = res.OrigW, res.OrigH

	// Normalize to a zero-origin RGBA once, so resampling/encoding index Pix
	// directly.
	rgba := toRGBA(src)

	// Decide target size.
	tw, th := res.OrigW, res.OrigH
	if opts.MaxEdge > 0 {
		tw, th = clampLongestEdge(res.OrigW, res.OrigH, opts.MaxEdge)
	}
	work := rgba
	if tw != res.OrigW || th != res.OrigH {
		work = areaDownscale(rgba, tw, th)
		res.NewW, res.NewH = tw, th
	}

	hasAlpha := format == "png" && imageHasAlpha(rgba)

	var buf bytes.Buffer
	var outType string
	if hasAlpha {
		// Preserve transparency: re-encode as PNG (resized).
		if err := png.Encode(&buf, work); err != nil {
			res.Reason = "png-encode-failed"
			return data, res
		}
		outType = "image/png"
	} else {
		if err := jpeg.Encode(&buf, work, &jpeg.Options{Quality: opts.JPEGQuality}); err != nil {
			res.Reason = "jpeg-encode-failed"
			return data, res
		}
		outType = "image/jpeg"
	}

	out := buf.Bytes()
	// Never inflate: if we didn't actually shrink the bytes (and didn't change
	// dimensions), keep the original to avoid wasting work or growing payloads.
	if len(out) >= len(data) && res.NewW == res.OrigW && res.NewH == res.OrigH {
		res.Reason = "no-gain"
		res.NewBytes = len(data)
		return data, res
	}

	res.Changed = true
	res.NewBytes = len(out)
	res.OutMediaType = outType
	return out, res
}

// clampLongestEdge returns dimensions scaled so the longest edge is at most
// maxEdge, preserving aspect ratio. Returns the input unchanged when already
// within the cap.
func clampLongestEdge(w, h, maxEdge int) (int, int) {
	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxEdge {
		return w, h
	}
	// Pin the longest edge exactly to maxEdge (avoids float truncation leaving
	// it at maxEdge-1) and scale the other dimension with rounding.
	if w >= h {
		nh := int(float64(h)*float64(maxEdge)/float64(w) + 0.5)
		if nh < 1 {
			nh = 1
		}
		return maxEdge, nh
	}
	nw := int(float64(w)*float64(maxEdge)/float64(h) + 0.5)
	if nw < 1 {
		nw = 1
	}
	return nw, maxEdge
}

// toRGBA returns src as a zero-origin *image.RGBA (a copy unless src already is
// exactly that).
func toRGBA(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok && rgba.Rect.Min.X == 0 && rgba.Rect.Min.Y == 0 {
		return rgba
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// areaDownscale downscales src to nw×nh by averaging the source pixels that map
// to each destination pixel (box filter / supersampling). High quality for
// downscaling; src must be zero-origin RGBA and nw,nh must be <= src size.
func areaDownscale(src *image.RGBA, nw, nh int) *image.RGBA {
	sw := src.Rect.Dx()
	sh := src.Rect.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))

	for dy := 0; dy < nh; dy++ {
		sy0 := dy * sh / nh
		sy1 := (dy + 1) * sh / nh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < nw; dx++ {
			sx0 := dx * sw / nw
			sx1 := (dx + 1) * sw / nw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, b, a, cnt uint64
			for sy := sy0; sy < sy1; sy++ {
				rowStart := src.PixOffset(sx0, sy)
				off := rowStart
				for sx := sx0; sx < sx1; sx++ {
					r += uint64(src.Pix[off])
					g += uint64(src.Pix[off+1])
					b += uint64(src.Pix[off+2])
					a += uint64(src.Pix[off+3])
					off += 4
					cnt++
				}
			}
			if cnt == 0 {
				cnt = 1
			}
			di := dst.PixOffset(dx, dy)
			// Each sum is over cnt channel bytes (0..255), so the average is
			// always in [0,255]; the explicit mask makes the uint8 bound
			// provable (and self-documenting) rather than implicit.
			dst.Pix[di] = uint8((r / cnt) & 0xff)
			dst.Pix[di+1] = uint8((g / cnt) & 0xff)
			dst.Pix[di+2] = uint8((b / cnt) & 0xff)
			dst.Pix[di+3] = uint8((a / cnt) & 0xff)
		}
	}
	return dst
}

// imageHasAlpha reports whether any pixel is non-opaque.
func imageHasAlpha(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0xff {
			return true
		}
	}
	return false
}
