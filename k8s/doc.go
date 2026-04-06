// Package k8s implements the Kubernetes resource watcher for ChatCLI,
// providing real-time cluster monitoring with LLM context injection.
//
// The watcher collects deployment status, pod health, container logs,
// Kubernetes events, HPA state, node health, and Prometheus metrics,
// then summarizes them into a context string injected into every LLM prompt.
//
// # Components
//
//   - Watcher: Main orchestrator that coordinates collectors on a configurable
//     interval (default 30s) and maintains a rolling time window (default 2h).
//   - Collectors: Specialized gatherers for pods, events, logs, metrics,
//     HPA, and node health.
//   - Store: In-memory store for collected data with time-window pruning.
//   - Summarizer: Generates a human-readable context string from collected
//     data, respecting a configurable character budget (default 32,000 chars).
//
// # Multi-Target Monitoring
//
// The watcher supports monitoring multiple targets across namespaces,
// including Deployments, StatefulSets, DaemonSets, CronJobs, and Jobs.
// Each target can have independent Prometheus metrics scraping configuration.
package k8s
