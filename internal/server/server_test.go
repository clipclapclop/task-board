package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clipclapclop/task-board/internal/model"
	"github.com/clipclapclop/task-board/internal/store"
)

type fixture struct {
	server *httptest.Server
	store  *store.Store
	admin  model.Actor
	token  string
}

func setup(t *testing.T) fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := st.CreateActor(context.Background(), model.Actor{Username: "chad", DisplayName: "Chad", Kind: "human", Role: "admin", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.CreateToken(context.Background(), admin.ID, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{DefaultActorUsername: "chad"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(func() { ts.Close(); st.Close() })
	return fixture{ts, st, admin, token}
}
func request(t *testing.T, f fixture, method, path string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, f.server.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPortalAndWhoAmI(t *testing.T) {
	f := setup(t)
	res, err := http.Get(f.server.URL + "/tasks")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "Acting as") || !strings.Contains(string(body), "Chad") {
		t.Fatalf("portal %d %s", res.StatusCode, body)
	}
	res = request(t, f, "GET", "/api/v1/whoami", nil, nil)
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("whoami=%d", res.StatusCode)
	}
}

func TestAPICreatePatchAndAuthentication(t *testing.T) {
	f := setup(t)
	res, err := http.Get(f.server.URL + "/api/v1/tasks")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("unauthenticated=%d", res.StatusCode)
	}
	res = request(t, f, "POST", "/api/v1/tasks", map[string]any{"title": "API task", "assigned_to": f.admin.ID}, map[string]string{"Idempotency-Key": "one"})
	var created model.Task
	if err = json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 || created.ID == "" {
		t.Fatalf("create=%d %#v", res.StatusCode, created)
	}
	res, err = http.Get(f.server.URL + "/tasks/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(detail), "API task") || !strings.Contains(string(detail), "History") {
		t.Fatalf("detail=%d %s", res.StatusCode, detail)
	}
	res = request(t, f, "PATCH", "/api/v1/tasks/"+created.ID, map[string]any{"status": "doing"}, nil)
	res.Body.Close()
	if res.StatusCode != 428 {
		t.Fatalf("missing precondition=%d", res.StatusCode)
	}
	res = request(t, f, "PATCH", "/api/v1/tasks/"+created.ID, map[string]any{"status": "doing"}, map[string]string{"If-Match": "1"})
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("patch=%d %s", res.StatusCode, body)
	}
}
