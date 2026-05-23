package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const (
	// PollInterval is how often the bridge polls the server for alerts.
	PollInterval = 30 * time.Second

	// DefaultDedupTTL is the default dedup TTL when no Instance AIOps config is set.
	// Lowered from 60 to 30 minutes (GAP-02 fix, 2026-05-23): UID-aware hashing
	// now handles resource recreation deterministically, so TTL only needs to be
	// long enough to suppress flapping during a single ongoing incident.
	DefaultDedupTTL = 30 * time.Minute

	// missingUIDSentinel is used in the dedup hash when the K8s resource named
	// by the alert can't be looked up (deleted, wrong kind, transient API error).
	// It ensures that "alert for a no-longer-existing resource" gets a stable
	// hash distinct from any existing resource's UID — so a recreated resource
	// (which gets a fresh UID) never collides with the missing-resource hash.
	missingUIDSentinel = "uid:missing"
)

// WatcherBridge polls the ChatCLI server's GetAlerts RPC and creates Anomaly CRs.
// It implements manager.Runnable so it runs as a background goroutine in the controller manager.
type WatcherBridge struct {
	client       client.Client
	scheme       *runtime.Scheme
	serverClient *ServerClient
	logger       *zap.Logger

	mu                sync.Mutex
	seen              map[string]dedupEntry      // hash → entry (timestamp + resource ref for invalidation)
	connectedInstance *platformv1alpha1.Instance // Instance we're connected to (for OwnerRef)
}

// dedupEntry tracks the resource the hash referred to so InvalidateDedupForResource
// can invalidate by name+namespace without having to brute-force every known
// alert type and UID combination. GAP-02 fix: required because the hash now
// includes the UID, which the invalidation caller doesn't know.
type dedupEntry struct {
	seenAt     time.Time
	deployment string
	namespace  string
}

// NewWatcherBridge creates a new WatcherBridge.
func NewWatcherBridge(c client.Client, scheme *runtime.Scheme, sc *ServerClient, logger *zap.Logger) *WatcherBridge {
	return &WatcherBridge{
		client:       c,
		scheme:       scheme,
		serverClient: sc,
		logger:       logger.Named("watcher-bridge"),
		seen:         make(map[string]dedupEntry),
	}
}

// Start implements manager.Runnable. It runs a polling loop until the context is canceled.
func (wb *WatcherBridge) Start(ctx context.Context) error {
	wb.logger.Info("WatcherBridge started", zap.Duration("poll_interval", PollInterval))

	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			wb.logger.Info("WatcherBridge stopped")
			return nil
		case <-ticker.C:
			wb.poll(ctx)
		}
	}
}

func (wb *WatcherBridge) poll(ctx context.Context) {
	if !wb.serverClient.IsConnected() {
		if err := wb.discoverAndConnect(ctx); err != nil {
			wb.logger.Debug("Server discovery failed", zap.Error(err))
			return
		}
	}

	resp, err := wb.serverClient.GetAlerts(ctx)
	if err != nil {
		wb.logger.Warn("GetAlerts RPC failed", zap.Error(err))
		return
	}

	if len(resp.Alerts) == 0 {
		return
	}

	wb.logger.Info("Received alerts from server", zap.Int("count", len(resp.Alerts)))

	for _, alert := range resp.Alerts {
		hash := wb.computeAlertHash(ctx, alert)
		if wb.isDuplicate(hash) {
			continue
		}

		if err := wb.createAnomaly(ctx, alert); err != nil {
			wb.logger.Error("Failed to create Anomaly CR", zap.Error(err), zap.String("alert_type", alert.Type))
			continue
		}
		ns := alert.Namespace
		if ns == "" {
			ns = "default"
		}
		wb.markSeen(hash, alert.Deployment, ns)
	}

	wb.pruneDedup()
}

