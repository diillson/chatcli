/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

// Capability advertisements for BuiltinRecallPlugin.

// IsReadOnly reports true: @recall only reads back content this session
// itself compressed. It is the way to undo tool-output compression, so a
// confirmation prompt on it would stall every coder turn whose output was
// compressed (and deny it outright where no one can answer).
func (*BuiltinRecallPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: lookups in the CCR store are independent
// reads, and the store is safe for concurrent use.
func (*BuiltinRecallPlugin) IsConcurrencySafe(_ []string) bool { return true }
