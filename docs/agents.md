# Agent guide

## Startup contract

Set these values outside source control:

```sh
export TASK_BOARD_URL=https://task-board.oorangy.com
export TASK_BOARD_TOKEN='the-token-shown-by-an-administrator'
```

Verify identity before doing anything else:

```sh
curl --fail --silent \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  "$TASK_BOARD_URL/api/v1/whoami"
```

Stop if the returned username is not the expected actor. A 401 means the token is missing,
expired, revoked, or its actor has been disabled.

## Find work

List tasks assigned to the verified actor using its returned `id`:

```sh
curl --fail --silent --get \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  --data-urlencode "assigned_to=$ACTOR_ID" \
  --data-urlencode "status=todo" \
  --data-urlencode "status=doing" \
  "$TASK_BOARD_URL/api/v1/tasks"
```

Prefer tasks with `actionable: true`. `is_blocked: true` means at least one dependency is not
done. Do not work around a block by changing the dependency unless the task instructions and your
authority explicitly call for it.

## Read before changing

`GET /api/v1/tasks/{id}` returns both the task and its event history. The response ETag and task
`version` represent the current revision.

Update using that version:

```sh
curl --fail --silent -X PATCH \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'If-Match: "3"' \
  -d '{"status":"doing"}' \
  "$TASK_BOARD_URL/api/v1/tasks/$TASK_ID"
```

A 412 response means somebody changed the task. Fetch it again and reconsider the update. Never
blindly increment or retry the old body.

When finishing, send status and result together. Use `done` for success and `failed` when the work
was attempted but could not succeed. Results should be concise and may contain repository paths,
commit IDs, or URLs. They must not contain credentials.

## Create work safely

Use a stable, operation-specific `Idempotency-Key` so retries do not duplicate tasks:

```sh
curl --fail --silent -X POST \
  -H "Authorization: Bearer $TASK_BOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $RUN_ID:create-follow-up" \
  -d '{"title":"Review generated report","assigned_to":"ACTOR_UUID"}' \
  "$TASK_BOARD_URL/api/v1/tasks"
```

The token determines `created_by`. Supplying that field is rejected. Idempotency keys last 24
hours and may not be reused with a different request body.

## Error handling

Errors use `application/problem+json` and include a stable `code`:

- `authentication_required` / `invalid_token`: stop and request credential help.
- `invalid_token`: stop; the token may be revoked or its actor administratively disabled.
- `validation_failed`: fix the request rather than retrying unchanged.
- `task_blocked`: fetch blocker state and wait or report the block.
- `version_conflict`: fetch, reconsider, and retry only if still appropriate.
- `forbidden`: the current actor does not own that mutation.

The complete contract is at `/api/v1/openapi.json` and the conventions are in `/docs/api.md`.