// discoverAndConnect finds a ready Instance CR and connects to its server.
func (wb *WatcherBridge) discoverAndConnect(ctx context.Context) error {
	var instances platformv1alpha1.InstanceList
	if err := wb.client.List(ctx, &instances); err != nil {
		return fmt.Errorf("listing instances: %w", err)
	}

	for _, inst := range instances.Items {
		if !inst.Status.Ready {
			continue
		}
		port := inst.Spec.Server.Port
		if port == 0 {
			port = 50051
		}
		address := fmt.Sprintf("dns:///%s.%s.svc.cluster.local:%d", inst.Name, inst.Namespace, port)

		// Build connection options from Instance spec
		opts, err := wb.buildConnectionOpts(ctx, &inst)
		if err != nil {
			wb.logger.Warn("Failed to build connection opts",
				zap.String("instance", inst.Name),
				zap.Error(err))
			continue
		}

		if err := wb.serverClient.Connect(address, opts); err != nil {
			wb.logger.Warn("Failed to connect to Instance",
				zap.String("instance", inst.Name),
				zap.String("address", address),
				zap.Error(err))
			continue
		}
		wb.logger.Info("Connected to Instance", zap.String("instance", inst.Name), zap.String("address", address))
		instCopy := inst
		wb.connectedInstance = &instCopy
		return nil
	}

	return fmt.Errorf("no ready Instance found")
}

// buildConnectionOpts reads TLS and token configuration from the Instance CR and its referenced Secrets.
func (wb *WatcherBridge) buildConnectionOpts(ctx context.Context, inst *platformv1alpha1.Instance) (ConnectionOpts, error) {
	var opts ConnectionOpts

	// TLS configuration
	if inst.Spec.Server.TLS != nil && inst.Spec.Server.TLS.Enabled {
		opts.TLSEnabled = true

		if inst.Spec.Server.TLS.SecretName != "" {
			var tlsSecret corev1.Secret
			key := types.NamespacedName{Name: inst.Spec.Server.TLS.SecretName, Namespace: inst.Namespace}
			if err := wb.client.Get(ctx, key, &tlsSecret); err != nil {
				wb.logger.Warn("Failed to read TLS secret, using system CAs",
					zap.String("secret", inst.Spec.Server.TLS.SecretName),
					zap.Error(err))
			} else if caCert, ok := tlsSecret.Data["ca.crt"]; ok {
				opts.CACert = caCert
			}
		}
	}

	// Token authentication
	if inst.Spec.Server.Token != nil && inst.Spec.Server.Token.Name != "" {
		var tokenSecret corev1.Secret
		key := types.NamespacedName{Name: inst.Spec.Server.Token.Name, Namespace: inst.Namespace}
		if err := wb.client.Get(ctx, key, &tokenSecret); err != nil {
			return opts, fmt.Errorf("reading token secret %q: %w", inst.Spec.Server.Token.Name, err)
		}
		tokenKey := inst.Spec.Server.Token.Key
		if tokenKey == "" {
			tokenKey = "token"
		}
		tokenValue, ok := tokenSecret.Data[tokenKey]
		if !ok {
			return opts, fmt.Errorf("key %q not found in secret %q", tokenKey, inst.Spec.Server.Token.Name)
		}
		opts.Token = string(tokenValue)
	}

	return opts, nil
}

func (wb *WatcherBridge) createAnomaly(ctx context.Context, alert *pb.WatcherAlert) error {
	signalType := MapAlertTypeToSignal(alert.Type)
	ns := alert.Namespace
	if ns == "" {
		ns = "default"
	}

	name := fmt.Sprintf("watcher-%s-%s-%d", strings.ToLower(alert.Type), alert.Deployment, alert.TimestampUnix)
	// Sanitize name for K8s (lowercase, max 63 chars, no invalid chars)
	name = sanitizeK8sName(name)

	labels := map[string]string{
		"platform.chatcli.io/source":     "watcher",
		"platform.chatcli.io/deployment": alert.Deployment,
	}
	// Link to the Instance that produced this anomaly (cross-namespace, so labels not OwnerRef)
	if wb.connectedInstance != nil {
		labels["platform.chatcli.io/instance"] = wb.connectedInstance.Name
		labels["platform.chatcli.io/instance-namespace"] = wb.connectedInstance.Namespace
	}

	anomaly := &platformv1alpha1.Anomaly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Spec: platformv1alpha1.AnomalySpec{
			Source:     platformv1alpha1.AnomalySourceWatcher,
			SignalType: signalType,
			Resource: platformv1alpha1.ResourceRef{
				Kind:      inferResourceKind(alert),
				Name:      alert.Deployment,
				Namespace: ns,
			},
			Value:       alert.Message,
			Threshold:   "normal",
			Description: alert.Message,
		},
	}

	if err := wb.client.Create(ctx, anomaly); err != nil {
		// Same (type, deployment, namespace, timestamp) produces a deterministic name.
		// If the CR already exists (operator restart wiped the in-memory dedup map,
		// or the server re-emits a still-active alert), treat it as a successful no-op
		// so the caller marks the hash as seen and stops re-trying each poll.
		if errors.IsAlreadyExists(err) {
			wb.logger.Debug("Anomaly CR already exists, treating as idempotent success",
				zap.String("name", name),
				zap.String("signal", string(signalType)),
				zap.String("deployment", alert.Deployment))
			return nil
		}
		return fmt.Errorf("creating anomaly %s: %w", name, err)
	}

	wb.logger.Info("Created Anomaly CR",
		zap.String("name", name),
		zap.String("signal", string(signalType)),
		zap.String("deployment", alert.Deployment))
	return nil
}

