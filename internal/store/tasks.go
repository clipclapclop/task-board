package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/clipclapclop/task-board/internal/model"
)

type CreateTaskInput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	ProjectID   string   `json:"project_id"`
	AssignedTo  string   `json:"assigned_to"`
	BlockedBy   []string `json:"blocked_by"`
}

type PatchTaskInput struct {
	Title         *string   `json:"title,omitempty"`
	Description   *string   `json:"description,omitempty"`
	ProjectID     *string   `json:"project_id,omitempty"`
	AssignedTo    *string   `json:"assigned_to,omitempty"`
	Status        *string   `json:"status,omitempty"`
	Result        *string   `json:"result,omitempty"`
	BlockedBy     *[]string `json:"blocked_by,omitempty"`
	QueueSequence *int64    `json:"queue_sequence,omitempty"`
}

type queryRow interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queryRows interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

const taskColumns = `id,title,description,COALESCE(project_id,''),created_by,assigned_to,status,result,queue_sequence,version,created_at,updated_at`

func scanTask(row interface{ Scan(...any) error }) (model.Task, error) {
	var t model.Task
	var created, updated string
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.ProjectID, &t.CreatedBy, &t.AssignedTo, &t.Status, &t.Result, &t.QueueSequence, &t.Version, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.CreatedAt = parseTime(created)
	t.UpdatedAt = parseTime(updated)
	return t, nil
}

func taskByID(ctx context.Context, q queryRow, id string) (model.Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
}

func hydrateTask(ctx context.Context, q queryRows, t *model.Task) error {
	rows, err := q.QueryContext(ctx, `SELECT x.id,x.title,x.status FROM task_dependencies d JOIN tasks x ON x.id=d.blocked_by_id WHERE d.task_id=? ORDER BY x.created_at,x.id`, t.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r model.TaskRef
		if err := rows.Scan(&r.ID, &r.Title, &r.Status); err != nil {
			return err
		}
		t.BlockedBy = append(t.BlockedBy, r)
		if r.Status != "done" {
			t.IsBlocked = true
		}
	}
	t.Actionable = t.Status == "todo" && !t.IsBlocked
	return rows.Err()
}

func (s *Store) hydrateTask(ctx context.Context, t *model.Task) error {
	return hydrateTask(ctx, s.DB, t)
}

func (s *Store) Task(ctx context.Context, id string) (model.Task, error) {
	t, err := taskByID(ctx, s.DB, id)
	if err != nil {
		return t, err
	}
	err = s.hydrateTask(ctx, &t)
	return t, err
}

func validStatus(v string) bool {
	switch v {
	case "todo", "doing", "done", "failed", "cancelled":
		return true
	}
	return false
}

func validateTaskInput(in CreateTaskInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || len(in.Title) > 200 {
		return fmt.Errorf("%w: title is required and must be at most 200 characters", ErrInvalid)
	}
	if len(in.Description) > 20000 {
		return fmt.Errorf("%w: description is too long", ErrInvalid)
	}
	if in.AssignedTo == "" {
		return fmt.Errorf("%w: assigned_to is required", ErrInvalid)
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return fmt.Errorf("%w: project_id is required", ErrInvalidProject)
	}
	return nil
}

func activeActorTx(ctx context.Context, tx *sql.Tx, id string) error {
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT active FROM actors WHERE id=?`, id).Scan(&active); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: actor not found", ErrInvalid)
	} else if err != nil {
		return err
	}
	if active != 1 {
		return fmt.Errorf("%w: actor disabled", ErrInvalid)
	}
	return nil
}
func activeProjectTx(ctx context.Context, tx *sql.Tx, id string) error {
	if id == "" {
		return fmt.Errorf("%w: project_id is required", ErrInvalidProject)
	}
	var ok int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=? AND archived_at IS NULL`, id).Scan(&ok); err != nil {
		return err
	}
	if ok != 1 {
		return fmt.Errorf("%w: project not found or archived", ErrInvalidProject)
	}
	return nil
}

