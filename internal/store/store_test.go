package store

import (
	"context"
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
	service := actor(t, s, "worker", "service", "member")
	_, secret, err := s.CreateToken(ctx, service.ID, "primary", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AuthenticateToken(ctx, secret)
	if err != nil || got.ID != service.ID {
		t.Fatalf("authenticate: %#v %v", got, err)
	}
	if _, err = s.UpdateActor(ctx, admin.ID, admin.DisplayName, admin.Role, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("last admin disable error=%v", err)
	}
	if _, err = s.UpdateActor(ctx, admin.ID, admin.DisplayName, "member", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("last admin demotion error=%v", err)
	}
	_, err = s.UpdateActor(ctx, service.ID, service.DisplayName, service.Role, false)
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
	worker := actor(t, s, "worker", "service", "member")
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
	second, replayed, err := s.CreateTask(ctx, admin, in, "key")
	if err != nil || !replayed || first.ID != second.ID {
		t.Fatalf("replay %#v %#v %v %v", first, second, replayed, err)
	}
	in.Title = "different"
	if _, _, err = s.CreateTask(ctx, admin, in, "key"); !errors.Is(err, ErrConflict) {
		t.Fatalf("key reuse=%v", err)
	}
}

func TestProjectsRequiredAndDoingDetailsFrozen(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "service", "member")
	p := project(t, s, admin, "work")
	if _, _, err := s.CreateTask(ctx, admin, CreateTaskInput{Title: "missing project", AssignedTo: worker.ID}, ""); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("missing project error=%v", err)
	}
	todo := task(t, s, admin, p.ID, "todo", worker.ID)
	empty := ""
	if _, err := s.PatchTask(ctx, admin, todo.ID, todo.Version, PatchTaskInput{ProjectID: &empty}); !errors.Is(err, ErrInvalidProject) {
		t.Fatalf("clear project error=%v", err)
	}
	delivery, found, err := s.ReadyWork(ctx, worker, p.ID)
	if err != nil || !found || delivery.Task.ID != todo.ID {
		t.Fatalf("ready=%#v found=%v err=%v", delivery, found, err)
	}
	title := "changed while doing"
	if _, err := s.PatchTask(ctx, admin, todo.ID, delivery.Task.Version, PatchTaskInput{Title: &title}); !errors.Is(err, ErrConflict) {
		t.Fatalf("frozen details error=%v", err)
	}
	if _, err := s.PatchTask(ctx, worker, todo.ID, delivery.Task.Version, PatchTaskInput{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("service patch error=%v", err)
	}
	cancelled := "cancelled"
	got, err := s.PatchTask(ctx, admin, todo.ID, delivery.Task.Version, PatchTaskInput{Status: &cancelled})
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("cancel=%#v err=%v", got, err)
	}
}

func TestReadyWorkOrderingIsolationAndRedelivery(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "service", "member")
	other := actor(t, s, "other-worker", "service", "member")
	p := project(t, s, admin, "work")
	q := project(t, s, admin, "other")
	blocker := task(t, s, admin, p.ID, "blocker", other.ID)
	blocked := task(t, s, admin, p.ID, "blocked oldest", worker.ID, blocker.ID)
	first := task(t, s, admin, p.ID, "first actionable", worker.ID)
	second := task(t, s, admin, p.ID, "second actionable", worker.ID)
	otherActor := task(t, s, admin, p.ID, "other actor", other.ID)
	otherProject := task(t, s, admin, q.ID, "other project", worker.ID)

	delivery, found, err := s.ReadyWork(ctx, worker, p.ID)
	if err != nil || !found || delivery.Delivery != "claimed" || delivery.Task.ID != first.ID {
		t.Fatalf("first delivery=%#v found=%v err=%v", delivery, found, err)
	}
	redelivery, found, err := s.ReadyWork(ctx, worker, p.ID)
	if err != nil || !found || redelivery.Delivery != "resumed" || redelivery.Task.ID != first.ID {
		t.Fatalf("redelivery=%#v found=%v err=%v", redelivery, found, err)
	}
	if _, err := s.CompleteWork(ctx, worker, first.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	delivery, found, err = s.ReadyWork(ctx, worker, p.ID)
	if err != nil || !found || delivery.Task.ID != second.ID {
		t.Fatalf("second delivery=%#v found=%v err=%v", delivery, found, err)
	}
	qDelivery, found, err := s.ReadyWork(ctx, worker, q.ID)
	if err != nil || !found || qDelivery.Task.ID != otherProject.ID {
		t.Fatalf("project delivery=%#v found=%v err=%v", qDelivery, found, err)
	}
	for _, untouched := range []model.Task{blocked, otherActor} {
		got, err := s.Task(ctx, untouched.ID)
		if err != nil || got.Status != "todo" {
			t.Fatalf("task %s status=%s err=%v", untouched.ID, got.Status, err)
		}
	}
	if _, found, err := s.ReadyWork(ctx, admin, p.ID); !errors.Is(err, ErrServiceActorRequired) || found {
		t.Fatalf("human ready found=%v err=%v", found, err)
	}
	if _, found, err := s.ReadyWork(ctx, worker, ""); !errors.Is(err, ErrInvalidProject) || found {
		t.Fatalf("invalid project found=%v err=%v", found, err)
	}
}

