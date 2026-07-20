# Task Board Worker Interoperability Contract

**Status:** Authoritative v1 worker contract

## Purpose and boundary

Task Board coordinates tasks between humans and specialized workers. It guarantees valid task
ownership, dependency gating, status-first ordered delivery, lifecycle persistence, and direct
dependency-result handoff. A worker remains responsible for correct execution, local scheduling,
recovery, locking, and the idempotency of external effects. This contract defines the behavior a
worker can rely on when communicating with Task Board.

## Core concepts

- **Actor:** an authenticated Task Board identity. Public actor kinds are `human` and `worker`.
- **Worker:** a configured instance of software that calls the ready and complete operations. Each
  independently coordinated worker has its own actor identity and token.
- **Task:** one work item. A task is assigned to exactly one human or worker through `assigned_to`.
- **Project:** required task metadata describing what the task pertains to. It is also an optional
  ready filter, not a worker identity or placement mechanism.

## Worker identity and hosts

Task Board does not model hosts, machines, operating systems, IP addresses, placement, fleets,
heartbeats, leases, worker-wide capacity, or queue-depth limits. Several workers can run on one
host, and one worker can receive tasks from any project. The same software can run under distinct
worker identities on different hosts.

Uncoordinated processes must not share a worker identity or token. A worker that uses several
processes or agents must coordinate ready responses and completion reporting locally.

## Identity and visibility

Workers have configurable usernames and display names, their own actor IDs, one or more revocable
tokens, and local execution and recovery state. Workers cannot be administrators.

Humans may browse all tasks through authorized portal and API routes. Workers receive task content
only through `POST /api/v1/work/ready` or the response to a task they create. Workers may list
active actors and projects needed to create tasks, but may not:

- list or fetch tasks through the generic task API;
- browse task history or terminal tasks;
- directly patch, cancel, or reopen tasks; or
- inspect a task delegated to another actor after its creation response.

Tokens must not appear in tasks, results, source control, or logs.

## Immutable creation sequence

Every task has a server-generated `queue_sequence`. It is globally unique, monotonically
increasing, immutable, independent of UUID randomness and client clocks, and exposed as a
read-only task field.

A task retains its original sequence when it is reassigned, edited while editable, or
administratively reopened. Consequently, a reassigned or reopened task returns to the `todo` tier
at its original creation position.

## Ready request

```http
POST /api/v1/work/ready
Authorization: Bearer WORKER_TOKEN
Content-Type: application/json

{}
```

The optional fields are:

```json
{"project_id":"PROJECT_UUID","count":3}
```

- Only an active worker may call ready.
- Omitted `project_id` means all projects. A supplied project must be active.
- Omitted `count` defaults to `1`.
- `count` must be an integer from `1` through `32`; otherwise Task Board returns
  `422 invalid_count`.
- `count` is the size of the returned ready window. It is neither total worker capacity nor a
  request for that many additional tasks.

### Status-first ordering

Ready evaluates tasks assigned to the authenticated worker within the optional project scope. It
constructs these tiers:

1. Existing `doing` tasks, ordered by `queue_sequence ASC`.
2. Actionable `todo` tasks, ordered by `queue_sequence ASC`.

Blocked and terminal tasks are excluded. Ready takes the first `count` tasks from the combined
tiers. Therefore existing owned work always precedes new work in the same filter scope. A newly
unblocked older task enters the ordered `todo` tier but never displaces `doing` work.

A smaller count returns an owned prefix without cancelling hidden work. A larger count redelivers
existing work and fills only the remaining positions with actionable tasks. Ready claims no new
task when the matching `doing` count is already at least the requested count.

### Atomic behavior

For each ready request, Task Board atomically:

1. validates the actor, optional project, and count;
2. selects matching `doing` tasks in sequence order up to count;
3. calculates the unfilled portion of the window;
4. selects that many oldest actionable `todo` tasks;
5. changes each selected `todo` task to `doing`, increments its version, and appends one `claimed`
   event;
6. includes direct dependency results for every returned task; and
7. returns the complete window.

If neither tier contributes a task, Task Board returns `204 No Content`.

### Response

A 200 response is an object containing a `deliveries` array:

```json
{
  "project_id": "PROJECT_UUID",
  "count": 3,
  "deliveries": [
    {
      "delivery": "resumed",
      "task": {
        "id": "TASK_1",
        "title": "Search Twitter for topic",
        "description": "...",
        "project_id": "PROJECT_UUID",
        "assigned_to": "WORKER_ID",
        "queue_sequence": 41,
        "status": "doing",
        "version": 2
      },
      "dependency_results": []
    },
    {
      "delivery": "claimed",
      "task": {
        "id": "TASK_2",
        "title": "Search Twitter for another topic",
        "description": "...",
        "project_id": "PROJECT_UUID",
        "assigned_to": "WORKER_ID",
        "queue_sequence": 42,
        "status": "doing",
        "version": 2
      },
      "dependency_results": [
        {
          "task_id": "BLOCKER_ID",
          "title": "Prepare query",
          "result": "artifact://queries/topic"
        }
      ]
    }
  ]
}
```