func addEvent(ctx context.Context, tx *sql.Tx, taskID, actorID, eventType string, changes map[string]any) error {
	id, err := NewID()
	if err != nil {
		return err
	}
	body, err := json.Marshal(changes)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_events(id,task_id,actor_id,event_type,changes_json,created_at) VALUES(?,?,?,?,?,?)`, id, taskID, actorID, eventType, string(body), stamp(time.Now()))
	return err
}

func nextQueueSequence(ctx context.Context, tx *sql.Tx) (int64, error) {
	var sequence int64
	err := tx.QueryRowContext(ctx, `UPDATE task_queue_counter SET value=value+1 WHERE id=1 RETURNING value`).Scan(&sequence)
	return sequence, err
}

func setDependencies(ctx context.Context, tx *sql.Tx, taskID string, deps []string) error {
	seen := map[string]bool{}
	for _, dep := range deps {
		if dep == "" {
			continue
		}
		if dep == taskID {
			return fmt.Errorf("%w: task cannot block itself", ErrInvalid)
		}
		if seen[dep] {
			continue
		}
		seen[dep] = true
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id=?`, dep).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return fmt.Errorf("%w: blocker %s not found", ErrInvalid, dep)
		}
		var cycle int
		err := tx.QueryRowContext(ctx, `WITH RECURSIVE reaches(id) AS (
			SELECT blocked_by_id FROM task_dependencies WHERE task_id=?
			UNION SELECT d.blocked_by_id FROM task_dependencies d JOIN reaches r ON d.task_id=r.id
		) SELECT COUNT(*) FROM reaches WHERE id=?`, dep, taskID).Scan(&cycle)
		if err != nil {
			return err
		}
		if cycle > 0 {
			return fmt.Errorf("%w: dependency cycle", ErrConflict)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_dependencies WHERE task_id=?`, taskID); err != nil {
		return err
	}
	for dep := range seen {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_dependencies(task_id,blocked_by_id) VALUES(?,?)`, taskID, dep); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateTask(ctx context.Context, actor model.Actor, in CreateTaskInput, idempotencyKey string) (model.Task, bool, error) {
	if !actor.Active {
		return model.Task{}, false, ErrForbidden
	}
	if err := validateTaskInput(in); err != nil {
		return model.Task{}, false, err
	}
	reqBody, _ := json.Marshal(in)
	reqHash := sha256.Sum256(reqBody)
	if idempotencyKey != "" {
		var stored []byte
		var taskID, expires string
		err := s.DB.QueryRowContext(ctx, `SELECT request_hash,task_id,expires_at FROM idempotency_keys WHERE actor_id=? AND key=?`, actor.ID, idempotencyKey).Scan(&stored, &taskID, &expires)
		if err == nil && parseTime(expires).After(time.Now()) {
			if string(stored) != string(reqHash[:]) {
				return model.Task{}, false, fmt.Errorf("%w: idempotency key reused with different request", ErrConflict)
			}
			t, err := s.Task(ctx, taskID)
			return t, true, err
		} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return model.Task{}, false, err
		}
	}
	id, err := NewID()
	if err != nil {
		return model.Task{}, false, err
	}
	now := time.Now().UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, false, err
	}
	defer tx.Rollback()
	if err = activeActorTx(ctx, tx, in.AssignedTo); err != nil {
		return model.Task{}, false, err
	}
	if err = activeProjectTx(ctx, tx, in.ProjectID); err != nil {
		return model.Task{}, false, err
	}
	queueSequence, err := nextQueueSequence(ctx, tx)
	if err != nil {
		return model.Task{}, false, err
	}
	var project any
	if in.ProjectID != "" {
		project = in.ProjectID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,title,description,project_id,created_by,assigned_to,status,result,queue_sequence,version,created_at,updated_at) VALUES(?,?,?,?,?,?,'todo','',?,1,?,?)`, id, strings.TrimSpace(in.Title), strings.TrimSpace(in.Description), project, actor.ID, in.AssignedTo, queueSequence, stamp(now), stamp(now))
	if err != nil {
		return model.Task{}, false, err
	}
	if err = setDependencies(ctx, tx, id, in.BlockedBy); err != nil {
		return model.Task{}, false, err
	}
	if err = addEvent(ctx, tx, id, actor.ID, "created", map[string]any{"title": strings.TrimSpace(in.Title), "assigned_to": in.AssignedTo, "blocked_by": in.BlockedBy}); err != nil {
		return model.Task{}, false, err
	}
	if idempotencyKey != "" {
		_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO idempotency_keys(actor_id,key,request_hash,task_id,expires_at) VALUES(?,?,?,?,?)`, actor.ID, idempotencyKey, reqHash[:], id, stamp(now.Add(24*time.Hour)))
		if err != nil {
			return model.Task{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return model.Task{}, false, err
	}
	t, err := s.Task(ctx, id)
	return t, false, err
}