// inferResourceKind determines the Kubernetes resource kind from a watcher alert.
// If the alert includes a resource_kind field (from enhanced watchers), it uses that.
// Otherwise, it infers from the alert type: node-level alerts → Node, job alerts → Job,
// and defaults to Deployment for backward compatibility.
func inferResourceKind(alert *pb.WatcherAlert) string {
	// If the proto message carries an explicit resource_kind, prefer it
	if alert.Object != "" {
		// alert.Object sometimes carries "kind/name" format (e.g., "StatefulSet/postgres")
		if idx := strings.Index(alert.Object, "/"); idx > 0 {
			kind := alert.Object[:idx]
			switch kind {
			case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Node":
				return kind
			}
		}
	}

	// Infer from alert type for known patterns
	switch alert.Type {
	case "NodeNotReady", "DiskPressure", "MemoryPressure", "PIDPressure", "NetworkUnavailable", "NodeUnschedulable", "PodCapacityHigh":
		return "Node"
	case "JobFailed", "CronJobMissed":
		return "Job"
	default:
		return "Deployment"
	}
}

// MapAlertTypeToSignal maps watcher AlertType strings to AnomalySignalType.
// Covers all known watcher alert types and maps them to the operator's 21 signal types.
func MapAlertTypeToSignal(alertType string) platformv1alpha1.AnomalySignalType {
	switch alertType {
	// Pod-level signals
	case "HighRestartCount":
		return platformv1alpha1.SignalPodRestart
	case "OOMKilled":
		return platformv1alpha1.SignalOOMKill
	case "PodNotReady":
		return platformv1alpha1.SignalPodNotReady
	case "CrashLoopBackOff":
		return platformv1alpha1.SignalCrashLoopBackOff
	case "ImagePullBackOff", "ErrImagePull", "ImagePullError":
		return platformv1alpha1.SignalImagePullError

	// Deployment/workload signals
	case "DeploymentFailing":
		return platformv1alpha1.SignalDeployFail

	// Resource signals
	case "CPUHigh", "HighCPU":
		return platformv1alpha1.SignalCPUHigh
	case "MemoryHigh", "HighMemory":
		return platformv1alpha1.SignalMemoryHigh

	// Node-level signals
	case "DiskPressure":
		return platformv1alpha1.SignalDiskPressure
	case "NodeNotReady":
		return platformv1alpha1.SignalNodeNotReady
	case "MemoryPressure":
		return platformv1alpha1.SignalMemoryHigh
	case "PIDPressure":
		return platformv1alpha1.SignalPIDPressure
	case "NetworkUnavailable":
		return platformv1alpha1.SignalNetworkUnavail
	case "NodeUnschedulable":
		return platformv1alpha1.SignalNodeNotReady
	case "PodCapacityHigh":
		return platformv1alpha1.SignalPodCapacityHigh

	// Application signals
	case "HighErrorRate", "ErrorRate":
		return platformv1alpha1.SignalErrorRate
	case "HighLatency", "Latency":
		return platformv1alpha1.SignalLatency

	// Infrastructure signals
	case "PVCPending":
		return platformv1alpha1.SignalPVCPending
	case "IngressError":
		return platformv1alpha1.SignalIngressError
	case "HPAMaxedOut", "HPAMaxed":
		return platformv1alpha1.SignalHPAMaxed
	case "CertificateExpiring":
		return platformv1alpha1.SignalCertExpiring

	// Job signals
	case "JobFailed":
		return platformv1alpha1.SignalJobFailed
	case "CronJobMissed":
		return platformv1alpha1.SignalCronJobMissed

	// GitOps signals
	case "HelmReleaseFailed":
		return platformv1alpha1.SignalHelmReleaseFailed
	case "ArgoCDDegraded":
		return platformv1alpha1.SignalArgoCDDegraded
	case "ConfigDrift":
		return platformv1alpha1.SignalConfigDrift

	default:
		// Normalize unknown alert types to snake_case for forward compatibility
		normalized := strings.ToLower(alertType)
		normalized = strings.ReplaceAll(normalized, " ", "_")
		return platformv1alpha1.AnomalySignalType(normalized)
	}
}

