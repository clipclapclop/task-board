package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/clipclapclop/task-board/internal/model"
	"github.com/clipclapclop/task-board/internal/store"
)

func TestCreateVerifyRehearse(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.CreateActor(context.Background(), model.Actor{Username: "admin", DisplayName: "Admin", Kind: "human", Role: "admin", Active: true}); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), st, filepath.Join(root, "backups"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(archive); err != nil {
		t.Fatal(err)
	}
	if err = Verify(archive); err != nil {
		t.Fatal(err)
	}
	if err = Rehearse(archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored", "task-board.sqlite3")
	if err = Restore(archive, restored); err != nil {
		t.Fatal(err)
	}
	check, err := store.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	actors, err := check.Actors(context.Background(), true)
	if err != nil || len(actors) != 1 {
		t.Fatalf("restored actors=%d err=%v", len(actors), err)
	}
}
