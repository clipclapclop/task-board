# API conventions

All JSON endpoints are under `/api/v1`. This worker revision predates production adoption, so the
provisional service API is intentionally unsupported.

Use bearer authentication on every endpoint except `/api/v1/openapi.json`. Tokens identify one
actor and are not interchangeable with browser actor-selection cookies. Call `/api/v1/whoami`
before worker operations and verify the expected active `worker` identity.

## Human and worker surfaces

Human API actors may use task collection, detail, PATCH, reopen, and export routes subject to
their normal permissions. Task lists support repeatable `status`, `assigned_to`, `created_by`,
`project`, RFC 3339 `updated_after`, boolean `actionable`, text `q`, opaque `cursor`, and `limit`
filters.

Workers may use:

- `GET /api/v1/whoami`;
- `GET /api/v1/actors` and `GET /api/v1/projects` for task creation;
- `POST /api/v1/work/ready` with optional `project_id` and optional `count`;
- `POST /api/v1/work/{task_id}/complete` with `done` or `failed`; and
- `POST /api/v1/tasks` for project-bearing peer or continuation work.

Generic task listing, detail, PATCH, reopen, and history routes return
`worker_task_access_forbidden` for workers.

## Ready windows and ordering

Ready defaults to all projects and count one. Count must be 1 through 32. It first returns matching
`doing` tasks ordered by immutable `queue_sequence`, then claims enough actionable `todo` tasks in
sequence order to fill the requested window. The 200 response contains `count`, optional echoed
`project_id`, and a `deliveries` array. It returns 204 if the window is empty.

Count is a window size, not capacity or incremental demand. A smaller count returns an active
prefix without releasing hidden work. A filtered request may claim work even when another project
has active tasks. Ready has no idempotency key: transactional status-first redelivery supplies its
retry semantics.

## Stable tasks and completion

Every task requires an active project and has a read-only queue sequence. While a task is doing,
title, description, project, assignee, and dependencies are frozen. Humans may cancel owned work,
and administrators retain explicit terminal reopen.

Human task PATCH uses optional fields and requires `If-Match` containing the integer task version.
Missing and stale versions return 428 and 412. Worker ready and completion enforce state
preconditions transactionally and do not use `If-Match`. Identical completion retries return the
existing terminal task without another event.

## Idempotency and errors

Task creation supports actor-scoped `Idempotency-Key` values for 24 hours. Reuse a key only with
the identical body.

Errors use `application/problem+json` with `type`, `title`, `status`, `detail`, `code`, and optional
`fields`. Clients must branch on `code`, not English detail. Worker-specific codes are:

- `unsupported_actor_kind`;
- `worker_task_access_forbidden`;
- `invalid_project`;
- `invalid_count`;
- `work_not_owned`;
- `completion_conflict`; and
- `queue_sequence_conflict`.

Operational endpoints `/health/live` and `/health/ready` remain unversioned and unauthenticated
because production exposes them only to loopback and Caddy.
