/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// applyClientCAs turns a server TLS config into a mutual-TLS one: clients
// must present a certificate, and that certificate must chain to the given
// CA bundle.
//
// Every failure here is fatal to the caller by contract, never a warning.
// An operator who configured a client CA asked for "only holders of a
// certificate I issued may connect"; a server that came up accepting
// anonymous clients because the bundle failed to parse would be answering
// a question nobody asked.
func applyClientCAs(tlsConfig *tls.Config, caFile string) error {
	if caFile == "" {
		return nil
	}

	data, err := os.ReadFile(filepath.Clean(caFile)) // #nosec G304 -- operator-configured CA bundle path
	if err != nil {
		return fmt.Errorf("reading client CA bundle %q: %w", caFile, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return fmt.Errorf("client CA bundle %q contains no usable PEM certificate", caFile)
	}

	tlsConfig.ClientCAs = pool
	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	return nil
}

// clientCACertCount reports how many certificates a bundle contributed, for
// the startup log. A bundle that parsed but carries one certificate where
// the operator expected a chain of five is worth seeing at boot.
func clientCACertCount(caFile string) int {
	data, err := os.ReadFile(filepath.Clean(caFile)) // #nosec G304 -- operator-configured CA bundle path
	if err != nil {
		return 0
	}
	certs := 0
	for rest := data; len(rest) > 0; {
		var block []byte
		block, rest = nextPEMCertificate(rest)
		if block == nil {
			break
		}
		certs++
	}
	return certs
}

// nextPEMCertificate returns the next CERTIFICATE block's bytes and the
// remaining input, skipping blocks of any other type.
func nextPEMCertificate(data []byte) (block, rest []byte) {
	for len(data) > 0 {
		b, r := pem.Decode(data)
		if b == nil {
			return nil, nil
		}
		if b.Type == "CERTIFICATE" {
			return b.Bytes, r
		}
		data = r
	}
	return nil, nil
}
