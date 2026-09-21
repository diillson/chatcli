/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package pulse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The lease is how a dashboard asks every chatcli process on the machine to
// start recording, including the ones with no prompt to type into (ACP in an
// IDE, the MCP server, the gateway daemon). Whoever serves a dashboard keeps
// <root>/lease.json renewed; every process polls it and records only while
// it is unexpired. When the last dashboard goes away the lease runs out and
// all of them go quiet again on their own — nothing stays on by accident.

// DefaultLeaseTTL is how long one renewal keeps recording on.
const DefaultLeaseTTL = 30 * time.Second

type leaseFile struct {
	ExpiresAt time.Time `json:"expires_at"`
	Holder    string    `json:"holder,omitempty"`
}

// RenewLease extends recording for ttl from now, on behalf of holder.
func RenewLease(root, holder string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return err
	}
	data, err := json.Marshal(leaseFile{ExpiresAt: time.Now().Add(ttl), Holder: holder})
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(root, leaseFileName), data)
}

// ReleaseLease ends recording now instead of waiting for the TTL.
func ReleaseLease(root string) {
	_ = os.Remove(filepath.Join(root, leaseFileName))
}

// LeaseActive reports whether an unexpired lease exists. A missing or
// unreadable file is simply "no lease".
func LeaseActive(root string, now time.Time) bool {
	// #nosec G304 -- fixed file name under the pulse root
	data, err := os.ReadFile(filepath.Join(root, leaseFileName))
	if err != nil {
		return false
	}
	var l leaseFile
	if json.Unmarshal(data, &l) != nil {
		return false
	}
	return now.Before(l.ExpiresAt)
}