func blockedTx(ctx context.Context, tx *sql.Tx, taskID string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies d JOIN tasks b ON b.id=d.blocked_by_id WHERE d.task_id=? AND b.status<>'done'`, taskID).Scan(&n)
	return n > 0, err
}

func (s *Store) PatchTask(ctx context.Context, actor model.Actor, id string, expected int64, in PatchTaskInput) (model.Task, error) {
	if !actor.Active {
		return model.Task{}, ErrForbidden
	}
	if actor.IsWorker() {
		return model.Task{}, ErrForbidden
	}
	if in.QueueSequence != nil {
		return model.Task{}, ErrQueueSequenceConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback()
	old, err := taskByID(ctx, tx, id)
	if err != nil {
		return model.Task{}, err
	}
	if old.Version != expected {
		return model.Task{}, ErrPrecondition
	}
	if old.Terminal() {
		return model.Task{}, fmt.Errorf("%w: terminal task", ErrConflict)
	}
	if old.Status == "doing" && (in.Title != nil || in.Description != nil || in.ProjectID != nil || in.AssignedTo != nil || in.BlockedBy != nil) {
		return model.Task{}, fmt.Errorf("%w: doing task details are frozen", ErrConflict)
	}
	next := old
	changes := map[string]any{}
	canEdit := actor.IsAdmin() || actor.ID == old.CreatedBy
	canWork := actor.IsAdmin() || actor.ID == old.AssignedTo
	if in.Title != nil {
		if !canEdit {
			return model.Task{}, ErrForbidden
		}
		v := strings.TrimSpace(*in.Title)
		if v == "" || len(v) > 200 {
			return model.Task{}, ErrInvalid
		}
		changes["title"] = map[string]any{"old": old.Title, "new": v}
		next.Title = v
	}
	if in.Description != nil {
		if !canEdit {
			return model.Task{}, ErrForbidden
		}
		if len(*in.Description) > 20000 {
			return model.Task{}, ErrInvalid
		}
		changes["description"] = map[string]any{"changed": true}
		next.Description = strings.TrimSpace(*in.Description)
	}
	if in.ProjectID != nil {
		if !canEdit {
			return model.Task{}, ErrForbidden
		}
		if err := activeProjectTx(ctx, tx, *in.ProjectID); err != nil {
			return model.Task{}, err
		}
		changes["project_id"] = map[string]any{"old": old.ProjectID, "new": *in.ProjectID}
		next.ProjectID = *in.ProjectID
	}
	if in.AssignedTo != nil {
		if !canEdit {
			return model.Task{}, ErrForbidden
		}
		if err := activeActorTx(ctx, tx, *in.AssignedTo); err != nil {
			return model.Task{}, err
		}
		changes["assigned_to"] = map[string]any{"old": old.AssignedTo, "new": *in.AssignedTo}
		next.AssignedTo = *in.AssignedTo
	}
	if in.BlockedBy != nil {
		if !canEdit {
			return model.Task{}, ErrForbidden
		}
		if err := setDependencies(ctx, tx, id, *in.BlockedBy); err != nil {
			return model.Task{}, err
		}
		changes["blocked_by"] = map[string]any{"new": *in.BlockedBy}
	}
	if in.Result != nil {
		if !canWork {
			return model.Task{}, ErrForbidden
		}
		if len(*in.Result) > 20000 {
			return model.Task{}, ErrInvalid
		}
		changes["result"] = map[string]any{"changed": true}
		next.Result = strings.TrimSpace(*in.Result)
	}
	if in.Status != nil && *in.Status != old.Status {
		v := *in.Status
		if !validStatus(v) {
			return model.Task{}, ErrInvalid
		}
		if v == "cancelled" {
			if !canEdit {
				return model.Task{}, ErrForbidden
			}
		} else {
			if !canWork {
				return model.Task{}, ErrForbidden
			}
			if v != "todo" && v != "doing" && v != "done" && v != "failed" {
				return model.Task{}, ErrInvalid
			}
			blocked, err := blockedTx(ctx, tx, id)
			if err != nil {
				return model.Task{}, err
			}
			if blocked && v != "todo" {
				return model.Task{}, ErrBlocked
			}
		}
		changes["status"] = map[string]any{"old": old.Status, "new": v}
		next.Status = v
	}
	if len(changes) == 0 {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return model.Task{}, err
		}
		return s.Task(ctx, id)
	}
	now := time.Now().UTC()
	var project any
	if next.ProjectID != "" {
		project = next.ProjectID
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET title=?,description=?,project_id=?,assigned_to=?,status=?,result=?,version=version+1,updated_at=? WHERE id=? AND version=?`, next.Title, next.Description, project, next.AssignedTo, next.Status, next.Result, stamp(now), id, expected)
	if err != nil {
		return model.Task{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return model.Task{}, ErrPrecondition
	}
	if err = addEvent(ctx, tx, id, actor.ID, "updated", changes); err != nil {
		return model.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Task{}, err
	}
	return s.Task(ctx, id)
}

