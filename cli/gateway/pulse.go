/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for the messaging daemon's inbound side: each message a
 * channel delivers is one node, named by its platform, open from receipt to
 * the final reply. The agent run it triggers, the replies it sends and the
 * platform API calls behind them hang from it. Only the platform, what kind
 * of media arrived and what kind of reply left are reported: never the text,
 * the chat, the sender or an error message.
 */
package gateway

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseInboundMedia names what an inbound message carried.
func pulseInboundMedia(msg *InboundMessage) string {
	switch {
	case msg.Audio != nil:
		return "voice"
	case msg.Image != nil:
		return "image"
	case msg.Document != nil:
		if mt := strings.ToLower(msg.Document.MimeType); mt == "" || strings.Contains(mt, "pdf") {
			return "pdf"
		}
		return "document"
	default:
		return "text"
	}
}

// pulseBeginInbound opens the node of one inbound message and returns the
// context the exchange must run under. platform is an adapter name the
// runner resolved, so the node set stays one per configured channel.
func pulseBeginInbound(ctx context.Context, msg *InboundMessage) (context.Context, *pulse.Span) {
	if !pulse.Enabled() {
		return ctx, nil
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindRPC, "gateway:"+msg.Platform, "")
	return ctx, span.With("direction", "inbound").With("platform", msg.Platform).With("media", pulseInboundMedia(msg))
}

// pulseReplyKind names what the final reply carried.
func pulseReplyKind(out *OutboundMessage) string {
	switch {
	case out.Audio != nil && out.Image != nil:
		return "voice+image"
	case out.Audio != nil:
		return "voice"
	case out.Image != nil:
		return "image"
	default:
		return "text"
	}
}

// pulseBeginSend opens the span of one outbound delivery (a progress chunk,
// the thinking notice or the final answer) under the exchange in ctx, and the
// context the platform call must use so it nests under the delivery.
func pulseBeginSend(ctx context.Context, platform, kind string) (context.Context, *pulse.Span) {
	if !pulse.Enabled() {
		return ctx, nil
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindBackground, "gateway:send", "")
	return ctx, span.With("platform", platform).With("kind", kind)
}

// pulseBeginVoice opens the span of a voice reply being synthesized and the
// context the synthesizer must use, so the engine call nests under it.
func pulseBeginVoice(ctx context.Context, platform string) (context.Context, *pulse.Span) {
	if !pulse.Enabled() {
		return ctx, nil
	}
	ctx, span := pulse.BeginCtx(ctx, pulse.KindBackground, "gateway:voice", "")
	return ctx, span.With("platform", platform)
}
