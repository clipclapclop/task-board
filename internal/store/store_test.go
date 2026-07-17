package store

import (
	"context"
	"errors"
	"path/filepath"
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
func task(t *testing.T, s *Store, a model.Actor, title, assigned string, deps ...string) model.Task {
	t.Helper()
	v, _, err := s.CreateTask(context.Background(), a, CreateTaskInput{Title: title, AssignedTo: assigned, BlockedBy: deps}, "")
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
	first := task(t, s, admin, "first", worker.ID)
	second := task(t, s, admin, "second", worker.ID)
	dependent := task(t, s, admin, "dependent", worker.ID, first.ID, second.ID)
	if !dependent.IsBlocked || dependent.Actionable {
		t.Fatalf("dependent state %#v", dependent)
	}
	doing := "doing"
	if _, err := s.PatchTask(ctx, worker, dependent.ID, dependent.Version, PatchTaskInput{Status: &doing}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked transition=%v", err)
	}
	done := "done"
	first, err := s.PatchTask(ctx, worker, first.ID, first.Version, PatchTaskInput{Status: &done})
	if err != nil {
		t.Fatal(err)
	}
	dependent, _ = s.Task(ctx, dependent.ID)
	if !dependent.IsBlocked {
		t.Fatal("one blocker should remain")
	}
	second, err = s.PatchTask(ctx, worker, second.ID, second.Version, PatchTaskInput{Status: &done})
	if err != nil {
		t.Fatal(err)
	}
	dependent, _ = s.Task(ctx, dependent.ID)
	if dependent.IsBlocked || !dependent.Actionable {
		t.Fatal("all blockers done should be actionable")
	}
	dependent, err = s.PatchTask(ctx, worker, dependent.ID, dependent.Version, PatchTaskInput{Status: &doing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReopenTask(ctx, admin, first.ID, first.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen with doing downstream=%v", err)
	}
	todo := "todo"
	dependent, err = s.PatchTask(ctx, worker, dependent.ID, dependent.Version, PatchTaskInput{Status: &todo})
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
	a := task(t, s, admin, "a", admin.ID)
	b := task(t, s, admin, "b", admin.ID, a.ID)
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
	in := CreateTaskInput{Title: "same", AssignedTo: admin.ID}
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
