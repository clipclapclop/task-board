ALTER TABLE tasks ADD COLUMN queue_sequence INTEGER;

WITH ranked AS (
    SELECT id, ROW_NUMBER() OVER (ORDER BY created_at ASC, id ASC) AS sequence
    FROM tasks
)
UPDATE tasks
SET queue_sequence = (SELECT sequence FROM ranked WHERE ranked.id = tasks.id);

CREATE UNIQUE INDEX tasks_queue_sequence_idx ON tasks(queue_sequence);

CREATE TABLE task_queue_counter (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    value INTEGER NOT NULL CHECK (value >= 0)
);

INSERT INTO task_queue_counter(id, value)
SELECT 1, COALESCE(MAX(queue_sequence), 0) FROM tasks;

CREATE TRIGGER tasks_queue_sequence_required_insert
BEFORE INSERT ON tasks
WHEN NEW.queue_sequence IS NULL
BEGIN
    SELECT RAISE(ABORT, 'queue_sequence is required');
END;

CREATE TRIGGER tasks_queue_sequence_immutable
BEFORE UPDATE OF queue_sequence ON tasks
WHEN NEW.queue_sequence IS NOT OLD.queue_sequence
BEGIN
    SELECT RAISE(ABORT, 'queue_sequence is immutable');
END;
