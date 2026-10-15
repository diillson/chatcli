/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import "context"

// directUseKey marks a context whose tool call a person typed themselves
// (`chatcli tool …`), as opposed to one a model chose: the agent loop,
// squad workers and MCP/ACP clients. Tools that would spend heavily on a
// model's say-so ask for the user's confirmation only on the latter.
type directUseKey struct{}

// WithDirectUse marks ctx as a tool call made directly by the user.
func WithDirectUse(ctx context.Context) context.Context {
	return context.WithValue(ctx, directUseKey{}, true)
}

// IsDirectUse reports whether the user invoked the tool themselves.
func IsDirectUse(ctx context.Context) bool {
	v, _ := ctx.Value(directUseKey{}).(bool)
	return v
}
