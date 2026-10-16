/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// failingAppendHub is a hub whose appends fail, for the error outcome.
type failingAppendHub struct{ *fakeHubClient }

func (failingAppendHub) AppendEvent(context.Context, models.ConversationEvent) (models.ConversationEvent, error) {
	return models.ConversationEvent{}, errors.New("SECRET-ERR")
}

// Each exchange a session has with the conversation hub shows under the turn
// that made it, with counts only: never a conversation id or a message.
func TestHubSyncExchangesShowUnderTheTurn(t *testing.T) {
	rec := pulsetest.Record(t)
	fc := newFakeHubClient()
	hs := newHubSync(fc, zap.NewNop())
	ctx := pulse.WithParent(context.Background(), "turn-9")

	if err := hs.startFresh(ctx); err != nil {
		t.Fatal(err)
	}
	fc.seed(models.ConversationEvent{Channel: "telegram", Role: models.ConvRoleUser, Content: "SECRET-REMOTE"})
	if got := hs.pull(ctx); len(got) != 1 {
		t.Fatalf("pull = %d messages, want 1", len(got))
	}
	hs.mirrorTurn(ctx, "SECRET-Q", "SECRET-A")
	if err := hs.newSession(ctx); err != nil {
		t.Fatal(err)
	}

	open := rec.WaitFor(t, "open end", pulsetest.Is(pulse.KindBackground, "hub:open", pulse.PhaseEnd))
	if open.Parent != "turn-9" || open.Attrs["mode"] != "fresh" || open.Status != pulse.StatusOK {
		t.Fatalf("open = %+v", open)
	}
	pull := rec.WaitFor(t, "pull end", pulsetest.Is(pulse.KindBackground, "hub:pull", pulse.PhaseEnd))
	if pull.Parent != "turn-9" || pull.Attrs["events"] != "1" || pull.Attrs["spliced"] != "1" {
		t.Fatalf("pull = %+v", pull)
	}
	mirror := rec.WaitFor(t, "mirror end", pulsetest.Is(pulse.KindBackground, "hub:mirror", pulse.PhaseEnd))
	if mirror.Parent != "turn-9" || mirror.Attrs["events"] != "2" || mirror.Status != pulse.StatusOK {
		t.Fatalf("mirror = %+v", mirror)
	}
	rec.WaitFor(t, "rotate", func(ev pulse.Event) bool {
		return ev.Name == "hub:open" && ev.Phase == pulse.PhaseEnd && ev.Attrs["mode"] == "rotate"
	})
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "conv-") {
		t.Fatalf("conversation content or id leaked: %s", wire)
	}
}

func TestHubSyncMirrorFailureIsAnError(t *testing.T) {
	rec := pulsetest.Record(t)
	hs := newHubSync(failingAppendHub{newFakeHubClient()}, zap.NewNop())
	if err := hs.startFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	hs.mirrorTurn(context.Background(), "q", "a")
	ev := rec.WaitFor(t, "mirror end", pulsetest.Is(pulse.KindBackground, "hub:mirror", pulse.PhaseEnd))
	if ev.Status != pulse.StatusError || ev.Attrs["events"] != "0" {
		t.Fatalf("mirror = %+v", ev)
	}
}

// The gateway's side of the hub hangs from the inbound message it serves.
func TestGatewayHubTurnShowsUnderTheInboundMessage(t *testing.T) {
	rec := pulsetest.Record(t)
	hs, _ := newTestHubSessions(t)
	ctx := pulse.WithParent(context.Background(), "rpc-5")

	turn := hs.begin(ctx, gateway.InboundMessage{Platform: "telegram", ChatID: "SECRET-CHAT", UserID: "SECRET-USER", Text: "SECRET-TEXT"})
	turn.finish(ctx, "SECRET-REPLY")

	begin := rec.WaitFor(t, "begin end", pulsetest.Is(pulse.KindBackground, "hub:begin", pulse.PhaseEnd))
	if begin.Parent != "rpc-5" || begin.Attrs["channel"] != "telegram" || begin.Attrs["history"] != "0" || begin.Status != pulse.StatusOK {
		t.Fatalf("begin = %+v", begin)
	}
	finish := rec.WaitFor(t, "finish end", pulsetest.Is(pulse.KindBackground, "hub:finish", pulse.PhaseEnd))
	if finish.Parent != "rpc-5" || finish.Status != pulse.StatusOK {
		t.Fatalf("finish = %+v", finish)
	}
	wire, _ := json.Marshal(rec.Events())
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("message, chat or user leaked: %s", wire)
	}

	// Without a store the turn runs with no continuity: one blocked point.
	newHubSessions(nil, zap.NewNop()).begin(ctx, gateway.InboundMessage{Platform: "telegram"})
	pt := rec.WaitFor(t, "no-store point", pulsetest.Is(pulse.KindBackground, "hub:begin", pulse.PhasePoint))
	if pt.Status != pulse.StatusBlocked || pt.Parent != "rpc-5" {
		t.Fatalf("point = %+v", pt)
	}
}
