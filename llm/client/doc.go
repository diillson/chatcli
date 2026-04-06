// Package client defines the LLMClient interface that all LLM providers
// must implement, along with shared types for tool calling, streaming,
// and metrics instrumentation.
//
// The LLMClient interface is the core abstraction that enables ChatCLI to
// support 11 different LLM providers through a single unified API.
//
// # Interface
//
//   - LLMClient: SendPrompt, GetModelName, ListModels, Close
//   - InstrumentedClient: Wraps any LLMClient with Prometheus metrics
//
// # Tool Calling
//
// Tool definitions and tool call results are passed through the LLMClient
// interface. Each provider implements tool calling via its native API
// (OpenAI function calling, Anthropic tool use, etc.) or falls back to
// XML-based tool calling for providers without native support.
package client
