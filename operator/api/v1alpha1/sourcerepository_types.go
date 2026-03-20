package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SourceRepositoryAuthType defines the authentication method for git access.
// +kubebuilder:validation:Enum=none;ssh;token;basic
type SourceRepositoryAuthType string

const (
	SourceRepoAuthNone  SourceRepositoryAuthType = "none"
	SourceRepoAuthSSH   SourceRepositoryAuthType = "ssh"
	SourceRepoAuthToken SourceRepositoryAuthType = "token"
	SourceRepoAuthBasic SourceRepositoryAuthType = "basic"
)

// SourceRepositorySpec defines the desired state of a SourceRepository.
type SourceRepositorySpec struct {
	// URL is the git repository URL (HTTPS or SSH).
	URL string `json:"url"`

	// Branch to track (defaults to "main").
	// +kubebuilder:default="main"
	// +optional
	Branch string `json:"branch,omitempty"`

	// AuthType is the authentication method.
	// +kubebuilder:default="none"
	// +optional
	AuthType SourceRepositoryAuthType `json:"authType,omitempty"`

	// SecretRef references a Secret containing auth credentials.
	// For token: key "token". For basic: keys "username" and "password". For ssh: key "ssh-key".
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// Resource links this repository to a specific Kubernetes resource.
	Resource ResourceRef `json:"resource"`

	// Paths are the relevant source paths to analyze (e.g., ["src/", "cmd/"]).
	// If empty, the entire repository is analyzed.
	// +optional
	Paths []string `json:"paths,omitempty"`

	// Dockerfile path within the repo for build context understanding.
	// +optional
	Dockerfile string `json:"dockerfile,omitempty"`

	// Language hint for the primary programming language.
	// +optional
	Language string `json:"language,omitempty"`

	// SyncIntervalMinutes is how often to re-sync the repository (default 30).
	// +kubebuilder:default=30
	// +kubebuilder:validation:Minimum=5
	// +optional
	SyncIntervalMinutes int32 `json:"syncIntervalMinutes,omitempty"`
}

// GitCommitInfo holds information about a git commit.
type GitCommitInfo struct {
	// SHA is the commit hash.
	SHA string `json:"sha"`

	// Message is the commit message.
	Message string `json:"message"`

	// Author of the commit.
	Author string `json:"author"`

	// Timestamp of the commit.
	Timestamp metav1.Time `json:"timestamp"`

	// FilesChanged lists the files modified in this commit.
	// +optional
	FilesChanged []string `json:"filesChanged,omitempty"`
}

// SourceRepositoryStatus defines the observed state of a SourceRepository.
type SourceRepositoryStatus struct {
	// Ready indicates whether the repository has been successfully cloned and indexed.
	Ready bool `json:"ready"`

	// LastSyncedAt is when the repository was last successfully synced.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`

	// HeadCommit is the latest commit on the tracked branch.
	// +optional
	HeadCommit *GitCommitInfo `json:"headCommit,omitempty"`

	// RecentCommits are the last 20 commits for correlation.
	// +optional
	RecentCommits []GitCommitInfo `json:"recentCommits,omitempty"`

	// LocalPath is where the repo is cloned locally on the operator.
	// +optional
	LocalPath string `json:"localPath,omitempty"`

	// Error describes any sync error.
	// +optional
	Error string `json:"error,omitempty"`

	// DetectedLanguages lists languages detected in the repository.
	// +optional
	DetectedLanguages []string `json:"detectedLanguages,omitempty"`

	// EntrypointFiles are the detected application entrypoints (main.go, app.py, index.js, etc.).
	// +optional
	EntrypointFiles []string `json:"entrypointFiles,omitempty"`

	// ConfigFiles are detected configuration files (Dockerfile, helm values, k8s manifests).
	// +optional
	ConfigFiles []string `json:"configFiles,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=srcrepo
// +kubebuilder:printcolumn:name="URL",type="string",JSONPath=".spec.url"
// +kubebuilder:printcolumn:name="Branch",type="string",JSONPath=".spec.branch"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SourceRepository links a Kubernetes workload to its source code repository
// for code-aware incident analysis and diagnostics.
type SourceRepository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SourceRepositorySpec   `json:"spec,omitempty"`
	Status SourceRepositoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SourceRepositoryList contains a list of SourceRepository.
type SourceRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SourceRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SourceRepository{}, &SourceRepositoryList{})
}
