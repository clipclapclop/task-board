---
name: task-board
description: Use the private household Task Board to verify a worker identity, request status-first ready windows, complete owned tasks, create delegated or dependency-gated continuation tasks, and coordinate results through the versioned API. Trigger whenever a request involves task-board.oorangy.com, household task handoff, receiving assigned work, reporting a task result, or delegating work to another worker.
---

# Task Board

Use the versioned API at `${TASK_BOARD_URL:-https://task-board.oorangy.com}/api/v1`.
Read `TASK_BOARD_TOKEN` from the environment. Never print it, place it in a task, commit it, or
write it into this skill.

## Start safely

1. Call `GET /whoami` with `Authorization: Bearer $TASK_BOARD_TOKEN`.
2. Confirm the response is the expected active actor with `kind: "worker"`. Stop on mismatch or
   401.
3. Read `/docs/agents.md` and `/docs/worker-contract.md` when the workflow is unfamiliar.
4. Read `/api/v1/openapi.json` when exact request fields matter.

Use `curl --fail-with-body --silent --show-error` and always quote the URL and token header.

## Receive work

Do not list or fetch tasks. Workers receive work only through ready:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"count":1}' \
  "$TASK_BOARD_URL/api/v1/work/ready"
```

`project_id` is optional. Supply it only when intentionally limiting the ready scope. Count is a
window size from 1 through 32, defaults to one, and is not worker capacity or incremental demand.

The response `deliveries` array frontloads matching `doing` tasks in queue order, then claims
enough actionable `todo` tasks to fill the window. Reconcile by task ID:

- `delivery: claimed` means this transaction made the task owned by this worker.
- `delivery: resumed` means the worker already owns it; load recovery state and never start
  duplicate processing or blindly repeat external effects.
- `204 No Content` means no matching work. Do not start task processing.

On cold start with uncertain ownership, begin unfiltered with a conservative count. Track tasks
outside the current project filter or window locally. Each delivery includes only direct completed
dependency results.

## Complete owned work

Use `done` when a task succeeded and `failed` for a genuine execution failure:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"status":"done","result":"optional concise result"}' \
  "$TASK_BOARD_URL/api/v1/work/TASK_UUID/complete"
```

Persist the exact completion body before sending it. Identical retries are safe. Stop and
reconcile `completion_conflict`, `work_not_owned`, or another stable 4xx response; do not invent a
new outcome. Exactly one component owns completion reporting. If a wrapper will complete the task,
return the outcome to it instead of calling completion from an invoked agent or script.

## Create peer and continuation tasks

Resolve IDs from `/actors` and `/projects`. Every task requires a project. Do not send
`created_by`; the token determines it. Supply a stable operation-specific `Idempotency-Key` for a
retryable creation and reuse it only with the identical body. Persist every returned task ID.

To resume after peer work, create the peer task first, then a self-assigned continuation whose
`blocked_by` contains the peer task ID. The peer's successful result will be included when Task
Board delivers the continuation. Workers cannot browse or edit delegated tasks afterward.

Write actionable titles and only necessary context. Store large outputs externally and return a
stable commit ID, URL, shared path, or artifact identifier. Never include credentials.

## Preserve worker semantics

- Task Board guarantees task ownership, gating, ordering, and lifecycle—not execution quality.
- Do not use generic task collection, detail, PATCH, cancellation, reopen, or history routes.
- Treat `doing` as owned responsibility until completion or human intervention.
- Treat delivery as at-least-once and make external effects idempotent by task ID plus operation.
- Do not report failure merely because the worker restarted.
- Stop requesting new work during graceful shutdown.
- Leave exceptional cancellation and orphan cleanup to humans.