func TestReadyWorkConcurrentRequestsReturnOneTask(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "service", "member")
	p := project(t, s, admin, "work")
	want := task(t, s, admin, p.ID, "only task", worker.ID)

	type outcome struct {
		delivery model.WorkDelivery
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
			d, found, err := s.ReadyWork(ctx, worker, p.ID)
			out <- outcome{d, found, err}
		}()
	}
	close(start)
	wg.Wait()
	close(out)
	for got := range out {
		if got.err != nil || !got.found || got.delivery.Task.ID != want.ID {
			t.Fatalf("concurrent delivery=%#v found=%v err=%v", got.delivery, got.found, got.err)
		}
	}
	events, err := s.TaskEvents(ctx, want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].EventType != "claimed" {
		t.Fatalf("events=%#v", events)
	}
}

func TestCompleteWorkReplayConflictAndOwnership(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "service", "member")
	other := actor(t, s, "other-worker", "service", "member")
	p := project(t, s, admin, "work")
	taskValue := task(t, s, admin, p.ID, "work", worker.ID)
	if _, err := s.CompleteWork(ctx, worker, taskValue.ID, "done", "too soon"); !errors.Is(err, ErrWorkNotOwned) {
		t.Fatalf("unclaimed completion=%v", err)
	}
	delivery, _, err := s.ReadyWork(ctx, worker, p.ID)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := s.CompleteWork(ctx, admin, delivery.Task.ID, "done", "output"); !errors.Is(err, ErrServiceActorRequired) {
		t.Fatalf("human completion=%v", err)
	}
	failedTask := task(t, s, admin, p.ID, "failing work", worker.ID)
	failedDelivery, found, err := s.ReadyWork(ctx, worker, p.ID)
	if err != nil || !found || failedDelivery.Task.ID != failedTask.ID {
		t.Fatalf("failed delivery=%#v found=%v err=%v", failedDelivery, found, err)
	}
	failed, err := s.CompleteWork(ctx, worker, failedTask.ID, "failed", "")
	if err != nil || failed.Status != "failed" || failed.Result != "" {
		t.Fatalf("failed completion=%#v err=%v", failed, err)
	}

	blocker := task(t, s, admin, p.ID, "reopenable blocker", other.ID)
	dependent := task(t, s, admin, p.ID, "completed dependent", worker.ID, blocker.ID)
	if _, _, err := s.ReadyWork(ctx, other, p.ID); err != nil {
		t.Fatal(err)
	}
	blocker, err = s.CompleteWork(ctx, other, blocker.ID, "done", "artifact")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadyWork(ctx, worker, p.ID); err != nil {
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

func TestReadyWorkRejectsAmbiguousOwnedTasks(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	worker := actor(t, s, "worker", "service", "member")
	p := project(t, s, admin, "work")
	first := task(t, s, admin, p.ID, "first", worker.ID)
	second := task(t, s, admin, p.ID, "second", worker.ID)
	doing := "doing"
	if _, err := s.PatchTask(ctx, admin, first.ID, first.Version, PatchTaskInput{Status: &doing}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchTask(ctx, admin, second.ID, second.Version, PatchTaskInput{Status: &doing}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.ReadyWork(ctx, worker, p.ID); !errors.Is(err, ErrAmbiguousActiveWork) || found {
		t.Fatalf("ambiguous ready found=%v err=%v", found, err)
	}
}

func TestDependencyResultsReachDirectContinuationOnly(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	admin := actor(t, s, "admin", "human", "admin")
	machineA := actor(t, s, "machine-a", "service", "member")
	machineB := actor(t, s, "machine-b", "service", "member")
	p := project(t, s, admin, "shared")
	upstream := task(t, s, machineA, p.ID, "B task", machineB.ID)
	continuation := task(t, s, machineA, p.ID, "A continuation", machineA.ID, upstream.ID)
	indirect := task(t, s, machineA, p.ID, "indirect", machineA.ID, continuation.ID)

	bWork, found, err := s.ReadyWork(ctx, machineB, p.ID)
	if err != nil || !found || bWork.Task.ID != upstream.ID {
		t.Fatalf("B ready=%#v found=%v err=%v", bWork, found, err)
	}
	if _, err := s.CompleteWork(ctx, machineB, upstream.ID, "done", "artifact://answer-4"); err != nil {
		t.Fatal(err)
	}
	aWork, found, err := s.ReadyWork(ctx, machineA, p.ID)
	if err != nil || !found || aWork.Task.ID != continuation.ID {
		t.Fatalf("A ready=%#v found=%v err=%v", aWork, found, err)
	}
	if len(aWork.DependencyResults) != 1 || aWork.DependencyResults[0].TaskID != upstream.ID || aWork.DependencyResults[0].Result != "artifact://answer-4" {
		t.Fatalf("dependency results=%#v", aWork.DependencyResults)
	}
	for _, result := range aWork.DependencyResults {
		if result.TaskID == indirect.ID {
			t.Fatalf("indirect result leaked: %#v", result)
		}
	}
}
