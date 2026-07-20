package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/clipclapclop/task-board/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func actor(t *testing.T, s *Store, username, kind, role string) model.Actor {
	t.Helper()
	a, err := s.CreateActor(context.Background(), model.Actor{Username: username, DisplayName: username, Kind: kind, Role: role, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func project(t *testing.T, s *Store, a model.Actor, name string) model.Project {
	t.Helper()
	p, err := s.CreateProject(context.Background(), a, name, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func task(t *testing.T, s *Store, a model.Actor, projectID, title, assigned string, deps ...string) model.Task {
	t.Helper()
	v, _, err := s.CreateTask(context.Background(), a, CreateTaskInput{Title: title, ProjectID: projectID, AssignedTo: assigned, BlockedBy: deps}, "")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestActorsTokensAndDisable(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "worker", "member")
	if _, err := s.CreateActor(ctx, model.Actor{Username: "worker-admin", DisplayName: "Worker Admin", Kind: "worker", Role: "admin", Active: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("worker administrator error=%v", err)
	}
	var storageKind string
	if err := s.DB.QueryRowContext(ctx, `SELECT kind FROM actors WHERE id=?`, worker.ID).Scan(&storageKind); err != nil || storageKind != "service" {
		t.Fatalf("storage kind=%q err=%v", storageKind, err)
	}
	_, secret, err := s.CreateToken(ctx, worker.ID, "primary", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AuthenticateToken(ctx, secret)
	if err != nil || got.ID != worker.ID || got.Kind != "worker" {
		t.Fatalf("authenticate: %#v %v", got, err)
	}
	if _, err = s.UpdateActor(ctx, admin.ID, admin.DisplayName, admin.Role, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("last admin disable error=%v", err)
	}
	if _, err = s.UpdateActor(ctx, admin.ID, admin.DisplayName, "member", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("last admin demotion error=%v", err)
	}
	_, err = s.UpdateActor(ctx, worker.ID, worker.DisplayName, worker.Role, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AuthenticateToken(ctx, secret); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled token error=%v", err)
	}
}

func TestDependenciesLifecycleAndHistory(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "worker", "member")
	p := project(t, s, admin, "work")
	first := task(t, s, admin, p.ID, "first", worker.ID)
	second := task(t, s, admin, p.ID, "second", worker.ID)
	dependent := task(t, s, admin, p.ID, "dependent", worker.ID, first.ID, second.ID)
	if !dependent.IsBlocked || dependent.Actionable {
		t.Fatalf("dependent state %#v", dependent)
	}
	doing := "doing"
	if _, err := s.PatchTask(ctx, admin, dependent.ID, dependent.Version, PatchTaskInput{Status: &doing}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked transition=%v", err)
	}
	done := "done"
	first, err := s.PatchTask(ctx, admin, first.ID, first.Version, PatchTaskInput{Status: &done})
	if err != nil {
		t.Fatal(err)
	}
	dependent, _ = s.Task(ctx, dependent.ID)
	if !dependent.IsBlocked {
		t.Fatal("one blocker should remain")
	}
	second, err = s.PatchTask(ctx, admin, second.ID, second.Version, PatchTaskInput{Status: &done})
	if err != nil {
		t.Fatal(err)
	}
	dependent, _ = s.Task(ctx, dependent.ID)
	if dependent.IsBlocked || !dependent.Actionable {
		t.Fatal("all blockers done should be actionable")
	}
	dependent, err = s.PatchTask(ctx, admin, dependent.ID, dependent.Version, PatchTaskInput{Status: &doing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReopenTask(ctx, admin, first.ID, first.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen with doing downstream=%v", err)
	}
	todo := "todo"
	dependent, err = s.PatchTask(ctx, admin, dependent.ID, dependent.Version, PatchTaskInput{Status: &todo})
	if err != nil {
		t.Fatal(err)
	}
	first, err = s.ReopenTask(ctx, admin, first.ID, first.Version)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "todo" {
		t.Fatalf("reopened status=%s", first.Status)
	}
	events, err := s.TaskEvents(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events=%d want 3", len(events))
	}
}

func TestCyclesPermissionsAndVersions(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	other := actor(t, s, "other", "human", "member")
	p := project(t, s, admin, "work")
	a := task(t, s, admin, p.ID, "a", admin.ID)
	b := task(t, s, admin, p.ID, "b", admin.ID, a.ID)
	deps := []string{b.ID}
	if _, err := s.PatchTask(ctx, admin, a.ID, a.Version, PatchTaskInput{BlockedBy: &deps}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cycle error=%v", err)
	}
	title := "changed"
	if _, err := s.PatchTask(ctx, other, a.ID, a.Version, PatchTaskInput{Title: &title}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("permission error=%v", err)
	}
	a, err := s.PatchTask(ctx, admin, a.ID, a.Version, PatchTaskInput{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	title2 := "stale"
	if _, err = s.PatchTask(ctx, admin, a.ID, a.Version-1, PatchTaskInput{Title: &title2}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("stale error=%v", err)
	}
}

func TestIdempotentCreate(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	p := project(t, s, admin, "work")
	in := CreateTaskInput{Title: "same", ProjectID: p.ID, AssignedTo: admin.ID}
	first, replayed, err := s.CreateTask(ctx, admin, in, "key")
	if err != nil || replayed {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE idempotency_keys SET expires_at='2000-01-01T00:00:00Z' WHERE actor_id=? AND key=?`, admin.ID, "key"); err != nil {
		t.Fatal(err)
	}
	second, replayed, err := s.CreateTask(ctx, admin, in, "key")
	if err != nil || !replayed || first.ID != second.ID {
		t.Fatalf("replay %#v %#v %v %v", first, second, replayed, err)
	}
	if first.QueueSequence == 0 || second.QueueSequence != first.QueueSequence {
		t.Fatalf("queue sequence first=%d second=%d", first.QueueSequence, second.QueueSequence)
	}
	in.Title = "different"
	if _, _, err = s.CreateTask(ctx, admin, in, "key"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("key reuse=%v", err)
	}
	in.Title = "same"
	third, replayed, err := s.CreateTask(ctx, admin, in, "other-key")
	if err != nil || replayed || third.ID == first.ID {
		t.Fatalf("distinct key %#v replayed=%v err=%v", third, replayed, err)
	}
	invalid := CreateTaskInput{Title: "correctable", AssignedTo: admin.ID}
	if _, _, err := s.CreateTask(ctx, admin, invalid, "correctable-key"); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("invalid creation=%v", err)
	}
	invalid.ProjectID = p.ID
	if _, replayed, err := s.CreateTask(ctx, admin, invalid, "correctable-key"); err != nil || replayed {
		t.Fatalf("corrected creation replayed=%v err=%v", replayed, err)
	}
}

func TestConcurrentIdempotentCreate(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	p := project(t, s, admin, "work")
	in := CreateTaskInput{Title: "concurrent", ProjectID: p.ID, AssignedTo: admin.ID}
	type result struct {
		task     model.Task
		replayed bool
		err      error
	}
	const callers = 16
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, replayed, err := s.CreateTask(ctx, admin, in, "concurrent-key")
			results <- result{task: task, replayed: replayed, err: err}
		}()
	}
	wg.Wait()
	close(results)
	var id string
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if id == "" {
			id = result.task.ID
		} else if result.task.ID != id {
			t.Fatalf("concurrent IDs differ: %s != %s", result.task.ID, id)
		}
		if !result.replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("new creations=%d", created)
	}
	events, err := s.TaskEvents(ctx, id)
	if err != nil || len(events) != 1 || events[0].EventType != "created" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	next := task(t, s, admin, p.ID, "next", admin.ID)
	if next.QueueSequence != 2 {
		t.Fatalf("next queue sequence=%d", next.QueueSequence)
	}
}

func TestProjectsRequiredAndDoingDetailsFrozen(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "worker", "member")
	p := project(t, s, admin, "work")
	if _, _, err := s.CreateTask(ctx, admin, CreateTaskInput{Title: "missing project", AssignedTo: worker.ID}, ""); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("missing project error=%v", err)
	}
	todo := task(t, s, admin, p.ID, "todo", worker.ID)
	empty := ""
	if _, err := s.PatchTask(ctx, admin, todo.ID, todo.Version, PatchTaskInput{ProjectID: &empty}); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("clear project error=%v", err)
	}
	response, found, err := s.ReadyWork(ctx, worker, p.ID, 1)
	if err != nil || !found || len(response.Deliveries) != 1 || response.Deliveries[0].Task.ID != todo.ID {
		t.Fatalf("ready=%#v found=%v err=%v", response, found, err)
	}
	delivery := response.Deliveries[0]
	if delivery.Task.QueueSequence != todo.QueueSequence {
		t.Fatalf("ready=%#v found=%v err=%v", delivery, found, err)
	}
	title := "changed while doing"
	if _, err := s.PatchTask(ctx, admin, todo.ID, delivery.Task.Version, PatchTaskInput{Title: &title}); !errors.Is(err, ErrConflict) {
		t.Fatalf("frozen details error=%v", err)
	}
	if _, err := s.PatchTask(ctx, worker, todo.ID, delivery.Task.Version, PatchTaskInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("worker patch error=%v", err)
	}
	cancelled := "cancelled"
	got, err := s.PatchTask(ctx, admin, todo.ID, delivery.Task.Version, PatchTaskInput{Status: &cancelled})
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("cancel=%#v err=%v", got, err)
	}
}

func TestQueueSequenceIsTransactionalAndImmutable(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "worker", "member")
	other := actor(t, s, "other-worker", "worker", "member")
	p := project(t, s, admin, "work")
	first := task(t, s, admin, p.ID, "first", worker.ID)
	if _, _, err := s.CreateTask(ctx, admin, CreateTaskInput{Title: "bad", ProjectID: p.ID, AssignedTo: worker.ID, BlockedBy: []string{"missing"}}, ""); err == nil {
		t.Fatal("missing dependency should fail")
	}
	second := task(t, s, admin, p.ID, "second", worker.ID)
	if second.QueueSequence != first.QueueSequence+1 {
		t.Fatalf("rolled-back sequence was consumed: first=%d second=%d", first.QueueSequence, second.QueueSequence)
	}
	reassigned, err := s.PatchTask(ctx, admin, first.ID, first.Version, PatchTaskInput{AssignedTo: &other.ID})
	if err != nil || reassigned.QueueSequence != first.QueueSequence {
		t.Fatalf("reassigned=%#v err=%v", reassigned, err)
	}
	changedSequence := reassigned.QueueSequence + 100
	if _, err := s.PatchTask(ctx, admin, reassigned.ID, reassigned.Version, PatchTaskInput{QueueSequence: &changedSequence}); !errors.Is(err, ErrQueueSequenceConflict) {
		t.Fatalf("queue sequence patch error=%v", err)
	}
	done := "done"
	second, err = s.PatchTask(ctx, admin, second.ID, second.Version, PatchTaskInput{Status: &done})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := s.ReopenTask(ctx, admin, second.ID, second.Version)
	if err != nil || reopened.QueueSequence != second.QueueSequence {
		t.Fatalf("reopened=%#v err=%v", reopened, err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE tasks SET queue_sequence=queue_sequence+100 WHERE id=?`, first.ID); err == nil {
		t.Fatal("queue sequence update should be rejected")
	}
}

func TestQueueSequenceMigrationBackfillsCreationOrder(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migration.sqlite3")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, name := range []string{"001_initial.sql", "002_require_task_project.sql"} {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,applied_at) VALUES(1,'2026-01-01T00:00:00Z'),(2,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO actors(id,username,display_name,kind,role,active,created_at,updated_at) VALUES('actor','actor','Actor','human','admin',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,name,description,created_by,created_at,updated_at) VALUES('project','Project','','actor','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, created string }{{"b", "2026-01-01T00:00:00Z"}, {"a", "2026-01-01T00:00:00Z"}, {"c", "2026-01-02T00:00:00Z"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id,title,project_id,created_by,assigned_to,status,created_at,updated_at) VALUES(?,?, 'project','actor','actor','todo',?,?)`, row.id, row.id, row.created, row.created); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO idempotency_keys(actor_id,key,request_hash,task_id,expires_at) VALUES('actor','legacy',X'01','a','2000-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: db}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for id, expected := range map[string]int64{"a": 1, "b": 2, "c": 3} {
		var sequence int64
		if err := db.QueryRowContext(ctx, `SELECT queue_sequence FROM tasks WHERE id=?`, id).Scan(&sequence); err != nil || sequence != expected {
			t.Fatalf("task %s sequence=%d err=%v", id, sequence, err)
		}
	}
	created, _, err := s.CreateTask(ctx, model.Actor{ID: "actor", Kind: "human", Role: "admin", Active: true}, CreateTaskInput{Title: "next", ProjectID: "project", AssignedTo: "actor"}, "")
	if err != nil || created.QueueSequence != 4 {
		t.Fatalf("next sequence=%d err=%v", created.QueueSequence, err)
	}
	var expires string
	if err := db.QueryRowContext(ctx, `SELECT expires_at FROM idempotency_keys WHERE actor_id='actor' AND key='legacy'`).Scan(&expires); err != nil || expires != idempotencyExpirySentinel {
		t.Fatalf("legacy idempotency expiry=%q err=%v", expires, err)
	}
}

func TestReadyWorkStatusFirstFiltersAndUnblocking(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "worker", "member")
	other := actor(t, s, "other-worker", "worker", "member")
	p := project(t, s, admin, "work")
	q := project(t, s, admin, "other")
	blocker := task(t, s, admin, p.ID, "blocker", other.ID)
	olderBlocked := task(t, s, admin, p.ID, "older blocked", worker.ID, blocker.ID)
	first := task(t, s, admin, p.ID, "first", worker.ID)
	second := task(t, s, admin, p.ID, "second", worker.ID)
	otherProject := task(t, s, admin, q.ID, "other project", worker.ID)

	response, found, err := s.ReadyWork(ctx, worker, p.ID, 2)
	if err != nil || !found || len(response.Deliveries) != 2 || response.Deliveries[0].Task.ID != first.ID || response.Deliveries[1].Task.ID != second.ID {
		t.Fatalf("initial ready=%#v found=%v err=%v", response, found, err)
	}
	if response.Deliveries[0].Task.Actionable || response.Deliveries[1].Task.Actionable {
		t.Fatalf("doing tasks must not be reported actionable: %#v", response)
	}
	done := "done"
	blocker, err = s.PatchTask(ctx, admin, blocker.ID, blocker.Version, PatchTaskInput{Status: &done})
	if err != nil {
		t.Fatal(err)
	}
	response, found, err = s.ReadyWork(ctx, worker, p.ID, 2)
	if err != nil || !found || response.Deliveries[0].Delivery != "resumed" || response.Deliveries[0].Task.ID != first.ID || response.Deliveries[1].Task.ID != second.ID {
		t.Fatalf("unblocked displaced doing=%#v err=%v", response, err)
	}
	if got, _ := s.Task(ctx, olderBlocked.ID); got.Status != "todo" {
		t.Fatalf("older blocked task status=%s", got.Status)
	}
	if _, err := s.CompleteWork(ctx, worker, first.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	response, _, err = s.ReadyWork(ctx, worker, p.ID, 2)
	if err != nil || response.Deliveries[0].Task.ID != second.ID || response.Deliveries[0].Delivery != "resumed" || response.Deliveries[1].Task.ID != olderBlocked.ID || response.Deliveries[1].Delivery != "claimed" {
		t.Fatalf("gap fill=%#v err=%v", response, err)
	}
	qResponse, _, err := s.ReadyWork(ctx, worker, q.ID, 1)
	if err != nil || qResponse.Deliveries[0].Task.ID != otherProject.ID {
		t.Fatalf("filtered expansion=%#v err=%v", qResponse, err)
	}
	global, _, err := s.ReadyWork(ctx, worker, "", 3)
	if err != nil || len(global.Deliveries) != 3 {
		t.Fatalf("global recovery=%#v err=%v", global, err)
	}
	for _, delivery := range global.Deliveries {
		if delivery.Delivery != "resumed" {
			t.Fatalf("global ready claimed despite three doing: %#v", global)
		}
	}
	if _, found, err := s.ReadyWork(ctx, admin, "", 1); !errors.Is(err, ErrWorkerRequired) || found {
		t.Fatalf("human ready found=%v err=%v", found, err)
	}
	if _, found, err := s.ReadyWork(ctx, worker, "", 0); !errors.Is(err, ErrInvalidCount) || found {
		t.Fatalf("invalid count found=%v err=%v", found, err)
	}
	if _, found, err := s.ReadyWork(ctx, worker, "missing", 1); !errors.Is(err, ErrInvalidProject) || found {
		t.Fatalf("invalid project found=%v err=%v", found, err)
	}
}

func TestReadyWorkConcurrentRequestsReturnSameWindow(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "worker", "member")
	p := project(t, s, admin, "work")
	first := task(t, s, admin, p.ID, "first", worker.ID)
	second := task(t, s, admin, p.ID, "second", worker.ID)

	type outcome struct {
		response model.ReadyResponse
		found    bool
		err      error
	}
	start := make(chan struct{})
	out := make(chan outcome, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			d, found, err := s.ReadyWork(ctx, worker, p.ID, 2)
			out <- outcome{d, found, err}
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	for got := range out {
		if got.err != nil || !got.found || len(got.response.Deliveries) != 2 || got.response.Deliveries[0].Task.ID != first.ID || got.response.Deliveries[1].Task.ID != second.ID {
			t.Fatalf("concurrent delivery=%#v found=%v err=%v", got.response, got.found, got.err)
		}
	}
	for _, taskValue := range []model.Task{first, second} {
		events, err := s.TaskEvents(ctx, taskValue.ID)
		if err != nil || len(events) != 2 || events[1].EventType != "claimed" {
			t.Fatalf("events=%#v err=%v", events, err)
		}
	}
}

func TestCompleteWorkReplayConflictAndOwnership(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "worker", "member")
	other := actor(t, s, "other-worker", "worker", "member")
	p := project(t, s, admin, "work")
	taskValue := task(t, s, admin, p.ID, "work", worker.ID)
	if _, err := s.CompleteWork(ctx, worker, taskValue.ID, "done", "too soon"); !errors.Is(err, ErrWorkNotOwned) {
		t.Fatalf("unclaimed completion=%v", err)
	}
	response, _, err := s.ReadyWork(ctx, worker, p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	delivery := response.Deliveries[0]
	if _, err := s.CompleteWork(ctx, other, delivery.Task.ID, "done", "wrong actor"); !errors.Is(err, ErrWorkNotOwned) {
		t.Fatalf("wrong actor completion=%v", err)
	}
	completed, err := s.CompleteWork(ctx, worker, delivery.Task.ID, "done", "  output  ")
	if err != nil || completed.Status != "done" || completed.Result != "output" {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	events, _ := s.TaskEvents(ctx, delivery.Task.ID)
	replayed, err := s.CompleteWork(ctx, worker, delivery.Task.ID, "done", "output")
	if err != nil || replayed.Version != completed.Version {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	after, _ := s.TaskEvents(ctx, delivery.Task.ID)
	if len(after) != len(events) {
		t.Fatalf("replay added event: before=%d after=%d", len(events), len(after))
	}
	if _, err := s.CompleteWork(ctx, worker, delivery.Task.ID, "failed", "different"); !errors.Is(err, ErrCompletionConflict) {
		t.Fatalf("conflicting completion=%v", err)
	}
	if _, err := s.CompleteWork(ctx, admin, delivery.Task.ID, "done", "output"); !errors.Is(err, ErrWorkerRequired) {
		t.Fatalf("human completion=%v", err)
	}
	failedTask := task(t, s, admin, p.ID, "failing work", worker.ID)
	failedResponse, found, err := s.ReadyWork(ctx, worker, p.ID, 1)
	if err != nil || !found || failedResponse.Deliveries[0].Task.ID != failedTask.ID {
		t.Fatalf("failed delivery=%#v found=%v err=%v", failedResponse, found, err)
	}
	failed, err := s.CompleteWork(ctx, worker, failedTask.ID, "failed", "")
	if err != nil || failed.Status != "failed" || failed.Result != "" {
		t.Fatalf("failed completion=%#v err=%v", failed, err)
	}

	blocker := task(t, s, admin, p.ID, "reopenable blocker", other.ID)
	dependent := task(t, s, admin, p.ID, "completed dependent", worker.ID, blocker.ID)
	if _, _, err := s.ReadyWork(ctx, other, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	blocker, err = s.CompleteWork(ctx, other, blocker.ID, "done", "artifact")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadyWork(ctx, worker, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	dependent, err = s.CompleteWork(ctx, worker, dependent.ID, "done", "consumed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReopenTask(ctx, admin, blocker.ID, blocker.Version); err != nil {
		t.Fatal(err)
	}
	if replayed, err := s.CompleteWork(ctx, worker, dependent.ID, "done", "consumed"); err != nil || replayed.Version != dependent.Version {
		t.Fatalf("replay after blocker reopen=%#v err=%v", replayed, err)
	}
}

func TestDependencyResultsReachDirectContinuationOnly(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	workerA := actor(t, s, "worker-a", "worker", "member")
	workerB := actor(t, s, "worker-b", "worker", "member")
	p := project(t, s, admin, "shared")
	upstream := task(t, s, workerA, p.ID, "B task", workerB.ID)
	continuation := task(t, s, workerA, p.ID, "A continuation", workerA.ID, upstream.ID)
	indirect := task(t, s, workerA, p.ID, "indirect", workerA.ID, continuation.ID)

	bResponse, found, err := s.ReadyWork(ctx, workerB, p.ID, 1)
	if err != nil || !found || bResponse.Deliveries[0].Task.ID != upstream.ID {
		t.Fatalf("B ready=%#v found=%v err=%v", bResponse, found, err)
	}
	if _, err := s.CompleteWork(ctx, workerB, upstream.ID, "done", "artifact://answer-4"); err != nil {
		t.Fatal(err)
	}
	aResponse, found, err := s.ReadyWork(ctx, workerA, p.ID, 1)
	if err != nil || !found || aResponse.Deliveries[0].Task.ID != continuation.ID {
		t.Fatalf("A ready=%#v found=%v err=%v", aResponse, found, err)
	}
	aWork := aResponse.Deliveries[0]
	if len(aWork.DependencyResults) != 1 || aWork.DependencyResults[0].TaskID != upstream.ID || aWork.DependencyResults[0].Result != "artifact://answer-4" {
		t.Fatalf("dependency results=%#v", aWork.DependencyResults)
	}
	for _, result := range aWork.DependencyResults {
		if result.TaskID == indirect.ID {
			t.Fatalf("indirect result leaked: %#v", result)
		}
	}
}