func (s *Store) ReopenTask(ctx context.Context, actor model.Actor, id string, expected int64) (model.Task, error) {
	if !actor.IsAdmin() {
		return model.Task{}, ErrForbidden
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback()
	old, err := taskByID(ctx, tx, id)
	if err != nil {
		return model.Task{}, err
	}
	if old.Version != expected {
		return model.Task{}, ErrPrecondition
	}
	if !old.Terminal() {
		return model.Task{}, fmt.Errorf("%w: task is not terminal", ErrConflict)
	}
	var doing int
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE downstream(id) AS (
		SELECT task_id FROM task_dependencies WHERE blocked_by_id=?
		UNION SELECT d.task_id FROM task_dependencies d JOIN downstream x ON d.blocked_by_id=x.id
	) SELECT COUNT(*) FROM downstream x JOIN tasks t ON t.id=x.id WHERE t.status='doing'`, id).Scan(&doing)
	if err != nil {
		return model.Task{}, err
	}
	if doing > 0 {
		return model.Task{}, fmt.Errorf("%w: a downstream task is doing", ErrConflict)
	}
	now := time.Now()
	r, err := tx.ExecContext(ctx, `UPDATE tasks SET status='todo',result='',version=version+1,updated_at=? WHERE id=? AND version=?`, stamp(now), id, expected)
	if err != nil {
		return model.Task{}, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return model.Task{}, ErrPrecondition
	}
	if err = addEvent(ctx, tx, id, actor.ID, "reopened", map[string]any{"status": map[string]any{"old": old.Status, "new": "todo"}, "result": map[string]any{"cleared": old.Result != ""}}); err != nil {
		return model.Task{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Task{}, err
	}
	return s.Task(ctx, id)
}

func dependencyResults(ctx context.Context, q queryRows, taskID string) ([]model.DependencyResult, error) {
	rows, err := q.QueryContext(ctx, `SELECT b.id,b.title,b.result,b.status
		FROM task_dependencies d JOIN tasks b ON b.id=d.blocked_by_id
		WHERE d.task_id=? ORDER BY b.created_at,b.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DependencyResult{}
	for rows.Next() {
		var result model.DependencyResult
		var status string
		if err := rows.Scan(&result.TaskID, &result.Title, &result.Result, &status); err != nil {
			return nil, err
		}
		if status != "done" {
			return nil, ErrBlocked
		}
		out = append(out, result)
	}
	return out, rows.Err()
}

func taskWithDependencyResults(ctx context.Context, q queryRows, task model.Task) (model.Task, []model.DependencyResult, error) {
	results, err := dependencyResults(ctx, q, task.ID)
	if err != nil {
		return model.Task{}, nil, err
	}
	for _, result := range results {
		task.BlockedBy = append(task.BlockedBy, model.TaskRef{ID: result.TaskID, Title: result.Title, Status: "done"})
	}
	task.IsBlocked = false
	task.Actionable = false
	return task, results, nil
}

// ReadyWork returns a status-first window of work assigned to the worker. It
// redelivers doing tasks before atomically claiming actionable todo tasks.
func (s *Store) ReadyWork(ctx context.Context, actor model.Actor, projectID string, count int) (model.ReadyResponse, bool, error) {
	response := model.ReadyResponse{ProjectID: strings.TrimSpace(projectID), Count: count, Deliveries: []model.TaskDelivery{}}
	if !actor.IsWorker() {
		return response, false, ErrWorkerRequired
	}
	if count < 1 || count > 32 {
		return response, false, ErrInvalidCount
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return response, false, err
	}
	defer tx.Rollback()
	if response.ProjectID != "" {
		if err := activeProjectTx(ctx, tx, response.ProjectID); err != nil {
			return response, false, err
		}
	}

	projectClause := ""
	args := []any{actor.ID}
	if response.ProjectID != "" {
		projectClause = " AND project_id=?"
		args = append(args, response.ProjectID)
	}
	args = append(args, count)
	rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE assigned_to=? AND status='doing'`+projectClause+` ORDER BY queue_sequence LIMIT ?`, args...)
	if err != nil {
		return response, false, err
	}
	var doing []model.Task
	for rows.Next() {
		t, scanErr := scanTask(rows)
		if scanErr != nil {
			rows.Close()
			return response, false, scanErr
		}
		doing = append(doing, t)
	}
	if err := rows.Close(); err != nil {
		return response, false, err
	}

	remaining := count - len(doing)
	var claimed []model.Task
	if remaining > 0 {
		args = []any{actor.ID}
		if response.ProjectID != "" {
			args = append(args, response.ProjectID)
		}
		args = append(args, remaining)
		rows, err = tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks t
			WHERE t.assigned_to=? AND t.status='todo'`+projectClause+`
			AND NOT EXISTS(SELECT 1 FROM task_dependencies d JOIN tasks b ON b.id=d.blocked_by_id WHERE d.task_id=t.id AND b.status<>'done')
			ORDER BY t.queue_sequence LIMIT ?`, args...)
		if err != nil {
			return response, false, err
		}
		for rows.Next() {
			t, scanErr := scanTask(rows)
			if scanErr != nil {
				rows.Close()
				return response, false, scanErr
			}
			claimed = append(claimed, t)
		}
		if err := rows.Close(); err != nil {
			return response, false, err
		}
	}

	now := time.Now().UTC()
	for i := range claimed {
		result, updateErr := tx.ExecContext(ctx, `UPDATE tasks SET status='doing',version=version+1,updated_at=? WHERE id=? AND version=? AND status='todo'`, stamp(now), claimed[i].ID, claimed[i].Version)
		if updateErr != nil {
			return response, false, updateErr
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return response, false, ErrPrecondition
		}
		if err := addEvent(ctx, tx, claimed[i].ID, actor.ID, "claimed", map[string]any{"status": map[string]any{"old": "todo", "new": "doing"}}); err != nil {
			return response, false, err
		}
		claimed[i].Status = "doing"
		claimed[i].Version++
		claimed[i].UpdatedAt = now
	}

	for _, entry := range []struct {
		delivery string
		tasks    []model.Task
	}{{"resumed", doing}, {"claimed", claimed}} {
		for _, task := range entry.tasks {
			hydrated, results, hydrateErr := taskWithDependencyResults(ctx, tx, task)
			if hydrateErr != nil {
				return response, false, hydrateErr
			}
			response.Deliveries = append(response.Deliveries, model.TaskDelivery{Delivery: entry.delivery, Task: hydrated, DependencyResults: results})
		}
	}
	if len(response.Deliveries) == 0 {
		return response, false, nil
	}
	if err := tx.Commit(); err != nil {
		return response, false, err
	}
	return response, true, nil
}

