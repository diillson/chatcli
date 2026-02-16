package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	chatcliv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const finalizerName = "chatcli.diillson.com/finalizer"

// ChatCLIInstanceReconciler reconciles a ChatCLIInstance object.
type ChatCLIInstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=chatcli.diillson.com,resources=chatcliinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=chatcli.diillson.com,resources=chatcliinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=chatcli.diillson.com,resources=chatcliinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services;configmaps;serviceaccounts;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete

func (r *ChatCLIInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// 1. Fetch the ChatCLIInstance
	var instance chatcliv1alpha1.ChatCLIInstance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Handle deletion with finalizer
	if instance.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&instance, finalizerName) {
			if err := r.cleanupResources(ctx, &instance); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&instance, finalizerName)
			if err := r.Update(ctx, &instance); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// 3. Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&instance, finalizerName) {
		controllerutil.AddFinalizer(&instance, finalizerName)
		if err := r.Update(ctx, &instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	// 4. Reconcile owned resources
	if err := r.reconcileServiceAccount(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile ServiceAccount")
		return ctrl.Result{}, err
	}

	if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled {
		if err := r.reconcileRBAC(ctx, &instance); err != nil {
			log.Error(err, "failed to reconcile RBAC")
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcileConfigMap(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile ConfigMap")
		return ctrl.Result{}, err
	}

	if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled && len(instance.Spec.Watcher.Targets) > 0 {
		if err := r.reconcileWatchConfigMap(ctx, &instance); err != nil {
			log.Error(err, "failed to reconcile watch config ConfigMap")
			return ctrl.Result{}, err
		}
	}

	if instance.Spec.Persistence != nil && instance.Spec.Persistence.Enabled {
		if err := r.reconcilePVC(ctx, &instance); err != nil {
			log.Error(err, "failed to reconcile PVC")
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcileService(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile Service")
		return ctrl.Result{}, err
	}

	if err := r.reconcileDeployment(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile Deployment")
		return ctrl.Result{}, err
	}

	// 5. Update status
	if err := r.updateStatus(ctx, &instance); err != nil {
		log.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ChatCLIInstanceReconciler) updateStatus(ctx context.Context, instance *chatcliv1alpha1.ChatCLIInstance) error {
	var deploy appsv1.Deployment
	nn := types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace}
	if err := r.Get(ctx, nn, &deploy); err != nil {
		if errors.IsNotFound(err) {
			instance.Status.Ready = false
			instance.Status.Replicas = 0
			instance.Status.ReadyReplicas = 0
		} else {
			return err
		}
	} else {
		instance.Status.Replicas = deploy.Status.Replicas
		instance.Status.ReadyReplicas = deploy.Status.ReadyReplicas

		desiredReplicas := int32(1)
		if instance.Spec.Replicas != nil {
			desiredReplicas = *instance.Spec.Replicas
		}
		instance.Status.Ready = deploy.Status.ReadyReplicas > 0 &&
			deploy.Status.ReadyReplicas >= desiredReplicas
	}

	// Set Available condition
	availableCond := metav1.Condition{
		Type:               "Available",
		ObservedGeneration: instance.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if instance.Status.Ready {
		availableCond.Status = metav1.ConditionTrue
		availableCond.Reason = "DeploymentReady"
		availableCond.Message = "All replicas are ready"
	} else {
		availableCond.Status = metav1.ConditionFalse
		availableCond.Reason = "DeploymentNotReady"
		availableCond.Message = fmt.Sprintf("%d/%d replicas ready",
			instance.Status.ReadyReplicas, instance.Status.Replicas)
	}
	meta.SetStatusCondition(&instance.Status.Conditions, availableCond)

	instance.Status.ObservedGeneration = instance.Generation
	return r.Status().Update(ctx, instance)
}

func (r *ChatCLIInstanceReconciler) cleanupResources(ctx context.Context, instance *chatcliv1alpha1.ChatCLIInstance) error {
	// Owned namespaced resources are garbage-collected via OwnerReferences.
	// Cluster-scoped resources (ClusterRole/ClusterRoleBinding) need manual cleanup.
	log := log.FromContext(ctx)
	log.Info("Cleaning up resources for ChatCLIInstance", "name", instance.Name)

	clusterRoleName := instance.Namespace + "-" + instance.Name + "-watcher"

	crb := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: clusterRoleName}, crb); err == nil {
		if err := r.Delete(ctx, crb); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	cr := &rbacv1.ClusterRole{}
	if err := r.Get(ctx, types.NamespacedName{Name: clusterRoleName}, cr); err == nil {
		if err := r.Delete(ctx, cr); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ChatCLIInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&chatcliv1alpha1.ChatCLIInstance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}
