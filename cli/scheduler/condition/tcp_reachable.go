/*
 * TCPReachable — evaluator that opens a TCP connection.
 *
 * Spec:
 *   host    string   — required
 *   port    int      — required
 *   timeout duration — optional per-attempt timeout (default 5s)
 */
package condition

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/diillson/chatcli/cli/scheduler"
)

// TCPReachable implements scheduler.ConditionEvaluator.
type TCPReachable struct{}

// NewTCPReachable builds the evaluator.
func NewTCPReachable() *TCPReachable { return &TCPReachable{} }

// Type returns the Condition.Type literal.
func (TCPReachable) Type() string { return "tcp_reachable" }

// ValidateSpec enforces required fields.
func (TCPReachable) ValidateSpec(spec map[string]any) error {
	if asString(spec, "host") == "" {
		return fmt.Errorf("tcp_reachable: spec.host is required")
	}
	port := asInt(spec, "port")
	if port <= 0 || port > 65535 {
		return fmt.Errorf("tcp_reachable: spec.port must be 1..65535")
	}
	return nil
}

// Evaluate tries to open a TCP connection.
func (TCPReachable) Evaluate(ctx context.Context, cond scheduler.Condition, _ *scheduler.EvalEnv) scheduler.EvalOutcome {
	host := cond.SpecString("host", "")
	port := cond.SpecInt("port", 0)
	timeout := cond.SpecDuration("timeout", 5*time.Second)

	dialer := &net.Dialer{Timeout: timeout}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return scheduler.EvalOutcome{
			Satisfied: false,
			Transient: true,
			Details:   fmt.Sprintf("%s: %v", addr, err),
			Err:       err,
		}
	}
	_ = conn.Close()
	return scheduler.EvalOutcome{
		Satisfied: true,
		Details:   fmt.Sprintf("%s: open", addr),
	}
}
