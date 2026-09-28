/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package main

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/diillson/chatcli/operator/api/rest"
)

const keysNS = "chatcli-system"

func keysSecret(yaml string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: apiKeysSecretName, Namespace: keysNS, ResourceVersion: "1"},
		Data:       map[string][]byte{apiKeysField: []byte(yaml)},
	}
}

// Item 3: keys loaded at startup stop working on the next poll once the
// Secret and the ConfigMap are both gone, even before any poll saw them.
func TestAPIKeyPoll_RemovedSourcesRevokeKeys(t *testing.T) {
	t.Setenv("POD_NAMESPACE", keysNS)
	t.Setenv("CHATCLI_OPERATOR_DEV_MODE", "")
	cs := fake.NewClientset(keysSecret("- key: key-1\n  role: operator\n  name: alice\n"))
	api := rest.NewAPIServer(nil, ":0")
	loadAPIKeysFromConfigMap(cs, api)
	if api.APIKeyCount() != 1 {
		t.Fatalf("startup load: %d keys", api.APIKeyCount())
	}

	if err := cs.CoreV1().Secrets(keysNS).Delete(context.Background(), apiKeysSecretName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	var st apiKeyWatchState
	st.poll(cs, keysNS, api)
	if api.APIKeyCount() != 0 {
		t.Fatalf("after deleting the Secret: %d keys still loaded", api.APIKeyCount())
	}
}

// Item 3: a Secret emptied of keys, or with broken YAML, revokes too.
func TestAPIKeyPoll_EmptyOrBrokenSecretRevokes(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "broken": "- key: [unclosed"} {
		cs := fake.NewClientset(keysSecret("- key: key-1\n  role: admin\n"))
		api := rest.NewAPIServer(nil, ":0")
		var st apiKeyWatchState
		st.poll(cs, keysNS, api)
		if api.APIKeyCount() != 1 {
			t.Fatalf("%s: first poll loaded %d keys", name, api.APIKeyCount())
		}
		s := keysSecret(body)
		s.ResourceVersion = "2"
		if _, err := cs.CoreV1().Secrets(keysNS).Update(context.Background(), s, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		st.poll(cs, keysNS, api)
		if api.APIKeyCount() != 0 {
			t.Fatalf("%s: %d keys still loaded", name, api.APIKeyCount())
		}
	}
}

// Item 3: a read error is not a deletion; the keys in force are kept.
func TestAPIKeyPoll_ReadErrorKeepsKeys(t *testing.T) {
	cs := fake.NewClientset(keysSecret("- key: key-1\n  role: admin\n"))
	api := rest.NewAPIServer(nil, ":0")
	var st apiKeyWatchState
	st.poll(cs, keysNS, api)
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	st.poll(cs, keysNS, api)
	if api.APIKeyCount() != 1 {
		t.Fatalf("a transient read error revoked the keys: %d", api.APIKeyCount())
	}
}

func TestParseAPIKeys_NameFallsBackToDescription(t *testing.T) {
	keys := parseAPIKeys([]byte("- key: key-1\n  role: operator\n  description: platform team\n- key: key-2\n  role: viewer\n  name: bob\n  description: ignored\n- key: key-3\n"), "1")
	if len(keys) != 2 || keys["key-1"].Name != "platform team" || keys["key-2"].Name != "bob" {
		t.Fatalf("keys = %+v", keys)
	}
}
