# Task Board v1 Requirements

**Status:** Authoritative v1 specification

## Product and trust model

Task Board is a private household issue tracker for humans and software agents. It is a
single-organization service available only through the household Tailnet at
`https://task-board.oorangy.com`.

Tailscale and Caddy provide the network-access boundary. The browser is deliberately
passwordless: it defaults to the configured household administrator and lets a visitor select
another active human actor. That choice is identification for attribution, not an authentication
claim. Every machine API request is authenticated by a named, revocable bearer token mapped to
one actor. There are no passwords, password-reset flows, or public registration.

## Actors

An actor has a UUIDv7 ID, immutable unique username, display name, kind (`human` or `service`),
role (`member` or `admin`), active flag, and timestamps. Only humans may be administrators or
selected in the portal. Administrators create and edit actors and issue/revoke API tokens.
Disabling an actor rejects its portal selection, invalidates all tokens, and prevents new
assignments without removing historical references. The last active administrator cannot be
disabled.

## Projects

Projects have a UUIDv7 ID, unique name, optional description, creator, timestamps, and optional
archive time. Administrators create, edit, and archive projects. Archived projects remain visible
on old tasks but cannot be used for new tasks.

## Tasks

Tasks have a UUIDv7 ID, title, optional description and project, creator, one assignee, status
(`todo`, `doing`, `done`, `failed`, or `cancelled`), optional result, optimistic-concurrency
version, and timestamps. Tasks are never deleted. All active actors may read all tasks.

Creators may edit and cancel their unfinished tasks. Assignees may update status and result.
Administrators may perform either action on any task and explicitly reopen a terminal task.
Terminal statuses are immutable except for administrator reopen. Reopen clears the current result
but history retains it.

A task may depend on multiple tasks, including tasks in other projects. It is actionable only
when every blocker is `done`; failed or cancelled blockers continue blocking. Blocked tasks may
not start, finish, or fail, but may be cancelled. Direct and indirect cycles are rejected.

Every mutation appends an immutable event recording actor, event type, changed fields, and time.
Comments and attachments are not part of v1.

## Portal

The responsive, server-rendered portal provides My Tasks, Created by Me, and All Tasks; filters
for actor, project, status, blocked/actionable state, update time, and text; task creation/detail/
editing; dependency and history views; a profile/token page; and administrator screens for actors,
tokens, projects, and sanitized export. Essential actions work without JavaScript.

## API

Machine routes are versioned under `/api/v1`. Operational health routes and browser pages are
unversioned. V1 includes actors, projects, task create/list/get/patch/reopen, `whoami`, sanitized
export, and OpenAPI endpoints. Task lists use stable cursor pagination. PATCH requires `If-Match`;
missing and stale versions return 428 and 412. Task creation supports actor-scoped idempotency keys
for 24 hours. Errors use `application/problem+json` with stable codes.

## Security and operations

API tokens contain at least 256 random bits, are displayed once, stored only as hashes, and never
logged or exported. Browser mutations use CSRF protection. Cookies are Secure, HttpOnly where
applicable, and SameSite. The app validates its configured host and emits structured logs without
credentials or sensitive task bodies.

ContainerBot runs the non-root app container with state in `/var/lib/task-board`, publishes only
`127.0.0.1:8787`, and uses the existing host Caddy bound to `100.122.228.62` with Cloudflare
DNS-01. Deployment is an explicit tested command with readiness checking and rollback.

A consistent SQLite snapshot is copied to the NAS once every 24 hours, verified with checksums and
`PRAGMA integrity_check`, and retained as 30 daily plus 12 monthly recovery points. Restoration is
rehearsed monthly. Sanitized JSON export excludes tokens and operational state.

## Deferred

Deferred features include public access, passwords or external login, comments, attachments,
notifications, email, tags, priority, due dates, recurring tasks, multiple assignees, drag-and-drop
Kanban, and multi-tenancy.
