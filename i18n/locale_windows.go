//go:build windows

/*
 * ChatCLI - Windows system locale detection
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * cmd.exe and PowerShell do not export LANG/LC_ALL (Git Bash does, which is
 * why translation "only worked in Git Bash"). When no language env var is
 * set, ask Windows for the user's locale directly: GetUserDefaultLocaleName
 * returns an IETF tag ("pt-BR") that language.Parse consumes as-is.
 */
package i18n

import (
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// localeNameMaxLength mirrors LOCALE_NAME_MAX_LENGTH from winnls.h.
const localeNameMaxLength = 85

var procGetUserDefaultLocaleName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")

// systemLocale returns the user's Windows locale as an IETF tag, or "" when
// the API is unavailable (fallback stays English).
func systemLocale() string {
	buf := make([]uint16, localeNameMaxLength)
	// #nosec G103 -- fixed-size buffer passed to a synchronous syscall.
	n, _, _ := procGetUserDefaultLocaleName.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	if n == 0 {
		return ""
	}
	return string(utf16.Decode(buf[:n-1])) // n includes the NUL terminator
}
