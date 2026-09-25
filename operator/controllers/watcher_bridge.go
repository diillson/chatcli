package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const (
	// PollInterval is how often the bridge polls the server for alerts when
	// it is on the polling transport, and how long it waits between
	// discovery attempts while no Instance is ready.
	PollInterval = 30 * time.Second

	// streamHeartbeatTimeout is how long the bridge tolerates silence on the
	// alert stream before it treats the connection as dead and reopens it.
	// The server heartbeats every 15 seconds, so this allows three misses.
	streamHeartbeatTimeout = 60 * time.Second

	// streamRetryAfter is how long the bridge stays on polling after a server
	// answered StreamAlerts with UNIMPLEMENTED, before it probes the stream
	// again. Server upgrades roll without an operator restart.
	streamRetryAfter = 10 * time.Minute

	// streamBackoffMax caps the exponential backoff between stream reopens.
	streamBackoffMax = 30 * time.Second

	// streamFailuresBeforeRediscover is how many silent stream attempts in a
	// row the bridge tolerates on one connection before it rediscovers the
	// Instance. gRPC re-resolves the Service address on its own, so a pod
	// restart heals without this; a replaced Instance does not.
	streamFailuresBeforeRediscover = 3

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

// AlertTransport selects how the bridge receives alerts from the server.
type AlertTransport string

const (
	// AlertTransportStream keeps a StreamAlerts stream open and falls back
	// to polling only while the server has no such RPC. The default.
	AlertTransportStream AlertTransport = "stream"
	// AlertTransportPoll queries GetAlerts every PollInterval.
	AlertTransportPoll AlertTransport = "poll"
)

// ParseAlertTransport reads the CHATCLI_OPERATOR_ALERT_TRANSPORT value. An
// empty value means the default, stream; anything else must be one of the
// two transports.
func ParseAlertTransport(v string) (AlertTransport, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", string(AlertTransportStream):
		return AlertTransportStream, nil
	case string(AlertTransportPoll):
		return AlertTransportPoll, nil
	}
	return "", fmt.Errorf("unknown alert transport %q: want %q or %q", v, AlertTransportStream, AlertTransportPoll)
}

// errStreamStalled is returned when the alert stream went silent for longer
// than streamHeartbeatTimeout.
var errStreamStalled = fmt.Errorf("alert stream stalled: no heartbeat from the server")

// WatcherBridge receives the ChatCLI server's watcher alerts and creates
// Anomaly CRs. It keeps a StreamAlerts stream open and polls GetAlerts only
// as a fallback for a server without that RPC or when configured to.
// It implements manager.Runnable so it runs as a background goroutine in the controller manager.
type WatcherBridge struct {
	client       client.Client
	scheme       *runtime.Scheme
	serverClient *ServerClient
	logger       *zap.Logger

	transport        AlertTransport
	pollInterval     time.Duration
	heartbeatTimeout time.Duration
	// streamUnsupportedUntil is set when the server answered UNIMPLEMENTED;
	// polling covers the gap until the stream is probed again.
	streamUnsupportedUntil time.Time
	// streamFailures counts consecutive stream attempts that delivered
	// nothing. After streamFailuresBeforeRediscover the connection is
	// dropped so the next round rediscovers the Instance: the address or
	// credentials may have changed underneath a long-lived connection.
	streamFailures int

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
		client:           c,
		scheme:           scheme,
		serverClient:     sc,
		logger:           logger.Named("watcher-bridge"),
		transport:        AlertTransportStream,
		pollInterval:     PollInterval,
		heartbeatTimeout: streamHeartbeatTimeout,
		seen:             make(map[string]dedupEntry),
	}
}

// SetAlertTransport selects stream or poll. Call it before Start.
func (wb *WatcherBridge) SetAlertTransport(t AlertTransport) {
	wb.transport = t
}

// Start implements manager.Runnable. It keeps the bridge attached to a ready
// Instance until the context is canceled: streaming alerts when the server
// offers StreamAlerts, polling GetAlerts otherwise.
func (wb *WatcherBridge) Start(ctx context.Context) error {
	wb.logger.Info("WatcherBridge started",
		zap.String("transport", string(wb.transport)),
		zap.Duration("poll_interval", wb.pollInterval))

	backoff := time.Second
	for ctx.Err() == nil {
		wb.cycle(ctx, &backoff)
	}
	wb.logger.Info("WatcherBridge stopped")
	return nil
}

