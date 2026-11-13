//go:build !linux && !darwin && !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

// parkProcessStart has no cheap source on this platform (the BSDs each
// have their own process-table call). Nothing is recorded and nothing is
// compared: a reused pid reads as the owner still alive, and its park is
// kept until /parked gc removes it by age.
func parkProcessStart(int) (string, bool) { return "", false }
