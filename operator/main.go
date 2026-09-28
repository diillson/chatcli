package main

import (
	"flag"
	"os"
	"strings"

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
	"github.com/diillson/chatcli/operator/internal/setup"
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

	// GAP-05 fix (chaos test report 2026-05-23): log the effective ExecDiagnostic
	// allowlist at startup so operators can audit what their pipeline is allowed
	// to run without having to inspect every Instance CR. Custom additions from
	// CHATCLI_ALLOWED_DIAGNOSTIC_COMMANDS are listed in full; the ~90 built-in
	// defaults are summarized by count.
	allowlistSummary := controllers.GetDiagnosticAllowlistSummary()
	setupLog.Info("Effective ExecDiagnostic allowlist loaded",
		"total", allowlistSummary.TotalCount,
		"defaults", allowlistSummary.DefaultCount,
		"custom_count", allowlistSummary.CustomCount,
		"custom", allowlistSummary.Custom)

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

	// Every reconciler and the WatcherBridge are wired by internal/setup so
	// the integration suite runs exactly what a deployed operator runs.
	setupOpts, err := setup.OptionsFromEnv(kubeClientset, serverClient, zapLogger)
	if err != nil {
		setupLog.Error(err, "invalid operator configuration")
		os.Exit(1)
	}
	components, err := setup.Controllers(mgr, setupOpts)
	if err != nil {
		setupLog.Error(err, "unable to set up controllers")
		os.Exit(1)
	}
	if setupOpts.PrometheusURL != "" {
		setupLog.Info("Prometheus metrics collector enabled", "url", setupOpts.PrometheusURL)
	}

	// REST API Gateway — provides HTTP API access to AIOps resources
	aiopsPort := os.Getenv("CHATCLI_AIOPS_PORT")
	if aiopsPort == "" {
		aiopsPort = "8090"
	}
	apiServer := rest.NewAPIServer(mgr.GetClient(), ":"+aiopsPort)
	// A manual resolve from the dashboard drops the watcher dedup for the
	// resource, so the next alert on it is not swallowed.
	apiServer.SetWatcherBridge(components.WatcherBridge)
	// The CORS policy comes from the environment the chart already sets.
	// Logged because it was previously read by nobody: an operator who
	// configured an origin and saw the dashboard blocked had no way to tell
	// whether the setting had arrived.
	if origins := apiServer.CORSAllowedOrigins(); len(origins) > 0 {
		setupLog.Info("CORS enabled", "allowedOrigins", origins)
	} else {
		setupLog.Info("CORS disabled (no allowed origin configured); browser cross-origin requests are blocked")
	}

	// Load API keys from ConfigMap chatcli-operator-config (field: api-keys)
	// and start a watcher to hot-reload on changes (no restart needed)
	loadAPIKeysFromConfigMap(kubeClientset, apiServer)
	go watchAPIKeysConfigMap(kubeClientset, apiServer)

	if err := mgr.Add(apiServer); err != nil {
		setupLog.Error(err, "unable to add REST API server")
		os.Exit(1)
	}

	// Platform role ClusterRoles (chatcli-role-viewer / -operator / -admin / -superadmin)
	// and the shared chatcli-watcher ClusterRole are pre-provisioned by the Helm chart /
	// kustomize overlay. The operator only creates RoleBindings/ClusterRoleBindings that
	// reference them — it never creates or modifies ClusterRoles at runtime (Security H5).

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

// resolveNamespace returns the namespace the operator is running in.
func resolveNamespace() string {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns
	}
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "chatcli-system"
}
