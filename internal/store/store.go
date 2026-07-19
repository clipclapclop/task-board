package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/clipclapclop/task-board/internal/model"
)

var (
	ErrNotFound              = errors.New("not found")
	ErrForbidden             = errors.New("forbidden")
	ErrConflict              = errors.New("conflict")
	ErrInvalid               = errors.New("invalid")
	ErrInvalidProject        = errors.New("invalid project")
	ErrBlocked               = errors.New("task is blocked")
	ErrPrecondition          = errors.New("precondition failed")
	ErrWorkerRequired        = errors.New("worker required")
	ErrInvalidCount          = errors.New("invalid count")
	ErrWorkNotOwned          = errors.New("work not owned")
	ErrCompletionConflict    = errors.New("completion conflict")
	ErrQueueSequenceConflict = errors.New("queue sequence conflict")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.Migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(entry.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		var exists int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,?)`, version, stamp(time.Now()))
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Ready(ctx context.Context) error {
	var result string
	if err := s.DB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("sqlite quick_check: %s", result)
	}
	return nil
}

func NewID() (string, error) {
	var b [16]byte
	ms := uint64(time.Now().UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func stamp(t time.Time) string     { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func nullableTime(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t := parseTime(v.String)
	return &t
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func scanActor(row interface{ Scan(...any) error }) (model.Actor, error) {
	var a model.Actor
	var active int
	var created, updated string
	err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Kind, &a.Role, &active, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if a.Kind == "service" {
		a.Kind = "worker"
	}
	a.Active, a.CreatedAt, a.UpdatedAt = active == 1, parseTime(created), parseTime(updated)
	return a, nil
}

const actorColumns = `id,username,display_name,kind,role,active,created_at,updated_at`

func (s *Store) EnsureDefaultAdmin(ctx context.Context, username, displayName string) (model.Actor, error) {
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&count); err != nil {
		return model.Actor{}, err
	}
	if count > 0 {
		return s.ActorByName(ctx, username)
	}
	return s.CreateActor(ctx, model.Actor{Username: username, DisplayName: displayName, Kind: "human", Role: "admin", Active: true})
}

func validateActor(a model.Actor) error {
	a.Username = strings.TrimSpace(a.Username)
	if a.Username == "" || len(a.Username) > 64 || a.DisplayName == "" {
		return fmt.Errorf("%w: username and display name are required", ErrInvalid)
	}
	for _, r := range a.Username {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return fmt.Errorf("%w: username must be lowercase letters, digits, '-' or '_'", ErrInvalid)
		}
	}
	if a.Kind != "human" && a.Kind != "worker" {
		return fmt.Errorf("%w: invalid actor kind", ErrInvalid)
	}
	if a.Role != "member" && a.Role != "admin" {
		return fmt.Errorf("%w: invalid role", ErrInvalid)
	}
	if a.Kind == "worker" && a.Role == "admin" {
		return fmt.Errorf("%w: workers cannot be administrators", ErrInvalid)
	}
	return nil
}

func (s *Store) CreateActor(ctx context.Context, a model.Actor) (model.Actor, error) {
	if err := validateActor(a); err != nil {
		return model.Actor{}, err
	}
	id, err := NewID()
	if err != nil {
		return model.Actor{}, err
	}
	now := time.Now().UTC()
	a.ID, a.CreatedAt, a.UpdatedAt = id, now, now
	storageKind := a.Kind
	if storageKind == "worker" {
		storageKind = "service"
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO actors(id,username,display_name,kind,role,active,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
		a.ID, a.Username, strings.TrimSpace(a.DisplayName), storageKind, a.Role, boolInt(a.Active), stamp(now), stamp(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return model.Actor{}, fmt.Errorf("%w: username already exists", ErrConflict)
		}
		return model.Actor{}, err
	}
	return a, nil
}

func (s *Store) Actors(ctx context.Context, includeDisabled bool) ([]model.Actor, error) {
	q := `SELECT ` + actorColumns + ` FROM actors`
	if !includeDisabled {
		q += ` WHERE active=1`
	}
	q += ` ORDER BY display_name COLLATE NOCASE, username`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Actor
	for rows.Next() {
		a, err := scanActor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Actor(ctx context.Context, id string) (model.Actor, error) {
	return scanActor(s.DB.QueryRowContext(ctx, `SELECT `+actorColumns+` FROM actors WHERE id=?`, id))
}

func (s *Store) ActorByName(ctx context.Context, username string) (model.Actor, error) {
	return scanActor(s.DB.QueryRowContext(ctx, `SELECT `+actorColumns+` FROM actors WHERE username=? COLLATE NOCASE`, username))
}

func (s *Store) UpdateActor(ctx context.Context, id, displayName, role string, active bool) (model.Actor, error) {
	a, err := s.Actor(ctx, id)
	if err != nil {
		return model.Actor{}, err
	}
	wasActiveAdmin := a.IsAdmin()
	a.DisplayName, a.Role, a.Active = strings.TrimSpace(displayName), role, active
	if err := validateActor(a); err != nil {
		return model.Actor{}, err
	}
	if wasActiveAdmin && (!active || role != "admin") {
		var admins int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors WHERE active=1 AND kind='human' AND role='admin'`).Scan(&admins); err != nil {
			return model.Actor{}, err
		}
		if admins <= 1 {
			return model.Actor{}, fmt.Errorf("%w: cannot disable or demote the last active administrator", ErrConflict)
		}
	}
	now := time.Now().UTC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Actor{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE actors SET display_name=?,role=?,active=?,updated_at=? WHERE id=?`, a.DisplayName, a.Role, boolInt(active), stamp(now), id); err == nil && !active {
		_, err = tx.ExecContext(ctx, `UPDATE api_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE actor_id=?`, stamp(now), id)
	}
	if err != nil {
		tx.Rollback()
		return model.Actor{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Actor{}, err
	}
	return s.Actor(ctx, id)
}

func (s *Store) CreateToken(ctx context.Context, actorID, name string, expires *time.Time) (model.APIToken, string, error) {
	a, err := s.Actor(ctx, actorID)
	if err != nil {
		return model.APIToken{}, "", err
	}
	if !a.Active {
		return model.APIToken{}, "", ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return model.APIToken{}, "", fmt.Errorf("%w: token name is required", ErrInvalid)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return model.APIToken{}, "", err
	}
	secret := "tb_v1_" + base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(secret))
	id, err := NewID()
	if err != nil {
		return model.APIToken{}, "", err
	}
	now := time.Now().UTC()
	prefix := secret[:min(15, len(secret))]
	var exp any
	if expires != nil {
		exp = stamp(*expires)
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO api_tokens(id,actor_id,name,token_hash,prefix,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`, id, actorID, name, hash[:], prefix, exp, stamp(now))
	if err != nil {
		return model.APIToken{}, "", err
	}
	return model.APIToken{ID: id, ActorID: actorID, Name: name, Prefix: prefix, ExpiresAt: expires, CreatedAt: now}, secret, nil
}

