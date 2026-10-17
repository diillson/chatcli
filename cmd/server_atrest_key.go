/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/atrest"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Encryption at rest for a server running in Kubernetes.
//
// `chatcli server` never invents a key on its own: a key generated inside a
// container dies with the container while the stores it sealed live on a
// persistent volume. CHATCLI_ENCRYPTION_KEY_SECRET names a Secret in the
// pod's own namespace that holds the key instead. The Helm chart and the
// operator set it so encryption at rest is on by default, and the key
// outlives every pod that uses it.

const (
	// envEncryptionKeySecretName names the Secret (in the pod's namespace) that
	// holds the encryption-at-rest key. It is read only when
	// CHATCLI_ENCRYPTION_KEY is empty, so an injected key always wins.
	envEncryptionKeySecretName = "CHATCLI_ENCRYPTION_KEY_SECRET"

	// atrestSecretDataKey is the Secret data entry that carries the key.
	atrestSecretDataKey = "encryption-key"

	// serviceAccountNamespaceFile is where the kubelet mounts the pod's
	// namespace next to the service account token.
	serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

	// atrestKeyBytes is the size of a generated key before base64.
	atrestKeyBytes = 32

	// atrestSecretTimeout bounds the startup round trips to the API server.
	atrestSecretTimeout = 30 * time.Second
)

var (
	errAtrestNoNamespace = errors.New("pod namespace unknown")
	errAtrestNoClient    = errors.New("no in-cluster Kubernetes client")
	errAtrestAPI         = errors.New("kubernetes API error")
	errAtrestEmptyKey    = errors.New("encryption key entry empty or missing")
)

// atrestSecretsAPI is the slice of the typed Secrets client the resolver
// uses; corev1 SecretInterface satisfies it, and so does the fake
// clientset in the tests.
type atrestSecretsAPI interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.Secret, error)
	Create(ctx context.Context, secret *corev1.Secret, opts metav1.CreateOptions) (*corev1.Secret, error)
}

// atrestKeyDeps are the environment-facing pieces of the startup step, so
// the tests can replace the namespace file and the in-cluster client.
type atrestKeyDeps struct {
	namespaceFile string
	secretsFor    func(namespace string) (atrestSecretsAPI, error)
}

// defaultAtrestKeyDeps reads the real namespace file and builds an
// in-cluster clientset.
func defaultAtrestKeyDeps() atrestKeyDeps {
	return atrestKeyDeps{
		namespaceFile: serviceAccountNamespaceFile,
		secretsFor:    inClusterSecrets,
	}
}

// inClusterSecrets builds the Secrets client from the pod's service
// account. There is no kubeconfig fallback on purpose: the key belongs to
// the cluster the server runs in, never to whatever context a developer's
// kubeconfig points at.
func inClusterSecrets(namespace string) (atrestSecretsAPI, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return cs.CoreV1().Secrets(namespace), nil
}

// ensureServerAtrestKey loads the encryption-at-rest key from the Secret
// named by CHATCLI_ENCRYPTION_KEY_SECRET into CHATCLI_ENCRYPTION_KEY, before
// any store opens. It does nothing when the variable is unset or a key was
// injected directly. With the variable set, every failure is fatal: the
// server never starts without the key it was told to use, because writing
// plaintext next to sealed data is worse than not starting.
func ensureServerAtrestKey(ctx context.Context, logger *zap.Logger, deps atrestKeyDeps) error {
	secretName := strings.TrimSpace(os.Getenv(envEncryptionKeySecretName))
	if secretName == "" || strings.TrimSpace(os.Getenv(atrest.EnvKey)) != "" {
		return nil
	}

	namespace, err := podNamespace(deps.namespaceFile)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("cmd.server.atrest_secret.no_namespace", secretName), errors.Join(errAtrestNoNamespace, err))
	}
	secrets, err := deps.secretsFor(namespace)
	if err != nil {
		return fmt.Errorf("%s: %w", i18n.T("cmd.server.atrest_secret.no_client", namespace, secretName), errors.Join(errAtrestNoClient, err))
	}

	ctx, cancel := context.WithTimeout(ctx, atrestSecretTimeout)
	defer cancel()
	key, created, err := resolveAtrestKey(ctx, secrets, namespace, secretName)
	if err != nil {
		return err
	}
	if err := os.Setenv(atrest.EnvKey, key); err != nil {
		return err
	}
	if logger != nil {
		// The Secret's coordinates and the key fingerprint only; never the key.
		logger.Info("encryption at rest: key loaded from Kubernetes Secret",
			zap.String("namespace", namespace),
			zap.String("secret", secretName),
			zap.Bool("created", created),
			zap.String("fingerprint", atrest.KeyFingerprint()),
		)
	}
	return nil
}

