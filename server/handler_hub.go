/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetHub attaches the conversation hub broker, enabling the cross-channel
// continuity RPCs. When nil, those RPCs return Unavailable.
//
// For live tailing to span the gateway adapters and remote CLIs, the gateway
// daemon and this gRPC server must share one in-process broker (same process),
// since fan-out is in-memory. Remote clients connect over gRPC from anywhere.
func (h *Handler) SetHub(broker hub.Broker) { h.hub = broker }

// hubPrincipal resolves the principal a request acts on. It defaults to the
// authenticated subject; acting on behalf of a different principal requires
// admin role, so one user can never read or write another's conversation.
func (h *Handler) hubPrincipal(ctx context.Context, requested string) (string, error) {
	u := UserFromContext(ctx)
	authed := ""
	if u != nil {
		authed = u.Subject
	}
	if requested == "" || requested == authed {
		if authed == "" {
			return "", status.Errorf(codes.Unauthenticated, "%s", i18n.T("server.hub.no_principal"))
		}
		return authed, nil
	}
	if u == nil || !u.HasRole(RoleAdmin) {
		return "", status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.hub.permission_denied"))
	}
	return requested, nil
}

// authorizeConv ensures principal owns convID (admins bypass). Returns a
// gRPC status error suitable to return directly.
func (h *Handler) authorizeConv(ctx context.Context, convID, principal string) error {
	owner, err := h.hub.OwnerOf(ctx, convID)
	if err != nil {
		return status.Errorf(codes.NotFound, "%s", i18n.T("server.hub.conv_not_found"))
	}
	if owner == principal {
		return nil
	}
	if u := UserFromContext(ctx); u != nil && u.HasRole(RoleAdmin) {
		return nil
	}
	return status.Errorf(codes.PermissionDenied, "%s", i18n.T("server.hub.conv_forbidden"))
}

func (h *Handler) hubReady() error {
	if h.hub == nil {
		return status.Errorf(codes.Unavailable, "%s", i18n.T("server.hub.unavailable"))
	}
	return nil
}

// ResolveActiveConversation returns (creating if needed) the principal's
// active conversation.
func (h *Handler) ResolveActiveConversation(ctx context.Context, req *pb.ResolveActiveConversationRequest) (*pb.ResolveActiveConversationResponse, error) {
	if err := h.hubReady(); err != nil {
		return nil, err
	}
	principal, err := h.hubPrincipal(ctx, req.GetPrincipal())
	if err != nil {
		return nil, err
	}
	convID, err := h.hub.Resolve(ctx, principal)
	if err != nil {
		h.logger.Error("hub resolve failed", zap.String("principal", principal), zap.Error(err))
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.hub.resolve_error", err))
	}
	return &pb.ResolveActiveConversationResponse{ConvId: convID, Principal: principal}, nil
}

// NewConversation rotates the principal's active conversation to a fresh thread.
func (h *Handler) NewConversation(ctx context.Context, req *pb.NewConversationRequest) (*pb.NewConversationResponse, error) {
	if err := h.hubReady(); err != nil {
		return nil, err
	}
	principal, err := h.hubPrincipal(ctx, req.GetPrincipal())
	if err != nil {
		return nil, err
	}
	convID, err := h.hub.NewConversation(ctx, principal)
	if err != nil {
		h.logger.Error("hub new conversation failed", zap.String("principal", principal), zap.Error(err))
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.hub.new_error", err))
	}
	h.logger.Info("hub: new conversation", zap.String("principal", principal), zap.String("conv_id", convID))
	return &pb.NewConversationResponse{ConvId: convID}, nil
}

