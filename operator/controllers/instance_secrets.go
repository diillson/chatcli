/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"io"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Secrets an Instance references, and what a change to each one means.
//
// Three consumers read them: the server pod (env valueFrom and mounted
// volumes, read once at startup, so a change needs a rollout), the
// operator itself (the credential and trust root it dials the server
// with), and the Secret watch that decides which Instances a Secret event
// concerns. Listing every reference in one place keeps the three in step:
// a Secret the watch misses is a rotation nobody acts on.

// credentialsHashAnnotation carries the hash of the credential Secrets the
// server pod reads at startup, so rotating one rolls the pods.
const credentialsHashAnnotation = "chatcli.io/credentials-hash"

// secretKeyUse is one Secret reference and the keys of it that matter.
type secretKeyUse struct {
	name string
	keys []string
}

// podCredentialSecretUses lists the Secrets the server container reads at
// startup, beyond the two that already carry their own rollout hash
// (spec.apiKeys and spec.server.tls.secretName). Only the keys the pod
// actually consumes are listed, so an unrelated key added to a shared
// Secret does not restart the server.
func podCredentialSecretUses(inst *platformv1alpha1.Instance) []secretKeyUse {
	var uses []secretKeyUse
	addRef := func(ref *platformv1alpha1.SecretKeyRefSpec) {
		if ref != nil && ref.Name != "" {
			uses = append(uses, secretKeyUse{name: ref.Name, keys: []string{ref.Key}})
		}
	}
	addRef(inst.Spec.Server.Token)
	if sec := inst.Spec.Server.Security; sec != nil {
		addRef(sec.JWTSecretRef)
		addRef(sec.JWTPublicKeyRef)
	}
	if tls := inst.Spec.Server.TLS; tls != nil && tls.Enabled && tls.ClientCASecretName != "" {
		uses = append(uses, secretKeyUse{name: tls.ClientCASecretName, keys: []string{"ca.crt"}})
	}
	if f := inst.Spec.Features; f != nil {
		if f.CABundleSecretName != "" {
			uses = append(uses, secretKeyUse{name: f.CABundleSecretName, keys: []string{"ca.crt"}})
		}
		addRef(f.EncryptionKeyRef)
	}
	for _, e := range inst.Spec.ExtraEnv {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name != "" {
			uses = append(uses, secretKeyUse{name: e.ValueFrom.SecretKeyRef.Name, keys: []string{e.ValueFrom.SecretKeyRef.Key}})
		}
	}
	return uses
}

// operatorCredentialSecretUses lists the Secrets the operator reads to dial
// this Instance (see instanceConnectionOpts), with the default key names
// that function applies.
func operatorCredentialSecretUses(inst *platformv1alpha1.Instance) []secretKeyUse {
	var uses []secretKeyUse
	addRef := func(ref *platformv1alpha1.SecretKeyRefSpec, defaultKey string) {
		if ref == nil || ref.Name == "" {
			return
		}
		k := ref.Key
		if k == "" {
			k = defaultKey
		}
		uses = append(uses, secretKeyUse{name: ref.Name, keys: []string{k}})
	}
	if tls := inst.Spec.Server.TLS; tls != nil && tls.Enabled && tls.SecretName != "" {
		uses = append(uses, secretKeyUse{name: tls.SecretName, keys: []string{"ca.crt"}})
	}
	addRef(inst.Spec.Server.Token, "token")
	if sec := inst.Spec.Server.Security; sec != nil {
		if sec.OperatorClientCertSecretName != "" {
			uses = append(uses, secretKeyUse{name: sec.OperatorClientCertSecretName, keys: []string{corev1.TLSCertKey, corev1.TLSPrivateKeyKey}})
		}
		addRef(sec.OperatorTokenRef, "token")
		addRef(sec.JWTSecretRef, "secret")
	}
	return uses
}

// referencedSecretNames is every Secret name the Instance points at, for
// the Secret watch. A superset of the two lists above plus the Secrets
// that carry their own hash.
func referencedSecretNames(inst *platformv1alpha1.Instance) map[string]struct{} {
	names := map[string]struct{}{}
	add := func(n string) {
		if n != "" {
			names[n] = struct{}{}
		}
	}
	if inst.Spec.APIKeys != nil {
		add(inst.Spec.APIKeys.Name)
	}
	if tls := inst.Spec.Server.TLS; tls != nil {
		add(tls.SecretName)
		add(tls.ClientCASecretName)
	}
	for _, u := range podCredentialSecretUses(inst) {
		add(u.name)
	}
	for _, u := range operatorCredentialSecretUses(inst) {
		add(u.name)
	}
	return names
}

// hashSecretUses digests the referenced keys of each Secret, in the order
// given. A missing Secret or key hashes as absent, so creating it later
// changes the digest: the pod started without the value (the env refs are
// optional) and needs a restart to pick it up. Any other read error is
// returned rather than folded in, so a transient failure never changes the
// digest and never triggers a rollout on its own.
func hashSecretUses(ctx context.Context, c client.Client, namespace string, uses []secretKeyUse) (string, error) {
	if len(uses) == 0 {
		return "", nil
	}
	h := sha256.New()
	for _, u := range uses {
		if err := hashSecretUse(ctx, c, namespace, u, h); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], nil
}

func hashSecretUse(ctx context.Context, c client.Client, namespace string, u secretKeyUse, h hash.Hash) error {
	_, _ = fmt.Fprintf(h, "secret %q\n", u.name)
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Name: u.name, Namespace: namespace}, &secret); err != nil {
		if errors.IsNotFound(err) {
			_, _ = io.WriteString(h, "absent\n")
			return nil
		}
		return fmt.Errorf("reading secret %q: %w", u.name, err)
	}
	for _, k := range u.keys {
		_, _ = fmt.Fprintf(h, "key %q ", k)
		v, ok := secret.Data[k]
		if !ok {
			_, _ = io.WriteString(h, "absent\n")
			continue
		}
		sum := sha256.Sum256(v)
		_, _ = fmt.Fprintf(h, "%x\n", sum)
	}
	return nil
}

// operatorCredentialFingerprint identifies what the operator dials an
// Instance with: address, transport and the credential material. When it
// changes, a long-lived connection built from the old material is stale
// and must be rebuilt.
func operatorCredentialFingerprint(ctx context.Context, c client.Client, inst *platformv1alpha1.Instance) (string, error) {
	secrets, err := hashSecretUses(ctx, c, inst.Namespace, operatorCredentialSecretUses(inst))
	if err != nil {
		return "", err
	}
	tlsEnabled := inst.Spec.Server.TLS != nil && inst.Spec.Server.TLS.Enabled
	var issuer, audience string
	publicKeyOnly := false
	if sec := inst.Spec.Server.Security; sec != nil {
		issuer, audience = sec.JWTIssuer, sec.JWTAudience
		publicKeyOnly = sec.JWTPublicKeyRef != nil
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%t|%q|%q|%t|%s",
		instanceAddress(inst), tlsEnabled, issuer, audience, publicKeyOnly, secrets))
	return fmt.Sprintf("%x", sum), nil
}
