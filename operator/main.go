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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

const (
	apiKeysSecretName    = "chatcli-operator-secrets"
	apiKeysConfigMapName = "chatcli-operator-config"
	apiKeysField         = "api-keys"
	apiKeysPollInterval  = 30 * time.Second
)

// apiKeyEntry represents a single API key entry from the Secret or
// ConfigMap. Name, when set, is the identity recorded on the approval
// decisions the key takes; Description is used when Name is empty.
type apiKeyEntry struct {
	Key         string `yaml:"key"`
	Role        string `yaml:"role"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// keySource is the outcome of reading one key source.
type keySource int

const (
	keySourceMissing    keySource = iota // the object does not exist
	keySourceFound                       // the object exists (it may hold no keys)
	keySourceUnreadable                  // the read failed: the keys in force are kept
)

func devModeEnabled() bool {
	return strings.EqualFold(os.Getenv("CHATCLI_OPERATOR_DEV_MODE"), "true")
}

// loadAPIKeysFromConfigMap reads API keys at startup.
// Security (M5): Prefers Secret "chatcli-operator-secrets" over ConfigMap for API keys.
// Security (C4): without keys the REST API rejects every call unless dev mode is on.
func loadAPIKeysFromConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer) {
	namespace := resolveNamespace()

	if keys, _, src, _ := tryLoadKeysFromSecret(clientset, namespace, apiKeysSecretName); src == keySourceFound && len(keys) > 0 {
		apiServer.SetAPIKeyEntries(keys)
		setupLog.Info("REST API authentication enabled (from Secret)", "keys", len(keys))
		return
	}

	keys, _, src, err := tryLoadKeysFromConfigMap(clientset, namespace, apiKeysConfigMapName)
	if src == keySourceFound && len(keys) > 0 {
		apiServer.SetAPIKeyEntries(keys)
		setupLog.Info("REST API authentication enabled", "keys", len(keys))
		return
	}
	where := fmt.Sprintf("%s/%s", namespace, apiKeysSecretName)
	if devModeEnabled() {
		setupLog.Info("WARNING: no API keys found, REST API running in DEV MODE (no auth)", "secret", where)
		return
	}
	setupLog.Error(err, "SECURITY: no API keys found and CHATCLI_OPERATOR_DEV_MODE is not set — REST API will reject all requests with 401",
		"secret", where, "configmap", fmt.Sprintf("%s/%s", namespace, apiKeysConfigMapName))
}

// watchAPIKeysConfigMap polls the Secret and the ConfigMap and hot-reloads
// the API keys. Priority: Secret "chatcli-operator-secrets" > ConfigMap
// "chatcli-operator-config".
func watchAPIKeysConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer) {
	namespace := resolveNamespace()
	var state apiKeyWatchState
	ticker := time.NewTicker(apiKeysPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		state.poll(clientset, namespace, apiServer)
	}
}

// apiKeyWatchState remembers which version of each source is in force.
type apiKeyWatchState struct {
	secretVersion    string
	configMapVersion string
}

// poll applies one round of the hot reload. Keys stop working as soon as
// their source says so: a Secret or ConfigMap without keys, or both
// objects deleted, leaves the API without keys (401 on every call unless
// dev mode). A source that cannot be read keeps the keys in force, so an
// API server hiccup does not lock everyone out.
func (st *apiKeyWatchState) poll(clientset kubernetes.Interface, namespace string, apiServer *rest.APIServer) {
	keys, version, src, err := tryLoadKeysFromSecret(clientset, namespace, apiKeysSecretName)
	switch src {
	case keySourceFound:
		st.configMapVersion = ""
		if version != st.secretVersion {
			st.secretVersion = version
			applyReloadedKeys(apiServer, keys, "Secret")
		}
		return // Secret takes priority — skip ConfigMap
	case keySourceUnreadable:
		setupLog.Error(err, "Failed to read the API keys Secret; keeping the keys in force", "secret", apiKeysSecretName)
		return
	}
	st.secretVersion = ""

	keys, version, src, err = tryLoadKeysFromConfigMap(clientset, namespace, apiKeysConfigMapName)
	switch src {
	case keySourceFound:
		if version != st.configMapVersion {
			st.configMapVersion = version
			applyReloadedKeys(apiServer, keys, "ConfigMap")
		}
		return
	case keySourceUnreadable:
		setupLog.Error(err, "Failed to read the API keys ConfigMap; keeping the keys in force", "configmap", apiKeysConfigMapName)
		return
	}
	st.configMapVersion = ""

	// Neither the Secret nor the ConfigMap exists: every key is revoked,
	// including keys loaded at startup before the first poll.
	if apiServer.APIKeyCount() > 0 {
		apiServer.SetAPIKeyEntries(map[string]rest.APIKey{})
		if devModeEnabled() {
			setupLog.Info("API keys removed (Secret and ConfigMap gone), REST API reverted to dev mode (no auth)")
		} else {
			setupLog.Info("API keys removed (Secret and ConfigMap gone): every REST API call is now rejected with 401")
		}
	}
}

// applyReloadedKeys puts a reloaded key set in force.
func applyReloadedKeys(apiServer *rest.APIServer, keys map[string]rest.APIKey, source string) {
	apiServer.SetAPIKeyEntries(keys)
	if len(keys) > 0 {
		setupLog.Info("API keys hot-reloaded from "+source, "keys", len(keys))
		return
	}
	setupLog.Info("API keys hot-reloaded from " + source + ": no valid key, every REST API call is rejected with 401 unless dev mode is on")
}

// tryLoadKeysFromSecret loads API keys from a Kubernetes Secret.
func tryLoadKeysFromSecret(clientset kubernetes.Interface, namespace, name string) (map[string]rest.APIKey, string, keySource, error) {
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, "", readFailure(err), err
	}
	return parseAPIKeys(secret.Data[apiKeysField], secret.ResourceVersion), secret.ResourceVersion, keySourceFound, nil
}

// tryLoadKeysFromConfigMap loads API keys from a Kubernetes ConfigMap.
func tryLoadKeysFromConfigMap(clientset kubernetes.Interface, namespace, name string) (map[string]rest.APIKey, string, keySource, error) {
	cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, "", readFailure(err), err
	}
	return parseAPIKeys([]byte(cm.Data[apiKeysField]), cm.ResourceVersion), cm.ResourceVersion, keySourceFound, nil
}

func readFailure(err error) keySource {
	if apierrors.IsNotFound(err) {
		return keySourceMissing
	}
	return keySourceUnreadable
}

// parseAPIKeys parses YAML api-keys data. Invalid YAML yields no keys:
// a broken key list must not keep revoked keys working.
func parseAPIKeys(data []byte, resourceVersion string) map[string]rest.APIKey {
	keys := make(map[string]rest.APIKey)
	if strings.TrimSpace(string(data)) == "" {
		return keys
	}
	var entries []apiKeyEntry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		setupLog.Error(err, "failed to parse api-keys", "resourceVersion", resourceVersion)
		return keys
	}
	for _, e := range entries {
		if e.Key == "" || e.Role == "" {
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" {
			name = strings.TrimSpace(e.Description)
		}
		keys[e.Key] = rest.APIKey{Role: e.Role, Name: name}
		setupLog.Info("loaded API key", "role", e.Role, "name", name)
	}
	return keys
}
