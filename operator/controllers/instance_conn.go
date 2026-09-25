/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.uber.org/zap"
)

// instanceAddress is the in-cluster gRPC address of an Instance's Service.
func instanceAddress(inst *platformv1alpha1.Instance) string {
	port := inst.Spec.Server.Port
	if port == 0 {
		port = 50051
	}
	return fmt.Sprintf("dns:///%s.%s.svc.cluster.local:%d", inst.Name, inst.Namespace, port)
}

// instanceConnectionOpts reads the transport and credential configuration
// of an Instance and its referenced Secrets. One function for every
// operator path that dials an Instance (the alerts bridge, the status
// probe), so the credential precedence documented on ServerSecuritySpec is
// decided in exactly one place:
//
//  1. spec.server.token — the shared token;
//  2. spec.server.security.operatorTokenRef — a credential issued for the
//     operator (an external JWT for RS256 servers, or a dedicated token);
//  3. spec.server.security.jwtSecretRef — the server's HS256 secret, from
//     which the operator mints its own short-lived tokens;
//  4. spec.server.security.operatorClientCertSecretName — the operator's
//     client certificate, its whole identity on an mTLS server.
//
// A client certificate is loaded whenever it is configured, on top of any
// bearer credential: the TLS handshake needs it when the server verifies
// client certificates, whichever role the bearer carries.
func instanceConnectionOpts(ctx context.Context, c client.Client, inst *platformv1alpha1.Instance, logger *zap.Logger) (ConnectionOpts, error) {
	var opts ConnectionOpts

	if tls := inst.Spec.Server.TLS; tls != nil && tls.Enabled {
		opts.TLSEnabled = true
		if tls.SecretName != "" {
			var tlsSecret corev1.Secret
			key := types.NamespacedName{Name: tls.SecretName, Namespace: inst.Namespace}
			if err := c.Get(ctx, key, &tlsSecret); err != nil {
				logger.Warn("Failed to read TLS secret, using system CAs",
					zap.String("secret", tls.SecretName),
					zap.Error(err))
			} else if caCert, ok := tlsSecret.Data["ca.crt"]; ok {
				opts.CACert = caCert
			}
		}
	}

	sec := inst.Spec.Server.Security
	if sec != nil && sec.OperatorClientCertSecretName != "" {
		var certSecret corev1.Secret
		key := types.NamespacedName{Name: sec.OperatorClientCertSecretName, Namespace: inst.Namespace}
		if err := c.Get(ctx, key, &certSecret); err != nil {
			return opts, fmt.Errorf("reading operator client certificate secret %q: %w", sec.OperatorClientCertSecretName, err)
		}
		crt, key1 := certSecret.Data[corev1.TLSCertKey], certSecret.Data[corev1.TLSPrivateKeyKey]
		if len(crt) == 0 || len(key1) == 0 {
			return opts, fmt.Errorf("operator client certificate secret %q needs %s and %s", sec.OperatorClientCertSecretName, corev1.TLSCertKey, corev1.TLSPrivateKeyKey)
		}
		pair, err := tls.X509KeyPair(crt, key1)
		if err != nil {
			return opts, fmt.Errorf("operator client certificate secret %q: %w", sec.OperatorClientCertSecretName, err)
		}
		opts.ClientCertificate = &pair
	}

	if inst.Spec.Server.Token != nil && inst.Spec.Server.Token.Name != "" {
		token, err := readSecretKey(ctx, c, inst.Namespace, inst.Spec.Server.Token, "token")
		if err != nil {
			return opts, err
		}
		opts.Token = token
		return opts, nil
	}
	if sec == nil {
		return opts, nil
	}
	if sec.OperatorTokenRef != nil && sec.OperatorTokenRef.Name != "" {
		token, err := readSecretKey(ctx, c, inst.Namespace, sec.OperatorTokenRef, "token")
		if err != nil {
			return opts, err
		}
		opts.TokenSource = staticTokenSource(token)
		return opts, nil
	}
	if sec.JWTSecretRef != nil && sec.JWTSecretRef.Name != "" {
		secret, err := readSecretKey(ctx, c, inst.Namespace, sec.JWTSecretRef, "secret")
		if err != nil {
			return opts, err
		}
		minter, err := newJWTMinter([]byte(secret), sec.JWTIssuer, sec.JWTAudience)
		if err != nil {
			return opts, fmt.Errorf("instance %s: %w", inst.Name, err)
		}
		opts.TokenSource = minter
		return opts, nil
	}
	if opts.ClientCertificate != nil {
		// The certificate is the credential.
		return opts, nil
	}
	if sec.JWTPublicKeyRef != nil && sec.JWTPublicKeyRef.Name != "" {
		return opts, fmt.Errorf("instance %s verifies RS256 tokens (jwtPublicKeyRef) and the operator has no credential to present: set spec.server.security.operatorTokenRef or operatorClientCertSecretName", inst.Name)
	}
	return opts, nil
}

