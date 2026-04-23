/*
 * ChatCLI - Scheduler / Chronos subsystem.
 *
 * The scheduler is chatcli's durable, crash-consistent automation layer.
 * It turns chatcli into a standing process that can be told "wait until
 * X, then do Y" and keep that promise across restarts, network hiccups,
 * and user-input lulls. The same machinery is used by the ReAct loop
 * (agents and workers gain schedule_job / wait_until tools) and by the
 * human via /schedule, /wait and /jobs commands.
 *
 * High-level model:
 *
 *   ┌─────────────┐        ┌──────────────┐        ┌─────────────┐
 *   │ CLI surface │◀──────▶│   Scheduler  │◀──────▶│  WAL + snap │
 *   │ /schedule…  │        │  state m/c   │        │  on disk    │
 *   └─────────────┘        │  priority Q  │        └─────────────┘
 *                          │  breakers    │
 *   ┌─────────────┐        │  rate limit  │        ┌─────────────┐
 *   │ Agent tools │◀──────▶│  dispatcher  │──────▶│ Condition    │
 *   │ schedule_*  │        │              │       │ evaluators   │
 *   └─────────────┘        │              │       └─────────────┘
 *                          │              │        ┌─────────────┐
 *                          │              │──────▶│ Action        │
 *                          │              │       │ executors     │
 *                          │              │       └─────────────┘
 *                          │              │
 *                          │              │─────▶ event bus + hooks + audit
 *                          └──────────────┘
 *
 * Durability contract (identical to the Reflexion lesson queue):
 *   1. Accept Enqueue only after the WAL record is fsynced.
 *   2. Process, then ACK (delete or update the WAL).
 *   3. On crash, replay the WAL on boot and resume.
 *   4. A missed fire is classified by MissPolicy (fire_once / fire_all /
 *      skip) so laptops that slept through a schedule do not thrash.
 *
 * Autonomy:
 *   In-process — jobs run while chatcli is open. WAL persists state; a
 *   new CLI session replays pending work.
 *   Daemon     — `chatcli daemon start` detaches the scheduler from the
 *   interactive process. Any CLI that comes up detects the daemon via
 *   UNIX socket (chatcli-scheduler.sock), thin-client style. See
 *   daemon.go and ipc.go for the protocol.
 *
 * Safety:
 *   Actions are gated by an allowlist (CHATCLI_SCHEDULER_ACTION_ALLOWLIST);
 *   shell actions run under CoderMode SafeMode; agent-created jobs are
 *   flagged trust=agent and cannot cancel user-owned jobs without the
 *   hook manager firing a PreJobCancel event. See action/ subpackage.
 *
 * See SCHEDULER_DESIGN.md (docs/) for the architecture rationale and
 * tradeoff discussion. Every design decision here was made explicitly
 * and the options are documented alongside the chosen path.
 */
package scheduler
