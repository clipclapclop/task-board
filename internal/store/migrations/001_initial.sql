CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE actors (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    display_name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('human','service')),
    role TEXT NOT NULL CHECK (role IN ('member','admin')),
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0,1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (kind = 'human' OR role = 'member')
);

CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    actor_id TEXT NOT NULL REFERENCES actors(id),
    name TEXT NOT NULL,
    token_hash BLOB NOT NULL UNIQUE,
    prefix TEXT NOT NULL,
    expires_at TEXT,
    last_used_at TEXT,
    revoked_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX api_tokens_actor_idx ON api_tokens(actor_id);

CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL REFERENCES actors(id),
    archived_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    project_id TEXT REFERENCES projects(id),
    created_by TEXT NOT NULL REFERENCES actors(id),
    assigned_to TEXT NOT NULL REFERENCES actors(id),
    status TEXT NOT NULL CHECK (status IN ('todo','doing','done','failed','cancelled')),
    result TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX tasks_updated_idx ON tasks(updated_at DESC, id DESC);
CREATE INDEX tasks_assigned_idx ON tasks(assigned_to, updated_at DESC);
CREATE INDEX tasks_created_idx ON tasks(created_by, updated_at DESC);

CREATE TABLE task_dependencies (
    task_id TEXT NOT NULL REFERENCES tasks(id),
    blocked_by_id TEXT NOT NULL REFERENCES tasks(id),
    PRIMARY KEY(task_id, blocked_by_id),
    CHECK(task_id <> blocked_by_id)
);

CREATE TABLE task_events (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    actor_id TEXT NOT NULL REFERENCES actors(id),
    event_type TEXT NOT NULL,
    changes_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX task_events_task_idx ON task_events(task_id, created_at, id);

CREATE TABLE idempotency_keys (
    actor_id TEXT NOT NULL REFERENCES actors(id),
    key TEXT NOT NULL,
    request_hash BLOB NOT NULL,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    expires_at TEXT NOT NULL,
    PRIMARY KEY(actor_id, key)
);
