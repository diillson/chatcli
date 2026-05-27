/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// ResolveActiveConversation returns the principal's current shared conversation
// (creating one on first contact). principal may be empty to use the
// authenticated identity.
func (c *Client) ResolveActiveConversation(ctx context.Context, principal string) (convID, resolvedPrincipal string, err error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ResolveActiveConversation(ctx, &pb.ResolveActiveConversationRequest{Principal: principal})
	if err != nil {
		return "", "", fmt.Errorf("remote ResolveActiveConversation failed: %w", err)
	}
	return resp.ConvId, resp.Principal, nil
}

// NewConversation rotates the principal's active conversation to a fresh thread.
func (c *Client) NewConversation(ctx context.Context, principal string) (string, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.NewConversation(ctx, &pb.NewConversationRequest{Principal: principal})
	if err != nil {
		return "", fmt.Errorf("remote NewConversation failed: %w", err)
	}
	return resp.ConvId, nil
}

// AppendEvent appends one dialog turn to the shared conversation and returns
// the stored event (with its server-assigned Seq).
func (c *Client) AppendEvent(ctx context.Context, ev models.ConversationEvent) (models.ConversationEvent, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.AppendEvent(ctx, &pb.AppendEventRequest{
		ConvId:      ev.ConvID,
		Channel:     ev.Channel,
		Role:        ev.Role,
		Content:     ev.Content,
		ClientMsgId: ev.ClientMsgID,
	})
	if err != nil {
		return ev, fmt.Errorf("remote AppendEvent failed: %w", err)
	}
	return protoToEvent(resp.Event), nil
}

// ReadConversation returns events with Seq > sinceSeq (limit <= 0 = all).
func (c *Client) ReadConversation(ctx context.Context, convID string, sinceSeq int64, limit int) ([]models.ConversationEvent, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ReadConversation(ctx, &pb.ReadConversationRequest{
		ConvId:   convID,
		SinceSeq: sinceSeq,
		Limit:    int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("remote ReadConversation failed: %w", err)
	}
	out := make([]models.ConversationEvent, 0, len(resp.Events))
	for _, e := range resp.Events {
		out = append(out, protoToEvent(e))
	}
	return out, nil
}

// SubscribeConversation live-tails a conversation: it returns a channel that
// first yields the backlog after sinceSeq, then live events. The channel closes
// when ctx is canceled or the server ends the stream (e.g. an overflow resync
// signal); callers should resubscribe from the last Seq they saw to catch up.
func (c *Client) SubscribeConversation(ctx context.Context, convID string, sinceSeq int64) (<-chan models.ConversationEvent, error) {
	authCtx := c.withAuth(ctx)
	stream, err := c.grpcClient.SubscribeConversation(authCtx, &pb.SubscribeConversationRequest{
		ConvId:   convID,
		SinceSeq: sinceSeq,
	})
	if err != nil {
		return nil, fmt.Errorf("remote SubscribeConversation failed: %w", err)
	}

	out := make(chan models.ConversationEvent)
	go func() {
		defer close(out)
		for {
			ev, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					c.logger.Debug("hub subscribe stream ended: " + err.Error())
				}
				return
			}
			select {
			case out <- protoToEvent(ev):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// SetBinding maps a channel identity to a principal (empty principal = self).
func (c *Client) SetBinding(ctx context.Context, platform, userID, principal string) error {
	ctx = c.withAuth(ctx)
	_, err := c.grpcClient.SetBinding(ctx, &pb.SetBindingRequest{
		Platform:  platform,
		UserId:    userID,
		Principal: principal,
	})
	if err != nil {
		return fmt.Errorf("remote SetBinding failed: %w", err)
	}
	return nil
}

// ListBindings returns channel→principal bindings (empty principal = all the
// caller is allowed to see).
func (c *Client) ListBindings(ctx context.Context, principal string) ([]models.HubBinding, error) {
	ctx = c.withAuth(ctx)
	resp, err := c.grpcClient.ListBindings(ctx, &pb.ListBindingsRequest{Principal: principal})
	if err != nil {
		return nil, fmt.Errorf("remote ListBindings failed: %w", err)
	}
	out := make([]models.HubBinding, 0, len(resp.Bindings))
	for _, b := range resp.Bindings {
		out = append(out, models.HubBinding{Platform: b.Platform, UserID: b.UserId, Principal: b.Principal})
	}
	return out, nil
}

func protoToEvent(e *pb.ConversationEvent) models.ConversationEvent {
	if e == nil {
		return models.ConversationEvent{}
	}
	return models.ConversationEvent{
		ConvID:      e.ConvId,
		Seq:         e.Seq,
		Principal:   e.Principal,
		Channel:     e.Channel,
		Role:        e.Role,
		Content:     e.Content,
		ClientMsgID: e.ClientMsgId,
		Timestamp:   time.UnixMilli(e.Ts).UTC(),
	}
}