func scanToken(row interface{ Scan(...any) error }) (model.APIToken, error) {
	var t model.APIToken
	var exp, used, revoked sql.NullString
	var created string
	err := row.Scan(&t.ID, &t.ActorID, &t.Name, &t.Prefix, &exp, &used, &revoked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt, t.CreatedAt = nullableTime(exp), nullableTime(used), nullableTime(revoked), parseTime(created)
	return t, nil
}

func (s *Store) Tokens(ctx context.Context, actorID string) ([]model.APIToken, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,actor_id,name,prefix,expires_at,last_used_at,revoked_at,created_at FROM api_tokens WHERE actor_id=? ORDER BY created_at DESC`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) RevokeToken(ctx context.Context, tokenID string) error {
	r, err := s.DB.ExecContext(ctx, `UPDATE api_tokens SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, stamp(time.Now()), tokenID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AuthenticateToken(ctx context.Context, secret string) (model.Actor, error) {
	if !strings.HasPrefix(secret, "tb_v1_") {
		return model.Actor{}, ErrForbidden
	}
	hash := sha256.Sum256([]byte(secret))
	now := time.Now().UTC()
	row := s.DB.QueryRowContext(ctx, `SELECT a.id,a.username,a.display_name,a.kind,a.role,a.active,a.created_at,a.updated_at,t.id
		FROM api_tokens t JOIN actors a ON a.id=t.actor_id
		WHERE t.token_hash=? AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at>?) AND a.active=1`, hash[:], stamp(now))
	var a model.Actor
	var active int
	var created, updated, tokenID string
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Kind, &a.Role, &active, &created, &updated, &tokenID); errors.Is(err, sql.ErrNoRows) {
		return model.Actor{}, ErrForbidden
	} else if err != nil {
		return model.Actor{}, err
	}
	a.Active = true
	if a.Kind == "service" {
		a.Kind = "worker"
	}
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(updated)
	_, _ = s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at<?)`, stamp(now), tokenID, stamp(now.Add(-time.Minute)))
	return a, nil
}

func scanProject(row interface{ Scan(...any) error }) (model.Project, error) {
	var p model.Project
	var archived sql.NullString
	var created, updated string
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedBy, &archived, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.ArchivedAt = nullableTime(archived)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

func (s *Store) Projects(ctx context.Context, includeArchived bool) ([]model.Project, error) {
	q := `SELECT id,name,description,created_by,archived_at,created_at,updated_at FROM projects`
	if !includeArchived {
		q += ` WHERE archived_at IS NULL`
	}
	q += ` ORDER BY name COLLATE NOCASE`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateProject(ctx context.Context, actor model.Actor, name, description string) (model.Project, error) {
	if !actor.IsAdmin() {
		return model.Project{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 120 {
		return model.Project{}, fmt.Errorf("%w: project name is required", ErrInvalid)
	}
	id, err := NewID()
	if err != nil {
		return model.Project{}, err
	}
	now := time.Now().UTC()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO projects(id,name,description,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?)`, id, name, strings.TrimSpace(description), actor.ID, stamp(now), stamp(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return model.Project{}, fmt.Errorf("%w: project exists", ErrConflict)
		}
		return model.Project{}, err
	}
	return model.Project{ID: id, Name: name, Description: description, CreatedBy: actor.ID, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) UpdateProject(ctx context.Context, actor model.Actor, id, name, description string, archived bool) (model.Project, error) {
	if !actor.IsAdmin() {
		return model.Project{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Project{}, ErrInvalid
	}
	now := time.Now().UTC()
	var archive any
	if archived {
		archive = stamp(now)
	}
	r, err := s.DB.ExecContext(ctx, `UPDATE projects SET name=?,description=?,archived_at=?,updated_at=? WHERE id=?`, name, strings.TrimSpace(description), archive, stamp(now), id)
	if err != nil {
		return model.Project{}, err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return model.Project{}, ErrNotFound
	}
	return scanProject(s.DB.QueryRowContext(ctx, `SELECT id,name,description,created_by,archived_at,created_at,updated_at FROM projects WHERE id=?`, id))
}
