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
	if _, err := setup.Controllers(mgr, setupOpts); err != nil {
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

// apiKeyEntry represents a single API key entry from the ConfigMap.
type apiKeyEntry struct {
	Key         string `yaml:"key"`
	Role        string `yaml:"role"`
	Description string `yaml:"description"`
}

// loadAPIKeysFromConfigMap reads API keys from Secret or ConfigMap.
// Security (M5): Prefers Secret "chatcli-operator-secrets" over ConfigMap for API keys.
func loadAPIKeysFromConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer) {
	namespace := resolveNamespace()

	// Security (C4): Fail-closed — refuse to start REST API without auth unless dev mode
	devMode := strings.EqualFold(os.Getenv("CHATCLI_OPERATOR_DEV_MODE"), "true")

	// Security (M5): Try Secret first, then fall back to ConfigMap
	secretName := "chatcli-operator-secrets"
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err == nil {
		if apiKeysData, ok := secret.Data["api-keys"]; ok && len(apiKeysData) > 0 {
			var entries []apiKeyEntry
			if err := yaml.Unmarshal(apiKeysData, &entries); err == nil && len(entries) > 0 {
				keys := make(map[string]string)
				for _, e := range entries {
					if e.Key != "" && e.Role != "" {
						keys[e.Key] = e.Role
						setupLog.Info("loaded API key from Secret", "role", e.Role, "description", e.Description)
					}
				}
				if len(keys) > 0 {
					apiServer.SetAPIKeys(keys)
					setupLog.Info("REST API authentication enabled (from Secret)", "keys", len(keys))
					return
				}
			}
		}
	}

	// Fall back to ConfigMap
	configMapName := "chatcli-operator-config"

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

// watchAPIKeys polls both Secret and ConfigMap for changes and hot-reloads API keys.
// Priority: Secret "chatcli-operator-secrets" > ConfigMap "chatcli-operator-config".
func watchAPIKeysConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer) {
	secretName := "chatcli-operator-secrets"
	configMapName := "chatcli-operator-config"
	namespace := resolveNamespace()

	var lastSecretVersion string
	var lastConfigMapVersion string
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Try Secret first (preferred for sensitive data)
		if keys, version, ok := tryLoadKeysFromSecret(clientset, namespace, secretName); ok {
			if version != lastSecretVersion {
				lastSecretVersion = version
				apiServer.SetAPIKeys(keys)
				if len(keys) > 0 {
					setupLog.Info("API keys hot-reloaded from Secret", "keys", len(keys))
				}
			}
			continue // Secret takes priority — skip ConfigMap
		}
		lastSecretVersion = ""

		// Fallback to ConfigMap
		if keys, version, ok := tryLoadKeysFromConfigMap(clientset, namespace, configMapName); ok {
			if version != lastConfigMapVersion {
				lastConfigMapVersion = version
				apiServer.SetAPIKeys(keys)
				if len(keys) > 0 {
					setupLog.Info("API keys hot-reloaded from ConfigMap", "keys", len(keys))
				}
			}
			continue
		}

		// Neither Secret nor ConfigMap found — clear keys if previously loaded
		if lastConfigMapVersion != "" || lastSecretVersion != "" {
			devMode := strings.EqualFold(os.Getenv("CHATCLI_OPERATOR_DEV_MODE"), "true")
			if devMode {
				apiServer.SetAPIKeys(make(map[string]string))
				setupLog.Info("API keys removed, REST API reverted to dev mode (no auth)")
			} else {
				setupLog.Info("API keys removed, REST API will reject unauthenticated requests")
			}
			lastConfigMapVersion = ""
			lastSecretVersion = ""
		}
	}
}

// tryLoadKeysFromSecret attempts to load API keys from a Kubernetes Secret.
func tryLoadKeysFromSecret(clientset kubernetes.Interface, namespace, name string) (map[string]string, string, bool) {
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, "", false
	}

	data, ok := secret.Data["api-keys"]
	if !ok || len(data) == 0 {
		return nil, secret.ResourceVersion, true // Secret exists but no keys
	}

	return parseAPIKeys(data, secret.ResourceVersion)
}

// tryLoadKeysFromConfigMap attempts to load API keys from a Kubernetes ConfigMap.
func tryLoadKeysFromConfigMap(clientset kubernetes.Interface, namespace, name string) (map[string]string, string, bool) {
	cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, "", false
	}

	data, ok := cm.Data["api-keys"]
	if !ok || strings.TrimSpace(data) == "" {
		return nil, cm.ResourceVersion, true // ConfigMap exists but no keys
	}

	return parseAPIKeys([]byte(data), cm.ResourceVersion)
}

// parseAPIKeys parses YAML api-keys data into a role map.
func parseAPIKeys(data []byte, resourceVersion string) (map[string]string, string, bool) {
	var entries []apiKeyEntry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		setupLog.Error(err, "failed to parse api-keys on hot-reload")
		return nil, resourceVersion, true
	}

	keys := make(map[string]string)
	for _, e := range entries {
		if e.Key != "" && e.Role != "" {
			keys[e.Key] = e.Role
		}
	}
	return keys, resourceVersion, true
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
