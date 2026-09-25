/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"context"
	"crypto/x509"
	"os"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// mtlsRoleEnv names the role granted to a caller identified by its client
// certificate alone (no bearer token). Defaults to "user": a certificate
// proves who the caller is, not that it may administer the server.
const mtlsRoleEnv = "CHATCLI_MTLS_ROLE"

// mtlsRoleFromEnv resolves CHATCLI_MTLS_ROLE with least privilege on an
// unrecognized value.
func mtlsRoleFromEnv() UserRole {
	raw := strings.TrimSpace(os.Getenv(mtlsRoleEnv))
	if raw == "" {
		return RoleUser
	}
	role, _ := ParseRoleStrict(raw)
	return role
}

// EnableMTLSIdentity maps a verified client certificate to a principal.
// Only meaningful when the listener requires and verifies client
// certificates (--tls-client-ca): the TLS handshake already rejected any
// peer without a certificate the CA signed, so what remains is naming it.
// The subject is the certificate's Common Name, or its first URI or DNS
// SAN when the CN is empty, prefixed with "mtls:"; role is the one given.
func (a *TokenAuthInterceptor) EnableMTLSIdentity(role UserRole) {
	a.mtlsIdentity = true
	a.mtlsRole = role
}

// certIdentity returns the principal a verified client certificate names,
// or nil when the peer presented none (or identity mapping is off).
func (a *TokenAuthInterceptor) certIdentity(ctx context.Context) *UserInfo {
	if !a.mtlsIdentity {
		return nil
	}
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil
	}
	cert := verifiedLeaf(tlsInfo)
	if cert == nil {
		return nil
	}
	subject := certSubject(cert)
	if subject == "" {
		return nil
	}
	return &UserInfo{Subject: "mtls:" + subject, Role: a.mtlsRole}
}

// verifiedLeaf returns the leaf of the first verified chain. Only chains
// the listener verified against its client CA count; a certificate the
// peer merely presented (ClientAuth below RequireAndVerifyClientCert) does
// not identify anyone.
func verifiedLeaf(info credentials.TLSInfo) *x509.Certificate {
	if len(info.State.VerifiedChains) == 0 || len(info.State.VerifiedChains[0]) == 0 {
		return nil
	}
	return info.State.VerifiedChains[0][0]
}

// certSubject picks the stable name of a certificate: CN, then the first
// URI SAN (SPIFFE ids live there), then the first DNS SAN.
func certSubject(cert *x509.Certificate) string {
	if cn := strings.TrimSpace(cert.Subject.CommonName); cn != "" {
		return cn
	}
	if len(cert.URIs) > 0 && cert.URIs[0] != nil {
		return cert.URIs[0].String()
	}
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0]
	}
	return ""
}