// AppendEvent appends one dialogue turn to a conversation's shared log.
func (h *Handler) AppendEvent(ctx context.Context, req *pb.AppendEventRequest) (*pb.AppendEventResponse, error) {
	if err := h.hubReady(); err != nil {
		return nil, err
	}
	principal, err := h.hubPrincipal(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeConv(ctx, req.GetConvId(), principal); err != nil {
		return nil, err
	}
	ev := models.ConversationEvent{
		ConvID:      req.GetConvId(),
		Principal:   principal,
		Channel:     req.GetChannel(),
		Role:        req.GetRole(),
		Content:     req.GetContent(),
		ClientMsgID: req.GetClientMsgId(),
	}
	stored, err := h.hub.Append(ctx, ev)
	if err != nil {
		h.logger.Error("hub append failed", zap.String("conv_id", ev.ConvID), zap.Error(err))
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.hub.append_error", err))
	}
	return &pb.AppendEventResponse{Event: eventToProto(stored)}, nil
}

// ReadConversation returns events after since_seq (hydration / resume).
func (h *Handler) ReadConversation(ctx context.Context, req *pb.ReadConversationRequest) (*pb.ReadConversationResponse, error) {
	if err := h.hubReady(); err != nil {
		return nil, err
	}
	principal, err := h.hubPrincipal(ctx, "")
	if err != nil {
		return nil, err
	}
	if err := h.authorizeConv(ctx, req.GetConvId(), principal); err != nil {
		return nil, err
	}
	events, err := h.hub.Read(ctx, req.GetConvId(), req.GetSinceSeq(), int(req.GetLimit()))
	if err != nil {
		h.logger.Error("hub read failed", zap.String("conv_id", req.GetConvId()), zap.Error(err))
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.hub.read_error", err))
	}
	out := make([]*pb.ConversationEvent, 0, len(events))
	for _, ev := range events {
		out = append(out, eventToProto(ev))
	}
	return &pb.ReadConversationResponse{Events: out}, nil
}

// SubscribeConversation live-tails a conversation. It streams the backlog after
// since_seq, then live events. On consumer overflow the stream ends with
// Aborted so the client resubscribes from its last seq to resync.
func (h *Handler) SubscribeConversation(req *pb.SubscribeConversationRequest, stream pb.ChatCLIService_SubscribeConversationServer) error {
	if err := h.hubReady(); err != nil {
		return err
	}
	ctx := stream.Context()
	principal, err := h.hubPrincipal(ctx, "")
	if err != nil {
		return err
	}
	if err := h.authorizeConv(ctx, req.GetConvId(), principal); err != nil {
		return err
	}

	ch, err := h.hub.Subscribe(ctx, req.GetConvId(), req.GetSinceSeq())
	if err != nil {
		return status.Errorf(codes.Internal, "%s", i18n.T("server.hub.read_error", err))
	}
	for ev := range ch {
		if err := stream.Send(eventToProto(ev)); err != nil {
			return err
		}
	}
	// Channel closed: distinguish clean cancellation from overflow.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return status.FromContextError(ctxErr).Err()
	}
	return status.Errorf(codes.Aborted, "%s", i18n.T("server.hub.subscribe_resync"))
}

// SetBinding maps a channel identity to a principal. A user may bind identities
// to themselves; binding to another principal requires admin.
func (h *Handler) SetBinding(ctx context.Context, req *pb.SetBindingRequest) (*pb.SetBindingResponse, error) {
	if err := h.hubReady(); err != nil {
		return nil, err
	}
	principal, err := h.hubPrincipal(ctx, req.GetPrincipal())
	if err != nil {
		return nil, err
	}
	if req.GetPlatform() == "" || req.GetUserId() == "" || principal == "" {
		return nil, status.Errorf(codes.InvalidArgument, "%s", i18n.T("server.hub.bind_missing"))
	}
	if err := h.hub.Bind(ctx, req.GetPlatform(), req.GetUserId(), principal); err != nil {
		h.logger.Error("hub bind failed", zap.Error(err))
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.hub.bind_error", err))
	}
	h.logger.Info("hub: binding set",
		zap.String("platform", req.GetPlatform()),
		zap.String("user_id", req.GetUserId()),
		zap.String("principal", principal))
	return &pb.SetBindingResponse{Success: true}, nil
}

// ListBindings returns bindings. Non-admins only see their own.
func (h *Handler) ListBindings(ctx context.Context, req *pb.ListBindingsRequest) (*pb.ListBindingsResponse, error) {
	if err := h.hubReady(); err != nil {
		return nil, err
	}
	filter := req.GetPrincipal()
	if u := UserFromContext(ctx); u == nil || !u.HasRole(RoleAdmin) {
		// Force the filter to the caller's own principal — no peeking at others'.
		self, err := h.hubPrincipal(ctx, "")
		if err != nil {
			return nil, err
		}
		filter = self
	}
	bindings, err := h.hub.ListBindings(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%s", i18n.T("server.hub.list_bindings_error", err))
	}
	out := make([]*pb.HubBinding, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, &pb.HubBinding{Platform: b.Platform, UserId: b.UserID, Principal: b.Principal})
	}
	return &pb.ListBindingsResponse{Bindings: out}, nil
}

func eventToProto(ev models.ConversationEvent) *pb.ConversationEvent {
	return &pb.ConversationEvent{
		ConvId:      ev.ConvID,
		Seq:         ev.Seq,
		Principal:   ev.Principal,
		Channel:     ev.Channel,
		Role:        ev.Role,
		Content:     ev.Content,
		ClientMsgId: ev.ClientMsgID,
		Ts:          ev.Timestamp.UnixMilli(),
	}
}
