/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

func peerWithCert(ctx context.Context, cert *x509.Certificate, verified bool) context.Context {
	state := tls.ConnectionState{}
	if cert != nil {
		state.PeerCertificates = []*x509.Certificate{cert}
		if verified {
			state.VerifiedChains = [][]*x509.Certificate{{cert}}
		}
	}
	return peer.NewContext(ctx, &peer.Peer{
		Addr:     &net.TCPAddr{IP: net.IPv4(10, 0, 0, 7), Port: 4242},
		AuthInfo: credentials.TLSInfo{State: state},
	})
}

func TestCertIdentity_NamesTheVerifiedCertificate(t *testing.T) {
	a := NewTokenAuthInterceptor("", zap.NewNop())
	cn := &x509.Certificate{Subject: pkix.Name{CommonName: "operator.chatcli"}}
	assert.Nil(t, a.certIdentity(peerWithCert(context.Background(), cn, true)), "off until enabled")

	a.EnableMTLSIdentity(RoleUser)
	u := a.certIdentity(peerWithCert(context.Background(), cn, true))
	require.NotNil(t, u)
	assert.Equal(t, "mtls:operator.chatcli", u.Subject)
	assert.Equal(t, RoleUser, u.Role)

	spiffe, _ := url.Parse("spiffe://cluster.local/ns/chatcli/sa/operator")
	san := &x509.Certificate{URIs: []*url.URL{spiffe}, DNSNames: []string{"op.example"}}
	assert.Equal(t, "mtls:spiffe://cluster.local/ns/chatcli/sa/operator", a.certIdentity(peerWithCert(context.Background(), san, true)).Subject, "URI SAN when CN is empty")
	dns := &x509.Certificate{DNSNames: []string{"op.example"}}
	assert.Equal(t, "mtls:op.example", a.certIdentity(peerWithCert(context.Background(), dns, true)).Subject)

	assert.Nil(t, a.certIdentity(peerWithCert(context.Background(), cn, false)), "a presented but unverified certificate names nobody")
	assert.Nil(t, a.certIdentity(peerWithCert(context.Background(), &x509.Certificate{}, true)), "a certificate with no name names nobody")
	assert.Nil(t, a.certIdentity(context.Background()), "no peer")
	assert.Nil(t, a.certIdentity(peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{}})), "no TLS")
}

func TestAuthorize_CertificateAloneIsAPrincipalAndBearerStillWins(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "")
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", "")
	a := NewTokenAuthInterceptor("shared", zap.NewNop())
	a.EnableMTLSIdentity(RoleViewer)
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "reader"}}

	// Certificate, no token: the certificate's principal with the mTLS role.
	ctx, err := a.authorize(peerWithCert(context.Background(), cert, true), "/chatcli.v1.ChatCLIService/GetServerInfo")
	require.NoError(t, err)
	u := UserFromContext(ctx)
	require.NotNil(t, u)
	assert.Equal(t, "mtls:reader", u.Subject)
	assert.Equal(t, RoleViewer, u.Role)

	// Certificate plus the shared token: the token's role (admin) wins.
	md := metadata.New(map[string]string{"authorization": "Bearer shared"})
	ctx, err = a.authorize(metadata.NewIncomingContext(peerWithCert(context.Background(), cert, true), md), "/x/Y")
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, UserFromContext(ctx).Role)

	// Certificate plus a WRONG token: refused, the certificate does not rescue a bad bearer.
	md = metadata.New(map[string]string{"authorization": "Bearer nope"})
	_, err = a.authorize(metadata.NewIncomingContext(peerWithCert(context.Background(), cert, true), md), "/x/Y")
	assert.Error(t, err)

	// No certificate, no token: refused.
	_, err = a.authorize(peerWithCert(context.Background(), nil, false), "/x/Y")
	assert.Error(t, err)
}

func TestAuthorize_MTLSOnlyServerDoesNotFallBackToAnonymousAdmin(t *testing.T) {
	t.Setenv("CHATCLI_JWT_SECRET", "")
	t.Setenv("CHATCLI_JWT_PUBLIC_KEY", "")
	a := NewTokenAuthInterceptor("", zap.NewNop())
	a.EnableMTLSIdentity(RoleUser)
	_, err := a.authorize(peerWithCert(context.Background(), nil, false), "/x/Y")
	assert.Error(t, err, "with identity mapping on, a peer without a verified certificate is not the anonymous admin")
	ctx, err := a.authorize(peerWithCert(context.Background(), nil, false), "/chatcli.v1.ChatCLIService/Health")
	require.NoError(t, err, "health stays open")
	assert.Nil(t, UserFromContext(ctx))
}

func TestMTLSRoleFromEnv(t *testing.T) {
	t.Setenv(mtlsRoleEnv, "")
	assert.Equal(t, RoleUser, mtlsRoleFromEnv())
	t.Setenv(mtlsRoleEnv, "admin")
	assert.Equal(t, RoleAdmin, mtlsRoleFromEnv())
	t.Setenv(mtlsRoleEnv, "viewer")
	assert.Equal(t, RoleViewer, mtlsRoleFromEnv())
	t.Setenv(mtlsRoleEnv, "root")
	assert.Equal(t, RoleReadonly, mtlsRoleFromEnv(), "unknown role is least privilege")
}
