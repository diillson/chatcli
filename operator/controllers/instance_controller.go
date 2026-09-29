package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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
	// Prober reaches the running server for its version and health; nil
	// disables the probe (tests, or an operator that never dials).
	Prober Prober
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
	// The status as stored: the reconcile writes it back only when what it
	// observed differs, so a reconcile that learns nothing new writes
	// nothing (and a status write never feeds another reconcile).
	observed := instance.Status.DeepCopy()

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

	if err := r.reconcileConfigMaps(ctx, &instance); err != nil {
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
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

	// The server refuses to start unauthenticated on a reachable address,
	// and exits when TLS is on with no certificate to load, so a spec in
	// either shape would produce a crash loop with the reason buried in
	// container logs. Say it on the Instance instead, and do not provision
	// the Deployment.
	tlsBlocked := applyTLSCondition(ctx, &instance)
	authBlocked := applyAuthCondition(ctx, &instance)
	if tlsBlocked || authBlocked {
		if err := r.writeStatusIfChanged(ctx, &instance, observed); err != nil {
			log.Error(err, "failed to record the provisioning precheck conditions")
		}
		reconciliationsTotal.WithLabelValues("success").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, nil
	}

	if err := r.reconcileDeployment(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile Deployment")
		reconciliationsTotal.WithLabelValues("error").Inc()
		reconcileDuration.Observe(time.Since(start).Seconds())
		return ctrl.Result{}, err
	}

	// 5. Update status
	if err := r.updateStatusFrom(ctx, &instance, observed); err != nil {
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

	return ctrl.Result{RequeueAfter: r.probeRequeueAfter(&instance)}, nil
}

// serverProbeInterval is how often a ready Instance is re-probed when
// nothing else triggers a reconcile, so ServerReachable and serverVersion
// follow a server that was upgraded, restarted or lost its credential.
const serverProbeInterval = 5 * time.Minute

// serverProbeRetryInterval is how soon a ready Instance whose last probe
// failed is probed again. A probe that lands while the pods are being
// replaced (a Recreate rollout, an upgrade) fails although the new pods
// serve a moment later; retrying on the full interval left ServerReachable
// False for minutes after a healthy rollout.
const serverProbeRetryInterval = 30 * time.Second

// probeRequeueAfter schedules the next probe: only when there is a prober
// and a ready Deployment to probe. While not ready, the Deployment's own
// status changes trigger the reconcile that probes it. A ready Instance the
// operator could not reach is retried sooner than a reachable one.
func (r *InstanceReconciler) probeRequeueAfter(instance *platformv1alpha1.Instance) time.Duration {
	if r.Prober == nil || !instance.Status.Ready {
		return 0
	}
	if c := meta.FindStatusCondition(instance.Status.Conditions, ServerReachableConditionType); c != nil && c.Status != metav1.ConditionTrue {
		return serverProbeRetryInterval
	}
	return serverProbeInterval
}

// writeStatusIfChanged writes the status subresource when it differs from
// what was read at the start of the reconcile. An unchanged status is not
// written: the write would be a no-op for the API server anyway, and
// skipping it keeps the reconcile from generating its own watch events.
func (r *InstanceReconciler) writeStatusIfChanged(ctx context.Context, instance *platformv1alpha1.Instance, observed *platformv1alpha1.InstanceStatus) error {
	if observed != nil && equality.Semantic.DeepEqual(*observed, instance.Status) {
		return nil
	}
	return r.Status().Update(ctx, instance)
}

// updateStatus refreshes the status against what the Instance carries now.
func (r *InstanceReconciler) updateStatus(ctx context.Context, instance *platformv1alpha1.Instance) error {
	return r.updateStatusFrom(ctx, instance, instance.Status.DeepCopy())
}

// updateStatusFrom refreshes the status from the Deployment and the server
// probe, and writes it when it differs from observed.
func (r *InstanceReconciler) updateStatusFrom(ctx context.Context, instance *platformv1alpha1.Instance, observed *platformv1alpha1.InstanceStatus) error {
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

	r.probeServer(ctx, instance)

	instance.Status.ObservedGeneration = instance.Generation
	return r.writeStatusIfChanged(ctx, instance, observed)
}

// ServerReachableConditionType reports whether the operator reached the
// running server with its own transport and credential, and what version
// answered. Ready (pods up) and reachable (the operator can drive it) are
// different facts; this is the second one.
const ServerReachableConditionType = "ServerReachable"

// probeServer asks the running server for its version and health and
// records the outcome. Nothing to probe while the Deployment is not ready.
func (r *InstanceReconciler) probeServer(ctx context.Context, instance *platformv1alpha1.Instance) {
	if r.Prober == nil {
		return
	}
	cond := metav1.Condition{
		Type:               ServerReachableConditionType,
		ObservedGeneration: instance.Generation,
	}
	if !instance.Status.Ready {
		cond.Status = metav1.ConditionUnknown
		cond.Reason = "DeploymentNotReady"
		cond.Message = "not probed: no ready replica"
		meta.SetStatusCondition(&instance.Status.Conditions, cond)
		return
	}
	var prev *metav1.Condition
	if c := meta.FindStatusCondition(instance.Status.Conditions, ServerReachableConditionType); c != nil {
		prev = c.DeepCopy()
	}
	prevVersion := instance.Status.ServerVersion
	now := metav1.Now()
	res, err := r.Prober.ProbeInstance(ctx, instance)
	switch {
	case err != nil:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "ProbeFailed"
		cond.Message = err.Error()
	case !res.Healthy:
		cond.Status = metav1.ConditionFalse
		cond.Reason = "NotServing"
		cond.Message = "server answered Health with NOT_SERVING"
	default:
		cond.Status = metav1.ConditionTrue
		cond.Reason = "Serving"
		cond.Message = fmt.Sprintf("server %s serving (%s/%s)", res.Version, res.Provider, res.Model)
	}
	if res.Version != "" {
		instance.Status.ServerVersion = res.Version
	}
	meta.SetStatusCondition(&instance.Status.Conditions, cond)
	if probeOutcomeChanged(prev, &cond) || instance.Status.ServerVersion != prevVersion ||
		probeStampStale(instance.Status.ServerProbeTime, now.Time) {
		instance.Status.ServerProbeTime = &now
	}
}

// probeOutcomeChanged reports whether the probe learned something the
// stored condition does not say.
func probeOutcomeChanged(prev, cur *metav1.Condition) bool {
	return prev == nil || prev.Status != cur.Status || prev.Reason != cur.Reason ||
		prev.Message != cur.Message || prev.ObservedGeneration != cur.ObservedGeneration
}

// probeStampStale reports whether serverProbeTime is due a refresh while
// the outcome is unchanged. Refreshing it on every reconcile would change
// the status on every pass, and each status write is itself a watch event
// that reconciles again: an endless loop of probes and writes. Refreshed
// once per probe interval, it still says the value was confirmed recently.
func probeStampStale(stamp *metav1.Time, now time.Time) bool {
	return stamp == nil || now.Sub(stamp.Time) >= serverProbeInterval
}

func (r *InstanceReconciler) cleanupResources(ctx context.Context, instance *platformv1alpha1.Instance) error {
	// Owned namespaced resources are garbage-collected via OwnerReferences.
	// The per-Instance ClusterRoleBinding is cluster-scoped so has no owner ref — delete
	// it manually. The referenced ClusterRole (chatcli-watcher) is shared and owned by
	// the Helm/kustomize release, so it must NOT be deleted here.
	log := log.FromContext(ctx)
	log.Info("Cleaning up resources for Instance", "name", instance.Name)

	crbName := instance.Namespace + "-" + instance.Name + "-watcher"

	crb := &rbacv1.ClusterRoleBinding{}
	if err := r.Get(ctx, types.NamespacedName{Name: crbName}, crb); err == nil {
		if err := r.Delete(ctx, crb); err != nil && !errors.IsNotFound(err) {
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
		// Watch user-managed Secrets (API keys) so that creating or updating
		// a Secret triggers a reconcile → hash change → rolling update.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.secretToInstance)).
		// Watch user-managed ConfigMaps mounted as files (MCP, agents,
		// skills) so an edit rolls the pods that read them at startup.
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.configMapToInstance)).
		Complete(r)
}

// secretToInstance maps a Secret event to the Instance(s) that reference it
// anywhere in their spec (see referencedSecretNames): the API keys, the TLS
// pair and client CA, the server token and JWT material, the operator's own
// credential and client certificate, the CA bundle, the encryption key and
// extraEnv secret refs. A reference the watch misses is a rotation that
// neither rolls the pods nor refreshes the operator's view.
func (r *InstanceReconciler) secretToInstance(ctx context.Context, obj client.Object) []reconcile.Request {
	var instances platformv1alpha1.InstanceList
	if err := r.List(ctx, &instances, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}

	secretName := obj.GetName()
	var requests []reconcile.Request
	for i := range instances.Items {
		inst := &instances.Items[i]
		if _, match := referencedSecretNames(inst)[secretName]; match {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      inst.Name,
					Namespace: inst.Namespace,
				},
			})
		}
	}
	return requests
}

// reconcileConfigMaps reconciles every ConfigMap an Instance owns: its own
// configuration, the MCP server list when MCP is enabled, and the watch
// targets when the watcher has any. Grouped because the three share a
// lifecycle and a failure mode, and reading them as one step keeps the
// reconcile loop legible.
func (r *InstanceReconciler) reconcileConfigMaps(ctx context.Context, instance *platformv1alpha1.Instance) error {
	log := log.FromContext(ctx)

	if err := r.reconcileConfigMap(ctx, instance); err != nil {
		log.Error(err, "failed to reconcile ConfigMap")
		return err
	}
	if instance.Spec.MCP != nil && instance.Spec.MCP.Enabled {
		if err := r.reconcileMCPConfigMap(ctx, instance); err != nil {
			log.Error(err, "failed to reconcile MCP ConfigMap")
			return err
		}
	}
	if instance.Spec.Watcher != nil && instance.Spec.Watcher.Enabled && len(instance.Spec.Watcher.Targets) > 0 {
		if err := r.reconcileWatchConfigMap(ctx, instance); err != nil {
			log.Error(err, "failed to reconcile watch config ConfigMap")
			return err
		}
	}
	return nil
}