The response echoes `project_id` only when the request supplied it. `delivery: "resumed"` means
the task was already `doing`; `delivery: "claimed"` means this transaction changed it from `todo`
to `doing`.

### Window examples

If B1, B2, and B3 are `doing`, `ready {project B, count 3}` returns B1/B2/B3 as resumed and claims
nothing. With count 4, it returns those three first and claims the oldest actionable Project B
todo.

If an older B0 becomes actionable while B1/B2/B3 are active, count 3 still returns B1/B2/B3. After
B1 completes, the same request returns B2/B3 as resumed and B0 as claimed.

An unfiltered request applies the same status-first rule across every project. A project filter is
an intentional scope: while Project B work is active, `ready {project A, count 3}` may claim three
Project A tasks. Task Board does not infer global worker capacity.

### Retry and concurrency

Ready does not use an idempotency key. Immutable sequence order, status-first selection, and
transactional claims make repeated requests naturally recoverable. Repeating the same filter and
count against unchanged task state returns the same active prefix. State changes can alter only
the unfilled `todo` portion; they do not displace matching `doing` tasks.

Concurrent identical ready requests are serialized and converge on the same window under
unchanged state. Each task is claimed and gets its event once.

## Completion

The owning worker reports one terminal outcome:

```http
POST /api/v1/work/TASK_UUID/complete
Authorization: Bearer WORKER_TOKEN
Content-Type: application/json

{"status":"done","result":"optional concise result"}
```

- Only an active worker may call completion.
- `status` must be `done` or `failed`.
- `result` is optional and limited to 20,000 characters.
- `result` is trimmed of surrounding whitespace and otherwise opaque. Task Board does not
  interpret its contents; each project must establish conventions for answers, structured data,
  and artifact references.
- The task must be `doing` and assigned to the authenticated worker.
- Completion atomically stores status and result, increments the version, and appends one event.
- Tasks in a ready window may complete in any order.
- Repeating an identical completion returns the existing task without another event.
- Repeating with a different status or result returns `409 completion_conflict`.
- A `todo`, reassigned, or otherwise unowned nonterminal task returns `409 work_not_owned` and is
  not mutated.
- A cancelled task, or another terminal task with a different outcome, returns
  `409 completion_conflict` and is not mutated.

## Lifecycle rules

Every task has exactly one project and one assignee. A task is actionable only when it is `todo`
and every direct blocker is `done`. Direct dependency results contain only the blocker task ID,
title, and result; indirect and unrelated task content is not exposed.

`doing` means the assigned worker owns responsibility for the task. It does not imply that a
particular local process is currently running. While a task is `doing`, its title,
description, project, assignee, and dependencies are frozen. Its human creator or an administrator
may still cancel it.

Task Board provides no cancellation notification, worker task-status read, or execution interrupt.
A worker cannot reliably observe cancellation while executing and may first learn of it when
completion returns `409 completion_conflict`. Cancellation prevents a later Task Board completion;
it cannot undo external effects that have already occurred.

Ready is also the only claim operation. Task Board does not match task contents to local execution
capabilities and provides no reject or requeue operation after a claim. If a claimed task cannot be
executed, the worker either reports a genuine `failed` outcome with a useful result or leaves the
task `doing`, stops requesting additional work, and escalates for human resolution.

Terminal tasks are immutable. An administrator may reopen one through the existing workflow,
which changes it to `todo` and clears its current result while retaining history and
`queue_sequence`. A completed blocker cannot be reopened while downstream work is `doing`.

## Task creation IDs

Every `POST /api/v1/tasks` request requires a non-empty opaque `Idempotency-Key`. The key is a
client-assigned creation ID selected when one logical creation process begins. Task Board does not
require a particular key format. A UUID, stable source operation ID, or namespaced identifier such
as `UPSTREAM_TASK_ID:delegate-report` are valid strategies.

Keys are permanent and scoped to the authenticated actor:

- A validation or authorization failure creates no task and does not bind the key. The client may
  correct or regenerate fields and try that unbound key again.
- The first successful request binds the key to its accepted creation fields and task, returning
  `201 Created`.
- Reuse with identical creation fields returns the original task with `200 OK` and
  `Idempotent-Replayed: true`. It creates no task, event, or queue sequence.
- Reuse with different fields returns `409 idempotency_key_conflict`. It does not mutate or replace
  the existing binding.

A different logical task always requires a different key, even if its fields are identical. Task
Board assigns no scheduling or recurrence meaning to repeated task creations.

## Delegation and result handoff

Workers may create project-bearing tasks for peers or themselves. The supported handoff is:

1. Worker A creates task 1 for worker B.
2. Worker A creates task 2 for itself with task 1 in `blocked_by`.
3. B completes task 1 with an optional concise answer or stable artifact reference.
4. Task 2 becomes actionable.
5. A's next applicable ready window includes task 1 under task 2's `dependency_results`.

