package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"go.uber.org/zap"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const (
	// PollInterval is how often the bridge polls the server for alerts.
	PollInterval = 30 * time.Second

	// DedupTTL is how long dedup hashes are kept to avoid creating duplicate anomalies.
	DedupTTL = 2 * time.Hour
)

// WatcherBridge polls the ChatCLI server's GetAlerts RPC and creates Anomaly CRs.
// It implements manager.Runnable so it runs as a background goroutine in the controller manager.
type WatcherBridge struct {
	client       client.Client
	scheme       *runtime.Scheme
	serverClient *ServerClient
	logger       *zap.Logger

	mu                sync.Mutex
	seen              map[string]time.Time       // hash → first-seen timestamp for dedup
	connectedInstance *platformv1alpha1.Instance // Instance we're connected to (for OwnerRef)
}

// NewWatcherBridge creates a new WatcherBridge.
func NewWatcherBridge(c client.Client, scheme *runtime.Scheme, sc *ServerClient, logger *zap.Logger) *WatcherBridge {
	return &WatcherBridge{
		client:       c,
		scheme:       scheme,
		serverClient: sc,
		logger:       logger.Named("watcher-bridge"),
		seen:         make(map[string]time.Time),
	}
}

// Start implements manager.Runnable. It runs a polling loop until the context is cancelled.
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
		hash := alertHash(alert)
		if wb.isDuplicate(hash) {
			continue
		}

		if err := wb.createAnomaly(ctx, alert); err != nil {
			wb.logger.Error("Failed to create Anomaly CR", zap.Error(err), zap.String("alert_type", alert.Type))
			continue
		}
		wb.markSeen(hash)
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
		address := fmt.Sprintf("%s.%s.svc.cluster.local:%d", inst.Name, inst.Namespace, port)

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
				Kind:      "Deployment",
				Name:      alert.Deployment,
				Namespace: ns,
			},
			Value:       alert.Message,
			Threshold:   "normal",
			Description: alert.Message,
		},
	}

	if err := wb.client.Create(ctx, anomaly); err != nil {
		return fmt.Errorf("creating anomaly %s: %w", name, err)
	}

	wb.logger.Info("Created Anomaly CR",
		zap.String("name", name),
		zap.String("signal", string(signalType)),
		zap.String("deployment", alert.Deployment))
	return nil
}

// MapAlertTypeToSignal maps watcher AlertType strings to AnomalySignalType.
func MapAlertTypeToSignal(alertType string) platformv1alpha1.AnomalySignalType {
	switch alertType {
	case "HighRestartCount":
		return platformv1alpha1.SignalPodRestart
	case "OOMKilled":
		return platformv1alpha1.SignalOOMKill
	case "PodNotReady":
		return platformv1alpha1.SignalPodNotReady
	case "DeploymentFailing":
		return platformv1alpha1.SignalDeployFail
	case "CrashLoopBackOff":
		return platformv1alpha1.SignalPodRestart
	default:
		return platformv1alpha1.AnomalySignalType(strings.ToLower(alertType))
	}
}

// alertHash generates a dedup hash for an alert using type|object|deployment|namespace|minute-bucket.
func alertHash(alert *pb.WatcherAlert) string {
	ts := time.Unix(alert.TimestampUnix, 0)
	minuteBucket := ts.Truncate(time.Minute).Unix()
	data := fmt.Sprintf("%s|%s|%s|%s|%d", alert.Type, alert.Object, alert.Deployment, alert.Namespace, minuteBucket)
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h[:8])
}

func (wb *WatcherBridge) isDuplicate(hash string) bool {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	_, exists := wb.seen[hash]
	return exists
}

func (wb *WatcherBridge) markSeen(hash string) {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	wb.seen[hash] = time.Now()
}

func (wb *WatcherBridge) pruneDedup() {
	wb.mu.Lock()
	defer wb.mu.Unlock()
	cutoff := time.Now().Add(-DedupTTL)
	for hash, ts := range wb.seen {
		if ts.Before(cutoff) {
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
		return fmt.Sprintf("%s.%s.svc.cluster.local:%d", inst.Name, inst.Namespace, port), nil
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
