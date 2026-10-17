/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	stderrors "errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Operator-managed encryption-at-rest key.
//
// Unless spec.features.encryptionKeyRef names a key, spec.extraEnv carries
// CHATCLI_ENCRYPTION_KEY, or spec.features.encryptionAtRest is false, every
// Instance gets a Secret <instance>-atrest-key holding a random key. The
// server is pointed at it through CHATCLI_ENCRYPTION_KEY_SECRET and reads it
// at startup with its own service account, which a Role allows to get that
// one Secret. The operator creates the Secret before the Deployment, so the
// server never needs create; and it never updates an existing one, because
// replacing the key makes everything sealed with it unreadable.

const (
	// atrestKeySecretSuffix names the key Secret after the Instance.
	atrestKeySecretSuffix = "-atrest-key"
	// atrestKeyDataKey is the Secret entry the server reads.
	atrestKeyDataKey = "encryption-key"
	// envEncryptionKey is the variable a directly supplied key arrives in.
	envEncryptionKey = "CHATCLI_ENCRYPTION_KEY"
	// envEncryptionKeySecretName points the server at the key Secret.
	envEncryptionKeySecretName = "CHATCLI_ENCRYPTION_KEY_SECRET" // #nosec G101 -- the name of an environment variable, not a credential
	// atrestKeyBytes is the size of a generated key before base64.
	atrestKeyBytes = 32
	// atrestKeyCreatedAnnotation records on the Instance that the operator
	// created its key Secret. With it present, a missing Secret is a lost
	// key, not a first run: the operator does not create another one.
	atrestKeyCreatedAnnotation = "platform.chatcli.io/atrest-key-created"
)

// errAtrestKeyLost reports a key Secret that was created once and is gone.
var errAtrestKeyLost = stderrors.New("the encryption-at-rest key Secret this operator created is missing; " +
	"a new key could not open the data sealed with it, so none is created: restore the Secret from a backup, " +
	"or, to start over and discard that data, remove the " + atrestKeyCreatedAnnotation + " annotation from the Instance")

// atrestKeySecretName is the key Secret (and its Role and RoleBinding) of
// an Instance.
func atrestKeySecretName(inst *platformv1alpha1.Instance) string {
	return inst.Name + atrestKeySecretSuffix
}

// operatorManagedAtrestKey reports whether the operator provides the
// Instance's encryption-at-rest key.
func operatorManagedAtrestKey(inst *platformv1alpha1.Instance) bool {
	if f := inst.Spec.Features; f != nil {
		if f.EncryptionKeyRef != nil {
			return false
		}
		if f.EncryptionAtRest != nil && !*f.EncryptionAtRest {
			return false
		}
	}
	for _, e := range inst.Spec.ExtraEnv {
		if e.Name == envEncryptionKey {
			return false
		}
	}
	return true
}

// atrestKeyEnv is the server variable of the operator-managed key, nil when
// the key comes from elsewhere (or encryption at rest is off).
func atrestKeyEnv(inst *platformv1alpha1.Instance) []corev1.EnvVar {
	if !operatorManagedAtrestKey(inst) {
		return nil
	}
	return []corev1.EnvVar{{Name: envEncryptionKeySecretName, Value: atrestKeySecretName(inst)}}
}

// reconcileAtrestKey ensures the key Secret and the Role that lets the
// server read it. When the operator does not manage the key, the Role and
// RoleBinding are removed (the Secret is kept: it may still be the only
// way to read data sealed with it).
func (r *InstanceReconciler) reconcileAtrestKey(ctx context.Context, instance *platformv1alpha1.Instance) error {
	if !operatorManagedAtrestKey(instance) {
		return r.removeAtrestKeyRBAC(ctx, instance)
	}
	if err := r.ensureAtrestKeySecret(ctx, instance); err != nil {
		return fmt.Errorf("encryption-at-rest key Secret %s/%s: %w", instance.Namespace, atrestKeySecretName(instance), err)
	}
	return r.reconcileAtrestKeyRBAC(ctx, instance)
}

