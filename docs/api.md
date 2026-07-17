# API conventions

All machine endpoints are under `/api/v1`. Breaking contracts require a new path version;
backward-compatible fields may be added to v1.

Use bearer authentication on every endpoint except `/api/v1/openapi.json`. Tokens identify one
actor and are not interchangeable with browser actor-selection cookies.

Task collection filters are repeatable `status`, `assigned_to`, `created_by`, `project`, RFC 3339
`updated_after`, boolean `actionable`, text `q`, opaque `cursor`, and `limit` (default 50, maximum
200). Results are ordered by `updated_at DESC, id DESC`.

Task PATCH uses JSON merge-like optional fields and requires `If-Match` containing the integer task
version. Use an empty `project_id` to clear a project and an empty `blocked_by` array to remove all
dependencies. Permission and lifecycle rules are enforced on the combined update.

Problem responses have media type `application/problem+json` and fields `type`, `title`, `status`,
`detail`, `code`, and optional `fields`. Client behavior must branch on `code`, not English detail.

Operational endpoints `/health/live` and `/health/ready` are intentionally outside API versioning
and require no bearer token because they are exposed only to loopback/Caddy in production.
