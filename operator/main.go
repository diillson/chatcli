package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	uberzap "go.uber.org/zap"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	auditRecorder := controllers.NewAuditRecorder(mgr.GetClient(), mgr.GetScheme())

	if err = (&controllers.IssueReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		DedupInvalidator: watcherBridge,
		AuditRecorder:    auditRecorder,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Issue")
		os.Exit(1)
	}

	// Shared components for AIOps pipeline
	patternStore := controllers.NewPatternStore(mgr.GetClient())
	costTracker := controllers.NewCostTracker(mgr.GetClient())
	noiseReducer := controllers.NewNoiseReducer(mgr.GetClient())

	// Shared components for enriched AI analysis
	contextBuilder := controllers.NewKubernetesContextBuilder(mgr.GetClient(), kubeClientset)
	logAnalyzer := controllers.NewLogAnalyzer(mgr.GetClient(), kubeClientset)
	gitOpsDetector := controllers.NewGitOpsDetector(mgr.GetClient())
	sourceCodeAnalyzer := controllers.NewSourceCodeAnalyzer(mgr.GetClient())
	cascadeAnalyzer := controllers.NewCascadeAnalyzer(mgr.GetClient())
	blastRadiusPredictor := controllers.NewBlastRadiusPredictor(mgr.GetClient())

	if err = (&controllers.RemediationReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		ServerClient:   serverClient,
		ContextBuilder: contextBuilder,
		AuditRecorder:  auditRecorder,
		PatternStore:   patternStore,
		CostTracker:    costTracker,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Remediation")
		os.Exit(1)
	}

	if err = (&controllers.AnomalyReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		NoiseReducer: noiseReducer,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Anomaly")
		os.Exit(1)
	}

	// MetricsCollector — optional, requires PROMETHEUS_URL env var
	var metricsCollector *controllers.MetricsCollector
	if promURL := os.Getenv("PROMETHEUS_URL"); promURL != "" {
		metricsCollector = controllers.NewMetricsCollector(promURL)
		setupLog.Info("Prometheus metrics collector enabled", "url", promURL)
	}

	// AIInsightReconciler — calls server AnalyzeIssue RPC with enriched context
	if err = (&controllers.AIInsightReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		ServerClient:         serverClient,
		ContextBuilder:       contextBuilder,
		LogAnalyzer:          logAnalyzer,
		MetricsCollector:     metricsCollector,
		GitOpsDetector:       gitOpsDetector,
		SourceCodeAnalyzer:   sourceCodeAnalyzer,
		CascadeAnalyzer:      cascadeAnalyzer,
		BlastRadiusPredictor: blastRadiusPredictor,
		CostTracker:          costTracker,
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
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		BlastRadiusPredictor: blastRadiusPredictor,
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

	// SourceRepositoryReconciler — syncs git repositories for code-aware diagnostics
	if err = (&controllers.SourceRepositoryReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SourceRepository")
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
	aiopsPort := os.Getenv("CHATCLI_AIOPS_PORT")
	if aiopsPort == "" {
		aiopsPort = "8090"
	}
	apiServer := rest.NewAPIServer(mgr.GetClient(), ":"+aiopsPort)

	// Load API keys from ConfigMap chatcli-operator-config (field: api-keys)
	// and start a watcher to hot-reload on changes (no restart needed)
	loadAPIKeysFromConfigMap(kubeClientset, apiServer)
	go watchAPIKeysConfigMap(kubeClientset, apiServer)

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

// apiKeyEntry represents a single API key entry from the ConfigMap.
type apiKeyEntry struct {
	Key         string `yaml:"key"`
	Role        string `yaml:"role"`
	Description string `yaml:"description"`
}

// loadAPIKeysFromConfigMap reads API keys from the chatcli-operator-config ConfigMap.
// The ConfigMap field "api-keys" contains a YAML list of {key, role, description}.
func loadAPIKeysFromConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer) {
	configMapName := "chatcli-operator-config"
	namespace := resolveNamespace()

	// Security (C4): Fail-closed — refuse to start REST API without auth unless dev mode
	devMode := strings.EqualFold(os.Getenv("CHATCLI_OPERATOR_DEV_MODE"), "true")

	cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), configMapName, metav1.GetOptions{})
	if err != nil {
		if devMode {
			setupLog.Info("WARNING: no API keys ConfigMap found, REST API running in DEV MODE (no auth)",
				"configmap", fmt.Sprintf("%s/%s", namespace, configMapName))
			return
		}
		setupLog.Error(err, "SECURITY: no API keys ConfigMap found and CHATCLI_OPERATOR_DEV_MODE is not set — REST API will reject all unauthenticated requests",
			"configmap", fmt.Sprintf("%s/%s", namespace, configMapName))
		return
	}

	apiKeysYAML, ok := cm.Data["api-keys"]
	if !ok || strings.TrimSpace(apiKeysYAML) == "" {
		if devMode {
			setupLog.Info("WARNING: ConfigMap found but no api-keys field, REST API running in DEV MODE")
			return
		}
		setupLog.Error(nil, "SECURITY: ConfigMap found but no api-keys field and CHATCLI_OPERATOR_DEV_MODE is not set — REST API will reject all unauthenticated requests")
		return
	}

	var entries []apiKeyEntry
	if err := yaml.Unmarshal([]byte(apiKeysYAML), &entries); err != nil {
		setupLog.Error(err, "failed to parse api-keys from ConfigMap")
		return
	}

	keys := make(map[string]string)
	for _, e := range entries {
		if e.Key != "" && e.Role != "" {
			keys[e.Key] = e.Role
			setupLog.Info("loaded API key", "role", e.Role, "description", e.Description)
		}
	}

	if len(keys) > 0 {
		apiServer.SetAPIKeys(keys)
		setupLog.Info("REST API authentication enabled", "keys", len(keys))
	}
}

// watchAPIKeysConfigMap polls the ConfigMap for changes and hot-reloads API keys.
func watchAPIKeysConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer) {
	configMapName := "chatcli-operator-config"
	namespace := resolveNamespace()

	var lastResourceVersion string
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), configMapName, metav1.GetOptions{})
		if err != nil {
			// ConfigMap deleted or not found — revert to dev mode (no auth)
			if lastResourceVersion != "" {
				apiServer.SetAPIKeys(make(map[string]string))
				lastResourceVersion = ""
				setupLog.Info("API keys ConfigMap removed, REST API reverted to dev mode (no auth)")
			}
			continue
		}

		if cm.ResourceVersion == lastResourceVersion {
			continue
		}
		lastResourceVersion = cm.ResourceVersion

		apiKeysYAML, ok := cm.Data["api-keys"]
		if !ok || strings.TrimSpace(apiKeysYAML) == "" {
			apiServer.SetAPIKeys(make(map[string]string))
			setupLog.Info("api-keys field empty or removed, REST API reverted to dev mode (no auth)")
			continue
		}

		var entries []apiKeyEntry
		if err := yaml.Unmarshal([]byte(apiKeysYAML), &entries); err != nil {
			setupLog.Error(err, "failed to parse api-keys on hot-reload")
			continue
		}

		keys := make(map[string]string)
		for _, e := range entries {
			if e.Key != "" && e.Role != "" {
				keys[e.Key] = e.Role
			}
		}

		apiServer.SetAPIKeys(keys)
		setupLog.Info("API keys hot-reloaded from ConfigMap", "keys", len(keys))
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