// alertHash generates a dedup hash for an alert using type|deployment|namespace|uid.
// The pure-function form is preserved so callers can pass any UID value (including
// the missing-UID sentinel). The bridge's computeAlertHash wrapper handles the
// live K8s lookup for the UID.
//
// GAP-02 fix (chaos test report 2026-05-23): the prior hash used only
// type|deployment|namespace, which made the operator blind to recreated resources
// for the full dedup TTL window (a common GitOps / rollback pattern). Including
// the UID makes the hash naturally change on resource recreation.
func alertHash(alert *pb.WatcherAlert, uid string) string {
	if uid == "" {
		uid = missingUIDSentinel
	}
	data := fmt.Sprintf("%s|%s|%s|%s", alert.Type, alert.Deployment, alert.Namespace, uid)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:8])
}

// computeAlertHash looks up the resource UID from the K8s API and returns the
// final dedup hash. Best-effort: when the resource can't be located, the sentinel
// UID is used (which is still a stable, distinct hash for the "resource gone"
// case — UID-bearing alerts for any existing resource will not collide with it).
func (wb *WatcherBridge) computeAlertHash(ctx context.Context, alert *pb.WatcherAlert) string {
	return alertHash(alert, wb.lookupResourceUID(ctx, alert))
}

// lookupResourceUID fetches the K8s UID of the resource referenced by the alert.
// Returns "" when the resource can't be located — the caller substitutes a
// sentinel in that case. The function tries the inferred kind first; on miss it
// falls back to other workload kinds because some watchers don't tag the kind
// explicitly. Errors other than NotFound are logged at Debug level only — this
// path runs on every poll cycle and must stay quiet under transient failures.
func (wb *WatcherBridge) lookupResourceUID(ctx context.Context, alert *pb.WatcherAlert) string {
	if alert.Deployment == "" {
		return ""
	}
	ns := alert.Namespace
	if ns == "" {
		ns = "default"
	}
	name := alert.Deployment
	key := types.NamespacedName{Name: name, Namespace: ns}

	kind := inferResourceKind(alert)

	// Try the inferred kind first.
	if uid := wb.fetchUIDForKind(ctx, key, kind); uid != "" {
		return uid
	}

	// Fallback to other workload kinds (some watchers don't tag the kind).
	fallbacks := []string{"Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"}
	for _, fk := range fallbacks {
		if fk == kind {
			continue
		}
		if uid := wb.fetchUIDForKind(ctx, key, fk); uid != "" {
			return uid
		}
	}
	return ""
}

// fetchUIDForKind returns the UID for a workload of the given kind, or "" if not
// found. Uses unstructured to avoid pulling in apps/v1 + batch/v1 type registrations.
func (wb *WatcherBridge) fetchUIDForKind(ctx context.Context, key types.NamespacedName, kind string) string {
	gvk, ok := workloadGVKForKind(kind)
	if !ok {
		return ""
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := wb.client.Get(ctx, key, obj); err != nil {
		if !errors.IsNotFound(err) {
			wb.logger.Debug("UID lookup failed",
				zap.String("kind", kind),
				zap.String("name", key.Name),
				zap.String("namespace", key.Namespace),
				zap.Error(err))
		}
		return ""
	}
	return string(obj.GetUID())
}

// workloadGVKForKind returns the GroupVersionKind for the workload kinds the
// watcher emits alerts for. Adding a new kind here also requires the operator's
// RBAC to include get/list on that resource (see config/rbac).
func workloadGVKForKind(kind string) (schema.GroupVersionKind, bool) {
	switch kind {
	case "Deployment":
		return schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, true
	case "StatefulSet":
		return schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "StatefulSet"}, true
	case "DaemonSet":
		return schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DaemonSet"}, true
	case "Job":
		return schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"}, true
	case "CronJob":
		return schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"}, true
	case "Node":
		return schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Node"}, true
	}
	return schema.GroupVersionKind{}, false
}

