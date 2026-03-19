package main

import (
	"context"
	"flag"
	"os"

	uberzap "go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/diillson/chatcli/operator/api/rest"
	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	"github.com/diillson/chatcli/operator/controllers"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metrics endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "chatcli-operator-lock",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Kubernetes clientset for pod logs
	kubeClientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create kubernetes clientset")
		os.Exit(1)
	}

	// Shared gRPC client for server communication
	zapLogger, _ := uberzap.NewProduction()
	serverClient := controllers.NewServerClient(zapLogger)
	defer serverClient.Close()

	// WatcherBridge — polls server alerts and creates Anomaly CRs
	watcherBridge := controllers.NewWatcherBridge(mgr.GetClient(), mgr.GetScheme(), serverClient, zapLogger)
	if err := mgr.Add(watcherBridge); err != nil {
		setupLog.Error(err, "unable to add WatcherBridge")
		os.Exit(1)
	}

	if err = (&controllers.InstanceReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Instance")
		os.Exit(1)
	}

	if err = (&controllers.IssueReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		DedupInvalidator: watcherBridge,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Issue")
		os.Exit(1)
	}

	if err = (&controllers.RemediationReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ServerClient:   serverClient,
		ContextBuilder: controllers.NewKubernetesContextBuilder(mgr.GetClient(), kubeClientset),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Remediation")
		os.Exit(1)
	}

	if err = (&controllers.AnomalyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Anomaly")
		os.Exit(1)
	}

	// AIInsightReconciler — calls server AnalyzeIssue RPC to fill analysis
	if err = (&controllers.AIInsightReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ServerClient:   serverClient,
		ContextBuilder: controllers.NewKubernetesContextBuilder(mgr.GetClient(), kubeClientset),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AIInsight")
		os.Exit(1)
	}

	if err = (&controllers.PostMortemReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PostMortem")
		os.Exit(1)
	}

	// NotificationReconciler — sends notifications on issue state changes and handles escalation
	if err = (&controllers.NotificationReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Notification")
		os.Exit(1)
	}

	// SLOReconciler — tracks SLO compliance with burn rate alerting
	if err = (&controllers.SLOReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SLO")
		os.Exit(1)
	}

	// SLAReconciler — monitors SLA compliance for incident response/resolution
	if err = (&controllers.SLAReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SLA")
		os.Exit(1)
	}

	// ApprovalReconciler — manages approval workflows for remediation actions
	if err = (&controllers.ApprovalReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Approval")
		os.Exit(1)
	}

	// FederationReconciler — manages multi-cluster registration and cross-cluster correlation
	if err = (&controllers.FederationReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Federation")
		os.Exit(1)
	}

	// ChaosReconciler — manages chaos engineering experiments
	if err = (&controllers.ChaosReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Chaos")
		os.Exit(1)
	}

	// REST API Gateway — provides HTTP API access to AIOps resources
	apiServer := rest.NewAPIServer(mgr.GetClient(), ":8090")
	if err := mgr.Add(apiServer); err != nil {
		setupLog.Error(err, "unable to add REST API server")
		os.Exit(1)
	}

	// RBAC Manager — ensure default roles exist
	rbacMgr := controllers.NewRBACManager(mgr.GetClient())
	go func() {
		// Wait for cache sync then ensure roles
		<-mgr.Elected()
		if err := rbacMgr.EnsureRoles(context.Background()); err != nil {
			setupLog.Error(err, "failed to ensure RBAC roles")
		}
	}()

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
