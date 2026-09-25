/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// streamingFakeClient streams its reply in fixed chunks and reports usage
// on the final chunk, like a real provider.
type streamingFakeClient struct {
	rpcChatFakeClient
	chunks   []string
	streamed bool
}

func (f *streamingFakeClient) SupportsStreaming() bool { return true }
func (f *streamingFakeClient) SendPromptStream(_ context.Context, _ string, hist []models.Message, _ int) (<-chan client.StreamChunk, error) {
	f.streamed = true
	f.lastHist = append([]models.Message(nil), hist...)
	ch := make(chan client.StreamChunk, len(f.chunks)+1)
	for _, c := range f.chunks {
		ch <- client.StreamChunk{Text: c}
	}
	ch <- client.StreamChunk{Done: true, Usage: &models.UsageInfo{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}}
	close(ch)
	return ch, nil
}

type chunkRecorder struct{ parts []string }

func (r *chunkRecorder) Chunk(text string) { r.parts = append(r.parts, text) }

// With a sink, the RPC chat turn streams through the provider and the
// sink sees every chunk while the turn still returns the whole reply;
// attached images reach the user message.
func TestRunChatTurnRPC_StreamsAndAttachesImages(t *testing.T) {
	t.Setenv("CHATCLI_VISION_INPUT", "native") // the fake model is off-catalog; keep the image native
	fake := &streamingFakeClient{chunks: []string{"Hel", "lo ", "web"}}
	c := newRPCChatCLI(t, &fake.rpcChatFakeClient)
	c.Client = fake
	rec := &chunkRecorder{}
	img := models.ImageContent{MediaType: "image/png", Data: []byte("\x89PNG"), FileName: "shot.png"}
	turn, err := c.RunChatTurnRPC(context.Background(), "web", "look", nil, RPCChatOpts{Stream: rec, Attachments: &TurnAttachments{Images: []models.ImageContent{img}}})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.streamed || turn.Reply != "Hello web" || strings.Join(rec.parts, "|") != "Hel|lo |web" {
		t.Fatalf("streamed=%v reply=%q chunks=%v", fake.streamed, turn.Reply, rec.parts)
	}
	var user *models.Message
	for i := range turn.History {
		if turn.History[i].Role == "user" {
			user = &turn.History[i]
		}
	}
	if user == nil || len(user.Images) != 1 || user.Images[0].FileName != "shot.png" {
		t.Fatalf("attached image missing from the user message: %+v", user)
	}
}

// A provider without streaming still serves a sink: the whole reply is
// delivered as one chunk.
func TestRunChatTurnRPC_SinkWithoutStreamingGetsOneChunk(t *testing.T) {
	fake := &rpcChatFakeClient{reply: "plain reply"}
	c := newRPCChatCLI(t, fake)
	rec := &chunkRecorder{}
	turn, err := c.RunChatTurnRPC(context.Background(), "web", "hi", nil, RPCChatOpts{Stream: rec})
	if err != nil || turn.Reply != "plain reply" || len(rec.parts) != 1 || rec.parts[0] != "plain reply" {
		t.Fatalf("reply=%q chunks=%v err=%v", turn.Reply, rec.parts, err)
	}
}
