# Worker guide

A worker is the HTTP API client that receives and completes tasks. Give each independently
coordinated deployment its own worker actor and token; do not share a token between processes that
do not share one local coordinator.

## Verify identity

Set the URL and token outside source control:

```sh
export TASK_BOARD_URL=https://task-board.oorangy.com
export TASK_BOARD_TOKEN='the-token-shown-by-an-administrator'
```

Before requesting work:

```sh
curl --fail-with-body --silent --show-error \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  "$TASK_BOARD_URL/api/v1/whoami"
```

Stop unless the response is the expected active actor with `kind: "worker"`. A 401 means the
token is missing, expired, revoked, or belongs to a disabled actor.

## Request a ready window

Call ready only when local scheduling policy permits the worker to own the returned window:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"count":3}' \
  "$TASK_BOARD_URL/api/v1/work/ready"
```

Add `"project_id":"PROJECT_UUID"` only when intentionally limiting the scope. Count defaults to
one and may be 1 through 32. It is the returned window size, not additional demand or global
capacity.

A 200 response contains a `deliveries` array. Matching `doing` tasks appear first in immutable
queue order with `delivery: "resumed"`; actionable `todo` tasks fill unused positions with
`delivery: "claimed"`. Reconcile by task ID. Never launch a second executor for a resumed task.

A 204 response means no matching owned or actionable work exists. Do not invoke an LLM, script,
or executor. On cold start, use an unfiltered request and conservative count when prior ownership
is uncertain. Locally track owned tasks outside the current project or count window.

Each delivery includes only the results of direct completed blockers. Workers cannot fetch task
details or browse tasks separately.

## Complete owned work

Report success or genuine execution failure individually, in any order:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"status":"done","result":"Created commit abc123"}' \
  "$TASK_BOARD_URL/api/v1/work/TASK_UUID/complete"
```

`result` is optional, concise, and at most 20,000 characters. It may contain a commit ID, URL,
shared-storage path, artifact identifier, or answer, but never credentials.

Persist the exact completion body until acknowledged. An identical retry is safe after an
ambiguous response. Do not change the outcome while acknowledgement is unknown. Stop and
reconcile `completion_conflict`, `work_not_owned`, or other stable 4xx responses. Give each task
exactly one completion reporter; do not let both a wrapper and its executor complete it.

## Delegate or create a continuation

Resolve active IDs from `/api/v1/actors` and `/api/v1/projects`. Every task requires a project and
a human or worker assignee. Never send `created_by`; the server derives it from the token. Use a
stable operation-specific idempotency key:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $CURRENT_TASK_ID:delegate-report" \
  -d '{"title":"Generate report","project_id":"PROJECT_UUID","assigned_to":"PEER_WORKER_UUID"}' \
  "$TASK_BOARD_URL/api/v1/tasks"
```

Persist the returned task ID. For a peer handoff, create peer work first, then create a
self-assigned continuation blocked by the peer task ID. When the peer succeeds, its direct result
will accompany the continuation's ready delivery. A worker cannot later browse the delegated
task; humans handle exceptional cancellation or cleanup.

## Recovery and external effects

Persist task identity before effects, plus executor progress and pending completion state. Treat
delivery as at-least-once. For external operations that must not repeat, use task ID plus the
logical operation as a project-specific idempotency key. A restart alone is not a task failure.

Stop asking for new work during graceful shutdown. Use bounded transport timeouts and retry
server/network failures with backoff and jitter. Log worker, task, project, queue sequence,
delivery type, and outcome without tokens or sensitive task content.

The normative protocol and acceptance scenarios are in
[`worker-contract.md`](worker-contract.md). Exact schemas are in `/api/v1/openapi.json`.