// ensureAtrestKeySecret creates the key Secret with a random key when it
// does not exist. An existing Secret is never modified, whatever it holds.
func (r *InstanceReconciler) ensureAtrestKeySecret(ctx context.Context, instance *platformv1alpha1.Instance) error {
	name := atrestKeySecretName(instance)
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: instance.Namespace}, &existing)
	if err == nil {
		// Upgrades from a version that did not record it yet.
		return r.markAtrestKeyCreated(ctx, instance)
	}
	if !errors.IsNotFound(err) {
		return err
	}
	if instance.Annotations[atrestKeyCreatedAnnotation] != "" {
		return errAtrestKeyLost
	}

	raw := make([]byte, atrestKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	secretLabels := labels(instance)
	secretLabels["app.kubernetes.io/component"] = "atrest-key"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: instance.Namespace,
			Labels:    secretLabels,
			Annotations: map[string]string{
				"chatcli.io/atrest-key": "Encryption-at-rest key of the ChatCLI server. Deleting or changing it makes the data the server encrypted unreadable.",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			atrestKeyDataKey: []byte(base64.StdEncoding.EncodeToString(raw)),
		},
	}
	// Same lifecycle as the sessions PVC: the key goes when the Instance
	// (and with it the data it protects) goes.
	if err := controllerutil.SetControllerReference(instance, secret, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, secret); err != nil {
		// A stale cache read: the Secret exists, and is left alone.
		if errors.IsAlreadyExists(err) {
			return r.markAtrestKeyCreated(ctx, instance)
		}
		return err
	}
	log.FromContext(ctx).Info("Created the encryption-at-rest key Secret", "secret", name)
	return r.markAtrestKeyCreated(ctx, instance)
}

// markAtrestKeyCreated records on the Instance that its key Secret exists,
// so a later deletion of the Secret is recognized as a lost key.
func (r *InstanceReconciler) markAtrestKeyCreated(ctx context.Context, instance *platformv1alpha1.Instance) error {
	if instance.Annotations[atrestKeyCreatedAnnotation] != "" {
		return nil
	}
	patch := client.MergeFrom(instance.DeepCopy())
	if instance.Annotations == nil {
		instance.Annotations = map[string]string{}
	}
	instance.Annotations[atrestKeyCreatedAnnotation] = atrestKeySecretName(instance)
	return r.Patch(ctx, instance, patch)
}

// atrestKeyPolicyRules lets the server get its key Secret and nothing else.
func atrestKeyPolicyRules(instance *platformv1alpha1.Instance) []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{{
		APIGroups:     []string{""},
		Resources:     []string{"secrets"},
		ResourceNames: []string{atrestKeySecretName(instance)},
		Verbs:         []string{"get"},
	}}
}

// reconcileAtrestKeyRBAC binds the Instance ServiceAccount to the Role
// that reads the key Secret.
func (r *InstanceReconciler) reconcileAtrestKeyRBAC(ctx context.Context, instance *platformv1alpha1.Instance) error {
	name := atrestKeySecretName(instance)
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: instance.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		if err := controllerutil.SetControllerReference(instance, role, r.Scheme); err != nil {
			return err
		}
		role.Labels = labels(instance)
		role.Rules = atrestKeyPolicyRules(instance)
		return nil
	}); err != nil {
		return err
	}

	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: instance.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		if err := controllerutil.SetControllerReference(instance, rb, r.Scheme); err != nil {
			return err
		}
		rb.Labels = labels(instance)
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     role.Name,
		}
		rb.Subjects = []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      instance.Name,
			Namespace: instance.Namespace,
		}}
		return nil
	})
	return err
}

// removeAtrestKeyRBAC deletes the key Role and RoleBinding left by an
// earlier operator-managed configuration. Only objects this Instance
// controls are deleted: a Role of the same name someone else created stays.
func (r *InstanceReconciler) removeAtrestKeyRBAC(ctx context.Context, instance *platformv1alpha1.Instance) error {
	key := types.NamespacedName{Name: atrestKeySecretName(instance), Namespace: instance.Namespace}
	for _, obj := range []client.Object{&rbacv1.RoleBinding{}, &rbacv1.Role{}} {
		if err := r.Get(ctx, key, obj); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			return err
		}
		if !metav1.IsControlledBy(obj, instance) {
			continue
		}
		if err := r.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}
