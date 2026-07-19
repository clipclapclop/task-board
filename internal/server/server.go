package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	projectdocs "github.com/clipclapclop/task-board/docs"
	"github.com/clipclapclop/task-board/internal/model"
	"github.com/clipclapclop/task-board/internal/store"
)

type Config struct {
	PublicURL            string
	AllowedHost          string
	DefaultActorUsername string
	SecureCookies        bool
}

type Server struct {
	Store *store.Store
	Log   *slog.Logger
	Cfg   Config
	mux   *http.ServeMux
	tmpl  *template.Template
}

func New(st *store.Store, logger *slog.Logger, cfg Config) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	t, err := template.New("portal").Funcs(template.FuncMap{
		"eq": func(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) },
		"checked": func(v bool) template.HTMLAttr {
			if v {
				return "checked"
			}
			return ""
		},
		"selected": func(a, b string) template.HTMLAttr {
			if a == b {
				return "selected"
			}
			return ""
		},
		"short": func(v string) string {
			if len(v) > 8 {
				return v[:8]
			}
			return v
		},
		"timefmt": func(t time.Time) string { return t.Local().Format("2006-01-02 15:04") },
		"strings": strings.Fields,
	}).Parse(portalTemplate)
	if err != nil {
		return nil, err
	}
	s := &Server{Store: st, Log: logger, Cfg: cfg, mux: http.NewServeMux(), tmpl: t}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.security(s.logging(s.mux)) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"live"}`)
	})
	s.mux.HandleFunc("GET /health/ready", s.ready)
	s.mux.HandleFunc("GET /api/v1/openapi.json", s.openAPI)
	s.mux.Handle("/api/v1/", s.apiAuth(http.HandlerFunc(s.api)))
	s.mux.HandleFunc("GET /static/style.css", s.style)
	s.mux.HandleFunc("GET /static/htmx.min.js", s.htmx)
	s.mux.HandleFunc("GET /llms.txt", s.projectDoc("llms.txt", "text/plain; charset=utf-8"))
	s.mux.HandleFunc("GET /docs/agents.md", s.projectDoc("agents.md", "text/markdown; charset=utf-8"))
	s.mux.HandleFunc("GET /docs/api.md", s.projectDoc("api.md", "text/markdown; charset=utf-8"))
	s.mux.HandleFunc("GET /docs/worker-contract.md", s.projectDoc("worker-contract.md", "text/markdown; charset=utf-8"))
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/tasks", http.StatusSeeOther) })
	s.mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/tasks", http.StatusSeeOther) })
	s.mux.HandleFunc("POST /logout", s.webPost(s.logout))
	s.mux.HandleFunc("POST /switch-actor", s.webPost(s.switchActor))
	s.mux.HandleFunc("GET /tasks", s.tasksPage)
	s.mux.HandleFunc("GET /tasks/new", s.taskNewPage)
	s.mux.HandleFunc("POST /tasks/new", s.webPost(s.taskCreateWeb))
	s.mux.HandleFunc("GET /tasks/{id}", s.taskDetailPage)
	s.mux.HandleFunc("POST /tasks/{id}", s.webPost(s.taskUpdateWeb))
	s.mux.HandleFunc("GET /profile", s.profilePage)
	s.mux.HandleFunc("POST /profile", s.webPost(s.profilePost))
	s.mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/projects", http.StatusSeeOther)
	})
	s.mux.HandleFunc("GET /admin/users", s.usersPage)
	s.mux.HandleFunc("POST /admin/users", s.webPost(s.userCreateWeb))
	s.mux.HandleFunc("GET /admin/users/{id}", s.userDetailPage)
	s.mux.HandleFunc("POST /admin/users/{id}", s.webPost(s.userUpdateWeb))
	s.mux.HandleFunc("GET /admin/projects", s.projectsPage)
	s.mux.HandleFunc("POST /admin/projects", s.webPost(s.projectPost))
	s.mux.HandleFunc("POST /admin/projects/{id}", s.webPost(s.projectUpdatePost))
	s.mux.HandleFunc("GET /admin/export", s.exportWeb)
}

func (s *Server) projectDoc(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := projectdocs.FS.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public,max-age=300")
		_, _ = w.Write(body)
	}
}

type contextKey string

const actorKey contextKey = "actor"

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.AllowedHost != "" && r.Host != s.Cfg.AllowedHost && !strings.HasPrefix(r.Host, "localhost:") && !strings.HasPrefix(r.Host, "127.0.0.1:") {
			http.Error(w, "invalid host", http.StatusMisdirectedRequest)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(v int) { r.status = v; r.ResponseWriter.WriteHeader(v) }
func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if v := recover(); v != nil {
				s.Log.Error("panic", "error", v)
				http.Error(rr, "internal error", 500)
			}
			s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rr.status, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rr, r)
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ready(ctx); err != nil {
		problem(w, 503, "not_ready", "Not ready", err.Error(), nil)
		return
	}
	jsonResponse(w, 200, map[string]string{"status": "ready"})
}

func (s *Server) apiAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		scheme, secret, ok := strings.Cut(header, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || secret == "" {
			problem(w, 401, "authentication_required", "Authentication required", "Provide a bearer token.", nil)
			return
		}
		a, err := s.Store.AuthenticateToken(r.Context(), secret)
		if err != nil {
			problem(w, 401, "invalid_token", "Invalid token", "The bearer token is invalid, expired, revoked, or belongs to a disabled actor.", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey, a)))
	})
}

func actorFrom(ctx context.Context) model.Actor { a, _ := ctx.Value(actorKey).(model.Actor); return a }
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, code, title, detail string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "https://task-board.oorangy.com/problems/" + code, "title": title, "status": status, "detail": detail, "code": code, "fields": fields})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		problem(w, 400, "invalid_json", "Invalid JSON", err.Error(), nil)
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		problem(w, 400, "invalid_json", "Invalid JSON", "Request must contain one JSON value.", nil)
		return false
	}
	return true
}

func statusFor(err error) (int, string) {
	switch {
	case errors.Is(err, store.ErrWorkerRequired):
		return 403, "unsupported_actor_kind"
	case errors.Is(err, store.ErrInvalidCount):
		return 422, "invalid_count"
	case errors.Is(err, store.ErrWorkNotOwned):
		return 409, "work_not_owned"
	case errors.Is(err, store.ErrCompletionConflict):
		return 409, "completion_conflict"
	case errors.Is(err, store.ErrQueueSequenceConflict):
		return 409, "queue_sequence_conflict"
	case errors.Is(err, store.ErrInvalidProject):
		return 422, "invalid_project"
	case errors.Is(err, store.ErrNotFound):
		return 404, "not_found"
	case errors.Is(err, store.ErrForbidden):
		return 403, "forbidden"
	case errors.Is(err, store.ErrPrecondition):
		return 412, "version_conflict"
	case errors.Is(err, store.ErrBlocked):
		return 409, "task_blocked"
	case errors.Is(err, store.ErrConflict):
		return 409, "conflict"
	case errors.Is(err, store.ErrInvalid):
		return 422, "validation_failed"
	default:
		return 500, "internal_error"
	}
}
func storeProblem(w http.ResponseWriter, err error) {
	status, code := statusFor(err)
	detail := err.Error()
	if status == 500 {
		detail = "An internal error occurred."
	}
	problem(w, status, code, http.StatusText(status), detail, nil)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	a := actorFrom(r.Context())
	taskID := func() string {
		v := strings.TrimPrefix(p, "/api/v1/tasks/")
		v = strings.TrimSuffix(v, "/reopen")
		return strings.Trim(v, "/")
	}
	workTaskID := func() string {
		v := strings.TrimPrefix(p, "/api/v1/work/")
		v = strings.TrimSuffix(v, "/complete")
		return strings.Trim(v, "/")
	}
	switch {
	case r.Method == "GET" && p == "/api/v1/whoami":
		jsonResponse(w, 200, a)
	case r.Method == "GET" && p == "/api/v1/actors":
		actors, err := s.Store.Actors(r.Context(), false)
		if err != nil {
			storeProblem(w, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"data": actors})
	case r.Method == "GET" && p == "/api/v1/projects":
		projects, err := s.Store.Projects(r.Context(), r.URL.Query().Get("archived") == "true")
		if err != nil {
			storeProblem(w, err)
			return
		}
		jsonResponse(w, 200, map[string]any{"data": projects})
	case r.Method == "POST" && p == "/api/v1/work/ready":
		var in struct {
			ProjectID string          `json:"project_id"`
			Count     json.RawMessage `json:"count"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		count := 1
		if len(in.Count) > 0 {
			if strings.TrimSpace(string(in.Count)) == "null" {
				problem(w, 422, "invalid_count", "Invalid count", "count must be an integer from 1 through 32.", nil)
				return
			}
			if err := json.Unmarshal(in.Count, &count); err != nil {
				problem(w, 422, "invalid_count", "Invalid count", "count must be an integer from 1 through 32.", nil)
				return
			}
		}
		delivery, found, err := s.Store.ReadyWork(r.Context(), a, in.ProjectID, count)
		if err != nil {
			storeProblem(w, err)
			return
		}
		if !found {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		jsonResponse(w, 200, delivery)
	case r.Method == "POST" && strings.HasPrefix(p, "/api/v1/work/") && strings.HasSuffix(p, "/complete"):
		var in struct {
			Status string `json:"status"`
			Result string `json:"result"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		t, err := s.Store.CompleteWork(r.Context(), a, workTaskID(), in.Status, in.Result)
		if err != nil {
			storeProblem(w, err)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, t.Version))
		jsonResponse(w, 200, t)
	case r.Method == "GET" && p == "/api/v1/tasks":
		if a.IsWorker() {
			problem(w, 403, "worker_task_access_forbidden", "Forbidden", "Workers receive tasks only through /api/v1/work/ready.", nil)
			return
		}
		s.apiTasks(w, r)
	case r.Method == "POST" && p == "/api/v1/tasks":
		var in store.CreateTaskInput
		if !decodeJSON(w, r, &in) {
			return
		}
		t, replayed, err := s.Store.CreateTask(r.Context(), a, in, r.Header.Get("Idempotency-Key"))
		if err != nil {
			storeProblem(w, err)
			return
		}
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, t.Version))
		jsonResponse(w, 201, t)
	case r.Method == "GET" && strings.HasPrefix(p, "/api/v1/tasks/") && !strings.HasSuffix(p, "/reopen"):
		if a.IsWorker() {
			problem(w, 403, "worker_task_access_forbidden", "Forbidden", "Workers cannot browse task details.", nil)
			return
		}
		id := taskID()
		t, err := s.Store.Task(r.Context(), id)
		if err != nil {
			storeProblem(w, err)
			return
		}
		events, err := s.Store.TaskEvents(r.Context(), id)
		if err != nil {
			storeProblem(w, err)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, t.Version))
		jsonResponse(w, 200, map[string]any{"task": t, "events": events})
	case r.Method == "PATCH" && strings.HasPrefix(p, "/api/v1/tasks/") && !strings.HasSuffix(p, "/reopen"):
		if a.IsWorker() {
			problem(w, 403, "worker_task_access_forbidden", "Forbidden", "Workers complete owned work through /api/v1/work/{task_id}/complete.", nil)
			return
		}
		id := taskID()
		if r.Header.Get("If-Match") == "" {
			problem(w, 428, "precondition_required", "Precondition required", "Supply the task version in If-Match.", nil)
			return
		}
		version, err := store.ParseIfMatch(r.Header.Get("If-Match"))
		if err != nil {
			storeProblem(w, err)
			return
		}
		var in store.PatchTaskInput
		if !decodeJSON(w, r, &in) {
			return
		}
		t, err := s.Store.PatchTask(r.Context(), a, id, version, in)
		if err != nil {
			storeProblem(w, err)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, t.Version))
		jsonResponse(w, 200, t)
	case r.Method == "POST" && strings.HasSuffix(p, "/reopen"):
		if a.IsWorker() {
			problem(w, 403, "worker_task_access_forbidden", "Forbidden", "Workers cannot reopen tasks.", nil)
			return
		}
		id := taskID()
		if r.Header.Get("If-Match") == "" {
			problem(w, 428, "precondition_required", "Precondition required", "Supply If-Match.", nil)
			return
		}
		version, err := store.ParseIfMatch(r.Header.Get("If-Match"))
		if err != nil {
			storeProblem(w, err)
			return
		}
		t, err := s.Store.ReopenTask(r.Context(), a, id, version)
		if err != nil {
			storeProblem(w, err)
			return
		}
		jsonResponse(w, 200, t)
	case r.Method == "GET" && p == "/api/v1/export":
		if !a.IsAdmin() {
			storeProblem(w, store.ErrForbidden)
			return
		}
		v, err := s.Store.Export(r.Context())
		if err != nil {
			storeProblem(w, err)
			return
		}
		w.Header().Set("Content-Disposition", "attachment; filename=task-board-export.json")
		jsonResponse(w, 200, v)
	default:
		problem(w, 404, "not_found", "Not found", "The requested API route does not exist.", nil)
	}
}

func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := model.TaskFilter{Statuses: q["status"], AssignedTo: q.Get("assigned_to"), CreatedBy: q.Get("created_by"), ProjectID: q.Get("project"), Query: q.Get("q"), Cursor: q.Get("cursor")}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("updated_after"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			storeProblem(w, store.ErrInvalid)
			return
		}
		f.UpdatedAfter = &t
	}
	if v := q.Get("actionable"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			storeProblem(w, store.ErrInvalid)
			return
		}
		f.Actionable = &b
	}
	page, err := s.Store.Tasks(r.Context(), f)
	if err != nil {
		storeProblem(w, err)
		return
	}
	jsonResponse(w, 200, page)
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Server) csrf(r *http.Request, w http.ResponseWriter) string {
	if c, err := r.Cookie("task_board_csrf"); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	v := randomString(24)
	http.SetCookie(w, &http.Cookie{Name: "task_board_csrf", Value: v, Path: "/", Secure: s.Cfg.SecureCookies, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400 * 30})
	return v
}
func (s *Server) webPost(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("task_board_csrf")
		if err != nil || r.FormValue("csrf_token") == "" || r.FormValue("csrf_token") != cookie.Value {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		next(w, r)
	}
}

func (s *Server) currentActor(ctx context.Context, r *http.Request) (model.Actor, error) {
	name := s.Cfg.DefaultActorUsername
	if c, err := r.Cookie("task_board_actor"); err == nil && c.Value != "" {
		name = c.Value
	}
	a, err := s.Store.ActorByName(ctx, name)
	if err == nil && a.Active && a.Kind == "human" {
		return a, nil
	}
	actors, err := s.Store.Actors(ctx, false)
	if err != nil {
		return model.Actor{}, err
	}
	for _, candidate := range actors {
		if candidate.Kind == "human" {
			return candidate, nil
		}
	}
	return model.Actor{}, store.ErrNotFound
}

type pageData struct {
	Page, Title, CSRF, View, Error, Notice, NewToken string
	NextURL                                          string
	Current                                          model.Actor
	Actors                                           []model.Actor
	Projects                                         []model.Project
	Tasks                                            []model.Task
	Task                                             model.Task
	Events                                           []model.TaskEvent
	Tokens                                           []model.APIToken
	User                                             model.Actor
	Filter                                           url.Values
}

func (s *Server) baseData(w http.ResponseWriter, r *http.Request, page, title string) (pageData, error) {
	a, err := s.currentActor(r.Context(), r)
	if err != nil {
		return pageData{}, err
	}
	actors, err := s.Store.Actors(r.Context(), a.IsAdmin())
	if err != nil {
		return pageData{}, err
	}
	projects, err := s.Store.Projects(r.Context(), a.IsAdmin())
	if err != nil {
		return pageData{}, err
	}
	return pageData{Page: page, Title: title, CSRF: s.csrf(r, w), Current: a, Actors: actors, Projects: projects, Filter: r.URL.Query()}, nil
}
func (s *Server) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "portal", data); err != nil {
		s.Log.Error("render", "error", err)
	}
}
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, err error) {
	data, e := s.baseData(w, r, "error", http.StatusText(status))
	if e != nil {
		http.Error(w, err.Error(), status)
		return
	}
	data.Error = err.Error()
	w.WriteHeader(status)
	s.render(w, data)
}

func (s *Server) switchActor(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.ActorByName(r.Context(), r.FormValue("actor"))
	if err != nil || !a.Active || a.Kind != "human" {
		s.renderError(w, r, 422, fmt.Errorf("invalid human actor"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "task_board_actor", Value: a.Username, Path: "/", Secure: s.Cfg.SecureCookies, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 365})
	target := r.FormValue("next")
	if !strings.HasPrefix(target, "/") {
		target = "/tasks"
	}
	http.Redirect(w, r, target, 303)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "task_board_actor", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/tasks", 303)
}

func (s *Server) tasksPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.baseData(w, r, "tasks", "Tasks")
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "mine"
	}
	data.View = view
	f := model.TaskFilter{Statuses: formIDs(r.URL.Query()["status"]), ProjectID: r.URL.Query().Get("project"), Query: r.URL.Query().Get("q"), AssignedTo: r.URL.Query().Get("assigned_to"), CreatedBy: r.URL.Query().Get("created_by"), Cursor: r.URL.Query().Get("cursor"), Limit: 50}
	if view == "mine" {
		if f.AssignedTo == "" {
			f.AssignedTo = data.Current.ID
		}
	} else if view == "created" {
		if f.CreatedBy == "" {
			f.CreatedBy = data.Current.ID
		}
	}
	if value := r.URL.Query().Get("actionable"); value != "" {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			s.renderError(w, r, 422, store.ErrInvalid)
			return
		}
		f.Actionable = &parsed
	}
	if value := r.URL.Query().Get("updated_after"); value != "" {
		parsed, parseErr := time.Parse(time.RFC3339, value)
		if parseErr != nil {
			s.renderError(w, r, 422, fmt.Errorf("%w: updated_after must be RFC 3339", store.ErrInvalid))
			return
		}
		f.UpdatedAfter = &parsed
	}
	page, err := s.Store.Tasks(r.Context(), f)
	if err != nil {
		s.renderError(w, r, 422, err)
		return
	}
	data.Tasks = page.Data
	if page.NextCursor != "" {
		query := r.URL.Query()
		query.Set("cursor", page.NextCursor)
		data.NextURL = "/tasks?" + query.Encode()
	}
	s.render(w, data)
}
func (s *Server) taskNewPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.baseData(w, r, "task-new", "New task")
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	page, err := s.Store.Tasks(r.Context(), model.TaskFilter{Limit: 200})
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	data.Tasks = page.Data
	s.render(w, data)
}
func formIDs(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
func (s *Server) taskCreateWeb(w http.ResponseWriter, r *http.Request) {
	a, err := s.currentActor(r.Context(), r)
	if err != nil {
		s.renderError(w, r, 403, err)
		return
	}
	in := store.CreateTaskInput{Title: r.FormValue("title"), Description: r.FormValue("description"), ProjectID: r.FormValue("project_id"), AssignedTo: r.FormValue("assigned_to"), BlockedBy: formIDs(r.Form["blocked_by"])}
	t, _, err := s.Store.CreateTask(r.Context(), a, in, "")
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	http.Redirect(w, r, "/tasks/"+t.ID, 303)
}
func statusForWeb(err error) int { n, _ := statusFor(err); return n }
func (s *Server) taskDetailPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.baseData(w, r, "task-detail", "Task")
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	data.Task, err = s.Store.Task(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	data.Events, err = s.Store.TaskEvents(r.Context(), data.Task.ID)
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	page, err := s.Store.Tasks(r.Context(), model.TaskFilter{Limit: 200})
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	data.Tasks = page.Data
	s.render(w, data)
}
func (s *Server) taskUpdateWeb(w http.ResponseWriter, r *http.Request) {
	a, err := s.currentActor(r.Context(), r)
	if err != nil {
		s.renderError(w, r, 403, err)
		return
	}
	version, _ := strconv.ParseInt(r.FormValue("version"), 10, 64)
	id := r.PathValue("id")
	action := r.FormValue("action")
	var t model.Task
	switch action {
	case "edit":
		title, desc, project, assigned := r.FormValue("title"), r.FormValue("description"), r.FormValue("project_id"), r.FormValue("assigned_to")
		deps := formIDs(r.Form["blocked_by"])
		t, err = s.Store.PatchTask(r.Context(), a, id, version, store.PatchTaskInput{Title: &title, Description: &desc, ProjectID: &project, AssignedTo: &assigned, BlockedBy: &deps})
	case "work":
		status, result := r.FormValue("status"), r.FormValue("result")
		t, err = s.Store.PatchTask(r.Context(), a, id, version, store.PatchTaskInput{Status: &status, Result: &result})
	case "cancel":
		status := "cancelled"
		t, err = s.Store.PatchTask(r.Context(), a, id, version, store.PatchTaskInput{Status: &status})
	case "reopen":
		t, err = s.Store.ReopenTask(r.Context(), a, id, version)
	default:
		err = store.ErrInvalid
	}
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	http.Redirect(w, r, "/tasks/"+t.ID, 303)
}

func (s *Server) profilePage(w http.ResponseWriter, r *http.Request) {
	data, err := s.baseData(w, r, "profile", "Profile")
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	data.User = data.Current
	data.Tokens, err = s.Store.Tokens(r.Context(), data.Current.ID)
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	s.render(w, data)
}
func (s *Server) profilePost(w http.ResponseWriter, r *http.Request) {
	a, err := s.currentActor(r.Context(), r)
	if err != nil {
		s.renderError(w, r, 403, err)
		return
	}
	if r.FormValue("action") == "revoke-token" {
		err = s.Store.RevokeToken(r.Context(), r.FormValue("token_id"))
		if err == nil {
			http.Redirect(w, r, "/profile", 303)
			return
		}
	} else {
		_, secret, e := s.Store.CreateToken(r.Context(), a.ID, r.FormValue("name"), nil)
		err = e
		if err == nil {
			data, _ := s.baseData(w, r, "profile", "Profile")
			data.User = a
			data.NewToken = secret
			data.Tokens, _ = s.Store.Tokens(r.Context(), a.ID)
			s.render(w, data)
			return
		}
	}
	s.renderError(w, r, statusForWeb(err), err)
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (model.Actor, bool) {
	a, err := s.currentActor(r.Context(), r)
	if err != nil || !a.IsAdmin() {
		s.renderError(w, r, 403, store.ErrForbidden)
		return model.Actor{}, false
	}
	return a, true
}
func (s *Server) usersPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	data, err := s.baseData(w, r, "users", "Users")
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	data.Actors, err = s.Store.Actors(r.Context(), true)
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	s.render(w, data)
}
func (s *Server) userCreateWeb(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	_, err := s.Store.CreateActor(r.Context(), model.Actor{Username: r.FormValue("username"), DisplayName: r.FormValue("display_name"), Kind: r.FormValue("kind"), Role: r.FormValue("role"), Active: true})
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	http.Redirect(w, r, "/admin/users", 303)
}
func (s *Server) userDetailPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	data, err := s.baseData(w, r, "user-detail", "User")
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	data.User, err = s.Store.Actor(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	data.Tokens, err = s.Store.Tokens(r.Context(), data.User.ID)
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	s.render(w, data)
}
func (s *Server) userUpdateWeb(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	id := r.PathValue("id")
	action := r.FormValue("action")
	var err error
	var secret string
	switch action {
	case "update":
		_, err = s.Store.UpdateActor(r.Context(), id, r.FormValue("display_name"), r.FormValue("role"), r.FormValue("active") == "on")
	case "create-token":
		_, secret, err = s.Store.CreateToken(r.Context(), id, r.FormValue("name"), nil)
	case "revoke-token":
		err = s.Store.RevokeToken(r.Context(), r.FormValue("token_id"))
	default:
		err = store.ErrInvalid
	}
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	if secret != "" {
		data, _ := s.baseData(w, r, "user-detail", "User")
		data.User, _ = s.Store.Actor(r.Context(), id)
		data.Tokens, _ = s.Store.Tokens(r.Context(), id)
		data.NewToken = secret
		s.render(w, data)
		return
	}
	http.Redirect(w, r, "/admin/users/"+id, 303)
}

func (s *Server) projectsPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	data, err := s.baseData(w, r, "projects", "Projects")
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	data.Projects, err = s.Store.Projects(r.Context(), true)
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	s.render(w, data)
}
func (s *Server) projectPost(w http.ResponseWriter, r *http.Request) {
	a, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	_, err := s.Store.CreateProject(r.Context(), a, r.FormValue("name"), r.FormValue("description"))
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	http.Redirect(w, r, "/admin/projects", 303)
}
func (s *Server) projectUpdatePost(w http.ResponseWriter, r *http.Request) {
	a, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	_, err := s.Store.UpdateProject(r.Context(), a, r.PathValue("id"), r.FormValue("name"), r.FormValue("description"), r.FormValue("archived") == "on")
	if err != nil {
		s.renderError(w, r, statusForWeb(err), err)
		return
	}
	http.Redirect(w, r, "/admin/projects", 303)
}
func (s *Server) exportWeb(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	v, err := s.Store.Export(r.Context())
	if err != nil {
		s.renderError(w, r, 500, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=task-board-export.json")
	_ = json.NewEncoder(w).Encode(v)
}
