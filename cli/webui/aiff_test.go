/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package webui

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"os"
	"testing"

	"github.com/diillson/chatcli/llm/tts"
)

// buildAIFF writes a mono 22050 Hz 16-bit clip the way macOS `say` does
// (AIFF-C, "twos" big-endian PCM) or as plain AIFF / little-endian "sowt".
func buildAIFF(form, compression string, samples []int16) []byte {
	var comm bytes.Buffer
	_ = binary.Write(&comm, binary.BigEndian, uint16(1))            // channels
	_ = binary.Write(&comm, binary.BigEndian, uint32(len(samples))) // frames
	_ = binary.Write(&comm, binary.BigEndian, uint16(16))           // bits
	comm.Write([]byte{0x40, 0x0D, 0xAC, 0x44, 0, 0, 0, 0, 0, 0})    // 22050 as 80-bit extended
	order := binary.ByteOrder(binary.BigEndian)
	if form == "AIFC" {
		comm.WriteString(compression)
		comm.Write([]byte{3, 'x', 'y', 'z'}) // pascal name, even length
		if compression == "sowt" {
			order = binary.LittleEndian
		}
	}
	var ssnd bytes.Buffer
	_ = binary.Write(&ssnd, binary.BigEndian, uint32(0))
	_ = binary.Write(&ssnd, binary.BigEndian, uint32(0))
	for _, s := range samples {
		_ = binary.Write(&ssnd, order, s)
	}
	var body bytes.Buffer
	body.WriteString(form)
	if form == "AIFC" {
		body.WriteString("FVER")
		_ = binary.Write(&body, binary.BigEndian, uint32(4))
		body.Write([]byte{0xA2, 0x80, 0x51, 0x40})
	}
	body.WriteString("COMM")
	_ = binary.Write(&body, binary.BigEndian, uint32(comm.Len()))
	body.Write(comm.Bytes())
	body.WriteString("SSND")
	_ = binary.Write(&body, binary.BigEndian, uint32(ssnd.Len()))
	body.Write(ssnd.Bytes())
	var out bytes.Buffer
	out.WriteString("FORM")
	_ = binary.Write(&out, binary.BigEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestAIFFToWAV(t *testing.T) {
	samples := []int16{0, 1000, -1000, 32767, -32768}
	want := new(bytes.Buffer)
	_ = binary.Write(want, binary.LittleEndian, samples)
	for _, c := range []struct{ form, comp string }{{"AIFC", "twos"}, {"AIFF", ""}, {"AIFC", "sowt"}, {"AIFC", "NONE"}} {
		wav, ok := aiffToWAV(buildAIFF(c.form, c.comp, samples))
		if !ok || audioKind(wav) != "wav" {
			t.Fatalf("%s/%s: not converted", c.form, c.comp)
		}
		if rate := binary.LittleEndian.Uint32(wav[24:28]); rate != 22050 {
			t.Fatalf("%s/%s: rate %d", c.form, c.comp, rate)
		}
		if !bytes.Equal(wav[44:], want.Bytes()) {
			t.Fatalf("%s/%s: samples % x", c.form, c.comp, wav[44:])
		}
	}
	for name, in := range map[string][]byte{
		"compressed": buildAIFF("AIFC", "ima4", samples), "wav": []byte("RIFF....WAVE"), "short": []byte("FORM"),
		"no sound": []byte("FORM\x00\x00\x00\x04AIFF"),
	} {
		if _, ok := aiffToWAV(in); ok {
			t.Errorf("%s must pass through unconverted", name)
		}
	}
	// A real `say` clip, when the fixture exists on this machine.
	if raw, err := os.ReadFile(os.Getenv("CHATCLI_TEST_AIFF")); err == nil {
		if wav, ok := aiffToWAV(raw); !ok || len(wav) < 1000 {
			t.Fatal("the say clip must convert")
		}
	}
}

// aiffTTS answers like the macOS `say` fallback.
type aiffTTS struct{}

func (aiffTTS) Name() string { return "local:say" }
func (aiffTTS) Synthesize(context.Context, string, string, string) (tts.Audio, error) {
	return tts.Audio{Data: buildAIFF("AIFC", "twos", []int16{1, 2, 3}), Mime: "audio/aiff", Ext: "aiff"}, nil
}

// The system voice fallback reaches the browser as WAV it can play.
func TestTTS_AIFFServedAsWAV(t *testing.T) {
	srv := startVoice(t, Options{Voice: aiffTTS{}})
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Host()+"/api/tts", bytes.NewReader([]byte(`{"text":"hi"}`)))
	req.Header.Set(tokenHeader, srv.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("tts = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}
