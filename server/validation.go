/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxPromptBytes      = 512 * 1024  // 500KB
	maxDescriptionBytes = 50 * 1024   // 50KB
	maxK8sContextBytes  = 1024 * 1024 // 1MB
	maxIssueNameBytes   = 1024        // 1KB
	maxSessionNameLen   = 256
	maxPluginNameLen    = 256
	maxPluginArgs       = 100
	maxPluginArgBytes   = 10 * 1024 // 10KB
	maxTokensMin        = 1
	maxTokensMax        = 200000
	temperatureMin      = 0.0
	temperatureMax      = 2.0
)

var sessionNameRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-.]{0,254}$`)

// ValidationInterceptor returns a gRPC unary interceptor that validates request fields.
func ValidationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if err := validateRequest(info.FullMethod, req); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "validation: %s", err.Error())
		}
		return handler(ctx, req)
	}
}

func validateRequest(method string, req interface{}) error {
	// Extract the short method name from the full gRPC method path
	parts := strings.Split(method, "/")
	shortMethod := parts[len(parts)-1]

	switch shortMethod {
	case "SendPrompt":
		return validateSendPrompt(req)
	case "StreamPrompt":
		return validateStreamPrompt(req)
	case "SaveSession":
		return validateSaveSession(req)
	case "LoadSession", "DeleteSession":
		return validateSessionName(req)
	case "ExecuteRemotePlugin":
		return validateExecutePlugin(req)
	case "DownloadPlugin":
		return validateDownloadPlugin(req)
	case "AnalyzeIssue":
		return validateAnalyzeIssue(req)
	}
	return nil
}

func validateSendPrompt(req interface{}) error {
	r, ok := req.(*pb.SendPromptRequest)
	if !ok {
		return nil
	}
	if len(r.Prompt) > maxPromptBytes {
		return fmt.Errorf("prompt exceeds maximum size of %d bytes", maxPromptBytes)
	}
	if r.MaxTokens != 0 && (r.MaxTokens < int32(maxTokensMin) || r.MaxTokens > int32(maxTokensMax)) {
		return fmt.Errorf("max_tokens must be between %d and %d", maxTokensMin, maxTokensMax)
	}
	return nil
}

func validateStreamPrompt(req interface{}) error {
	r, ok := req.(*pb.StreamPromptRequest)
	if !ok {
		return nil
	}
	if len(r.Prompt) > maxPromptBytes {
		return fmt.Errorf("prompt exceeds maximum size of %d bytes", maxPromptBytes)
	}
	if r.MaxTokens != 0 && (r.MaxTokens < int32(maxTokensMin) || r.MaxTokens > int32(maxTokensMax)) {
		return fmt.Errorf("max_tokens must be between %d and %d", maxTokensMin, maxTokensMax)
	}
	return nil
}

func validateSaveSession(req interface{}) error {
	r, ok := req.(*pb.SaveSessionRequest)
	if !ok {
		return nil
	}
	return validateSessionNameStr(r.Name)
}

func validateSessionName(req interface{}) error {
	switch r := req.(type) {
	case *pb.LoadSessionRequest:
		return validateSessionNameStr(r.Name)
	case *pb.DeleteSessionRequest:
		return validateSessionNameStr(r.Name)
	}
	return nil
}

func validateSessionNameStr(name string) error {
	if name == "" {
		return fmt.Errorf("session name is required")
	}
	if len(name) > maxSessionNameLen {
		return fmt.Errorf("session name exceeds maximum length of %d characters", maxSessionNameLen)
	}
	if !sessionNameRegex.MatchString(name) {
		return fmt.Errorf("session name contains invalid characters (only alphanumeric, dash, underscore, dot allowed)")
	}
	return nil
}

func validateExecutePlugin(req interface{}) error {
	r, ok := req.(*pb.ExecuteRemotePluginRequest)
	if !ok {
		return nil
	}
	if r.PluginName == "" {
		return fmt.Errorf("plugin_name is required")
	}
	if len(r.PluginName) > maxPluginNameLen {
		return fmt.Errorf("plugin_name exceeds maximum length of %d characters", maxPluginNameLen)
	}
	if len(r.Args) > maxPluginArgs {
		return fmt.Errorf("too many plugin arguments (max %d)", maxPluginArgs)
	}
	for i, arg := range r.Args {
		if len(arg) > maxPluginArgBytes {
			return fmt.Errorf("plugin argument %d exceeds maximum size of %d bytes", i, maxPluginArgBytes)
		}
	}
	return nil
}

func validateDownloadPlugin(req interface{}) error {
	r, ok := req.(*pb.DownloadPluginRequest)
	if !ok {
		return nil
	}
	if r.PluginName == "" {
		return fmt.Errorf("plugin_name is required")
	}
	if len(r.PluginName) > maxPluginNameLen {
		return fmt.Errorf("plugin_name exceeds maximum length of %d characters", maxPluginNameLen)
	}
	return nil
}

func validateAnalyzeIssue(req interface{}) error {
	r, ok := req.(*pb.AnalyzeIssueRequest)
	if !ok {
		return nil
	}
	if len(r.IssueName) > maxIssueNameBytes {
		return fmt.Errorf("issue_name exceeds maximum size of %d bytes", maxIssueNameBytes)
	}
	if len(r.Description) > maxDescriptionBytes {
		return fmt.Errorf("description exceeds maximum size of %d bytes", maxDescriptionBytes)
	}
	if len(r.KubernetesContext) > maxK8sContextBytes {
		return fmt.Errorf("kubernetes_context exceeds maximum size of %d bytes", maxK8sContextBytes)
	}
	return nil
}
