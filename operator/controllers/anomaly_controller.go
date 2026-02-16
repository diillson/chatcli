package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const (
	// CorrelationTimeWindow is the time window used to find related anomalies.
	CorrelationTimeWindow = 10 * time.Minute
)

var (
	anomaliesProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "anomalies_processed_total",
		Help:      "Total anomalies processed by result.",
	}, []string{"result"})

	issuesCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "issues_created_by_correlation_total",
		Help:      "Total issues created via anomaly correlation.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		anomaliesProcessed,
		issuesCreated,
	)
}

// AnomalyReconciler reconciles Anomaly objects and correlates them into Issues.
type AnomalyReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	CorrelationEngine *CorrelationEngine
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=anomalies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=anomalies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch;create;update;patch

func (r *AnomalyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var anomaly platformv1alpha1.Anomaly
	if err := r.Get(ctx, req.NamespacedName, &anomaly); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Skip already correlated anomalies
	if anomaly.Status.Correlated {
		return ctrl.Result{}, nil
	}

	log.Info("Processing anomaly", "name", anomaly.Name, "signal", anomaly.Spec.SignalType, "resource", anomaly.Spec.Resource.Name)

	resource := anomaly.Spec.Resource

	// Check if an active Issue already exists for this resource
	existingIssue, err := r.CorrelationEngine.FindExistingIssue(ctx, resource)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("finding existing issue: %w", err)
	}

	if existingIssue != nil {
		// Correlate with existing issue
		log.Info("Correlating anomaly with existing issue", "issue", existingIssue.Name)
		if err := r.CorrelationEngine.MarkAnomalyCorrelated(ctx, &anomaly, existingIssue.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("marking anomaly correlated: %w", err)
		}

		// Update issue risk score based on new anomaly count
		related, err := r.CorrelationEngine.FindRelatedAnomalies(ctx, resource, CorrelationTimeWindow)
		if err != nil {
			log.Error(err, "Failed to find related anomalies for risk recalculation")
		} else {
			newScore := r.CorrelationEngine.CalculateRiskScore(related)
			if newScore > existingIssue.Spec.RiskScore {
				existingIssue.Spec.RiskScore = newScore
				if err := r.Update(ctx, existingIssue); err != nil {
					log.Error(err, "Failed to update issue risk score")
				}
			}
		}

		anomaliesProcessed.WithLabelValues("correlated_existing").Inc()
		return ctrl.Result{}, nil
	}

	// No existing issue — find related anomalies and create a new Issue
	related, err := r.CorrelationEngine.FindRelatedAnomalies(ctx, resource, CorrelationTimeWindow)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("finding related anomalies: %w", err)
	}

	riskScore := r.CorrelationEngine.CalculateRiskScore(related)
	severity := r.CorrelationEngine.DetermineSeverity(anomaly.Spec.SignalType, riskScore)
	source := r.CorrelationEngine.DetermineSource(anomaly.Spec.Source)

	incID, err := r.CorrelationEngine.GenerateIncidentID(ctx, anomaly.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("generating incident ID: %w", err)
	}

	issueName := fmt.Sprintf("%s-%s-%d", resource.Name, anomaly.Spec.SignalType, time.Now().Unix())

	newIssue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      issueName,
			Namespace: anomaly.Namespace,
			Labels: map[string]string{
				"platform.chatcli.io/inc-id":    incID,
				"platform.chatcli.io/resource":  resource.Name,
				"platform.chatcli.io/signal":    string(anomaly.Spec.SignalType),
			},
		},
		Spec: platformv1alpha1.IssueSpec{
			Severity:    severity,
			Source:      source,
			Resource:    resource,
			Description: fmt.Sprintf("Auto-detected %s anomaly: %s (value=%s, threshold=%s)", anomaly.Spec.SignalType, anomaly.Spec.Description, anomaly.Spec.Value, anomaly.Spec.Threshold),
			RiskScore:   riskScore,
		},
	}

	if err := r.Create(ctx, newIssue); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating issue: %w", err)
	}
	log.Info("Created new issue from anomaly", "issue", issueName, "severity", severity, "riskScore", riskScore, "incID", incID)
	issuesCreated.Inc()

	// Mark all related anomalies (including current) as correlated
	for i := range related {
		if err := r.CorrelationEngine.MarkAnomalyCorrelated(ctx, &related[i], issueName); err != nil {
			log.Error(err, "Failed to mark related anomaly as correlated", "anomaly", related[i].Name)
		}
	}

	// Mark current anomaly too (in case it wasn't in the related list yet)
	if !anomaly.Status.Correlated {
		if err := r.CorrelationEngine.MarkAnomalyCorrelated(ctx, &anomaly, issueName); err != nil {
			log.Error(err, "Failed to mark current anomaly as correlated")
		}
	}

	anomaliesProcessed.WithLabelValues("new_issue_created").Inc()
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AnomalyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.CorrelationEngine == nil {
		r.CorrelationEngine = NewCorrelationEngine(r.Client)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Anomaly{}).
		Complete(r)
}