// CompleteWork atomically completes worker-owned work. Identical terminal
// replays are successful and do not append duplicate events.
func (s *Store) CompleteWork(ctx context.Context, actor model.Actor, id, status, result string) (model.Task, error) {
	if !actor.IsWorker() {
		return model.Task{}, ErrWorkerRequired
	}
	if status != "done" && status != "failed" {
		return model.Task{}, fmt.Errorf("%w: status must be done or failed", ErrInvalid)
	}
	if len(result) > 20000 {
		return model.Task{}, fmt.Errorf("%w: result is too long", ErrInvalid)
	}
	result = strings.TrimSpace(result)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback()
	old, err := taskByID(ctx, tx, id)
	if err != nil {
		return model.Task{}, err
	}
	if old.AssignedTo != actor.ID {
		return model.Task{}, ErrWorkNotOwned
	}
	if old.Terminal() {
		if old.Status == status && old.Result == result {
			if err = hydrateTask(ctx, tx, &old); err != nil {
				return model.Task{}, err
			}
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return model.Task{}, err
			}
			return old, nil
		}
		return model.Task{}, ErrCompletionConflict
	}
	if old.Status != "doing" {
		return model.Task{}, ErrWorkNotOwned
	}
	blocked, err := blockedTx(ctx, tx, id)
	if err != nil {
		return model.Task{}, err
	}
	if blocked {
		return model.Task{}, ErrBlocked
	}
	now := time.Now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE tasks SET status=?,result=?,version=version+1,updated_at=? WHERE id=? AND assigned_to=? AND status='doing'`, status, result, stamp(now), id, actor.ID)
	if err != nil {
		return model.Task{}, err
	}
	n, _ := updated.RowsAffected()
	if n != 1 {
		return model.Task{}, ErrWorkNotOwned
	}
	changes := map[string]any{"status": map[string]any{"old": "doing", "new": status}}
	if result != "" {
		changes["result"] = map[string]any{"changed": true}
	}
	if err := addEvent(ctx, tx, id, actor.ID, "completed", changes); err != nil {
		return model.Task{}, err
	}
	old.Status = status
	old.Result = result
	old.Version++
	old.UpdatedAt = now
	if err = hydrateTask(ctx, tx, &old); err != nil {
		return model.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, err
	}
	return old, nil
}

func encodeCursor(t time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(stamp(t) + "|" + id))
}
func decodeCursor(v string) (string, string, error) {
	if v == "" {
		return "", "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return "", "", ErrInvalid
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return "", "", ErrInvalid
	}
	return parts[0], parts[1], nil
}

func (s *Store) Tasks(ctx context.Context, f model.TaskFilter) (model.TaskPage, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	var where []string
	var args []any
	if len(f.Statuses) > 0 {
		var marks []string
		for _, v := range f.Statuses {
			if !validStatus(v) {
				return model.TaskPage{}, ErrInvalid
			}
			marks = append(marks, "?")
			args = append(args, v)
		}
		where = append(where, "t.status IN ("+strings.Join(marks, ",")+")")
	}
	for col, val := range map[string]string{"t.assigned_to": f.AssignedTo, "t.created_by": f.CreatedBy, "t.project_id": f.ProjectID} {
		if val != "" {
			where = append(where, col+"=?")
			args = append(args, val)
		}
	}
	if f.UpdatedAfter != nil {
		where = append(where, "t.updated_at>?")
		args = append(args, stamp(*f.UpdatedAfter))
	}
	if f.Query != "" {
		where = append(where, "(t.title LIKE ? OR t.description LIKE ?)")
		q := "%" + f.Query + "%"
		args = append(args, q, q)
	}
	if f.Actionable != nil {
		clause := `t.status='todo' AND NOT EXISTS(SELECT 1 FROM task_dependencies d JOIN tasks b ON b.id=d.blocked_by_id WHERE d.task_id=t.id AND b.status<>'done')`
		if !*f.Actionable {
			clause = "NOT (" + clause + ")"
		}
		where = append(where, clause)
	}
	ct, cid, err := decodeCursor(f.Cursor)
	if err != nil {
		return model.TaskPage{}, err
	}
	if ct != "" {
		where = append(where, "(t.updated_at<? OR (t.updated_at=? AND t.id<?))")
		args = append(args, ct, ct, cid)
	}
	q := `SELECT ` + taskColumns + ` FROM tasks t`
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += ` ORDER BY t.updated_at DESC,t.id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return model.TaskPage{}, err
	}
	var tasks []model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return model.TaskPage{}, err
		}
		tasks = append(tasks, t)
	}
	if err = rows.Close(); err != nil {
		return model.TaskPage{}, err
	}
	page := model.TaskPage{}
	if len(tasks) > f.Limit {
		last := tasks[f.Limit-1]
		page.NextCursor = encodeCursor(last.UpdatedAt, last.ID)
		tasks = tasks[:f.Limit]
	}
	for i := range tasks {
		if err := s.hydrateTask(ctx, &tasks[i]); err != nil {
			return model.TaskPage{}, err
		}
	}
	page.Data = tasks
	return page, nil
}

