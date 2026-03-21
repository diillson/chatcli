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

// EnsureRoles creates or updates all ChatCLI ClusterRoles.
func (rm *RBACManager) EnsureRoles(ctx context.Context) error {
	roles := map[string][]rbacv1.PolicyRule{
		RoleViewer: {
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"issues", "anomalies", "aiinsights", "postmortems", "auditevents", "servicelevelobjectives", "incidentslas"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"remediationplans", "runbooks", "approvalrequests"}, Verbs: []string{"get", "list", "watch"}},
		},
		RoleOperator: {
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"issues", "anomalies", "aiinsights", "postmortems", "auditevents", "servicelevelobjectives", "incidentslas"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"remediationplans", "runbooks", "approvalrequests"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"issues"}, Verbs: []string{"update", "patch"}},
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"approvalrequests"}, Verbs: []string{"update", "patch"}},
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"postmortems"}, Verbs: []string{"update", "patch"}},
		},
		RoleAdmin: {
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"issues", "anomalies", "aiinsights", "postmortems", "auditevents", "servicelevelobjectives", "incidentslas", "remediationplans", "approvalrequests"}, Verbs: []string{"get", "list", "watch", "update", "patch"}},
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{"runbooks", "notificationpolicies", "servicelevelobjectives", "incidentslas"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		},
		RoleSuperAdmin: {
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{
				"instances", "issues", "anomalies", "aiinsights", "remediationplans",
				"runbooks", "postmortems", "sourcerepositories",
				"notificationpolicies", "escalationpolicies",
				"approvalpolicies", "approvalrequests",
				"servicelevelobjectives", "incidentslas",
				"auditevents", "chaosexperiments", "clusterregistrations",
			}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{APIGroups: []string{"platform.chatcli.io"}, Resources: []string{
				"instances/status", "issues/status", "anomalies/status",
				"aiinsights/status", "remediationplans/status", "postmortems/status",
				"sourcerepositories/status", "notificationpolicies/status",
				"escalationpolicies/status", "approvalpolicies/status",
				"approvalrequests/status", "servicelevelobjectives/status",
				"incidentslas/status", "auditevents/status",
				"chaosexperiments/status", "clusterregistrations/status",
			}, Verbs: []string{"get", "update", "patch"}},
		},
	}

	for name, rules := range roles {
		cr := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "chatcli-operator", "app.kubernetes.io/component": "rbac"},
			},
			Rules: rules,
		}

		existing := &rbacv1.ClusterRole{}
		err := rm.client.Get(ctx, types.NamespacedName{Name: name}, existing)
		if err != nil {
			if errors.IsNotFound(err) {
				if err := rm.client.Create(ctx, cr); err != nil {
					return fmt.Errorf("creating ClusterRole %s: %w", name, err)
				}
				continue
			}
			return err
		}
		existing.Rules = rules
		existing.Labels = cr.Labels
		if err := rm.client.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating ClusterRole %s: %w", name, err)
		}
	}
	return nil
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
