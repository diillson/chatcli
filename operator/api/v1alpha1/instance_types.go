package v1alpha1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstanceSpec defines the desired state of a ChatCLI deployment.
type InstanceSpec struct {
	// Replicas is the number of ChatCLI server pods.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Provider is the LLM provider (OPENAI, OPENAI_ASSISTANT, CLAUDEAI, BEDROCK, GOOGLEAI, XAI, ZAI, MINIMAX, STACKSPOT, OLLAMA, COPILOT, GITHUB_MODELS, OPENROUTER).
	// BEDROCK uses the AWS credentials chain (IAM role via IRSA, static keys in the Secret, or instance profile).
	Provider string `json:"provider"`

	// Model is the LLM model name.
	// +optional
	Model string `json:"model,omitempty"`

	// Image defines the container image for ChatCLI.
	// +optional
	Image ImageSpec `json:"image,omitempty"`

	// Server configures the gRPC server.
	// +optional
	Server ServerSpec `json:"server,omitempty"`

	// Watcher configures the Kubernetes resource watcher.
	// +optional
	Watcher *WatcherSpec `json:"watcher,omitempty"`

	// Resources defines resource requests/limits for the ChatCLI container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Persistence configures persistent storage for sessions.
	// +optional
	Persistence *PersistenceSpec `json:"persistence,omitempty"`

	// SecurityContext for the pod.
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`

	// Fallback configures automatic provider failover.
	// When the primary provider fails (rate limit, timeout, server error),
	// the system tries the next provider in the chain automatically.
	// +optional
	Fallback *FallbackSpec `json:"fallback,omitempty"`

	// AIOps configures the autonomous incident management pipeline.
	// All fields are optional with sensible defaults.
	// +optional
	AIOps *AIOpsSpec `json:"aiops,omitempty"`

	// APIKeys references a Secret containing provider API keys and config.
	// Expected keys: OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLEAI_API_KEY, XAI_API_KEY,
	// ZAI_API_KEY, MINIMAX_API_KEY, MINIMAX_API_COMPAT, GITHUB_COPILOT_TOKEN, OPENROUTER_API_KEY.
	// For BEDROCK: AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY (+ optional AWS_SESSION_TOKEN),
	// or leave unset and use IRSA by annotating the pod's ServiceAccount with
	// eks.amazonaws.com/role-arn. Region is set via AWS_REGION or BEDROCK_REGION.
	// All providers used in the fallback chain must have their credentials in this Secret.
	// +optional
	APIKeys *SecretRefSpec `json:"apiKeys,omitempty"`

	// Agents configures agent/skill provisioning for the instance.
	// +optional
	Agents *AgentProvisionSpec `json:"agents,omitempty"`

	// Plugins configures plugin provisioning for the instance.
	// +optional
	Plugins *PluginProvisionSpec `json:"plugins,omitempty"`

	// MCP configures MCP (Model Context Protocol) servers for the instance.
	// MCP servers expose external tools (filesystem, search, databases, etc.)
	// that the AI can invoke during conversations.
	// +optional
	MCP *MCPSpec `json:"mcp,omitempty"`

	// ExtraEnv allows passing arbitrary environment variables to the ChatCLI server pod.
	// Use this for security configuration (CHATCLI_RATE_LIMIT_RPS, CHATCLI_AGENT_SECURITY_MODE, etc.)
	// or any other CHATCLI_* env vars not covered by structured fields.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`
}

// MCPSpec configures MCP server connections for the instance.
type MCPSpec struct {
	// Enabled activates MCP server connections.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Servers is the list of MCP server configurations.
	// +optional
	Servers []MCPServerSpec `json:"servers,omitempty"`

	// ExistingConfigMap references an existing ConfigMap containing mcp_servers.json.
	// If set, Servers field is ignored and this ConfigMap is mounted instead.
	// +optional
	ExistingConfigMap string `json:"existingConfigMap,omitempty"`
}

