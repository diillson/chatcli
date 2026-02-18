package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const finalizerName = "platform.chatcli.io/finalizer"

var (
	reconciliationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "reconciliations_total",
		Help:      "Total reconciliation attempts by result.",
	}, []string{"result"})

	reconcileDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "reconciliation_duration_seconds",
		Help:      "Histogram of reconciliation durations.",
		Buckets:   prometheus.DefBuckets,
	})

	managedInstances = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "managed_instances",
		Help:      "Number of Instance resources currently managed.",
	})

	instanceReady = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "instance_ready",
		Help:      "Whether an Instance is ready (1) or not (0).",
	}, []string{"name", "namespace"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		reconciliationsTotal,
		reconcileDuration,
		managedInstances,
		instanceReady,
	)
}

// InstanceReconciler reconciles an Instance object.
type InstanceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=instances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=replicasets;statefulsets;daemonsets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services;configmaps;serviceaccounts;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=endpoints,verbs=get;list;watch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete

func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	start := time.Now()

	// 1. Fetch the Instance
	var instance platformv1alpha1.Instance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	// 2. Handle deletion with finalizer
	if instance.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&instance, finalizerName) {
			if err := r.cleanupResources(ctx, &instance); err != nil {
				reconciliationsTotal.WithLabelValues("error").Inc()
				reconcileDuration.Observe(time.Since(start).Seconds())
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&instance, finalizerName)
			if err := r.Update(ctx, &instance); err != nil {
				reconciliationsTotal.WithLabelValues("error").Inc()
				reconcileDuration.Observe(time.Since(start).Seconds())
				return ctrl.Result{}, err
			}
		}
		reconciliationsTotal.WithLabelValues("success").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, nil
	}

	// 3. Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&instance, finalizerName) {
		controllerutil.AddFinalizer(&instance, finalizerName)
		if err := r.Update(ctx, &instance); err != nil {
			reconciliationsTotal.WithLabelValues("error").Inc()
			reconcileDuration.Observe(time.Since(start).Seconds())
			return ctrl.Result{}, err
		}
	}

	// 4. Reconcile owned resources
	if err := r.reconcileServiceAccount(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile ServiceAccount")
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled {
		if err := r.reconcileRBAC(ctx, &instance); err != nil {
			log.Error(err, "failed to reconcile RBAC")
			reconciliationsTotal.WithLabelValues("error").Inc()
			reconcileDuration.Observe(time.Since(start).Seconds())
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcileConfigMap(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile ConfigMap")
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled && len(instance.Spec.Watcher.Targets) > 0 {
		if err := r.reconcileWatchConfigMap(ctx, &instance); err != nil {
			log.Error(err, "failed to reconcile watch config ConfigMap")
			reconciliationsTotal.WithLabelValues("error").Inc()
			reconcileDuration.Observe(time.Since(start).Seconds())
			return ctrl.Result{}, err
		}
	}

	if instance.Spec.Persistence != nil && instance.Spec.Persistence.Enabled {
		if err := r.reconcilePVC(ctx, &instance); err != nil {
			log.Error(err, "failed to reconcile PVC")
			reconciliationsTotal.WithLabelValues("error").Inc()
			reconcileDuration.Observe(time.Since(start).Seconds())
			return ctrl.Result{}, err
		}
	}

	if err := r.reconcileService(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile Service")
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	if err := r.reconcileDeployment(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile Deployment")
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	// 5. Update status
	if err := r.updateStatus(ctx, &instance); err != nil {
		log.Error(err, "failed to update status")
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	// 6. Update operator metrics
	reconciliationsTotal.WithLabelValues("success").Inc()
	reconcileDuration.Observe(time.Since(start).Seconds())

	// Update managed instances gauge
	var list platformv1alpha1.InstanceList
	if err := r.List(ctx, &list); err == nil {
		managedInstances.Set(float64(len(list.Items)))
		for _, item := range list.Items {
			ready := 0.0
			if item.Status.Ready {
				ready = 1.0
			}
			instanceReady.WithLabelValues(item.Name, item.Namespace).Set(ready)
		}
	}

	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) updateStatus(ctx context.Context, instance *platformv1alpha1.Instance) error {
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

func (r *InstanceReconciler) cleanupResources(ctx context.Context, instance *platformv1alpha1.Instance) error {
	// Owned namespaced resources are garbage-collected via OwnerReferences.
	// Cluster-scoped resources (ClusterRole/ClusterRoleBinding) need manual cleanup.
	log := log.FromContext(ctx)
	log.Info("Cleaning up resources for Instance", "name", instance.Name)

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
func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Instance{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}
