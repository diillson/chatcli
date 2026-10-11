//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import "io/fs"

// ownedByCurrentUser: Windows file modes carry no owner. The state root
// lives in the user profile, whose ACL already admits only that user.
func ownedByCurrentUser(fs.FileInfo) bool { return true }

// restrictToOwner is a no-op on Windows: os.Chmod only toggles the
// read-only attribute there, and access is governed by the profile's ACL.
func restrictToOwner(string, fs.FileMode, bool) error { return nil }
