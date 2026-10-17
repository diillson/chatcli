/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/pkg/atrest"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testAtrestNS     = "chatcli-test"
	testAtrestSecret = "chatcli-atrest-key"
	// A low-entropy dummy: the tests only check that it is passed through.
	testAtrestKey = "dummy-key-dummy-key-dummy-key-dummy-key"
)

// atrestTestEnv sets the two variables the step reads and pins the
// namespace through POD_NAMESPACE.
func atrestTestEnv(t *testing.T, key, secret string) {
	t.Helper()
	t.Setenv(atrest.EnvKey, key)
	t.Setenv(envEncryptionKeySecretName, secret)
	t.Setenv("POD_NAMESPACE", testAtrestNS)
}

// fakeDeps wires a fake clientset into the startup step.
func fakeDeps(cs *fake.Clientset) atrestKeyDeps {
	return atrestKeyDeps{
		namespaceFile: filepath.Join(os.TempDir(), "chatcli-test-no-such-namespace-file"),
		secretsFor: func(ns string) (atrestSecretsAPI, error) {
			return cs.CoreV1().Secrets(ns), nil
		},
	}
}

func existingSecret(key string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testAtrestSecret, Namespace: testAtrestNS},
		Data:       map[string][]byte{atrestSecretDataKey: []byte(key)},
	}
}

func countActions(cs *fake.Clientset, verb string) int {
	n := 0
	for _, a := range cs.Actions() {
		if a.GetVerb() == verb {
			n++
		}
	}
	return n
}

// NotFound creates the Secret with a 32-byte random key, labels it, and
// exports the key to the process.
func TestEnsureServerAtrestKey_CreatesSecretWhenMissing(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	cs := fake.NewClientset()

	if err := ensureServerAtrestKey(context.Background(), nil, fakeDeps(cs)); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	sec, err := cs.CoreV1().Secrets(testAtrestNS).Get(context.Background(), testAtrestSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret not created: %v", err)
	}
	stored := string(sec.Data[atrestSecretDataKey])
	raw, err := base64.StdEncoding.DecodeString(stored)
	if err != nil || len(raw) != atrestKeyBytes {
		t.Fatalf("stored key must be %d random bytes in base64, got %d bytes (err %v)", atrestKeyBytes, len(raw), err)
	}
	if got := os.Getenv(atrest.EnvKey); got != stored {
		t.Fatal("CHATCLI_ENCRYPTION_KEY must carry the key stored in the Secret")
	}
	if sec.Labels["app.kubernetes.io/managed-by"] != "chatcli-server" || sec.Labels["app.kubernetes.io/component"] != "atrest-key" {
		t.Fatalf("labels: %v", sec.Labels)
	}
	if len(sec.Annotations) == 0 {
		t.Fatal("the Secret must carry the annotation warning against deleting it")
	}
}

// An existing Secret is used as is and never written.
func TestEnsureServerAtrestKey_UsesExistingSecret(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	cs := fake.NewClientset(existingSecret(testAtrestKey))

	if err := ensureServerAtrestKey(context.Background(), nil, fakeDeps(cs)); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := os.Getenv(atrest.EnvKey); got != testAtrestKey {
		t.Fatal("the existing key must be exported")
	}
	if n := countActions(cs, "create") + countActions(cs, "update") + countActions(cs, "patch"); n != 0 {
		t.Fatalf("an existing Secret must never be written, got %d writes", n)
	}
}

// Another replica wins the create race: the step re-reads and uses the
// winner's key instead of its own.
func TestEnsureServerAtrestKey_AlreadyExistsRaceRereads(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	cs := fake.NewClientset()
	gets := 0
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, testAtrestSecret)
		}
		return true, existingSecret(testAtrestKey), nil
	})
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, testAtrestSecret)
	})

	if err := ensureServerAtrestKey(context.Background(), nil, fakeDeps(cs)); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := os.Getenv(atrest.EnvKey); got != testAtrestKey {
		t.Fatal("the race winner's key must be used")
	}
	if gets != 2 {
		t.Fatalf("expected a re-read after AlreadyExists, got %d gets", gets)
	}
}