// readSecretKey returns the value of ref.Key (or defaultKey when the ref
// names none) in the referenced Secret.
func readSecretKey(ctx context.Context, c client.Client, namespace string, ref *platformv1alpha1.SecretKeyRefSpec, defaultKey string) (string, error) {
	var secret corev1.Secret
	key := types.NamespacedName{Name: ref.Name, Namespace: namespace}
	if err := c.Get(ctx, key, &secret); err != nil {
		return "", fmt.Errorf("reading secret %q: %w", ref.Name, err)
	}
	k := ref.Key
	if k == "" {
		k = defaultKey
	}
	value, ok := secret.Data[k]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %q", k, ref.Name)
	}
	return string(value), nil
}

// ProbeResult is what a server probe learned.
type ProbeResult struct {
	// Healthy is true when Health answered SERVING.
	Healthy bool
	// Version is the server's reported version, when GetServerInfo answered.
	Version string
	// Provider and Model are the server defaults, when GetServerInfo answered.
	Provider string
	Model    string
}

// Prober reaches an Instance's server and reports what it found. The
// reconciler records the outcome on the Instance status so `kubectl get
// instance` shows the version that is really running and whether the
// operator can talk to it (transport, credential and all).
type Prober interface {
	ProbeInstance(ctx context.Context, inst *platformv1alpha1.Instance) (ProbeResult, error)
}

// probeTimeout bounds one status probe; a reconcile must not hang on a
// server that is Ready for Kubernetes but not answering gRPC.
const probeTimeout = 5 * time.Second

// grpcProber dials the Instance with the operator's own credential and asks
// Health and GetServerInfo. A fresh connection per probe keeps the probe
// independent from the alerts bridge's long-lived connection.
type grpcProber struct {
	client client.Client
	logger *zap.Logger
	dial   func(address string, opts ConnectionOpts) (*ServerClient, error)
}

// NewGRPCProber returns the production prober.
func NewGRPCProber(c client.Client, logger *zap.Logger) Prober {
	p := &grpcProber{client: c, logger: logger.Named("instance-probe")}
	p.dial = func(address string, opts ConnectionOpts) (*ServerClient, error) {
		sc := NewServerClient(p.logger)
		if err := sc.Connect(address, opts); err != nil {
			return nil, err
		}
		return sc, nil
	}
	return p
}

// ProbeInstance implements Prober.
func (p *grpcProber) ProbeInstance(ctx context.Context, inst *platformv1alpha1.Instance) (ProbeResult, error) {
	opts, err := instanceConnectionOpts(ctx, p.client, inst, p.logger)
	if err != nil {
		return ProbeResult{}, err
	}
	sc, err := p.dial(instanceAddress(inst), opts)
	if err != nil {
		return ProbeResult{}, err
	}
	defer func() { _ = sc.Close() }()

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var res ProbeResult
	health, err := sc.Health(ctx)
	if err != nil {
		return res, fmt.Errorf("health: %w", err)
	}
	res.Healthy = health.GetStatus() == 0 // SERVING
	res.Version = health.GetVersion()
	info, err := sc.GetServerInfo(ctx)
	if err != nil {
		// Health is unauthenticated; GetServerInfo is not. Failing here
		// means the transport works and the credential does not.
		return res, fmt.Errorf("server info (credential check): %w", err)
	}
	if info.GetVersion() != "" {
		res.Version = info.GetVersion()
	}
	res.Provider, res.Model = info.GetProvider(), info.GetModel()
	return res, nil
}
