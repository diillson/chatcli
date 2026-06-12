/*
 * ChatCLI - tests for gateway voice transcription wiring.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/llm/transcription"
	"go.uber.org/zap"
)

// fakeTranscriber is a controllable transcription.Provider for tests.
type fakeTranscriber struct {
	out string
	err error
}

func (f *fakeTranscriber) Name() string { return "fake" }
func (f *fakeTranscriber) Transcribe(_ context.Context, _ []byte, _, _, _ string) (string, error) {
	return f.out, f.err
}

func newAudioMsg(caption string) *gateway.InboundMessage {
	return &gateway.InboundMessage{
		Platform: "telegram",
		Text:     caption,
		Audio:    &gateway.InboundAudio{Data: []byte("audio-bytes"), MimeType: "audio/ogg"},
	}
}

func TestTranscribeInbound_Disabled(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	_, handled, reply := cli.transcribeInbound(context.Background(), transcription.NewNull(), "", newAudioMsg(""))
	if !handled {
		t.Fatal("null provider must short-circuit (handled=true)")
	}
	if reply == "" {
		t.Error("disabled path must return a user-facing reply")
	}
}

func TestTranscribeInbound_Success(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	transcript, handled, _ := cli.transcribeInbound(context.Background(),
		&fakeTranscriber{out: "  hello there "}, "", newAudioMsg(""))
	if handled {
		t.Fatal("a successful transcript must not be handled inline")
	}
	if transcript != "hello there" {
		t.Errorf("transcript = %q", transcript)
	}
}

func TestTranscribeInbound_MergesCaption(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	transcript, handled, _ := cli.transcribeInbound(context.Background(),
		&fakeTranscriber{out: "spoken words"}, "", newAudioMsg("written caption"))
	if handled {
		t.Fatal("should not be handled inline")
	}
	if !strings.Contains(transcript, "written caption") || !strings.Contains(transcript, "spoken words") {
		t.Errorf("caption and transcript must both appear: %q", transcript)
	}
}

func TestTranscribeInbound_ErrorAndEmpty(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}

	_, handled, reply := cli.transcribeInbound(context.Background(),
		&fakeTranscriber{err: errors.New("boom")}, "", newAudioMsg(""))
	if !handled || reply == "" {
		t.Error("transcription error must be handled with a reply")
	}

	_, handled, reply = cli.transcribeInbound(context.Background(),
		&fakeTranscriber{out: "   "}, "", newAudioMsg(""))
	if !handled || reply == "" {
		t.Error("empty transcript must be handled with a reply")
	}
}

func TestTranscribeInbound_NeedsFFmpegReplyIsActionable(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	wrapped := fmt.Errorf("no decoder for audio/mpeg: %w", transcription.ErrNeedsFFmpeg)

	_, handled, reply := cli.transcribeInbound(context.Background(),
		&fakeTranscriber{err: wrapped}, "", newAudioMsg(""))
	if !handled {
		t.Fatal("ffmpeg-missing must be handled with a reply")
	}
	if !strings.Contains(reply, transcription.FFmpegInstallHint()) {
		t.Errorf("reply must carry the platform install hint: %q", reply)
	}
	if reply == transcriptionFailureReply(errors.New("generic")) {
		t.Error("ffmpeg-missing reply must differ from the generic failure")
	}
}
