# Task Board Worker Interoperability Contract

**Status:** Authoritative v1 worker contract

## Purpose and boundary

Task Board delivers project work between humans and specialized systems. A service actor represents
one machine or specialized system, and a project identifies the worker being requested.

Task Board guarantees assignment, dependency gating, ordered delivery, lifecycle persistence, and
result handoff. It does not judge whether a worker performed its specialty correctly. Each worker
owns its executor, agents and subagents, local scheduling, locking, crash recovery, and the
idempotency of external effects.

This contract does not prescribe a runner program, polling interval, process model, concurrency,
LLM, service manager, workspace layout, or recovery algorithm.

V1 defines no worker concurrency or queue-depth limits. It also adds no generic runner or
reference worker, fleet registry, availability heartbeat, execution lease, comments, or
attachments.

This is a pre-adoption revision of the provisional v1 service API, so backward compatibility with
service task browsing is not required. Before rollout, operators must backfill or discard any
projectless records. They must not create a synthetic `Legacy` project.

## Task visibility

Humans may browse all tasks through the portal and authorized API routes.

Service actors do not have task collection, task detail, task PATCH, cancellation, reopen, or
history access. A service receives task content only from `POST /api/v1/work/ready`. It may create
project-bearing tasks for peers or itself, but the creation response is its only view of a task
assigned to another actor.

Services may list active actors and projects for routing. Tokens remain scoped to one actor and
must never be placed in tasks, results, source control, or logs.

## Ready delivery

A worker declares that it is ready for one project:

```http
POST /api/v1/work/ready
Authorization: Bearer SERVICE_TOKEN
Content-Type: application/json

{"project_id":"PROJECT_UUID"}
```

Only an active service actor may call this operation. In one transaction, Task Board:

1. Finds an existing `doing` task assigned to that actor and project. If exactly one exists, it is
   returned with `delivery: "resumed"`.
2. If no task is already owned, finds the oldest actionable `todo` task assigned to that actor and
   project, ordered by `created_at ASC, id ASC`.
3. Atomically changes the selected task to `doing`, increments its version, appends a `claimed`
   event, and returns it with `delivery: "claimed"`.
4. Returns `204 No Content` when neither owned nor actionable work exists.

Blocked, terminal, differently assigned, and differently projected tasks are never delivered. If
invalid legacy state contains more than one `doing` task for the actor/project pair, delivery fails
with `ambiguous_active_work` for human cleanup.

```json
{
  "delivery": "claimed",
  "task": {
    "id": "TASK_UUID",
    "title": "Produce the report",
    "description": "...",
    "project_id": "PROJECT_UUID",
    "assigned_to": "ACTOR_UUID",
    "status": "doing",
    "version": 2
  },
  "dependency_results": [
    {
      "task_id": "BLOCKER_UUID",
      "title": "Collect source data",
      "result": "artifact://reports/source-data"
    }
  ]
}
```

Ready delivery is naturally recoverable: if a response is lost, another ready call for the same
project redelivers the existing `doing` task rather than claiming another one. `doing` means the
task has been delivered to and is owned by the assigned system; it does not describe the worker's
internal CPU or process state.

## Completion

The owning service reports one terminal outcome:

```http
POST /api/v1/work/TASK_UUID/complete
Authorization: Bearer SERVICE_TOKEN
Content-Type: application/json

{"status":"done","result":"optional concise result"}
```

- `status` must be `done` or `failed`.
- `result` is optional and limited to 20,000 characters.
- The task must be `doing` and assigned to the authenticated service actor.
- Completion atomically stores status and result, increments the version, and appends one
  `completed` event.
- Repeating an identical completion succeeds without adding another event.
- Repeating with a different status or result returns `completion_conflict`.
- Completing todo, cancelled, reassigned, or otherwise unowned work returns `work_not_owned` or
  `completion_conflict` without mutation.

Workers should keep a stable pending completion record until Task Board acknowledges it. A worker
must have exactly one completion owner: an outer orchestrator or the invoked skill/script, not both.

## Stable owned work

Every task has a project. While a task is `doing`, its title, description, project, assignee, and
dependencies are frozen. A human may cancel it, and an administrator may later reopen it. Terminal
tasks remain immutable; reopen returns them to `todo` and clears the current result while retaining
history.

All direct blockers are `done` before delivery. An administrator cannot reopen a completed blocker
while downstream work is `doing`.

## Delegation and result handoff

Service actors may create tasks using stable, operation-specific `Idempotency-Key` values. A common
handoff is:

1. Machine A creates task 1 for machine B.
2. Machine A creates task 2 for itself with task 1 in `blocked_by`.
3. B completes task 1 with an optional result.
4. Task 2 becomes actionable.
5. A's ready delivery for task 2 includes task 1 under `dependency_results`.

Only direct blocker IDs, titles, and results are included. Indirect and unrelated task content is
not exposed. Failed or cancelled blockers do not release downstream work.

Task Board does not store attachments. Large outputs belong in project-appropriate shared storage;
the result should contain a stable commit ID, URL, path, artifact identifier, or concise answer.
Results must not contain credentials.

## Reference worker state machine

The following is guidance, not a prescribed implementation:

```text
startup:
  actor = GET /api/v1/whoami
  stop unless actor is the expected active service identity

when locally ready for project P:
  response = POST /api/v1/work/ready {project_id: P}
  if response is 204:
    do not invoke an LLM or executor
    wait according to local policy
  if response is resumed:
    load local recovery state
    inspect before repeating any non-idempotent external effect
  if response is claimed:
    persist task identity before beginning effects

  execute the delivered task and direct dependency inputs
  choose done or failed according to project-specific semantics
  persist the completion body
  retry POST /api/v1/work/{id}/complete until acknowledged or contradicted
```

External APIs, skills, and scripts should use operation-specific idempotency identifiers wherever
an interrupted retry could duplicate an effect. If a resumed task cannot be recovered safely, the
worker should report `failed` with a useful result rather than guess.

## Required worker acceptance scenarios

Each specialized worker should turn these scenarios into tests appropriate to its implementation:

- A caught-up ready response invokes no executor.
- Newly delivered work is performed and completed.
- Restart redelivers and safely recovers existing owned work.
- Genuine execution failure is reported as `failed`.
- A lost completion response does not create a second event or contradictory result.
- Retried external effects use project-specific idempotency.
- Direct dependency results reach downstream work; unrelated results do not.
- Human cancellation causes a later completion attempt to fail safely.

Task Board's own executable suite tests the server-enforceable half of these rules. There is no
universal worker harness because specialized worker internals are deliberately outside this
contract.
