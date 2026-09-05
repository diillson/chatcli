//go:build windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows Credential Manager, through the advapi32 credential API.
//
// There is no CLI equivalent to reach for here: cmdkey can create and list
// credentials but never reveals a secret, so the API is the only way to
// read one back. The calls used are the generic-credential trio —
// CredReadW, CredWriteW, CredDeleteW — plus CredFree for the buffer
// CredReadW allocates.

var (
	advapi32          = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW     = advapi32.NewProc("CredReadW")
	procCredWriteW    = advapi32.NewProc("CredWriteW")
	procCredDeleteW   = advapi32.NewProc("CredDeleteW")
	procCredFree      = advapi32.NewProc("CredFree")
	errCredentialGone = fmt.Errorf("credential not found")
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

// winCredential mirrors the CREDENTIALW structure. Field order and width
// are load-bearing: the struct is passed to and read from the Windows API
// verbatim.
type winCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// credentialTarget is the name a ChatCLI secret is filed under. Namespacing
// by service keeps ChatCLI's entries identifiable in the Credential Manager
// UI and away from anything else the user stores.
func credentialTarget(account string) string {
	return keychainServiceName + ":" + account
}

// nativeAvailable reports whether the credential API can be reached. A
// machine where advapi32 cannot be loaded — a stripped container image, an
// unusual Windows edition — falls back to the file backend rather than
// failing.
func nativeAvailable() bool {
	return advapi32.Load() == nil &&
		procCredReadW.Find() == nil &&
		procCredWriteW.Find() == nil &&
		procCredDeleteW.Find() == nil
}

func platformGet(account string) ([]byte, error) {
	target, err := windows.UTF16PtrFromString(credentialTarget(account))
	if err != nil {
		return nil, err
	}

	var cred *winCredential
	ret, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeGeneric),
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	if ret == 0 {
		return nil, fmt.Errorf("credential read failed: %w", callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred))) //nolint:errcheck // CredFree has no failure mode worth handling

	if cred == nil || cred.CredentialBlob == nil || cred.CredentialBlobSize == 0 {
		return nil, errCredentialGone
	}

	// Copy out of the API-owned buffer before CredFree reclaims it.
	blob := unsafe.Slice(cred.CredentialBlob, cred.CredentialBlobSize)
	out := make([]byte, len(blob))
	copy(out, blob)
	return out, nil
}

func platformSet(account string, data []byte) error {
	target, err := windows.UTF16PtrFromString(credentialTarget(account))
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString(account)
	if err != nil {
		return err
	}

	cred := winCredential{
		Type:       credTypeGeneric,
		TargetName: target,
		// Local machine rather than roaming: an encryption key that follows
		// a roaming profile onto other machines is a wider secret than the
		// one the user asked to store.
		Persist:  credPersistLocalMachine,
		UserName: user,
	}
	if len(data) > 0 {
		cred.CredentialBlobSize = uint32(len(data))
		cred.CredentialBlob = &data[0]
	}

	ret, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if ret == 0 {
		return fmt.Errorf("credential write failed: %w", callErr)
	}
	return nil
}

func platformDelete(account string) error {
	target, err := windows.UTF16PtrFromString(credentialTarget(account))
	if err != nil {
		return err
	}
	ret, _, callErr := procCredDeleteW.Call(
		uintptr(unsafe.Pointer(target)),
		uintptr(credTypeGeneric),
		0,
	)
	if ret == 0 {
		return fmt.Errorf("credential delete failed: %w", callErr)
	}
	return nil
}