// A Secret without the key entry is refused, never overwritten.
func TestEnsureServerAtrestKey_EmptyKeyRefuses(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	cs := fake.NewClientset(existingSecret(""))

	err := ensureServerAtrestKey(context.Background(), nil, fakeDeps(cs))
	if !errors.Is(err, errAtrestEmptyKey) {
		t.Fatalf("want errAtrestEmptyKey, got %v", err)
	}
	if os.Getenv(atrest.EnvKey) != "" {
		t.Fatal("no key may be exported on failure")
	}
	if n := countActions(cs, "create") + countActions(cs, "update"); n != 0 {
		t.Fatalf("an existing Secret must never be written, got %d writes", n)
	}
}

// RBAC forbidden is fatal and names the Secret.
func TestEnsureServerAtrestKey_ForbiddenRefuses(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	cs := fake.NewClientset()
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, testAtrestSecret, errors.New("rbac"))
	})

	err := ensureServerAtrestKey(context.Background(), nil, fakeDeps(cs))
	if !errors.Is(err, errAtrestAPI) || !apierrors.IsForbidden(err) {
		t.Fatalf("want a forbidden API error, got %v", err)
	}
	if os.Getenv(atrest.EnvKey) != "" {
		t.Fatal("no key may be exported on failure")
	}
}

// Create forbidden (get allowed, create denied) is fatal too.
func TestEnsureServerAtrestKey_CreateForbiddenRefuses(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	cs := fake.NewClientset()
	cs.PrependReactor("create", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, testAtrestSecret, errors.New("rbac"))
	})
	if err := ensureServerAtrestKey(context.Background(), nil, fakeDeps(cs)); !errors.Is(err, errAtrestAPI) {
		t.Fatalf("want errAtrestAPI, got %v", err)
	}
}

// No in-cluster client is fatal when the variable is set.
func TestEnsureServerAtrestKey_NoClientRefuses(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	deps := atrestKeyDeps{secretsFor: func(string) (atrestSecretsAPI, error) {
		return nil, errors.New("not in a cluster")
	}}
	if err := ensureServerAtrestKey(context.Background(), nil, deps); !errors.Is(err, errAtrestNoClient) {
		t.Fatalf("want errAtrestNoClient, got %v", err)
	}
}

// An unknown namespace is fatal when the variable is set.
func TestEnsureServerAtrestKey_NoNamespaceRefuses(t *testing.T) {
	atrestTestEnv(t, "", testAtrestSecret)
	t.Setenv("POD_NAMESPACE", "")
	deps := fakeDeps(fake.NewClientset())
	if err := ensureServerAtrestKey(context.Background(), nil, deps); !errors.Is(err, errAtrestNoNamespace) {
		t.Fatalf("want errAtrestNoNamespace, got %v", err)
	}
}

// The namespace comes from the service account file when POD_NAMESPACE is
// unset.
func TestPodNamespace_FromServiceAccountFile(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	path := filepath.Join(t.TempDir(), "namespace")
	if err := os.WriteFile(path, []byte("team-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ns, err := podNamespace(path)
	if err != nil || ns != "team-a" {
		t.Fatalf("got %q, %v", ns, err)
	}
	t.Setenv("POD_NAMESPACE", "team-b")
	if ns, _ := podNamespace(path); ns != "team-b" {
		t.Fatalf("POD_NAMESPACE must win, got %q", ns)
	}
}

// An injected key wins: no API call at all.
func TestEnsureServerAtrestKey_EnvKeySetMakesNoAPICall(t *testing.T) {
	atrestTestEnv(t, testAtrestKey, testAtrestSecret)
	deps := atrestKeyDeps{secretsFor: func(string) (atrestSecretsAPI, error) {
		t.Fatal("no client may be built when CHATCLI_ENCRYPTION_KEY is set")
		return nil, nil
	}}
	if err := ensureServerAtrestKey(context.Background(), nil, deps); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if os.Getenv(atrest.EnvKey) != testAtrestKey {
		t.Fatal("the injected key must be left alone")
	}
}

// Without the variable the step is inert.
func TestEnsureServerAtrestKey_UnsetIsNoop(t *testing.T) {
	atrestTestEnv(t, "", "")
	deps := atrestKeyDeps{secretsFor: func(string) (atrestSecretsAPI, error) {
		t.Fatal("no client may be built without CHATCLI_ENCRYPTION_KEY_SECRET")
		return nil, nil
	}}
	if err := ensureServerAtrestKey(context.Background(), nil, deps); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if os.Getenv(atrest.EnvKey) != "" {
		t.Fatal("encryption must stay off")
	}
}