func (s *Store) TaskEvents(ctx context.Context, taskID string) ([]model.TaskEvent, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,task_id,actor_id,event_type,changes_json,created_at FROM task_events WHERE task_id=? ORDER BY created_at,id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TaskEvent
	for rows.Next() {
		var e model.TaskEvent
		var body, created string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.ActorID, &e.EventType, &body, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(created)
		if err := json.Unmarshal([]byte(body), &e.Changes); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Export(ctx context.Context) (map[string]any, error) {
	actors, err := s.Actors(ctx, true)
	if err != nil {
		return nil, err
	}
	projects, err := s.Projects(ctx, true)
	if err != nil {
		return nil, err
	}
	var allTasks []model.Task
	cursor := ""
	for {
		page, pageErr := s.Tasks(ctx, model.TaskFilter{Limit: 200, Cursor: cursor})
		if pageErr != nil {
			return nil, pageErr
		}
		allTasks = append(allTasks, page.Data...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	var events []model.TaskEvent
	for _, t := range allTasks {
		es, err := s.TaskEvents(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		events = append(events, es...)
	}
	return map[string]any{"schema_version": 1, "exported_at": time.Now().UTC(), "actors": actors, "projects": projects, "tasks": allTasks, "task_events": events}, nil
}

func ParseIfMatch(v string) (int64, error) {
	v = strings.Trim(strings.TrimSpace(v), `"`)
	if strings.HasPrefix(v, "W/") {
		v = strings.Trim(strings.TrimPrefix(v, "W/"), `"`)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 {
		return 0, ErrInvalid
	}
	return n, nil
}
