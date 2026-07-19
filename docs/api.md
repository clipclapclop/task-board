# API conventions

All machine endpoints are under `/api/v1`. This v1 worker revision predates production service
consumers, so the earlier provisional service task-browsing behavior is intentionally unsupported.

Use bearer authentication on every endpoint except `/api/v1/openapi.json`. Tokens identify one
actor and are not interchangeable with browser actor-selection cookies.

## Human and service surfaces

Human API actors may use task collection, detail, PATCH, reopen, and export routes subject to their
normal permissions. Task lists support repeatable `status`, `assigned_to`, `created_by`, `project`,
RFC 3339 `updated_after`, boolean `actionable`, text `q`, opaque `cursor`, and `limit` filters.

Service actors receive work only through:

- `POST /api/v1/work/ready` with one `project_id`;
- `POST /api/v1/work/{task_id}/complete` with `done` or `failed`;
- `POST /api/v1/tasks` for project-bearing delegation and continuation tasks.

Generic task listing, detail, PATCH, reopen, and history routes return
`service_task_access_forbidden` for service actors.

## Projects and stable work

Every new task requires an active project. An empty project cannot clear task routing. While a task
is `doing`, title, description, project, assignee, and dependencies are frozen. Humans may cancel
owned work, and administrators retain explicit terminal reopen.

Human task PATCH uses JSON merge-like optional fields and requires `If-Match` containing the
integer task version. Missing and stale versions return 428 and 412. Service ready and completion
operations enforce their state preconditions transactionally and do not use `If-Match`.

## Idempotency and errors

Task creation supports actor-scoped `Idempotency-Key` values for 24 hours. The ready operation
redelivers existing owned work after an ambiguous response. Identical completion retries return the
existing terminal task without another event.

Errors use `application/problem+json` and fields `type`, `title`, `status`, `detail`, `code`, and
optional `fields`. Clients must branch on `code`, not English detail. Worker-specific codes include:

- `service_actor_required`;
- `service_task_access_forbidden`;
- `invalid_project`;
- `ambiguous_active_work`;
- `work_not_owned`;
- `completion_conflict`.

Operational endpoints `/health/live` and `/health/ready` remain unversioned and unauthenticated
because production exposes them only to loopback/Caddy.
