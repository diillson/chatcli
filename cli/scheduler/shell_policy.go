/*
 * ChatCLI - Scheduler: shell policy classification types.
 *
 * Dedicated file so the CLIBridge interface stays readable and tests
 * can import the ShellPolicy symbol without pulling the full bridge
 * surface.
 *
 * The scheduler package intentionally does NOT depend on cli/coder;
 * classification is delegated to the host process through the
 * CLIBridge abstraction. This keeps the scheduler engine unit-
 * testable against mock policies and keeps the import graph shallow.
 */
package scheduler

// ShellPolicy classifies a shell command's security disposition as
// reported by the host CoderMode policy manager.
type ShellPolicy int

const (
	// ShellPolicyAllow — command is on the allowlist (or is a known
	// read-only command like `kubectl get`, `git status`, etc.).
	// Scheduler admits the job without further questions.
	ShellPolicyAllow ShellPolicy = iota

	// ShellPolicyAsk — command would require interactive approval in
	// coder mode. The scheduler never prompts (daemon/autonomous
	// context); it rejects the enqueue unless the job is marked
	// DangerousConfirmed (user used --i-know, or an agent tool call
	// passed i_know:true with explicit upstream blessing).
	ShellPolicyAsk

	// ShellPolicyDeny — command is on the denylist. Always rejected
	// regardless of flags — --i-know CANNOT override a deny.
	ShellPolicyDeny
)

// String makes the policy log-friendly.
func (p ShellPolicy) String() string {
	switch p {
	case ShellPolicyAllow:
		return "allow"
	case ShellPolicyAsk:
		return "ask"
	case ShellPolicyDeny:
		return "deny"
	}
	return "unknown"
}

// IsTerminal reports whether this classification by itself is enough
// to decide admission (Allow or Deny). Ask is non-terminal — the
// caller must consult DangerousConfirmed.
func (p ShellPolicy) IsTerminal() bool {
	return p == ShellPolicyAllow || p == ShellPolicyDeny
}
