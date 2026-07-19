CREATE TRIGGER tasks_project_required_insert
BEFORE INSERT ON tasks
WHEN NEW.project_id IS NULL OR NEW.project_id = ''
BEGIN
    SELECT RAISE(ABORT, 'project_id is required');
END;

CREATE TRIGGER tasks_project_required_update
BEFORE UPDATE OF project_id ON tasks
WHEN NEW.project_id IS NULL OR NEW.project_id = ''
BEGIN
    SELECT RAISE(ABORT, 'project_id is required');
END;
