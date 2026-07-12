//go:build !windows

/*
 * ChatCLI - system locale detection (non-Windows no-op)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package i18n

// systemLocale is a no-op outside Windows: Unix shells export LANG/LC_ALL,
// which the detection chain already consumes.
func systemLocale() string { return "" }
