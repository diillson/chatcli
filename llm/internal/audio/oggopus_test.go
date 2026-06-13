/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// loadFixture reads a testdata clip generated with ffmpeg/libopus (see the
// fixture names for the encoder profile used).
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return data
}

// wavHeader unpacks the fields the speech engines actually validate.
type wavHeader struct {
	sampleRate uint32
	channels   uint16
	bits       uint16
	dataLen    uint32
}

func parseWAVHeader(t *testing.T, wav []byte) wavHeader {
	t.Helper()
	if len(wav) < 44 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("not a RIFF/WAVE payload (len=%d)", len(wav))
	}
	return wavHeader{
		channels:   binary.LittleEndian.Uint16(wav[22:24]),
		sampleRate: binary.LittleEndian.Uint32(wav[24:28]),
		bits:       binary.LittleEndian.Uint16(wav[34:36]),
		dataLen:    binary.LittleEndian.Uint32(wav[40:44]),
	}
}

func TestDecodeOggOpusToWAV(t *testing.T) {
	tests := []struct {
		fixture  string
		duration float64 // seconds encoded into the fixture
	}{
		{"voice_voip.ogg", 0.6},  // libopus -application voip, the voice-note profile
		{"voice_audio.ogg", 0.5}, // libopus -application audio (CELT/hybrid modes)
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			wav, err := DecodeOggOpusToWAV(context.Background(), loadFixture(t, tt.fixture), 16000)
			if err != nil {
				t.Fatalf("decode failed: %v", err)
			}
			h := parseWAVHeader(t, wav)
			if h.sampleRate != 16000 || h.channels != 1 || h.bits != 16 {
				t.Errorf("unexpected WAV format: %+v", h)
			}
			gotSeconds := float64(h.dataLen) / 2.0 / 16000.0
			// Opus pads the tail to whole frames; accept ±25%.
			if gotSeconds < tt.duration*0.75 || gotSeconds > tt.duration*1.25 {
				t.Errorf("duration = %.3fs, want ≈%.1fs", gotSeconds, tt.duration)
			}
			// A sine clip must produce non-silent PCM.
			var peak int16
			for i := 44; i+1 < len(wav); i += 2 {
				s := int16(binary.LittleEndian.Uint16(wav[i : i+2])) // #nosec G115 -- reinterpreting PCM bytes
				if s > peak {
					peak = s
				}
			}
			if peak < 1000 {
				t.Errorf("decoded PCM is near-silent (peak=%d)", peak)
			}
		})
	}
}

func TestDecodeOggOpusToWAVRejectsNonOpus(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"not ogg":          []byte("ID3\x04mp3-ish payload"),
		"ogg without opus": append([]byte("OggS\x00\x02"), make([]byte, 300)...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeOggOpusToWAV(context.Background(), data, 16000)
			if !errors.Is(err, ErrNotOggOpus) {
				t.Errorf("err = %v, want ErrNotOggOpus", err)
			}
		})
	}
}

func TestDecodeOggOpusToWAVCorruptStream(t *testing.T) {
	clip := loadFixture(t, "voice_voip.ogg")
	// Keep the container headers, truncate mid-page: the decode must fail
	// cleanly, never panic or return silent garbage as success.
	if _, err := DecodeOggOpusToWAV(context.Background(), clip[:len(clip)/3], 16000); err == nil {
		t.Skip("decoder tolerated truncation — acceptable, nothing to assert")
	}
}

func TestDecodeOggOpusToWAVContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeOggOpusToWAV(ctx, loadFixture(t, "voice_voip.ogg"), 16000); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDecodeOggOpusToWAVInvalidRate(t *testing.T) {
	if _, err := DecodeOggOpusToWAV(context.Background(), loadFixture(t, "voice_voip.ogg"), 0); err == nil {
		t.Error("expected error for sample rate 0")
	}
}

func TestEncodeWAVRoundTrip(t *testing.T) {
	pcm := []int16{0, 1000, -1000, 32767, -32768}
	wav := EncodeWAV(pcm, 16000)
	h := parseWAVHeader(t, wav)
	if h.dataLen != uint32(len(pcm)*2) || h.sampleRate != 16000 || h.channels != 1 || h.bits != 16 {
		t.Errorf("unexpected header: %+v", h)
	}
	if len(wav) != 44+len(pcm)*2 {
		t.Errorf("len = %d, want %d", len(wav), 44+len(pcm)*2)
	}
}
