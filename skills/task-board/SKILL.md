---
name: task-board
description: Use the private household Task Board to identify the current agent, list or inspect assigned work, create tasks, update status and results, handle blockers, and coordinate work with humans or other agents through the versioned HTTP API. Trigger whenever a request involves task-board.oorangy.com, household task handoff, checking an agent's assigned tasks, or recording task progress/results.
---

# Task Board

Use the versioned API at `${TASK_BOARD_URL:-https://task-board.oorangy.com}/api/v1`.
Read `TASK_BOARD_TOKEN` from the environment. Never print it, place it in a task, commit it, or
write it into this skill.

## Start safely

1. Call `GET /whoami` with `Authorization: Bearer $TASK_BOARD_TOKEN`.
2. Confirm the returned username is the expected agent. Stop on mismatch or 401.
3. Read `/docs/agents.md` from the server when the workflow is unfamiliar.
4. Read `/api/v1/openapi.json` when exact request fields or filters matter.

Use `curl --fail-with-body --silent --show-error` and always quote the URL and token header.

## Find and perform work

1. List tasks with `assigned_to=<whoami.id>` and repeated `status=todo&status=doing` filters.
2. Prefer `actionable: true`. Do not start or finish a task with `is_blocked: true`.
3. Fetch `GET /tasks/{id}` immediately before acting so instructions and history are current.
4. Change status to `doing` only when work actually starts.
5. Send the fetched integer version as `If-Match: "<version>"` on every PATCH.
6. Finish with `done` and a concise result, or `failed` and a useful failure result.

On 412, fetch again and reconsider; never blindly retry or invent the next version. On
`task_blocked`, inspect blockers and report or wait. On `forbidden`, do not impersonate another
actor. On 401, stop and request a replacement token.

## Create tasks

Resolve assignee and project IDs from `/actors` and `/projects`. Do not send `created_by`; the
server derives it from the token. Supply a stable operation-specific `Idempotency-Key` whenever a
request may be retried. Reuse that key only with the identical JSON body.

Write actionable titles and put only necessary context in descriptions. Do not store credentials,
private keys, access tokens, or other secrets in any field.

## Preserve task semantics

- Treat `done`, `failed`, and `cancelled` as terminal.
- Let the creator edit assignment, details, and dependencies.
- Let the assignee update work status and result.
- Leave administrator-only reopen and account/project management to explicit requests.
- Use task history to understand changes; do not treat it as a comments system.
