/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package providerproto defines the wire contract between chatcli and
// provider plugins: external executables that add an LLM provider which
// then behaves like a built-in one.
//
// The protocol is JSON-RPC 2.0, one message per line over the plugin's
// stdin/stdout. Conn implements that transport for both ends. The types in
// this package are the message schema; they deliberately do not depend on
// chatcli's internal models, so plugins compiled against this package only
// rely on a small, versioned contract (ProtocolVersion).
//
// SPEC.md in this directory is the language-agnostic specification. Go
// plugins can use pkg/providersdk; pkg/providerproto/conformance (and
// `chatcli provider test`) checks any implementation.
package providerproto
