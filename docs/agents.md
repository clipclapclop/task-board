# Service worker guide

## Startup contract

Set these values outside source control:

```sh
export TASK_BOARD_URL=https://task-board.oorangy.com
export TASK_BOARD_TOKEN='the-token-shown-by-an-administrator'
```

Verify identity before requesting work:

```sh
curl --fail-with-body --silent --show-error \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  "$TASK_BOARD_URL/api/v1/whoami"
```

Stop unless the response is the expected active service actor. A 401 means the token is missing,
expired, revoked, or belongs to a disabled actor.

## Become ready for project work

Call ready only when the local project worker is prepared to own work:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"project_id":"PROJECT_UUID"}' \
  "$TASK_BOARD_URL/api/v1/work/ready"
```

The response is one of:

- `200` with `delivery: "claimed"`: the oldest actionable assigned task was atomically moved to
  `doing` and is now owned by this system.
- `200` with `delivery: "resumed"`: this system already owns the returned `doing` task and must
  recover it rather than claim another.
- `204`: there is no owned or actionable work. Do not invoke an LLM or executor.

The delivery contains only valid work assigned to the token actor for the requested project. It
also contains the results of direct completed dependencies. Service actors cannot browse task
collections or fetch task details separately.

## Complete owned work

Report success or genuine failure through the worker completion route:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"status":"done","result":"Created commit abc123"}' \
  "$TASK_BOARD_URL/api/v1/work/TASK_UUID/complete"
```

`status` must be `done` or `failed`. `result` is optional, should be concise, and may contain a
commit ID, URL, shared-storage path, or artifact identifier. It must not contain credentials.

An identical completion retry is safe. A `completion_conflict` means another terminal outcome was
already recorded. `work_not_owned` means the task is not current work owned by this service.

Exactly one component should report completion. Do not let both a wrapper and its invoked
skill/script complete the same task independently.

## Delegate or schedule continuation work

Resolve active actor and project IDs from `/api/v1/actors` and `/api/v1/projects`. Create every task
with a project and a stable, operation-specific idempotency key:

```sh
curl --fail-with-body --silent --show-error -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $CURRENT_TASK_ID:delegate-report" \
  -d '{"title":"Generate report","project_id":"PROJECT_UUID","assigned_to":"PEER_ACTOR_UUID"}' \
  "$TASK_BOARD_URL/api/v1/tasks"
```

A service may create a task for a peer and then create a self-assigned continuation blocked by the
peer task. When the peer finishes, its result is included automatically when the continuation is
delivered. Task Board does not provide service actors with later browse/edit access to delegated
tasks; humans handle exceptional cancellation or cleanup.

Never send `created_by`; the server derives it from the token. Reuse an idempotency key only with
the identical request body.

## Recovery and external effects

Persist enough local state to recover any delivered task. A repeated ready call redelivers current
owned work, but Task Board cannot determine whether an email, upload, purchase, or other external
effect already happened. Use operation-specific idempotency in project skills and APIs. If safe
recovery is impossible, complete the task as `failed` with a useful result.

The authoritative protocol and acceptance scenarios are in
[`worker-contract.md`](worker-contract.md). Exact API shapes are in `/api/v1/openapi.json`.
