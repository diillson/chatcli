package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// AnnotationHumanActionAcknowledged signals (via PostMortem annotation) that a
// human has acknowledged a contained incident and applied the required fix.
// Required to transition RequiresHumanAction=true PostMortems out of Open/InReview
// into Closed. Anything truthy ("true", "yes", "ack") counts. GAP-03 fix.
const AnnotationHumanActionAcknowledged = "aiops.chatcli.io/human-action-acknowledged"

// PostMortemReconciler reconciles PostMortem objects.
type PostMortemReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=postmortems,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=postmortems/status,verbs=get;update;patch

func (r *PostMortemReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var pm platformv1alpha1.PostMortem
	if err := r.Get(ctx, req.NamespacedName, &pm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// GAP-03 fix: PostMortems for contained incidents must not be closed by
	// automation while the underlying bug is unresolved. If something already
	// marked it Closed without the acknowledgement annotation, revert to Open
	// to keep the incident visible in dashboards and metrics.
	if pm.Status.State == platformv1alpha1.PostMortemStateClosed {
		if pm.Spec.RequiresHumanAction && !humanActionAcknowledged(&pm) {
			log.Info("Reverting PostMortem from Closed to Open: requires human action and acknowledgement is missing",
				"name", pm.Name, "issue", pm.Spec.IssueRef.Name)
			pm.Status.State = platformv1alpha1.PostMortemStateOpen
			if err := r.Status().Update(ctx, &pm); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		// Genuinely closed — no-op.
		return ctrl.Result{}, nil
	}

	// Initialize state if empty
	if pm.Status.State == "" {
		log.Info("Initializing PostMortem state", "name", pm.Name)
		pm.Status.State = platformv1alpha1.PostMortemStateOpen
		if err := r.Status().Update(ctx, &pm); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// humanActionAcknowledged returns true when the operator has explicitly marked
// the contained PostMortem as having received the required follow-up action.
// Accepts a handful of truthy values to be friendly to manual `kubectl annotate`.
func humanActionAcknowledged(pm *platformv1alpha1.PostMortem) bool {
	v, ok := pm.Annotations[AnnotationHumanActionAcknowledged]
	if !ok {
		return false
	}
	switch v {
	case "true", "True", "yes", "ack", "acknowledged":
		return true
	}
	return false
}

func (r *PostMortemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.PostMortem{}).
		Complete(r)
}