// podNamespace is POD_NAMESPACE when set (the downward API), otherwise the
// namespace the kubelet mounted with the service account token.
func podNamespace(namespaceFile string) (string, error) {
	if ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE")); ns != "" {
		return ns, nil
	}
	raw, err := os.ReadFile(namespaceFile) // #nosec G304 -- fixed kubelet path, overridable only by tests
	if err != nil {
		return "", err
	}
	ns := strings.TrimSpace(string(raw))
	if ns == "" {
		return "", fmt.Errorf("%s is empty", namespaceFile)
	}
	return ns, nil
}

// resolveAtrestKey returns the key stored in the Secret, creating the
// Secret with a fresh random key when it does not exist. It never updates
// an existing Secret: a missing or empty entry is an error, because
// replacing a key makes everything sealed with it unreadable. created
// reports whether this call created the Secret.
func resolveAtrestKey(ctx context.Context, secrets atrestSecretsAPI, namespace, name string) (key string, created bool, err error) {
	sec, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		key, err = atrestKeyFromSecret(sec, namespace, name)
		return key, false, err
	}
	if !apierrors.IsNotFound(err) {
		return "", false, atrestAPIError(namespace, name, err)
	}

	fresh, err := newAtrestSecret(name)
	if err != nil {
		return "", false, err
	}
	sec, err = secrets.Create(ctx, fresh, metav1.CreateOptions{})
	switch {
	case err == nil:
		key, err = atrestKeyFromSecret(sec, namespace, name)
		return key, err == nil, err
	case apierrors.IsAlreadyExists(err):
		// Another replica created it between our Get and Create: its key
		// is the one every replica must share.
		sec, err = secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", false, atrestAPIError(namespace, name, err)
		}
		key, err = atrestKeyFromSecret(sec, namespace, name)
		return key, false, err
	default:
		return "", false, atrestAPIError(namespace, name, err)
	}
}

// atrestKeyFromSecret extracts the key entry, refusing an empty one.
func atrestKeyFromSecret(sec *corev1.Secret, namespace, name string) (string, error) {
	var key string
	if sec != nil {
		key = strings.TrimSpace(string(sec.Data[atrestSecretDataKey]))
		if key == "" {
			key = strings.TrimSpace(sec.StringData[atrestSecretDataKey])
		}
	}
	if key == "" {
		return "", fmt.Errorf("%s: %w", i18n.T("cmd.server.atrest_secret.empty_key", namespace, name, atrestSecretDataKey), errAtrestEmptyKey)
	}
	return key, nil
}

// atrestAPIError names the Secret and the RBAC the service account needs.
func atrestAPIError(namespace, name string, err error) error {
	return fmt.Errorf("%s: %w", i18n.T("cmd.server.atrest_secret.api_failed", namespace, name), errors.Join(errAtrestAPI, err))
}

// newAtrestSecret builds the Secret holding a fresh 32-byte random key.
func newAtrestSecret(name string) (*corev1.Secret, error) {
	raw := make([]byte, atrestKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "chatcli-server",
				"app.kubernetes.io/component":  "atrest-key",
			},
			Annotations: map[string]string{
				"chatcli.io/atrest-key": "Encryption-at-rest key of the ChatCLI server. Deleting or changing it makes the data the server encrypted unreadable.",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			atrestSecretDataKey: []byte(base64.StdEncoding.EncodeToString(raw)),
		},
	}, nil
}
