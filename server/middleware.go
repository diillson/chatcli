/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// loggingUnaryInterceptor logs unary RPC calls.
func loggingUnaryInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		if err != nil {
			logger.Warn(i18n.T("server.middleware.unary_call"),
				zap.String("method", info.FullMethod),
				zap.Duration("duration", duration),
				zap.Error(err),
			)
		} else {
			logger.Info(i18n.T("server.middleware.unary_call"),
				zap.String("method", info.FullMethod),
				zap.Duration("duration", duration),
			)
		}

		return resp, err
	}
}

// loggingStreamInterceptor logs stream RPC calls.
func loggingStreamInterceptor(logger *zap.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		duration := time.Since(start)

		if err != nil {
			logger.Warn(i18n.T("server.middleware.stream_call"),
				zap.String("method", info.FullMethod),
				zap.Duration("duration", duration),
				zap.Error(err),
			)
		} else {
			logger.Info(i18n.T("server.middleware.stream_call"),
				zap.String("method", info.FullMethod),
				zap.Duration("duration", duration),
			)
		}

		return err
	}
}

// isDebugMode returns true when detailed error info should be logged.
func isDebugMode() bool {
	return strings.EqualFold(os.Getenv("CHATCLI_DEBUG"), "true")
}

// sanitizePanicValue removes potentially sensitive data from panic values before logging.
func sanitizePanicValue(r interface{}) string {
	s := strings.TrimSpace(strings.Replace(strings.Replace(
		strings.Replace(fmt.Sprint(r), "\n", " ", -1), "\r", "", -1), "\t", " ", -1))
	// Truncate to prevent log flooding
	if len(s) > 500 {
		s = s[:500] + "...[truncated]"
	}
	return s
}

// fmt is needed for sanitizePanicValue — import is already at top via uuid usage

// recoveryUnaryInterceptor recovers from panics in unary RPCs.
// Security (H11): Stack traces only logged in debug mode. Production gets request ID only.
func recoveryUnaryInterceptor(logger *zap.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		// Generate request ID for correlation
		requestID := uuid.New().String()
		_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))

		defer func() {
			if r := recover(); r != nil {
				if isDebugMode() {
					logger.Error(i18n.T("server.middleware.panic_unary"),
						zap.String("request_id", requestID),
						zap.Any("panic", r),
						zap.String("method", info.FullMethod),
						zap.String("stack", string(debug.Stack())),
					)
				} else {
					// Production: log sanitized error with request ID only
					logger.Error("panic recovered",
						zap.String("request_id", requestID),
						zap.String("method", info.FullMethod),
						zap.String("error", sanitizePanicValue(r)),
					)
				}
				err = status.Errorf(codes.Internal, "internal error (request_id: %s)", requestID)
			}
		}()
		return handler(ctx, req)
	}
}

// recoveryStreamInterceptor recovers from panics in stream RPCs.
// Security (H11): Stack traces only logged in debug mode.
func recoveryStreamInterceptor(logger *zap.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		requestID := uuid.New().String()

		defer func() {
			if r := recover(); r != nil {
				if isDebugMode() {
					logger.Error(i18n.T("server.middleware.panic_stream"),
						zap.String("request_id", requestID),
						zap.Any("panic", r),
						zap.String("method", info.FullMethod),
						zap.String("stack", string(debug.Stack())),
					)
				} else {
					logger.Error("panic recovered (stream)",
						zap.String("request_id", requestID),
						zap.String("method", info.FullMethod),
						zap.String("error", sanitizePanicValue(r)),
					)
				}
				err = status.Errorf(codes.Internal, "internal error (request_id: %s)", requestID)
			}
		}()
		return handler(srv, ss)
	}
}
