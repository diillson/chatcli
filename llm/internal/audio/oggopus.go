/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Package audio decodes the OGG/Opus voice notes messaging platforms emit
 * (Telegram, WhatsApp) into the 16-bit PCM WAV the local speech engines
 * consume — in pure Go, so voice input works on machines without ffmpeg.
 *
 * Scope is deliberately narrow: OGG/Opus in, mono 16-bit WAV out. Every other
 * container/codec (mp3, m4a, …) stays on the ffmpeg path; this package is the
 * zero-dependency lane for the dominant gateway case, not a media framework.
 */
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/pion/opus"
	"github.com/pion/opus/pkg/oggreader"
)

// ErrNotOggOpus reports input that is not an OGG container carrying an Opus
// stream. Callers use it to route other formats to their fallback decoder.
var ErrNotOggOpus = errors.New("audio: not an OGG/Opus stream")

const (
	// maxOpusFrameSamples is the largest Opus packet duration (120 ms) at the
	// highest output rate we request, with one extra frame of headroom so a
	// decoder rounding difference can never overflow the buffer.
	maxOpusFrameMs = 120
	// oggCapture is the OGG page capture pattern.
	oggCapture = "OggS"
	// opusHead identifies the Opus identification header packet (RFC 7845).
	opusHead = "OpusHead"
	// opusTags identifies the Opus comment header packet (RFC 7845).
	opusTags = "OpusTags"
)

// LooksLikeOggOpus sniffs whether the payload is plausibly an OGG container
// with an Opus stream: the OggS capture pattern at offset 0 and the RFC 7845
// OpusHead marker inside the first page. MIME types and file names from
// messaging platforms are unreliable, so detection trusts the bytes only.
func LooksLikeOggOpus(data []byte) bool {
	if !bytes.HasPrefix(data, []byte(oggCapture)) {
		return false
	}
	// The identification header is the sole packet of the first page, which
	// is at most 282 bytes (27-byte header + 255 lacing + payload); scanning a
	// fixed window keeps the sniff O(1) on arbitrary inputs.
	window := data
	if len(window) > 512 {
		window = window[:512]
	}
	return bytes.Contains(window, []byte(opusHead))
}

// DecodeOggOpusToWAV decodes an OGG/Opus payload to mono 16-bit PCM WAV at
// the requested sample rate (the decoder resamples internally). It returns
// ErrNotOggOpus for non-Opus payloads so callers can fall through to other
// decoders, and a descriptive error for genuinely corrupt Opus data.
//
// ctx is checked between OGG pages: a canceled transcription aborts the
// decode instead of burning CPU on a clip nobody is waiting for.
func DecodeOggOpusToWAV(ctx context.Context, data []byte, sampleRate int) ([]byte, error) {
	if sampleRate <= 0 {
		return nil, fmt.Errorf("audio: invalid target sample rate %d", sampleRate)
	}
	if !LooksLikeOggOpus(data) {
		return nil, ErrNotOggOpus
	}

	ogg, _, err := oggreader.NewWith(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("audio: parse OGG container: %w", err)
	}

	decoder, err := opus.NewDecoderWithOutput(sampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("audio: init opus decoder: %w", err)
	}

	// One reusable packet buffer: 120 ms (the max Opus packet duration) at
	// the output rate, plus one frame of headroom.
	frame := make([]int16, sampleRate*maxOpusFrameMs/1000*2)
	var pcm []int16

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		segments, _, err := ogg.ParseNextPage()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("audio: read OGG page: %w", err)
		}
		for _, segment := range segments {
			if len(segment) == 0 ||
				bytes.HasPrefix(segment, []byte(opusHead)) ||
				bytes.HasPrefix(segment, []byte(opusTags)) {
				continue
			}
			n, err := decoder.DecodeToInt16(segment, frame)
			if err != nil {
				return nil, fmt.Errorf("audio: decode opus packet: %w", err)
			}
			pcm = append(pcm, frame[:n]...)
		}
	}

	if len(pcm) == 0 {
		return nil, errors.New("audio: OGG/Opus stream contained no audio packets")
	}
	return EncodeWAV(pcm, sampleRate), nil
}

// EncodeWAV wraps mono 16-bit PCM samples in a canonical RIFF/WAVE header.
func EncodeWAV(pcm []int16, sampleRate int) []byte {
	const (
		headerSize    = 44
		bitsPerSample = 16
		channels      = 1
	)
	dataLen := len(pcm) * 2
	buf := bytes.NewBuffer(make([]byte, 0, headerSize+dataLen))

	byteRate := sampleRate * channels * bitsPerSample / 8
	blockAlign := channels * bitsPerSample / 8

	buf.WriteString("RIFF")
	_ = binary.Write(buf, binary.LittleEndian, uint32(36+dataLen)) // #nosec G115 -- bounded by clip size
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(buf, binary.LittleEndian, uint32(16)) // PCM fmt chunk size
	_ = binary.Write(buf, binary.LittleEndian, uint16(1))  // PCM format
	_ = binary.Write(buf, binary.LittleEndian, uint16(channels))
	_ = binary.Write(buf, binary.LittleEndian, uint32(sampleRate)) // #nosec G115 -- validated positive
	_ = binary.Write(buf, binary.LittleEndian, uint32(byteRate))   // #nosec G115 -- derived from sampleRate
	_ = binary.Write(buf, binary.LittleEndian, uint16(blockAlign)) // #nosec G115 -- constant expression
	_ = binary.Write(buf, binary.LittleEndian, uint16(bitsPerSample))
	buf.WriteString("data")
	_ = binary.Write(buf, binary.LittleEndian, uint32(dataLen)) // #nosec G115 -- bounded by clip size
	_ = binary.Write(buf, binary.LittleEndian, pcm)
	return buf.Bytes()
}