// cycle runs one attach-and-consume round: connect when needed, then either
// hold the stream open until it ends or poll once and wait. backoff grows
// while stream reopens keep failing and resets once one delivers.
func (wb *WatcherBridge) cycle(ctx context.Context, backoff *time.Duration) {
	if !wb.serverClient.IsConnected() {
		if err := wb.discoverAndConnect(ctx); err != nil {
			wb.logger.Debug("Server discovery failed", zap.Error(err))
			sleepCtx(ctx, wb.pollInterval)
			return
		}
	}

	if wb.streamWanted() {
		received, err := wb.consumeStream(ctx)
		if ctx.Err() != nil {
			return
		}
		if received > 0 {
			*backoff = time.Second
			wb.streamFailures = 0
		} else {
			wb.streamFailures++
		}
		switch {
		case status.Code(err) == codes.Unimplemented:
			// Older server: poll until it is worth probing the stream again.
			wb.streamUnsupportedUntil = time.Now().Add(streamRetryAfter)
			wb.logger.Info("Server has no StreamAlerts RPC; polling GetAlerts",
				zap.Duration("retry_stream_in", streamRetryAfter))
		case status.Code(err) == codes.Aborted:
			// The server dropped us for falling behind: reopen at once with
			// the current snapshot, dedup absorbs the repeats.
			wb.logger.Warn("Alert stream asked for a resync", zap.Error(err))
		default:
			// Transport failure or silence: back off and reopen. gRPC keeps
			// re-resolving the Service, so only a run of silent attempts
			// drops the connection for a full rediscovery of the Instance.
			wb.logger.Warn("Alert stream ended; reopening",
				zap.Error(err), zap.Duration("backoff", *backoff), zap.Int("silent_attempts", wb.streamFailures))
			if wb.streamFailures >= streamFailuresBeforeRediscover {
				wb.streamFailures = 0
				_ = wb.serverClient.Close()
			}
			sleepCtx(ctx, *backoff)
			*backoff = min(*backoff*2, streamBackoffMax)
		}
		return
	}

	wb.poll(ctx)
	sleepCtx(ctx, wb.pollInterval)
}

// streamWanted reports whether this round should open the stream.
func (wb *WatcherBridge) streamWanted() bool {
	if wb.transport != AlertTransportStream {
		return false
	}
	return time.Now().After(wb.streamUnsupportedUntil)
}

// consumeStream holds a StreamAlerts stream open and turns every alert into
// an Anomaly. It returns how many messages arrived and why the stream ended:
// the context, a gRPC status, or errStreamStalled after heartbeatTimeout of
// silence.
func (wb *WatcherBridge) consumeStream(ctx context.Context) (int, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := wb.serverClient.StreamAlerts(sctx, true)
	if err != nil {
		return 0, err
	}
	wb.logger.Info("Alert stream open")

	type msg struct {
		resp *pb.StreamAlertsResponse
		err  error
	}
	msgs := make(chan msg, 1)
	go func() {
		for {
			resp, err := stream.Recv()
			select {
			case msgs <- msg{resp: resp, err: err}:
			case <-sctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	received := 0
	timer := time.NewTimer(wb.heartbeatTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return received, ctx.Err()
		case <-timer.C:
			return received, errStreamStalled
		case m := <-msgs:
			if m.err != nil {
				return received, m.err
			}
			received++
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(wb.heartbeatTimeout)
			if m.resp.GetHeartbeat() {
				wb.pruneDedup()
				continue
			}
			if a := m.resp.GetAlert(); a != nil {
				wb.handleAlert(ctx, a)
			}
		}
	}
}

// poll queries GetAlerts once and turns new alerts into Anomalies.
func (wb *WatcherBridge) poll(ctx context.Context) {
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
		wb.handleAlert(ctx, alert)
	}

	wb.pruneDedup()
}

// handleAlert creates the Anomaly for an alert the bridge has not seen in
// the dedup window. Shared by the stream and the polling paths.
func (wb *WatcherBridge) handleAlert(ctx context.Context, alert *pb.WatcherAlert) {
	hash := wb.computeAlertHash(ctx, alert)
	if wb.isDuplicate(hash) {
		return
	}

	if err := wb.createAnomaly(ctx, alert); err != nil {
		wb.logger.Error("Failed to create Anomaly CR", zap.Error(err), zap.String("alert_type", alert.Type))
		return
	}
	ns := alert.Namespace
	if ns == "" {
		ns = "default"
	}
	wb.markSeen(hash, alert.Deployment, ns)
}

// sleepCtx waits for d or until ctx ends, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
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

// buildConnectionOpts reads the transport and credential configuration of
// the Instance (see instanceConnectionOpts for the precedence).
func (wb *WatcherBridge) buildConnectionOpts(ctx context.Context, inst *platformv1alpha1.Instance) (ConnectionOpts, error) {
	return instanceConnectionOpts(ctx, wb.client, inst, wb.logger)
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
