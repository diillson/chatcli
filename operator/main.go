package main

import (
	"context"
	"flag"
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
	// CHATCLI_ALLOWED_DIAGNOSTIC_COMMANDS are listed in full; the built-in
	// defaults (100 entries) are summarized by count.
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

	// Load API keys from the Secret chatcli-operator-secrets, or else the
	// ConfigMap chatcli-operator-config (field: api-keys), and keep polling
	// both to hot-reload changes (no restart needed).
	keyState := loadAPIKeysFromConfigMap(kubeClientset, apiServer)
	go watchAPIKeysConfigMap(kubeClientset, apiServer, keyState)

	if err := mgr.Add(apiServer); err != nil {
		setupLog.Error(err, "unable to add REST API server")
		os.Exit(1)
	}

	// Platform role ClusterRoles (chatcli-role-viewer / -operator / -admin / -superadmin)
	// and the shared chatcli-watcher ClusterRole are pre-provisioned by the Helm chart /
	// kustomize overlay. The operator only binds chatcli-watcher (one ClusterRoleBinding
	// per Instance whose watcher reads outside its namespace); the platform roles are for
	// cluster administrators to bind. It never creates or modifies ClusterRoles at
	// runtime (Security H5).

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
	keySourceMissing    keySource = iota // no object, or an object without an api-keys entry: the next source is consulted
	keySourceFound                       // a parsable api-keys entry (it may yield no usable key)
	keySourceUnreadable                  // the read failed: the keys in force are kept
	keySourceInvalid                     // the api-keys entry is not valid YAML: the keys in force are kept
)

// keyRead is what one key source said on one read.
type keyRead struct {
	keys    map[string]rest.APIKey
	version string
	src     keySource
	err     error
}

// loadAPIKeysFromConfigMap reads the API keys at startup with the same
// rules the hot reload applies, and returns the reload state to keep
// polling with.
// Security (M5): the Secret "chatcli-operator-secrets" wins over the
// ConfigMap "chatcli-operator-config".
// Security (C4): without keys the REST API rejects every call unless dev
// mode is on.
func loadAPIKeysFromConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer) *apiKeyWatchState {
	st := &apiKeyWatchState{}
	st.poll(clientset, resolveNamespace(), apiServer)
	return st
}

// watchAPIKeysConfigMap polls the Secret and the ConfigMap and hot-reloads
// the API keys, continuing from the state the startup load left.
func watchAPIKeysConfigMap(clientset kubernetes.Interface, apiServer *rest.APIServer, st *apiKeyWatchState) {
	namespace := resolveNamespace()
	ticker := time.NewTicker(apiKeysPollInterval)
	defer ticker.Stop()
	for range ticker.C {
		st.poll(clientset, namespace, apiServer)
	}
}

// apiKeyWatchState remembers which key set is in force and which broken
// version was already reported.
type apiKeyWatchState struct {
	inForce       string // "<source>/<resourceVersion>", or keysNone
	reportedError string // "<source>/<resourceVersion>" of the last invalid entry logged
}

// keysNone tags the state where no source provides keys.
const keysNone = "none"