func (wb *WatcherBridge) isDuplicate(hash string) bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	_, exists := wb.seen[hash]
	return exists
}

func (wb *WatcherBridge) markSeen(hash, deployment, namespace string) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	wb.seen[hash] = dedupEntry{
		seenAt:     time.Now(),
		deployment: deployment,
		namespace:  namespace,
	}
}

func (wb *WatcherBridge) pruneDedup() {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	ttl := DefaultDedupTTL
	if wb.connectedInstance != nil {
		ttl = wb.connectedInstance.Spec.AIOps.GetDedupTTL()
	}
	cutoff := time.Now().Add(-ttl)
	for hash, entry := range wb.seen {
		if entry.seenAt.Before(cutoff) {
			delete(wb.seen, hash)
		}
	}
}

// InvalidateDedupForResource removes all dedup entries for a specific
// deployment+namespace, allowing new anomalies to be detected immediately.
// Called when an Issue reaches a terminal state (Resolved/Escalated/Failed) so
// that genuine new problems are detected without delay.
//
// GAP-02 fix: prior implementation brute-forced the hash by trying every known
// alert type, which broke after we added UID into the hash. The dedup map now
// carries the resource ref alongside the hash, so we can match by name+namespace
// directly — and it covers ALL alert types, not just the original five.
func (wb *WatcherBridge) InvalidateDedupForResource(deployment, namespace string) {
	wb.mu.Lock()
	defer wb.mu.Unlock()

	for hash, entry := range wb.seen {
		if entry.deployment == deployment && entry.namespace == namespace {
			delete(wb.seen, hash)
		}
	}
}

func sanitizeK8sName(name string) string {
	// Replace invalid characters with dashes
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	result := b.String()
	// Trim leading/trailing dashes
	result = strings.Trim(result, "-.")
	// Truncate to 63 characters (K8s name limit)
	if len(result) > 63 {
		result = result[:63]
	}
	result = strings.TrimRight(result, "-.")
	return result
}

// GetSeenCount returns the number of dedup entries (for testing).
func (wb *WatcherBridge) GetSeenCount() int {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	return len(wb.seen)
}

// SetServerAddress is a convenience method to directly set the server address.
// Used primarily in testing or local development.
func (wb *WatcherBridge) SetServerAddress(address string) error {
	return wb.serverClient.Connect(address, ConnectionOpts{})
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (wb *WatcherBridge) NeedLeaderElection() bool {
	return true
}

// ResolveServerAddress looks up Instance CRs and returns the gRPC address of the first ready instance.
func (wb *WatcherBridge) ResolveServerAddress(ctx context.Context) (string, error) {
	var instances platformv1alpha1.InstanceList
	if err := wb.client.List(ctx, &instances); err != nil {
		return "", fmt.Errorf("listing instances: %w", err)
	}

	for _, inst := range instances.Items {
		if !inst.Status.Ready {
			continue
		}
		port := inst.Spec.Server.Port
		if port == 0 {
			port = 50051
		}
		return fmt.Sprintf("dns:///%s.%s.svc.cluster.local:%d", inst.Name, inst.Namespace, port), nil
	}

	return "", fmt.Errorf("no ready Instance found")
}

// GetAnomalyByName returns an Anomaly CR by name from the given namespace.
func (wb *WatcherBridge) GetAnomalyByName(ctx context.Context, name, namespace string) (*platformv1alpha1.Anomaly, error) {
	var anomaly platformv1alpha1.Anomaly
	if err := wb.client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &anomaly); err != nil {
		return nil, err
	}
	return &anomaly, nil
}
