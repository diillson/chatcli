/*
 * DockerRunning — evaluator that queries the Docker engine.
 *
 * Spec:
 *   container   string — required (or container_prefix)
 *   container_prefix string — optional
 *   healthy     bool   — optional; require State.Health.Status == "healthy"
 *   docker      string — optional; binary (default "docker")
 *
 * Implementation: shells out to `docker inspect <name> -f '<format>'`.
 * When container_prefix is set, uses `docker ps` to resolve matches.
 */
package condition

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/scheduler"
)

// DockerRunning implements scheduler.ConditionEvaluator.
type DockerRunning struct{}

// NewDockerRunning constructs the evaluator.
func NewDockerRunning() *DockerRunning { return &DockerRunning{} }

// Type returns the Condition.Type literal.
func (DockerRunning) Type() string { return "docker_running" }

// ValidateSpec enforces required fields.
func (DockerRunning) ValidateSpec(spec map[string]any) error {
	if asString(spec, "container") == "" && asString(spec, "container_prefix") == "" {
		return fmt.Errorf("docker_running: spec.container or spec.container_prefix is required")
	}
	return nil
}

// Evaluate shells out to docker.
func (DockerRunning) Evaluate(ctx context.Context, cond scheduler.Condition, env *scheduler.EvalEnv) scheduler.EvalOutcome {
	container := cond.SpecString("container", "")
	prefix := cond.SpecString("container_prefix", "")
	requireHealthy := cond.SpecBool("healthy", false)
	binary := cond.SpecString("docker", "docker")
	if env == nil || env.Bridge == nil {
		return scheduler.EvalOutcome{Err: fmt.Errorf("docker_running: no CLI bridge wired")}
	}

	envOverrides := map[string]string{}
	if sock := env.Bridge.DockerSocketPath(); sock != "" {
		envOverrides["DOCKER_HOST"] = sock
	}

	resolveNames := func() ([]string, error) {
		if container != "" {
			return []string{container}, nil
		}
		cmd := binary + " ps --format {{.Names}}"
		stdout, stderr, code, err := env.Bridge.RunShell(ctx, cmd, envOverrides, false)
		if err != nil || code != 0 {
			return nil, nonNilErr(err, fmt.Errorf("docker ps failed: %s", stderr))
		}
		out := []string{}
		for _, ln := range strings.Split(stdout, "\n") {
			ln = strings.TrimSpace(ln)
			if ln != "" && strings.HasPrefix(ln, prefix) {
				out = append(out, ln)
			}
		}
		return out, nil
	}

	names, err := resolveNames()
	if err != nil {
		return scheduler.EvalOutcome{Err: err}
	}
	if len(names) == 0 {
		return scheduler.EvalOutcome{
			Satisfied: false,
			Details:   fmt.Sprintf("no containers match %q", firstNonEmpty(container, prefix)),
		}
	}

	allReady := true
	details := []string{}
	for _, name := range names {
		format := "{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}"
		cmd := fmt.Sprintf("%s inspect %s -f %q", binary, name, format)
		stdout, stderr, code, err := env.Bridge.RunShell(ctx, cmd, envOverrides, false)
		if err != nil || code != 0 {
			details = append(details, fmt.Sprintf("%s: %s", name, strings.TrimSpace(stderr)))
			allReady = false
			continue
		}
		raw := strings.TrimSpace(stdout)
		parts := strings.SplitN(raw, "|", 2)
		running := len(parts) > 0 && strings.EqualFold(parts[0], "true")
		health := "none"
		if len(parts) > 1 {
			health = parts[1]
		}
		ready := running
		if requireHealthy {
			ready = ready && (health == "healthy")
		}
		if !ready {
			allReady = false
		}
		details = append(details, fmt.Sprintf("%s running=%v health=%s", name, running, health))
	}
	return scheduler.EvalOutcome{
		Satisfied: allReady,
		Details:   strings.Join(details, "; "),
	}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}
