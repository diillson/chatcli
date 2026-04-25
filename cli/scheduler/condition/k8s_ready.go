/*
 * K8sResourceReady — evaluator that asks kubectl whether a resource is
 * in the given condition.
 *
 * Spec:
 *   kind       string   — required (pod, deployment, statefulset, ...)
 *   name       string   — required, or name_prefix
 *   name_prefix string  — optional; match any resource with this prefix
 *   namespace  string   — optional (default: current context)
 *   condition  string   — optional (default "Ready"). For deployments
 *                         use "Available"; for statefulsets "Available".
 *   kubeconfig string   — optional; defaults to CHATCLI_KUBECONFIG or
 *                         KUBECONFIG or the value surfaced by the bridge.
 *   kubectl    string   — optional; binary to use (default "kubectl").
 *   jsonpath   string   — optional; custom jsonpath to match (overrides condition).
 *   expected   string   — optional; when jsonpath is set, expected value
 *                         (default "True").
 *
 * Implementation: shells out to `kubectl get <kind> <name> -n <ns>
 *                 -o jsonpath='{.status.conditions[?(@.type=="<condition>")].status}'`
 * and matches the expected value. This reuses every auth path kubectl
 * already has (kubeconfig, in-cluster, IAM plugins) without us writing
 * an API client.
 *
 * The binary is resolved via PATH; shell injection is impossible because
 * all arguments are passed as a []string to the bridge's RunShell (no
 * shell interpolation).
 */
package condition

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// K8sReady implements scheduler.ConditionEvaluator.
type K8sReady struct{}

// NewK8sReady constructs the evaluator.
func NewK8sReady() *K8sReady { return &K8sReady{} }

// Type returns the Condition.Type literal.
func (K8sReady) Type() string { return "k8s_resource_ready" }

// ValidateSpec enforces required fields.
func (K8sReady) ValidateSpec(spec map[string]any) error {
	if strings.TrimSpace(asString(spec, "kind")) == "" {
		return fmt.Errorf("k8s_resource_ready: spec.kind is required")
	}
	if asString(spec, "name") == "" && asString(spec, "name_prefix") == "" {
		return fmt.Errorf("k8s_resource_ready: spec.name or spec.name_prefix is required")
	}
	return nil
}

// Evaluate shells out to kubectl.
func (K8sReady) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	kind := cond.SpecString("kind", "")
	name := cond.SpecString("name", "")
	prefix := cond.SpecString("name_prefix", "")
	ns := cond.SpecString("namespace", "")
	condType := cond.SpecString("condition", "Ready")
	jsonpath := cond.SpecString("jsonpath", "")
	expected := cond.SpecString("expected", "True")
	binary := cond.SpecString("kubectl", "kubectl")
	kcfg := cond.SpecString("kubeconfig", "")

	if env == nil || env.Bridge == nil {
		return scheduler.EvalOutcome{Err: fmt.Errorf("k8s_resource_ready: no CLI bridge wired")}
	}
	if kcfg == "" {
		kcfg = env.Bridge.KubeconfigPath()
	}

	envOverrides := map[string]string{}
	if kcfg != "" {
		envOverrides["KUBECONFIG"] = kcfg
	}

	// When name_prefix is set, first list matching names.
	if prefix != "" {
		listArgs := []string{binary, "get", kind}
		if ns != "" {
			listArgs = append(listArgs, "-n", ns)
		}
		listArgs = append(listArgs, "-o", "json")
		cmd := strings.Join(listArgs, " ")
		stdout, stderr, code, err := env.Bridge.RunShell(ctx, cmd, envOverrides, false, env.DangerousConfirmed)
		if err != nil || code != 0 {
			transient := code != 0 && (strings.Contains(stderr, "connection refused") || strings.Contains(stderr, "i/o timeout"))
			return scheduler.EvalOutcome{
				Err:       nonNilErr(err, fmt.Errorf("kubectl list failed: %s", strings.TrimSpace(stderr))),
				Transient: transient,
				Details:   stdout + "\n" + stderr,
			}
		}
		var res struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			return scheduler.EvalOutcome{Err: fmt.Errorf("k8s_resource_ready: parse list: %w", err)}
		}
		anyMatched := false
		allReady := true
		details := []string{}
		for _, it := range res.Items {
			if !strings.HasPrefix(it.Metadata.Name, prefix) {
				continue
			}
			anyMatched = true
			ready := false
			for _, c := range it.Status.Conditions {
				if c.Type == condType && c.Status == expected {
					ready = true
					break
				}
			}
			if !ready {
				allReady = false
			}
			details = append(details, fmt.Sprintf("%s=%v", it.Metadata.Name, ready))
		}
		if !anyMatched {
			return scheduler.EvalOutcome{
				Satisfied: false,
				Details:   fmt.Sprintf("no %s matching prefix=%q in ns=%q", kind, prefix, ns),
			}
		}
		return scheduler.EvalOutcome{
			Satisfied: allReady,
			Details:   strings.Join(details, ", "),
		}
	}

	// Single-resource path.
	path := jsonpath
	if path == "" {
		path = fmt.Sprintf("{.status.conditions[?(@.type==\"%s\")].status}", condType)
	}
	args := []string{binary, "get", kind, name}
	if ns != "" {
		args = append(args, "-n", ns)
	}
	args = append(args, "-o", "jsonpath="+path)
	cmd := strings.Join(args, " ")
	stdout, stderr, code, err := env.Bridge.RunShell(ctx, cmd, envOverrides, false, env.DangerousConfirmed)
	if err != nil || code != 0 {
		transient := code != 0 && (strings.Contains(stderr, "connection refused") || strings.Contains(stderr, "i/o timeout") || strings.Contains(stderr, "ContainerCreating") || strings.Contains(stderr, "not found"))
		return scheduler.EvalOutcome{
			Err:       nonNilErr(err, fmt.Errorf("kubectl get failed: %s", strings.TrimSpace(stderr))),
			Transient: transient,
			Details:   stdout + "\n" + stderr,
		}
	}
	got := strings.TrimSpace(stdout)
	satisfied := strings.EqualFold(got, expected)
	return scheduler.EvalOutcome{
		Satisfied: satisfied,
		Details:   fmt.Sprintf("%s %s ns=%s condition=%s -> %q (expected %q)", kind, name, ns, condType, got, expected),
	}
}