// poll applies one round of the key rules:
//   - the Secret wins when it holds an api-keys entry; a Secret without
//     one (or with a blank one) defers to the ConfigMap;
//   - when neither object provides keys (both deleted, or neither holds an
//     api-keys entry) every key is revoked, including keys loaded before;
//   - an api-keys entry that is not valid YAML keeps the last good set in
//     force and is logged: a typo must not lock every user out, and a
//     revocation is done by removing the entry, not by breaking it;
//   - a source that cannot be read keeps the keys in force, so an API
//     server hiccup does not lock everyone out either.
func (st *apiKeyWatchState) poll(clientset kubernetes.Interface, namespace string, apiServer *rest.APIServer) {
	sources := []struct {
		name string
		read func() keyRead
	}{
		{"Secret", func() keyRead { return tryLoadKeysFromSecret(clientset, namespace, apiKeysSecretName) }},
		{"ConfigMap", func() keyRead { return tryLoadKeysFromConfigMap(clientset, namespace, apiKeysConfigMapName) }},
	}
	for _, source := range sources {
		r := source.read()
		switch r.src {
		case keySourceFound:
			st.apply(apiServer, source.name+"/"+r.version, source.name, r.keys)
			return
		case keySourceUnreadable:
			setupLog.Error(r.err, "Failed to read the API keys "+source.name+"; keeping the keys in force",
				"namespace", namespace, "keys", apiServer.APIKeyCount())
			return
		case keySourceInvalid:
			tag := source.name + "/" + r.version
			if st.reportedError != tag {
				st.reportedError = tag
				setupLog.Error(r.err, "The api-keys entry of the "+source.name+" is not valid YAML; keeping the last valid key set in force",
					"namespace", namespace, "resourceVersion", r.version, "keys", apiServer.APIKeyCount())
			}
			return
		}
	}
	st.apply(apiServer, keysNone, "", map[string]rest.APIKey{})
}

// apply puts a key set in force unless it is already the one in force.
func (st *apiKeyWatchState) apply(apiServer *rest.APIServer, tag, source string, keys map[string]rest.APIKey) {
	if tag == st.inForce {
		return
	}
	first := st.inForce == ""
	st.inForce = tag
	apiServer.SetAPIKeyEntries(keys)
	for _, k := range keys {
		setupLog.Info("loaded API key", "role", k.Role, "name", k.Name)
	}
	switch {
	case len(keys) > 0 && first:
		setupLog.Info("REST API authentication enabled (from "+source+")", "keys", len(keys))
	case len(keys) > 0:
		setupLog.Info("API keys hot-reloaded from "+source, "keys", len(keys))
	case rest.DevModeEnabled():
		setupLog.Info("WARNING: no API key configured, REST API running in DEV MODE (no auth)",
			"secret", apiKeysSecretName, "configmap", apiKeysConfigMapName)
	default:
		setupLog.Info("SECURITY: no valid API key configured and CHATCLI_OPERATOR_DEV_MODE is not set — every REST API call is rejected with 401",
			"secret", apiKeysSecretName, "configmap", apiKeysConfigMapName)
	}
}

// tryLoadKeysFromSecret loads API keys from a Kubernetes Secret.
func tryLoadKeysFromSecret(clientset kubernetes.Interface, namespace, name string) keyRead {
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return keyRead{src: readFailure(err), err: err}
	}
	return readKeyEntry(secret.Data[apiKeysField], secret.ResourceVersion)
}

// tryLoadKeysFromConfigMap loads API keys from a Kubernetes ConfigMap.
func tryLoadKeysFromConfigMap(clientset kubernetes.Interface, namespace, name string) keyRead {
	cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return keyRead{src: readFailure(err), err: err}
	}
	return readKeyEntry([]byte(cm.Data[apiKeysField]), cm.ResourceVersion)
}

// readKeyEntry classifies the api-keys entry of an existing object.
func readKeyEntry(data []byte, resourceVersion string) keyRead {
	if strings.TrimSpace(string(data)) == "" {
		return keyRead{src: keySourceMissing, version: resourceVersion}
	}
	keys, err := parseAPIKeys(data)
	if err != nil {
		return keyRead{src: keySourceInvalid, version: resourceVersion, err: err}
	}
	return keyRead{keys: keys, src: keySourceFound, version: resourceVersion}
}

func readFailure(err error) keySource {
	if apierrors.IsNotFound(err) {
		return keySourceMissing
	}
	return keySourceUnreadable
}

// parseAPIKeys parses YAML api-keys data. Entries without a key or a role
// are skipped; invalid YAML is an error so the caller keeps the last good
// set.
func parseAPIKeys(data []byte) (map[string]rest.APIKey, error) {
	keys := make(map[string]rest.APIKey)
	var entries []apiKeyEntry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil, err
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
	}
	return keys, nil
}
