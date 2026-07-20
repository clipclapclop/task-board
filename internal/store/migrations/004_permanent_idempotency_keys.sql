-- Creation IDs are permanent. Keep the legacy expires_at column populated so
-- the previous binary can still read the table during a rollback.
UPDATE idempotency_keys
SET expires_at = '9999-12-31T23:59:59Z';
