---
name: task-board
description: Use the private household Task Board to verify a service identity, become ready for project work, complete owned work, create delegated or dependency-gated continuation tasks, and coordinate outputs through the versioned worker API. Trigger whenever a request involves task-board.oorangy.com, household task handoff, requesting assigned work, reporting a task result, or delegating work to another machine.
---

# Task Board

Use the versioned API at `${TASK_BOARD_URL:-https://task-board.oorangy.com}/api/v1`.
Read `TASK_BOARD_TOKEN` from the environment. Never print it, place it in a task, commit it, or
write it into this skill.

## Start safely

1. Call `GET /whoami` with `Authorization: Bearer $TASK_BOARD_TOKEN`.
2. Confirm the returned username is the expected active service actor. Stop on mismatch or 401.
3. Read `/docs/agents.md` and `/docs/worker-contract.md` when the workflow is unfamiliar.
4. Read `/api/v1/openapi.json` when exact request fields matter.

Use `curl --fail-with-body --silent --show-error` and always quote the URL and token header.

## Receive project work

Do not list or fetch tasks. Service actors receive only valid work delivered by the worker API:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"project_id":"PROJECT_UUID"}' \
  "$TASK_BOARD_URL/api/v1/work/ready"
```

- `delivery: claimed` means the returned task is newly owned by this service.
- `delivery: resumed` means this service already owns it; recover local state before repeating
  external effects.
- `204 No Content` means caught up. Do not invoke an LLM, script, or executor.

The delivery includes results from direct completed dependencies. Treat the delivered task as the
only current Task Board instruction visible to this worker.

## Complete owned work

Use `done` when the assignment succeeded and `failed` when it was attempted but could not succeed:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"status":"done","result":"optional concise result"}' \
  "$TASK_BOARD_URL/api/v1/work/TASK_UUID/complete"
```

Identical retries are safe. Stop and reconcile on `completion_conflict` or `work_not_owned`; do not
invent another outcome. Exactly one component must own completion reporting. If a wrapper will
complete the task, return the outcome to it instead of calling completion from this skill.

## Create delegated and continuation work

Resolve actor and project IDs from `/actors` and `/projects`. Every task requires a project. Do not
send `created_by`; the token determines it. Supply a stable operation-specific `Idempotency-Key`
whenever creation may be retried, and reuse it only with the identical JSON body.

To resume after peer work, create the peer task first, then create a self-assigned continuation
whose `blocked_by` contains the peer task ID. The completed peer result will be included when Task
Board later delivers the continuation.

Write actionable titles and only necessary context. Put large outputs in project-appropriate
shared storage and return a stable commit ID, URL, path, or artifact identifier. Never store
credentials, private keys, access tokens, or other secrets in a task or result.

## Preserve worker semantics

- Task Board guarantees assignment and dependency gating, not execution quality.
- Do not attempt task list, detail, PATCH, cancellation, reopen, or history routes as a service.
- Treat `doing` as work owned by this system until completion or human intervention.
- Use project-specific idempotency for external effects that an interrupted worker might repeat.
- If resumed work cannot be recovered safely, report `failed` with a useful result.
- Leave exceptional cancellation and orphan cleanup to humans.
