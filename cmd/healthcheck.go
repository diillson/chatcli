/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/diillson/chatcli/i18n"
)

// Exit codes of `chatcli healthcheck`, the contract a container runtime or
// a script reads.
const (
	healthcheckServing    = 0
	healthcheckNotServing = 1
	healthcheckUsage      = 2
)

// healthcheckOptions is one probe: where to ask, about which service, and
// how to authenticate the connection.
type healthcheckOptions struct {
	addr       string
	service    string
	timeout    time.Duration
	serverCert string // the certificate the server serves; enables TLS
	serverName string // name to verify it by; default: its first SAN
	clientCert string // client certificate, for a server that requires mTLS
	clientKey  string
	clientCA   string // set when the server requires client certificates
}

// RunHealthcheck executes `chatcli healthcheck`: a single
// grpc.health.v1.Health/Check against a ChatCLI server, exiting 0 when it
// answers SERVING and 1 otherwise. It reads the same CHATCLI_SERVER_*
// settings as `chatcli server`, so inside the server's own container it
// needs no flags, in plaintext, TLS or mutual TLS.
//
// With TLS, the probe trusts exactly the certificate the server is
// configured to serve and verifies it by one of that certificate's names:
// full verification, expiry included, with no CA to provision and no
// verification skipped.
//
// It boots nothing beyond flag parsing and one gRPC call, because a
// container runtime runs it every few seconds on a read-only filesystem.
func RunHealthcheck(args []string, stdout, stderr io.Writer) int {
	opts, err := parseHealthcheckFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return healthcheckServing
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return healthcheckUsage
	}

	status, err := probeHealth(opts)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("healthcheck.failed", opts.addr, err))
		return healthcheckNotServing
	}
	if status != healthpb.HealthCheckResponse_SERVING {
		fmt.Fprintln(stderr, i18n.T("healthcheck.not_serving", opts.addr, status.String()))
		return healthcheckNotServing
	}
	fmt.Fprintln(stdout, i18n.T("healthcheck.serving", opts.addr))
	return healthcheckServing
}

func parseHealthcheckFlags(args []string, stderr io.Writer) (healthcheckOptions, error) {
	var opts healthcheckOptions
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.addr, "addr", "localhost:"+strconv.Itoa(getEnvInt("CHATCLI_SERVER_PORT", 50051)), i18n.T("healthcheck.flag.addr"))
	fs.StringVar(&opts.service, "service", "", i18n.T("healthcheck.flag.service"))
	fs.DurationVar(&opts.timeout, "timeout", 4*time.Second, i18n.T("healthcheck.flag.timeout"))
	fs.StringVar(&opts.serverCert, "tls-cert", os.Getenv("CHATCLI_SERVER_TLS_CERT"), i18n.T("healthcheck.flag.tls_cert"))
	fs.StringVar(&opts.serverName, "tls-server-name", "", i18n.T("healthcheck.flag.tls_server_name"))
	fs.StringVar(&opts.clientCert, "tls-client-cert", os.Getenv("CHATCLI_HEALTHCHECK_TLS_CLIENT_CERT"), i18n.T("healthcheck.flag.tls_client_cert"))
	fs.StringVar(&opts.clientKey, "tls-client-key", os.Getenv("CHATCLI_HEALTHCHECK_TLS_CLIENT_KEY"), i18n.T("healthcheck.flag.tls_client_key"))
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, errors.New(i18n.T("healthcheck.unexpected_args", fs.Args()))
	}
	if opts.timeout <= 0 {
		return opts, errors.New(i18n.T("healthcheck.bad_timeout"))
	}
	opts.clientCA = os.Getenv("CHATCLI_SERVER_TLS_CLIENT_CA")
	return opts, nil
}

// probeHealth asks the server once and returns its answer.
func probeHealth(opts healthcheckOptions) (healthpb.HealthCheckResponse_ServingStatus, error) {
	creds, err := healthcheckCredentials(opts)
	if err != nil {
		return healthpb.HealthCheckResponse_UNKNOWN, err
	}
	conn, err := grpc.NewClient(opts.addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return healthpb.HealthCheckResponse_UNKNOWN, err
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: opts.service})
	if err != nil {
		return healthpb.HealthCheckResponse_UNKNOWN, err
	}
	return resp.GetStatus(), nil
}

// healthcheckCredentials builds the transport for the probe: plaintext when
// the server serves no certificate, otherwise TLS that trusts the server's
// own certificate, presenting a client certificate when one is configured.
func healthcheckCredentials(opts healthcheckOptions) (credentials.TransportCredentials, error) {
	if opts.serverCert == "" {
		return insecure.NewCredentials(), nil
	}
	certPEM, err := os.ReadFile(filepath.Clean(opts.serverCert)) // #nosec G304 -- operator-configured certificate path, as for `chatcli server`
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("healthcheck.read_cert", opts.serverCert), err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		return nil, errors.New(i18n.T("healthcheck.no_cert", opts.serverCert))
	}
	name := opts.serverName
	if name == "" {
		if name, err = certificateName(certPEM); err != nil {
			return nil, fmt.Errorf("%s: %w", opts.serverCert, err)
		}
	}
	cfg := &tls.Config{
		RootCAs:    roots,
		ServerName: name,
		MinVersion: tls.VersionTLS13,
	}
	switch {
	case opts.clientCert != "" || opts.clientKey != "":
		pair, err := tls.LoadX509KeyPair(opts.clientCert, opts.clientKey)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T("healthcheck.client_pair"), err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	case opts.clientCA != "":
		return nil, errors.New(i18n.T("healthcheck.mtls_needs_client_cert"))
	}
	return credentials.NewTLS(cfg), nil
}

// certificateName returns a name the leaf certificate in data is valid for:
// its first DNS name, else its first IP address. Clients verify the server
// by these names, so the probe does too.
func certificateName(data []byte) (string, error) {
	for rest := data; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		if len(cert.DNSNames) > 0 {
			return cert.DNSNames[0], nil
		}
		if len(cert.IPAddresses) > 0 {
			return cert.IPAddresses[0].String(), nil
		}
		return "", errors.New(i18n.T("healthcheck.no_san"))
	}
	return "", errors.New(i18n.T("healthcheck.no_cert", ""))
}
