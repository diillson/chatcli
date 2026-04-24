/*
 * Package action hosts the built-in ActionExecutor implementations
 * used by cli/scheduler.
 *
 * Each executor runs exactly one action type. The scheduler owns
 * concurrency, timeouts, and breakers; executors focus on semantics.
 *
 * Registration follows the same pattern as the condition package:
 * RegisterAll is called once from scheduler.RegisterBuiltins.
 *
 * Safety contract:
 *   - Shell actions always go through CLIBridge.RunShell, which in
 *     turn enforces CoderMode's allowlist / denylist.
 *   - Agent actions pass through CLIBridge.RunAgentTask so the ReAct
 *     loop's policy adapter sees every scheduled invocation.
 *   - Webhook actions honor redirect / size limits to avoid SSRF.
 */
package action
