package controllers

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

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

	// Terminal states — no-op
	if pm.Status.State == platformv1alpha1.PostMortemStateClosed {
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

func (r *PostMortemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.PostMortem{}).
		Complete(r)
}