Failed or cancelled blockers do not release downstream work. Task Board does not automatically
cancel, retry, or rewrite their continuations; cleanup is the responsibility of an authorized
human or domain workflow. Task Board does not store attachments. Large outputs belong in
project-appropriate external storage; use a stable commit ID, URL, shared path, or artifact
identifier in `result`, without credentials.

## Error codes

Clients should branch on stable problem codes rather than message text:

- `unsupported_actor_kind`
- `worker_task_access_forbidden`
- `invalid_project`
- `invalid_count`
- `work_not_owned`
- `completion_conflict`
- `queue_sequence_conflict`
- `missing_idempotency_key`
- `idempotency_key_conflict`

## Best practices (non-normative)

### Identity and scheduling

- Give each independently coordinated deployment its own worker identity and token.
- Make worker name, Task Board URL, token, and local execution capabilities configurable.
- Use one coordinator when several processes or agents operate under one worker.
- Choose project filters and counts according to local priorities and resource costs.
- Treat count as window size, not capacity or incremental demand.
- Reconcile every delivery by task ID and never start duplicate processing for a resumed task.
- Remember that a project-filtered request can expand ownership while other-project work is active.

### Ordering

- Create strategically ordered tasks sequentially and wait for creation responses when order
  matters; concurrent request issue order is not a contract.
- Treat `queue_sequence` as immutable.
- Treat unblocking as a change in `todo` eligibility, not active-task priority.

### Recovery

- On cold start, use unfiltered ready when project ownership is uncertain.
- Use a conservative count to recover active work serially.
- Persist task identity before external effects, plus processing state and pending completion
  bodies.
- Locally track owned tasks outside the current filter or window.
- Stop requesting new work during graceful shutdown.
- Do not start task processing after `204 No Content`.
- Do not report failure merely because the worker restarted.

### External effects and completion

- Treat delivery as at-least-once.
- Use task ID plus the logical operation as the external idempotency key where exactly-once effects
  matter.
- Give each task exactly one completion reporter; an agent and wrapper must not both complete it.
- Persist and retry the identical completion body after an ambiguous response. Do not change the
  outcome while its acknowledgement is unknown.
- Reconcile stable 4xx responses for ownership, cancellation, validation, and conflicts instead of
  retrying them indefinitely.

### Delegation and observability

- Select a task-creation key when the logical creation process begins and carry it through content
  generation, request assembly, and transmission; do not wait until the final send step.
- Reuse that key while attempting the same logical creation. After success, identical fields
  safely replay the original task and changed fields conflict.
- Use a new key for every distinct task. Do not derive identity from task-body equality, and do not
  rely on a datetime alone when more than one creation could share it.
- Check for `200` with `Idempotent-Replayed: true`, and persist returned task IDs.
- Create peer work before its blocked continuation.
- Consume peer output through direct dependency results and store large artifacts externally.
- Log worker ID, task ID, project ID, queue sequence, delivery type, and outcome.
- Keep tokens and sensitive task content out of logs.
- Use bounded transport timeouts and backoff with jitter for server or network failures.

## Reference worker state machine (non-normative)

```text
startup:
  actor = GET /api/v1/whoami
  stop unless actor is the expected active worker identity

when local scheduling policy permits work:
  choose optional project filter P and ready window count N
  response = POST /api/v1/work/ready {project_id?: P, count: N}

  if response is 204:
    do not start task processing
    wait according to local policy

  for each delivery, reconciled by task ID:
    if resumed:
      load local recovery state
      do not blindly repeat non-idempotent effects or start a duplicate local run
    if claimed:
      persist task identity before effects

    execute using the task and its direct dependency results
    persist one stable completion body with done or failed
    retry POST /api/v1/work/{id}/complete until acknowledged or contradicted
```

## Worker acceptance scenarios

Each specialized worker should turn these behavioral scenarios into implementation-appropriate
tests:

- startup verifies its unique worker identity;
- project and count scheduling produces the intended local window;
- unsupported claimed work follows the worker's failure or human-escalation policy;
- status-first reconciliation starts no duplicate processing for resumed tasks;
- newly unblocked work waits behind matching active work;
- conservative unfiltered recovery finds previously owned tasks;
- tasks outside the current filter or window remain locally tracked;
- `204` starts no task processing;
- graceful shutdown requests no new work;
- external effects are idempotent across redelivery;
- exactly one component reports completion;
- an ambiguous completion response is retried with the identical body;
- task creation selects a permanent actor-scoped key early, replays identical accepted fields, and
  rejects different fields after binding;
- direct dependency results reach the intended downstream task; and
- cancellation makes a different later completion conflict without implying that external effects
  were interrupted.

Task Board's executable tests cover the server-enforceable side. Specialized worker projects own
their implementation tests. No generic worker executable, worker registry, or universal worker
harness is part of v1.
