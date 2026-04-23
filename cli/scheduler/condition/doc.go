/*
 * Package condition hosts the built-in ConditionEvaluator
 * implementations used by cli/scheduler.
 *
 * Each evaluator answers one question ("is the HTTP endpoint up?",
 * "does the kubernetes resource report Ready?", "does the file
 * exist?"). The scheduler owns concurrency, timeouts, and breakers;
 * evaluators are synchronous, pure, and idempotent.
 *
 * Adding a new evaluator:
 *   1. Implement scheduler.ConditionEvaluator in this package.
 *   2. Include it in RegisterAll (builtins.go).
 *   3. Add a spec-schema test in the sibling *_test.go.
 *
 * Third-party / user-defined evaluators can be registered at any time
 * before Scheduler.Start via scheduler.Conditions().Register.
 */
package condition
