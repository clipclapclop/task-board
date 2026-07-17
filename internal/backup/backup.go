package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/clipclapclop/task-board/internal/store"
)

type Manifest struct {
	SchemaVersion  int    `json:"schema_version"`
	CreatedAt      string `json:"created_at"`
	Revision       string `json:"revision"`
	DatabaseSize   int64  `json:"database_size"`
	DatabaseSHA256 string `json:"database_sha256"`
}

func Create(ctx context.Context, st *store.Store, destination, revision string) (string, error) {
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return "", err
	}
	// Build and validate the snapshot in the container's private temporary
	// filesystem. The destination may be a mount at the filesystem root (for
	// example /nas), so its parent is not necessarily writable by the non-root
	// runtime user.
	tmp, err := os.MkdirTemp("", "task-board-backup-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	dbPath := filepath.Join(tmp, "task-board.sqlite3")
	if _, err = st.DB.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL)`); err != nil {
		return "", err
	}
	if _, err = st.DB.ExecContext(ctx, `VACUUM INTO ?`, dbPath); err != nil {
		return "", fmt.Errorf("sqlite snapshot: %w", err)
	}
	check, err := store.Open(dbPath)
	if err != nil {
		return "", err
	}
	err = check.Ready(ctx)
	_ = check.Close()
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dbPath)
	if err != nil {
		return "", err
	}
	sum, err := fileHash(dbPath)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	manifest := Manifest{SchemaVersion: 1, CreatedAt: now.Format(time.RFC3339), Revision: revision, DatabaseSize: info.Size(), DatabaseSHA256: sum}
	body, _ := json.MarshalIndent(manifest, "", "  ")
	if err = os.WriteFile(filepath.Join(tmp, "manifest.json"), append(body, '\n'), 0o600); err != nil {
		return "", err
	}
	name := "task-board-" + now.Format("20060102T150405Z") + ".tar.gz"
	partial := filepath.Join(destination, "."+name+".tmp")
	if err = archive(partial, tmp, []string{"task-board.sqlite3", "manifest.json"}); err != nil {
		return "", err
	}
	final := filepath.Join(destination, name)
	if err = os.Rename(partial, final); err != nil {
		return "", err
	}
	if err = Verify(final); err != nil {
		return "", err
	}
	if err = Prune(destination, now); err != nil {
		return "", err
	}
	return final, nil
}

func archive(path, root string, names []string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	ok := false
	defer func() {
		if !ok {
			os.Remove(path)
		}
	}()
	for _, name := range names {
		source := filepath.Join(root, name)
		info, err := os.Stat(source)
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = name
		if err = tw.WriteHeader(h); err != nil {
			return err
		}
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func Verify(path string) error {
	tmp, err := os.MkdirTemp("", "task-board-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = extract(path, tmp); err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(tmp, "manifest.json"))
	if err != nil {
		return err
	}
	var m Manifest
	if err = json.Unmarshal(body, &m); err != nil {
		return err
	}
	dbPath := filepath.Join(tmp, "task-board.sqlite3")
	info, err := os.Stat(dbPath)
	if err != nil {
		return err
	}
	if info.Size() != m.DatabaseSize {
		return fmt.Errorf("database size mismatch")
	}
	sum, err := fileHash(dbPath)
	if err != nil {
		return err
	}
	if sum != m.DatabaseSHA256 {
		return fmt.Errorf("database checksum mismatch")
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Ready(context.Background())
}

func Rehearse(path string) error { return Verify(path) }

func Restore(path, database string) error {
	if err := Verify(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(database), 0o750); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(database), "task-board-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err = extract(path, tmp); err != nil {
		return err
	}
	staged := database + ".restore"
	_ = os.Remove(staged)
	if err = copyFile(filepath.Join(tmp, "task-board.sqlite3"), staged); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(database + suffix)
	}
	return os.Rename(staged, database)
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func extract(path, destination string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Name != "task-board.sqlite3" && h.Name != "manifest.json" {
			return fmt.Errorf("unexpected archive member %q", h.Name)
		}
		target := filepath.Join(destination, h.Name)
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type record struct {
	path string
	at   time.Time
}

func Prune(root string, now time.Time) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	var records []record
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "task-board-") || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(e.Name(), "task-board-"), ".tar.gz")
		t, err := time.Parse("20060102T150405Z", stamp)
		if err == nil {
			records = append(records, record{filepath.Join(root, e.Name()), t})
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].at.After(records[j].at) })
	keep := map[string]bool{}
	for i, r := range records {
		if i < 30 {
			keep[r.path] = true
		}
	}
	months := map[string]bool{}
	for _, r := range records {
		if now.Sub(r.at) > 366*24*time.Hour {
			continue
		}
		key := r.at.Format("2006-01")
		if !months[key] {
			months[key] = true
			keep[r.path] = true
		}
	}
	for _, r := range records {
		if !keep[r.path] {
			if err := os.Remove(r.path); err != nil {
				return err
			}
		}
	}
	return nil
}
