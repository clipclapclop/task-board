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
	server  *httptest.Server
	store   *store.Store
	admin   model.Actor
	project model.Project
	token   string
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
	project, err := st.CreateProject(context.Background(), admin, "Test Project", "")
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
	return fixture{ts, st, admin, project, token}
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

func serviceToken(t *testing.T, f fixture, username string) (model.Actor, string) {
	t.Helper()
	a, err := f.store.CreateActor(context.Background(), model.Actor{Username: username, DisplayName: username, Kind: "service", Role: "member", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.store.CreateToken(context.Background(), a.ID, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, token
}

func responseCode(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Code
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
	res = request(t, f, "POST", "/api/v1/tasks", map[string]any{"title": "API task", "project_id": f.project.ID, "assigned_to": f.admin.ID}, map[string]string{"Idempotency-Key": "one"})
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

func TestProjectsRequiredInAPIAndPortal(t *testing.T) {
	f := setup(t)
	res := request(t, f, "POST", "/api/v1/tasks", map[string]any{"title": "No project", "assigned_to": f.admin.ID}, nil)
	if res.StatusCode != 422 || responseCode(t, res) != "invalid_project" {
		t.Fatalf("missing project status=%d", res.StatusCode)
	}
	res, err := http.Get(f.server.URL + "/tasks/new")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), `select required name="project_id"`) || strings.Contains(string(body), "No project") {
		t.Fatalf("new task project control=%d %s", res.StatusCode, body)
	}
	res = request(t, f, "POST", "/api/v1/tasks", map[string]any{"title": "Has project", "project_id": f.project.ID, "assigned_to": f.admin.ID}, nil)
	var created model.Task
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	res = request(t, f, "PATCH", "/api/v1/tasks/"+created.ID, map[string]any{"project_id": ""}, map[string]string{"If-Match": "1"})
	if res.StatusCode != 422 || responseCode(t, res) != "invalid_project" {
		t.Fatalf("clear project status=%d", res.StatusCode)
	}
}

func TestServiceUsesOnlyReadyCompleteAndCreate(t *testing.T) {
	f := setup(t)
	worker, token := serviceToken(t, f, "worker")
	serviceFixture := f
	serviceFixture.token = token
	created, _, err := f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "Assigned work", ProjectID: f.project.ID, AssignedTo: worker.ID}, "")
	if err != nil {
		t.Fatal(err)
	}

	for _, call := range []struct {
		method  string
		path    string
		body    any
		headers map[string]string
	}{
		{"GET", "/api/v1/tasks", nil, nil},
		{"GET", "/api/v1/tasks/" + created.ID, nil, nil},
		{"PATCH", "/api/v1/tasks/" + created.ID, map[string]any{"status": "doing"}, map[string]string{"If-Match": "1"}},
		{"POST", "/api/v1/tasks/" + created.ID + "/reopen", nil, map[string]string{"If-Match": "1"}},
	} {
		res := request(t, serviceFixture, call.method, call.path, call.body, call.headers)
		if res.StatusCode != 403 || responseCode(t, res) != "service_task_access_forbidden" {
			t.Fatalf("%s %s status=%d", call.method, call.path, res.StatusCode)
		}
	}

	res := request(t, serviceFixture, "POST", "/api/v1/tasks", map[string]any{"title": "Self continuation", "project_id": f.project.ID, "assigned_to": worker.ID, "blocked_by": []string{created.ID}}, map[string]string{"Idempotency-Key": "continuation"})
	if res.StatusCode != 201 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("service create=%d %s", res.StatusCode, body)
	}
	res.Body.Close()

	res = request(t, serviceFixture, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	var delivery model.WorkDelivery
	if err := json.NewDecoder(res.Body).Decode(&delivery); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || delivery.Delivery != "claimed" || delivery.Task.ID != created.ID || delivery.Task.Status != "doing" {
		t.Fatalf("delivery=%d %#v", res.StatusCode, delivery)
	}
	res = request(t, serviceFixture, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	if err := json.NewDecoder(res.Body).Decode(&delivery); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if delivery.Delivery != "resumed" || delivery.Task.ID != created.ID {
		t.Fatalf("redelivery=%#v", delivery)
	}

	res = request(t, serviceFixture, "POST", "/api/v1/work/"+created.ID+"/complete", map[string]any{"status": "done", "result": ""}, nil)
	var completed model.Task
	if err := json.NewDecoder(res.Body).Decode(&completed); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || completed.Status != "done" || completed.Result != "" {
		t.Fatalf("completed=%d %#v", res.StatusCode, completed)
	}
	res = request(t, serviceFixture, "POST", "/api/v1/work/"+created.ID+"/complete", map[string]any{"status": "done", "result": ""}, nil)
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("completion replay=%d %s", res.StatusCode, body)
	}
	res.Body.Close()
	res = request(t, serviceFixture, "POST", "/api/v1/work/"+created.ID+"/complete", map[string]any{"status": "failed", "result": "different"}, nil)
	if res.StatusCode != 409 || responseCode(t, res) != "completion_conflict" {
		t.Fatalf("completion conflict status=%d", res.StatusCode)
	}

	res = request(t, f, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	if res.StatusCode != 403 || responseCode(t, res) != "service_actor_required" {
		t.Fatalf("human ready status=%d", res.StatusCode)
	}
}

func TestReadyCaughtUpIsolationAndDependencyResults(t *testing.T) {
	f := setup(t)
	machineA, tokenA := serviceToken(t, f, "machine-a")
	machineB, tokenB := serviceToken(t, f, "machine-b")
	fixtureA, fixtureB := f, f
	fixtureA.token, fixtureB.token = tokenA, tokenB
	otherProject, err := f.store.CreateProject(context.Background(), f.admin, "Other Project", "")
	if err != nil {
		t.Fatal(err)
	}

	res := request(t, fixtureA, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("caught up=%d", res.StatusCode)
	}
	blocker, _, err := f.store.CreateTask(context.Background(), machineA, store.CreateTaskInput{Title: "Produce output", ProjectID: f.project.ID, AssignedTo: machineB.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	continuation, _, err := f.store.CreateTask(context.Background(), machineA, store.CreateTaskInput{Title: "Use output", ProjectID: f.project.ID, AssignedTo: machineA.ID, BlockedBy: []string{blocker.ID}}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "Wrong project", ProjectID: otherProject.ID, AssignedTo: machineB.ID}, "")
	if err != nil {
		t.Fatal(err)
	}

	res = request(t, fixtureB, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	var delivery model.WorkDelivery
	if err := json.NewDecoder(res.Body).Decode(&delivery); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if delivery.Task.ID != blocker.ID {
		t.Fatalf("B delivery=%#v", delivery)
	}
	res = request(t, fixtureB, "POST", "/api/v1/work/"+blocker.ID+"/complete", map[string]any{"status": "done", "result": "artifact://report"}, nil)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("B complete=%d", res.StatusCode)
	}
	res = request(t, fixtureA, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	if err := json.NewDecoder(res.Body).Decode(&delivery); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if delivery.Task.ID != continuation.ID || len(delivery.DependencyResults) != 1 || delivery.DependencyResults[0].TaskID != blocker.ID || delivery.DependencyResults[0].Result != "artifact://report" {
		t.Fatalf("A delivery=%#v", delivery)
	}
	res = request(t, fixtureB, "POST", "/api/v1/work/ready", map[string]any{"project_id": otherProject.ID}, nil)
	if err := json.NewDecoder(res.Body).Decode(&delivery); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if delivery.Task.ProjectID != otherProject.ID || delivery.Task.AssignedTo != machineB.ID {
		t.Fatalf("isolated delivery=%#v", delivery)
	}
}

func TestDoingTaskFrozenAndCancellationRejectsCompletion(t *testing.T) {
	f := setup(t)
	worker, token := serviceToken(t, f, "worker")
	serviceFixture := f
	serviceFixture.token = token
	taskValue, _, err := f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "Stable instructions", ProjectID: f.project.ID, AssignedTo: worker.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	res := request(t, serviceFixture, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	var delivery model.WorkDelivery
	if err := json.NewDecoder(res.Body).Decode(&delivery); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	res = request(t, f, "PATCH", "/api/v1/tasks/"+taskValue.ID, map[string]any{"title": "changed"}, map[string]string{"If-Match": "2"})
	if res.StatusCode != 409 || responseCode(t, res) != "conflict" {
		t.Fatalf("frozen patch status=%d", res.StatusCode)
	}
	res = request(t, f, "PATCH", "/api/v1/tasks/"+taskValue.ID, map[string]any{"status": "cancelled"}, map[string]string{"If-Match": "2"})
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("cancel status=%d", res.StatusCode)
	}
	res = request(t, serviceFixture, "POST", "/api/v1/work/"+taskValue.ID+"/complete", map[string]any{"status": "done", "result": "late"}, nil)
	if res.StatusCode != 409 || responseCode(t, res) != "completion_conflict" {
		t.Fatalf("late completion status=%d", res.StatusCode)
	}
}

func TestPublishedWorkerContractAndOpenAPI(t *testing.T) {
	f := setup(t)
	res, err := http.Get(f.server.URL + "/docs/worker-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "Worker Interoperability Contract") || !strings.Contains(string(body), "/api/v1/work/ready") {
		t.Fatalf("worker contract=%d %s", res.StatusCode, body)
	}
	res, err = http.Get(f.server.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var document struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.NewDecoder(res.Body).Decode(&document); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/work/ready", "/api/v1/work/{task_id}/complete"} {
		if _, ok := document.Paths[path]; !ok {
			t.Fatalf("OpenAPI missing %s", path)
		}
	}
}
