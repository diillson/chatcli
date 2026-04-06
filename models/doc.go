// Package models defines the shared data types used across all ChatCLI
// components — CLI, server, operator, and LLM providers.
//
// # Core Types
//
//   - Message: A single conversation message with role, content, and metadata.
//   - SessionData: Complete session state including chat history, agent history,
//     and tool call scoped history.
//   - ToolDefinition: Describes a tool available to the LLM (name, description,
//     parameters as JSON Schema).
//   - ToolCall: A tool invocation requested by the LLM.
//   - LLMResponse: The response from an LLM provider including content,
//     tool calls, and usage statistics.
//
// These types form the contract between LLM providers, the CLI interface,
// the gRPC server, and the Kubernetes operator.
package models
