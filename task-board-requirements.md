# Task Board v1 Requirements

**Status:** Authoritative v1 specification

## Product and trust model

Task Board is a private household issue tracker for humans and workers. It is a
single-organization service available only through the household Tailnet at
`https://task-board.oorangy.com`.

Tailscale and Caddy provide the network-access boundary. The browser is deliberately passwordless:
it defaults to the configured household administrator and lets a visitor select another active
human actor. That choice is attribution, not an authentication claim. Every worker API request is
authenticated by a named, revocable bearer token mapped to one actor. There are no passwords,
password-reset flows, or public registration.

## Actors and workers

An actor has a UUIDv7 ID, immutable unique username, display name, public kind (`human` or
`worker`), role (`member` or `admin`), active flag, and timestamps. Workers cannot be
administrators or selected in the portal. Administrators create and edit actors and issue or
revoke tokens. Disabling an actor invalidates its tokens and prevents new task creation for it
without removing historical references. The last active administrator cannot be disabled.

A worker is the configurable HTTP API client that calls ready and complete. Each independently
coordinated deployment has its own actor and token. Hosts are not actors and are not modeled.
Several workers may run on one host, and each worker may receive tasks from any project.

## Projects

Projects have a UUIDv7 ID, unique name, optional description, creator, timestamps, and optional
archive time. Administrators create, edit, and archive projects. Archived projects remain visible
on old tasks but cannot be used for new tasks. Every task has exactly one project. A project says
what a task pertains to and can filter ready; it is not a worker identity.

## Tasks and ordering

Tasks have a UUIDv7 ID, title, optional description, required project, creator, one human or worker
assignee, status (`todo`, `doing`, `done`, `failed`, or `cancelled`), optional result,
optimistic-concurrency version, immutable `queue_sequence`, and timestamps. Tasks are never
deleted.

The server allocates a globally unique, monotonically increasing queue sequence transactionally
with task creation. It does not change on edit, reassignment, or administrative reopen. Ready uses
this sequence rather than client clocks or UUID ordering.

Human actors may read all tasks. Workers receive task content only through ready delivery or the
creation response for a task they created. Workers cannot browse task lists, details, history, or
terminal work, and cannot directly patch, cancel, or reopen tasks.

Creators may edit and cancel todo tasks. While a task is doing, its title, description, project,
assignee, and dependencies are frozen, though a human may cancel it. Human assignees use the
general task API. Worker assignees use ready and complete. Administrators may perform human
actions on any task and explicitly reopen terminal tasks. Reopen clears the result, preserves
history and queue sequence, and returns the task to todo. Other terminal mutations are forbidden.

A task may depend on multiple tasks, including tasks in other projects. It is actionable only when
every blocker is `done`; failed or cancelled blockers continue blocking. Blocked tasks may not
start, finish, or fail, but may be cancelled. Direct and indirect cycles are rejected. A completed
blocker cannot be reopened while downstream work is doing.

Every mutation appends an immutable event recording actor, event type, changed fields, and time.
Comments and attachments are not part of v1.

## Worker delivery and completion

Ready accepts an optional project filter and a `count` from 1 through 32, defaulting to one. Within
the selected scope, it returns up to count tasks in two tiers: existing doing tasks first, then
enough actionable todo tasks to fill the window. Each tier is ordered by queue sequence. Selected
todo tasks are atomically changed to doing, versioned, and given one claimed event. A newly
unblocked todo never displaces active work. A project-filtered request may intentionally expand
ownership while the worker has active work in another project.

Ready returns direct completed blocker results with each delivery. It returns 204 when there is no
matching owned or actionable work. Repeating a request naturally redelivers the matching active
prefix; concurrent identical requests converge without duplicate claims or events.

Workers individually complete owned doing tasks as `done` or `failed`, in any order. Completion
stores an optional result of at most 20,000 characters, increments the version, and appends one
event. Identical retries are idempotent; a different repeated outcome conflicts. Task Board
enforces delivery and lifecycle semantics, not result correctness or exactly-once external
effects.

## Portal

The responsive server-rendered portal provides My Tasks, Created by Me, and All Tasks; filters for
actor, project, status, blocked/actionable state, update time, and text; task creation, detail, and
editing; dependency and history views; a profile/token page; and administrator screens for actors,
tokens, projects, and sanitized export. Project selection is required. Essential actions work
without JavaScript. Portal and CLI labels use worker, not service or client.

## API

Worker routes are versioned under `/api/v1`. Operational health routes and browser pages are
unversioned. V1 includes actors, projects, human task list/get/patch/reopen, worker ready/complete,
task creation, `whoami`, sanitized export, and OpenAPI endpoints. Task lists use stable cursor
pagination. Human PATCH requires `If-Match`; missing and stale versions return 428 and 412. Task
creation supports actor-scoped idempotency keys for 24 hours. Errors use
`application/problem+json` with stable codes.

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
Kanban, multi-tenancy, a generic worker executable, worker registry, host or fleet inventory,
availability heartbeat, execution leases, worker capacity, and queue-depth limits.