// MCPServerSpec defines a single MCP server connection.
type MCPServerSpec struct {
	// Name is a unique identifier for this MCP server.
	Name string `json:"name"`

	// Transport is the communication protocol: "stdio" (local process) or "sse" (HTTP SSE).
	// +kubebuilder:validation:Enum=stdio;sse
	Transport string `json:"transport"`

	// Command is the executable to start (stdio transport only).
	// +optional
	Command string `json:"command,omitempty"`

	// Args are command-line arguments for the executable (stdio transport only).
	// +optional
	Args []string `json:"args,omitempty"`

	// Env are environment variables for the process (stdio transport only).
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// URL is the SSE endpoint URL (sse transport only).
	// +optional
	URL string `json:"url,omitempty"`

	// Enabled controls whether this server is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Overrides lists built-in plugin names that this MCP server replaces.
	// When the server is connected, these built-ins are hidden from the LLM prompt.
	// If the server disconnects, the built-ins are automatically restored (fallback).
	// Example: ["@webfetch", "@websearch"]
	// +optional
	Overrides []string `json:"overrides,omitempty"`
}

// ImageSpec defines the container image configuration.
type ImageSpec struct {
	// Repository is the container image repository.
	// +kubebuilder:default="ghcr.io/diillson/chatcli"
	Repository string `json:"repository,omitempty"`

	// Tag is the container image tag.
	// +kubebuilder:default="latest"
	Tag string `json:"tag,omitempty"`

	// PullPolicy defines the image pull policy.
	// +kubebuilder:default="IfNotPresent"
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// ServerSpec configures the gRPC server.
type ServerSpec struct {
	// Port is the gRPC server port.
	// +kubebuilder:default=50051
	Port int32 `json:"port,omitempty"`

	// MetricsPort is the Prometheus metrics HTTP port (0 = disabled).
	// +kubebuilder:default=9090
	// +optional
	MetricsPort int32 `json:"metricsPort,omitempty"`

	// TLS configures TLS for the gRPC server.
	// +optional
	TLS *TLSSpec `json:"tls,omitempty"`

	// Token references a Secret containing the auth token.
	// +optional
	Token *SecretKeyRefSpec `json:"token,omitempty"`

	// Security configures server security parameters.
	// +optional
	Security *ServerSecuritySpec `json:"security,omitempty"`
}

// ServerSecuritySpec defines security settings for the gRPC server.
type ServerSecuritySpec struct {
	// JWTSecretRef references a Secret key containing the JWT signing secret.
	// Maps to CHATCLI_JWT_SECRET env var.
	// +optional
	JWTSecretRef *SecretKeyRefSpec `json:"jwtSecretRef,omitempty"`

	// RateLimitRPS is the per-client rate limit in requests per second.
	// Maps to CHATCLI_RATE_LIMIT_RPS env var. Default: 10.
	// +optional
	RateLimitRPS *int32 `json:"rateLimitRps,omitempty"`

	// RateLimitBurst is the maximum burst size for rate limiting.
	// Maps to CHATCLI_RATE_LIMIT_BURST env var. Default: 30.
	// +optional
	RateLimitBurst *int32 `json:"rateLimitBurst,omitempty"`

	// MaxRecvMsgSize is the maximum message size in bytes the server can receive.
	// Maps to CHATCLI_MAX_RECV_MSG_SIZE env var. Default: 52428800 (50MB).
	// +optional
	MaxRecvMsgSize *int32 `json:"maxRecvMsgSize,omitempty"`

	// MaxSendMsgSize is the maximum message size in bytes the server can send.
	// Maps to CHATCLI_MAX_SEND_MSG_SIZE env var. Default: 52428800 (50MB).
	// +optional
	MaxSendMsgSize *int32 `json:"maxSendMsgSize,omitempty"`

	// MaxConcurrentStreams limits simultaneous gRPC streams per connection.
	// Maps to CHATCLI_MAX_CONCURRENT_STREAMS env var. Default: 100.
	// +optional
	MaxConcurrentStreams *int32 `json:"maxConcurrentStreams,omitempty"`

	// BindAddress is the address to bind the server to.
	// Maps to CHATCLI_BIND_ADDRESS env var. Default: "127.0.0.1".
	// Set to "0.0.0.0" to expose to all interfaces.
	// +optional
	BindAddress string `json:"bindAddress,omitempty"`

	// AuditLogPath is the file path for structured audit logs (JSON lines).
	// Maps to CHATCLI_AUDIT_LOG_PATH env var.
	// +optional
	AuditLogPath string `json:"auditLogPath,omitempty"`

	// Debug enables verbose logging including stack traces on errors.
	// Maps to CHATCLI_DEBUG env var.
	// +optional
	Debug bool `json:"debug,omitempty"`

	// EnableReflection enables gRPC reflection (requires also CHATCLI_GRPC_REFLECTION=true).
	// +optional
	EnableReflection bool `json:"enableReflection,omitempty"`
}

// TLSSpec configures TLS.
type TLSSpec struct {
	// Enabled enables TLS.
	Enabled bool `json:"enabled,omitempty"`

	// SecretName is the name of the Secret containing tls.crt and tls.key.
	// If the Secret also contains a ca.crt key, the operator will use it as
	// the trust root when dialing the gRPC server — required for self-signed
	// certificates, otherwise the connection fails with
	// "certificate signed by unknown authority".
	SecretName string `json:"secretName,omitempty"`
}

// SecretKeyRefSpec references a specific key in a Secret.
type SecretKeyRefSpec struct {
	// Name is the Secret name.
	Name string `json:"name"`
	// Key is the key within the Secret.
	Key string `json:"key"`
}

// SecretRefSpec references a Secret by name.
type SecretRefSpec struct {
	// Name is the Secret name.
	Name string `json:"name"`
}

// WatcherSpec configures the Kubernetes resource watcher.
type WatcherSpec struct {
	// Enabled activates the watcher.
	Enabled bool `json:"enabled,omitempty"`

	// Deployment is the target deployment to watch (legacy single-target mode).
	// +optional
	Deployment string `json:"deployment,omitempty"`

	// Namespace is the namespace of the target deployment (legacy single-target mode).
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Targets defines multiple deployments to watch simultaneously.
	// When set, Deployment and Namespace fields are ignored.
	// +optional
	Targets []WatchTargetSpec `json:"targets,omitempty"`

	// Interval is the collection interval (e.g., "30s", "1m").
	// +kubebuilder:default="30s"
	Interval string `json:"interval,omitempty"`

	// Window is the observation window (e.g., "2h", "30m").
	// +kubebuilder:default="2h"
	Window string `json:"window,omitempty"`

	// MaxLogLines is the maximum number of log lines to collect per pod.
	// +kubebuilder:default=100
	MaxLogLines int32 `json:"maxLogLines,omitempty"`

	// MaxContextChars is the maximum characters for LLM context budget.
	// +kubebuilder:default=32000
	MaxContextChars int32 `json:"maxContextChars,omitempty"`
}

// WatchTargetSpec defines a single resource target for multi-target watching.
type WatchTargetSpec struct {
	// Name is the resource name to monitor (e.g., "api-gateway", "postgres", "fluentd").
	// +optional
	Name string `json:"name,omitempty"`

	// Deployment is the resource name to monitor.
	// Deprecated: Use "name" instead. Kept for backward compatibility — if both are set, "name" takes precedence.
	// +optional
	Deployment string `json:"deployment,omitempty"`

	// Kind is the Kubernetes resource kind to monitor.
	// Supported values: Deployment, StatefulSet, DaemonSet, Job, CronJob.
	// Defaults to "Deployment" when omitted for backward compatibility.
	// +kubebuilder:validation:Enum=Deployment;StatefulSet;DaemonSet;Job;CronJob
	// +kubebuilder:default=Deployment
	// +optional
	Kind string `json:"kind,omitempty"`

	// Namespace is the namespace of the resource.
	Namespace string `json:"namespace"`

	// MetricsPort is the port where Prometheus metrics are exposed (0 = disabled).
	// +optional
	MetricsPort int32 `json:"metricsPort,omitempty"`

	// MetricsPath is the HTTP path for Prometheus metrics (default: "/metrics").
	// +optional
	MetricsPath string `json:"metricsPath,omitempty"`

	// MetricsFilter is a list of glob patterns to filter Prometheus metrics.
	// +optional
	MetricsFilter []string `json:"metricsFilter,omitempty"`
}

// ResourceName returns the effective resource name, preferring Name over deprecated Deployment.
func (w *WatchTargetSpec) ResourceName() string {
	if w.Name != "" {
		return w.Name
	}
	return w.Deployment
}

// ResourceKind returns the effective resource kind, defaulting to "Deployment".
func (w *WatchTargetSpec) ResourceKind() string {
	if w.Kind != "" {
		return w.Kind
	}
	return "Deployment"
}

// AgentProvisionSpec configures agent/skill provisioning for the instance.
type AgentProvisionSpec struct {
	// ConfigMapRef references a ConfigMap containing agent .md files as keys.
	// +optional
	ConfigMapRef *string `json:"configMapRef,omitempty"`

	// SkillsConfigMapRef references a ConfigMap containing skill .md files.
	// +optional
	SkillsConfigMapRef *string `json:"skillsConfigMapRef,omitempty"`
}

// PluginProvisionSpec configures plugin provisioning for the instance.
type PluginProvisionSpec struct {
	// Image is an init container image containing plugin binaries in /plugins/.
	// +optional
	Image string `json:"image,omitempty"`

	// PVCName references a PVC with pre-installed plugin binaries.
	// +optional
	PVCName string `json:"pvcName,omitempty"`
}

// FallbackSpec configures automatic provider failover.
// When the primary provider fails, the system automatically tries the next
// provider in the chain. Requires API keys for all providers in the Secret.
type FallbackSpec struct {
	// Enabled activates the fallback chain.
	Enabled bool `json:"enabled"`

	// Providers is an ordered list of fallback providers to try.
	// First entry is highest priority. The primary provider (spec.provider)
	// is always tried first, then these in order.
	Providers []FallbackProviderEntry `json:"providers"`

	// MaxRetries is the number of retries per provider before moving to next.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxRetries int32 `json:"maxRetries,omitempty"`

	// CooldownBase is the initial cooldown duration after a provider fails (e.g., "30s").
	// Subsequent failures use exponential backoff up to CooldownMax.
	// +kubebuilder:default="30s"
	// +optional
	CooldownBase string `json:"cooldownBase,omitempty"`

	// CooldownMax is the maximum cooldown duration (e.g., "5m").
	// +kubebuilder:default="5m"
	// +optional
	CooldownMax string `json:"cooldownMax,omitempty"`
}

// FallbackProviderEntry defines a single provider in the fallback chain.
type FallbackProviderEntry struct {
	// Name is the provider name (OPENAI, OPENAI_ASSISTANT, CLAUDEAI, BEDROCK, GOOGLEAI, XAI, ZAI, MINIMAX, STACKSPOT, OLLAMA, COPILOT, GITHUB_MODELS, OPENROUTER).
	// +kubebuilder:validation:Enum=OPENAI;OPENAI_ASSISTANT;CLAUDEAI;BEDROCK;GOOGLEAI;XAI;ZAI;MINIMAX;STACKSPOT;OLLAMA;COPILOT;GITHUB_MODELS;OPENROUTER
	Name string `json:"name"`

	// Model is the LLM model to use for this provider.
	// +optional
	Model string `json:"model,omitempty"`
}

// AIOpsSpec configures the autonomous incident management pipeline.
type AIOpsSpec struct {
	// MaxRemediationAttempts is the maximum number of remediation attempts before escalating to human.
	// Higher values give the AI more chances to try different strategies.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxRemediationAttempts int32 `json:"maxRemediationAttempts,omitempty"`

	// ResolutionCooldownMinutes is how long (in minutes) after an issue is resolved before
	// new anomalies for the same resource can create a new issue. Prevents stale re-triggers.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=120
	// +optional
	ResolutionCooldownMinutes int32 `json:"resolutionCooldownMinutes,omitempty"`

	// DedupTTLMinutes is how long (in minutes) the bridge dedup cache retains alert hashes.
	// After this period, the same alert can create a new Anomaly CR.
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=5
	// +kubebuilder:validation:Maximum=1440
	// +optional
	DedupTTLMinutes int32 `json:"dedupTTLMinutes,omitempty"`

	// EnableAutoResolve enables automatic resolution of Escalated issues when the
	// watcher detects the resource has recovered (all pods healthy).
	// +kubebuilder:default=true
	// +optional
	EnableAutoResolve *bool `json:"enableAutoResolve,omitempty"`

	// AgenticMaxSteps is the maximum number of steps the AI can take in agentic
	// remediation mode before forced failure. Higher values give the AI more room
	// to investigate and try different approaches.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=3
	// +kubebuilder:validation:Maximum=30
	// +optional
	AgenticMaxSteps int32 `json:"agenticMaxSteps,omitempty"`
}

// GetAgenticMaxSteps returns the configured max agentic steps or the default (10).
func (a *AIOpsSpec) GetAgenticMaxSteps() int32 {
	if a != nil && a.AgenticMaxSteps > 0 {
		return a.AgenticMaxSteps
	}
	return 10
}

// GetMaxRemediationAttempts returns the configured max attempts or the default (5).
func (a *AIOpsSpec) GetMaxRemediationAttempts() int32 {
	if a != nil && a.MaxRemediationAttempts > 0 {
		return a.MaxRemediationAttempts
	}
	return 5
}

// GetResolutionCooldown returns the configured cooldown or the default (10 minutes).
func (a *AIOpsSpec) GetResolutionCooldown() time.Duration {
	if a != nil && a.ResolutionCooldownMinutes > 0 {
		return time.Duration(a.ResolutionCooldownMinutes) * time.Minute
	}
	return 10 * time.Minute
}

// GetDedupTTL returns the configured dedup TTL or the default (60 minutes).
func (a *AIOpsSpec) GetDedupTTL() time.Duration {
	if a != nil && a.DedupTTLMinutes > 0 {
		return time.Duration(a.DedupTTLMinutes) * time.Minute
	}
	return 60 * time.Minute
}

// IsAutoResolveEnabled returns whether auto-resolve is enabled (default: true).
func (a *AIOpsSpec) IsAutoResolveEnabled() bool {
	if a != nil && a.EnableAutoResolve != nil {
		return *a.EnableAutoResolve
	}
	return true
}

// PersistenceSpec configures persistent storage.
type PersistenceSpec struct {
	// Enabled activates persistent storage.
	Enabled bool `json:"enabled,omitempty"`

	// Size is the PVC size (e.g., "1Gi").
	// +kubebuilder:default="1Gi"
	Size string `json:"size,omitempty"`

	// StorageClassName is the storage class to use.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`
}

// InstanceStatus defines the observed state of Instance.
type InstanceStatus struct {
	// Ready indicates whether all replicas are available.
	Ready bool `json:"ready"`

	// Replicas is the total number of desired replicas.
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of ready replicas.
	ReadyReplicas int32 `json:"readyReplicas"`

	// Conditions represent the latest available observations of the instance state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration tracks which generation was last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=inst
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.replicas"
// +kubebuilder:printcolumn:name="Provider",type="string",JSONPath=".spec.provider"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Instance is the Schema for the instances API.
type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec,omitempty"`
	Status InstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InstanceList contains a list of Instance.
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Instance{}, &InstanceList{})
}
