package controllers

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Platform role ClusterRole names. Pre-provisioned by the Helm chart / kustomize
// overlay (deploy/helm/chatcli-operator/templates/rbac.yaml). The operator only creates
// RoleBindings that reference these — it never creates or mutates the ClusterRoles
// themselves at runtime (Security H5).
const (
	RoleViewer     = "chatcli-role-viewer"
	RoleOperator   = "chatcli-role-operator"
	RoleAdmin      = "chatcli-role-admin"
	RoleSuperAdmin = "chatcli-role-superadmin"
)

type RBACManager struct {
	client client.Client
}

func NewRBACManager(c client.Client) *RBACManager {
	return &RBACManager{client: c}
}

// GrantRole creates a RoleBinding for a user in a namespace.
func (rm *RBACManager) GrantRole(ctx context.Context, username, role, namespace string) error {
	bindingName := fmt.Sprintf("%s-%s", role, username)
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bindingName,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "chatcli-operator", "platform.chatcli.io/role": role, "platform.chatcli.io/user": username},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     role,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "User", Name: username, APIGroup: "rbac.authorization.k8s.io"},
		},
	}

	existing := &rbacv1.RoleBinding{}
	err := rm.client.Get(ctx, types.NamespacedName{Name: bindingName, Namespace: namespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			return rm.client.Create(ctx, rb)
		}
		return err
	}
	existing.RoleRef = rb.RoleRef
	existing.Subjects = rb.Subjects
	return rm.client.Update(ctx, existing)
}

// RevokeRole removes a RoleBinding for a user in a namespace.
func (rm *RBACManager) RevokeRole(ctx context.Context, username, role, namespace string) error {
	bindingName := fmt.Sprintf("%s-%s", role, username)
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName, Namespace: namespace},
	}
	err := rm.client.Delete(ctx, rb)
	if errors.IsNotFound(err) {
		return nil
	}
	return err
}

// CheckPermission verifies if a user has a specific role in a namespace.
func (rm *RBACManager) CheckPermission(ctx context.Context, username, role, namespace string) (bool, error) {
	bindingName := fmt.Sprintf("%s-%s", role, username)
	rb := &rbacv1.RoleBinding{}
	err := rm.client.Get(ctx, types.NamespacedName{Name: bindingName, Namespace: namespace}, rb)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
