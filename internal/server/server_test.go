package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
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

func workerToken(t *testing.T, f fixture, username string) (model.Actor, string) {
	t.Helper()
	a, err := f.store.CreateActor(context.Background(), model.Actor{Username: username, DisplayName: username, Kind: "worker", Role: "member", Active: true})
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

var creationIDPattern = regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`)

func portalTaskForm(t *testing.T, f fixture, client *http.Client) (string, string) {
	t.Helper()
	res, err := client.Get(f.server.URL + "/tasks/new")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	match := creationIDPattern.FindSubmatch(body)
	if res.StatusCode != 200 || len(match) != 2 {
		t.Fatalf("new task form=%d %s", res.StatusCode, body)
	}
	var csrf string
	for _, cookie := range client.Jar.Cookies(res.Request.URL) {
		if cookie.Name == "task_board_csrf" {
			csrf = cookie.Value
		}
	}
	if csrf == "" {
		t.Fatal("missing CSRF cookie")
	}
	return csrf, string(match[1])
}

func postPortalTask(t *testing.T, f fixture, client *http.Client, values url.Values) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", f.server.URL+"/tasks/new", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPortalAndWhoAmI(t *testing.T) {
	f := setup(t)
	worker, _ := workerToken(t, f, "portal-worker")
	res, err := http.Get(f.server.URL + "/tasks")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "Acting as") || !strings.Contains(string(body), "Chad") || !strings.Contains(string(body), `href="/docs/worker-contract.md">Worker contract</a>`) {
		t.Fatalf("portal %d %s", res.StatusCode, body)
	}
	res, err = http.Get(f.server.URL + "/admin/users")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), worker.DisplayName) || !strings.Contains(string(body), "worker") || strings.Contains(string(body), `option value="service"`) {
		t.Fatalf("worker portal label=%d %s", res.StatusCode, body)
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
	if res.StatusCode != 201 || created.ID == "" || created.QueueSequence < 1 {
		t.Fatalf("create=%d %#v", res.StatusCode, created)
	}
	res = request(t, f, "GET", "/api/v1/tasks", nil, nil)
	var page model.TaskPage
	if err := json.NewDecoder(res.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || len(page.Data) != 1 || page.Data[0].ID != created.ID {
		t.Fatalf("human task visibility=%d %#v", res.StatusCode, page)
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
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("patch=%d %s", res.StatusCode, body)
	}
	res.Body.Close()
	res = request(t, f, "PATCH", "/api/v1/tasks/"+created.ID, map[string]any{"queue_sequence": created.QueueSequence}, map[string]string{"If-Match": "2"})
	if res.StatusCode != 409 || responseCode(t, res) != "queue_sequence_conflict" {
		t.Fatalf("queue sequence patch=%d", res.StatusCode)
	}
}

func TestAPITaskCreationIDs(t *testing.T) {
	f := setup(t)
	body := map[string]any{"title": "Created once", "project_id": f.project.ID, "assigned_to": f.admin.ID}
	for _, headers := range []map[string]string{nil, {"Idempotency-Key": "   "}} {
		res := request(t, f, "POST", "/api/v1/tasks", body, headers)
		if res.StatusCode != 400 || responseCode(t, res) != "missing_idempotency_key" {
			t.Fatalf("missing creation ID status=%d", res.StatusCode)
		}
	}

	res := request(t, f, "POST", "/api/v1/tasks", map[string]any{"title": "Correctable", "assigned_to": f.admin.ID}, map[string]string{"Idempotency-Key": "correctable"})
	if res.StatusCode != 422 || responseCode(t, res) != "invalid_project" {
		t.Fatalf("invalid creation status=%d", res.StatusCode)
	}
	res = request(t, f, "POST", "/api/v1/tasks", body, map[string]string{"Idempotency-Key": "correctable"})
	if res.StatusCode != 201 || res.Header.Get("Idempotent-Replayed") != "" {
		response, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("corrected creation=%d replay=%q %s", res.StatusCode, res.Header.Get("Idempotent-Replayed"), response)
	}
	var created model.Task
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res = request(t, f, "POST", "/api/v1/tasks", body, map[string]string{"Idempotency-Key": "correctable"})
	var replayed model.Task
	if err := json.NewDecoder(res.Body).Decode(&replayed); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Idempotent-Replayed") != "true" || replayed.ID != created.ID {
		t.Fatalf("replay=%d header=%q task=%#v", res.StatusCode, res.Header.Get("Idempotent-Replayed"), replayed)
	}
	res = request(t, f, "POST", "/api/v1/tasks", body, map[string]string{"Idempotency-Key": "same-fields-new-task"})
	var sameFields model.Task
	if err := json.NewDecoder(res.Body).Decode(&sameFields); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 || sameFields.ID == created.ID {
		t.Fatalf("distinct creation ID=%d task=%#v", res.StatusCode, sameFields)
	}

	changed := map[string]any{"title": "Changed", "project_id": f.project.ID, "assigned_to": f.admin.ID}
	res = request(t, f, "POST", "/api/v1/tasks", changed, map[string]string{"Idempotency-Key": "correctable"})
	if res.StatusCode != 409 || responseCode(t, res) != "idempotency_key_conflict" {
		t.Fatalf("creation conflict status=%d", res.StatusCode)
	}
	res = request(t, f, "POST", "/api/v1/tasks", changed, map[string]string{"Idempotency-Key": "changed-task"})
	if res.StatusCode != 201 {
		t.Fatalf("new creation ID status=%d", res.StatusCode)
	}
	res.Body.Close()

	worker, token := workerToken(t, f, "creation-worker")
	workerFixture := f
	workerFixture.token = token
	workerBody := map[string]any{"title": "Created once", "project_id": f.project.ID, "assigned_to": worker.ID}
	res = request(t, workerFixture, "POST", "/api/v1/tasks", workerBody, map[string]string{"Idempotency-Key": "correctable"})
	if res.StatusCode != 201 {
		t.Fatalf("actor-scoped creation ID status=%d", res.StatusCode)
	}
	res.Body.Close()
}

func TestPortalTaskCreationIDs(t *testing.T) {
	f := setup(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	csrf, creationID := portalTaskForm(t, f, client)
	_, otherCreationID := portalTaskForm(t, f, client)
	if creationID == otherCreationID {
		t.Fatalf("separate forms reused creation ID %q", creationID)
	}

	values := url.Values{"csrf_token": {csrf}, "idempotency_key": {creationID}, "title": {"Portal task"}, "project_id": {f.project.ID}, "assigned_to": {f.admin.ID}}
	res := postPortalTask(t, f, client, values)
	res.Body.Close()
	location := res.Header.Get("Location")
	if res.StatusCode != 303 || !strings.HasPrefix(location, "/tasks/") {
		t.Fatalf("portal create=%d location=%q", res.StatusCode, location)
	}
	res = postPortalTask(t, f, client, values)
	res.Body.Close()
	if res.StatusCode != 303 || res.Header.Get("Location") != location {
		t.Fatalf("portal replay=%d location=%q", res.StatusCode, res.Header.Get("Location"))
	}

	changed := url.Values{"csrf_token": {csrf}, "idempotency_key": {creationID}, "title": {"Changed portal task"}, "project_id": {f.project.ID}, "assigned_to": {f.admin.ID}}
	res = postPortalTask(t, f, client, changed)
	conflictBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 409 || !strings.Contains(string(conflictBody), "fresh New Task form") {
		t.Fatalf("portal conflict=%d %s", res.StatusCode, conflictBody)
	}

	for _, invalidID := range []string{"", "not-a-uuid"} {
		invalid := url.Values{"csrf_token": {csrf}, "idempotency_key": {invalidID}, "title": {"Invalid form"}, "project_id": {f.project.ID}, "assigned_to": {f.admin.ID}}
		res = postPortalTask(t, f, client, invalid)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("invalid portal creation ID %q status=%d", invalidID, res.StatusCode)
		}
	}

	csrf, correctableID := portalTaskForm(t, f, client)
	correctable := url.Values{"csrf_token": {csrf}, "idempotency_key": {correctableID}, "title": {"Corrected portal task"}, "assigned_to": {f.admin.ID}}
	res = postPortalTask(t, f, client, correctable)
	res.Body.Close()
	if res.StatusCode != 422 {
		t.Fatalf("portal validation status=%d", res.StatusCode)
	}
	correctable.Set("project_id", f.project.ID)
	res = postPortalTask(t, f, client, correctable)
	res.Body.Close()
	if res.StatusCode != 303 {
		t.Fatalf("corrected portal creation=%d", res.StatusCode)
	}

	page, err := f.store.Tasks(context.Background(), model.TaskFilter{Limit: 20})
	if err != nil || len(page.Data) != 2 {
		t.Fatalf("portal tasks=%#v err=%v", page.Data, err)
	}
}

func TestProjectsRequiredInAPIAndPortal(t *testing.T) {
	f := setup(t)
	res := request(t, f, "POST", "/api/v1/tasks", map[string]any{"title": "No project", "assigned_to": f.admin.ID}, map[string]string{"Idempotency-Key": "no-project"})
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
	res = request(t, f, "POST", "/api/v1/tasks", map[string]any{"title": "Has project", "project_id": f.project.ID, "assigned_to": f.admin.ID}, map[string]string{"Idempotency-Key": "has-project"})
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

func TestWorkerUsesOnlyReadyCompleteAndCreate(t *testing.T) {
	f := setup(t)
	worker, token := workerToken(t, f, "worker")
	workerFixture := f
	workerFixture.token = token
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
		res := request(t, workerFixture, call.method, call.path, call.body, call.headers)
		if res.StatusCode != 403 || responseCode(t, res) != "worker_task_access_forbidden" {
			t.Fatalf("%s %s status=%d", call.method, call.path, res.StatusCode)
		}
	}

	res := request(t, workerFixture, "POST", "/api/v1/tasks", map[string]any{"title": "Self continuation", "project_id": f.project.ID, "assigned_to": worker.ID, "blocked_by": []string{created.ID}}, map[string]string{"Idempotency-Key": "continuation"})
	if res.StatusCode != 201 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("worker create=%d %s", res.StatusCode, body)
	}
	res.Body.Close()

	res = request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	var ready model.ReadyResponse
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || ready.Count != 1 || ready.ProjectID != f.project.ID || len(ready.Deliveries) != 1 || ready.Deliveries[0].Delivery != "claimed" || ready.Deliveries[0].Task.ID != created.ID || ready.Deliveries[0].Task.Status != "doing" {
		t.Fatalf("delivery=%d %#v", res.StatusCode, ready)
	}
	res = request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(ready.Deliveries) != 1 || ready.Deliveries[0].Delivery != "resumed" || ready.Deliveries[0].Task.ID != created.ID {
		t.Fatalf("redelivery=%#v", ready)
	}

	res = request(t, workerFixture, "POST", "/api/v1/work/"+created.ID+"/complete", map[string]any{"status": "done", "result": ""}, nil)
	var completed model.Task
	if err := json.NewDecoder(res.Body).Decode(&completed); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || completed.Status != "done" || completed.Result != "" {
		t.Fatalf("completed=%d %#v", res.StatusCode, completed)
	}
	res = request(t, workerFixture, "POST", "/api/v1/work/"+created.ID+"/complete", map[string]any{"status": "done", "result": ""}, nil)
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("completion replay=%d %s", res.StatusCode, body)
	}
	res.Body.Close()
	res = request(t, workerFixture, "POST", "/api/v1/work/"+created.ID+"/complete", map[string]any{"status": "failed", "result": "different"}, nil)
	if res.StatusCode != 409 || responseCode(t, res) != "completion_conflict" {
		t.Fatalf("completion conflict status=%d", res.StatusCode)
	}

	res = request(t, f, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	if res.StatusCode != 403 || responseCode(t, res) != "unsupported_actor_kind" {
		t.Fatalf("human ready status=%d", res.StatusCode)
	}
}

func TestReadyHTTPUsesStatusFirstCountWindows(t *testing.T) {
	f := setup(t)
	worker, token := workerToken(t, f, "window-worker")
	workerFixture := f
	workerFixture.token = token
	otherProject, err := f.store.CreateProject(context.Background(), f.admin, "Other Project", "")
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "First", ProjectID: otherProject.ID, AssignedTo: worker.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "Second", ProjectID: f.project.ID, AssignedTo: worker.ID}, "")
	if err != nil {
		t.Fatal(err)
	}

	res := request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"count": 2}, nil)
	var ready model.ReadyResponse
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || ready.ProjectID != "" || ready.Count != 2 || len(ready.Deliveries) != 2 {
		t.Fatalf("ready window=%d %#v", res.StatusCode, ready)
	}
	if ready.Deliveries[0].Task.ID != first.ID || ready.Deliveries[1].Task.ID != second.ID || ready.Deliveries[0].Delivery != "claimed" || ready.Deliveries[1].Delivery != "claimed" {
		t.Fatalf("claim order=%#v", ready.Deliveries)
	}
	if ready.Deliveries[0].Task.QueueSequence >= ready.Deliveries[1].Task.QueueSequence {
		t.Fatalf("queue sequence order=%#v", ready.Deliveries)
	}
	third, _, err := f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "Third", ProjectID: f.project.ID, AssignedTo: worker.ID}, "")
	if err != nil {
		t.Fatal(err)
	}

	res = request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"count": 1}, nil)
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(ready.Deliveries) != 1 || ready.Deliveries[0].Delivery != "resumed" || ready.Deliveries[0].Task.ID != first.ID {
		t.Fatalf("active prefix=%#v", ready)
	}
	if got, err := f.store.Task(context.Background(), third.ID); err != nil || got.Status != "todo" {
		t.Fatalf("smaller window claimed hidden work=%#v err=%v", got, err)
	}
	res = request(t, workerFixture, "POST", "/api/v1/work/"+second.ID+"/complete", map[string]any{"status": "done", "result": "completed out of order"}, nil)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("out-of-order completion=%d", res.StatusCode)
	}
	res = request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"count": 2}, nil)
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(ready.Deliveries) != 2 || ready.Deliveries[0].Task.ID != first.ID || ready.Deliveries[0].Delivery != "resumed" || ready.Deliveries[1].Task.ID != third.ID || ready.Deliveries[1].Delivery != "claimed" {
		t.Fatalf("expanded window=%#v", ready)
	}

	for _, count := range []int{0, 33} {
		res = request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"count": count}, nil)
		if res.StatusCode != 422 || responseCode(t, res) != "invalid_count" {
			t.Fatalf("invalid count %d status=%d", count, res.StatusCode)
		}
	}
	for _, count := range []any{1.5, "2", nil} {
		res = request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"count": count}, nil)
		if res.StatusCode != 422 || responseCode(t, res) != "invalid_count" {
			t.Fatalf("invalid count %#v status=%d", count, res.StatusCode)
		}
	}

	res = request(t, workerFixture, "GET", "/api/v1/whoami", nil, nil)
	var actor model.Actor
	if err := json.NewDecoder(res.Body).Decode(&actor); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if actor.Kind != "worker" {
		t.Fatalf("public actor kind=%q", actor.Kind)
	}
}

func TestReadyCaughtUpIsolationAndDependencyResults(t *testing.T) {
	f := setup(t)
	workerA, tokenA := workerToken(t, f, "worker-a")
	workerB, tokenB := workerToken(t, f, "worker-b")
	fixtureA, fixtureB := f, f
	fixtureA.token, fixtureB.token = tokenA, tokenB
	otherProject, err := f.store.CreateProject(context.Background(), f.admin, "Other Project", "")
	if err != nil {
		t.Fatal(err)
	}

	res := request(t, fixtureA, "POST", "/api/v1/work/ready", map[string]any{}, nil)
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("caught up=%d", res.StatusCode)
	}
	blocker, _, err := f.store.CreateTask(context.Background(), workerA, store.CreateTaskInput{Title: "Produce output", ProjectID: f.project.ID, AssignedTo: workerB.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	continuation, _, err := f.store.CreateTask(context.Background(), workerA, store.CreateTaskInput{Title: "Use output", ProjectID: f.project.ID, AssignedTo: workerA.ID, BlockedBy: []string{blocker.ID}}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "Wrong project", ProjectID: otherProject.ID, AssignedTo: workerB.ID}, "")
	if err != nil {
		t.Fatal(err)
	}

	res = request(t, fixtureB, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	var ready model.ReadyResponse
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	delivery := ready.Deliveries[0]
	if delivery.Task.ID != blocker.ID {
		t.Fatalf("B delivery=%#v", delivery)
	}
	res = request(t, fixtureB, "POST", "/api/v1/work/"+blocker.ID+"/complete", map[string]any{"status": "done", "result": "artifact://report"}, nil)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("B complete=%d", res.StatusCode)
	}
	res = request(t, fixtureA, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	delivery = ready.Deliveries[0]
	if delivery.Task.ID != continuation.ID || len(delivery.DependencyResults) != 1 || delivery.DependencyResults[0].TaskID != blocker.ID || delivery.DependencyResults[0].Result != "artifact://report" {
		t.Fatalf("A delivery=%#v", delivery)
	}
	res = request(t, fixtureB, "POST", "/api/v1/work/ready", map[string]any{"project_id": otherProject.ID}, nil)
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	delivery = ready.Deliveries[0]
	if delivery.Task.ProjectID != otherProject.ID || delivery.Task.AssignedTo != workerB.ID {
		t.Fatalf("isolated delivery=%#v", delivery)
	}
}

func TestDoingTaskFrozenAndCancellationRejectsCompletion(t *testing.T) {
	f := setup(t)
	worker, token := workerToken(t, f, "worker")
	workerFixture := f
	workerFixture.token = token
	taskValue, _, err := f.store.CreateTask(context.Background(), f.admin, store.CreateTaskInput{Title: "Stable instructions", ProjectID: f.project.ID, AssignedTo: worker.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	res := request(t, workerFixture, "POST", "/api/v1/work/ready", map[string]any{"project_id": f.project.ID}, nil)
	var ready model.ReadyResponse
	if err := json.NewDecoder(res.Body).Decode(&ready); err != nil {
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
	res = request(t, workerFixture, "POST", "/api/v1/work/"+taskValue.ID+"/complete", map[string]any{"status": "done", "result": "late"}, nil)
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
	contractText := string(body)
	if res.StatusCode != 200 || !strings.Contains(contractText, "Worker Interoperability Contract") || !strings.Contains(contractText, "/api/v1/work/ready") {
		t.Fatalf("worker contract=%d %s", res.StatusCode, body)
	}
	for _, requiredText := range []string{"Keys are permanent", "Idempotent-Replayed", "missing_idempotency_key", "idempotency_key_conflict", "no cancellation notification", "no reject or requeue operation"} {
		if !strings.Contains(contractText, requiredText) {
			t.Fatalf("worker contract missing %q", requiredText)
		}
	}
	if strings.Contains(contractText, "24-hour") || strings.Contains(contractText, "24 hours") {
		t.Fatal("worker contract still contains expiring creation-ID guidance")
	}
	res, err = http.Get(f.server.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var document struct {
		Paths      map[string]any `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.NewDecoder(res.Body).Decode(&document); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/work/ready", "/api/v1/work/{task_id}/complete"} {
		if _, ok := document.Paths[path]; !ok {
			t.Fatalf("OpenAPI missing %s", path)
		}
	}
	for _, schema := range []string{"Actor", "Task", "ReadyInput", "TaskDelivery", "ReadyResponse"} {
		if _, ok := document.Components.Schemas[schema]; !ok {
			t.Fatalf("OpenAPI missing schema %s", schema)
		}
	}
	createPath, err := json.Marshal(document.Paths["/api/v1/tasks"])
	if err != nil || !strings.Contains(string(createPath), `"required":true`) || !strings.Contains(string(createPath), "Idempotent-Replayed") || !strings.Contains(string(createPath), `"400"`) || !strings.Contains(string(createPath), `"409"`) || !strings.Contains(string(createPath), "idempotency_key_conflict") {
		t.Fatalf("OpenAPI task creation missing idempotency retention: %s", createPath)
	}
	openAPIText := string(document.Components.Schemas["ReadyInput"]) + string(document.Components.Schemas["ReadyResponse"]) + string(document.Components.Schemas["Task"]) + string(document.Components.Schemas["Actor"])
	for _, requiredText := range []string{`"count"`, `"maximum": 32`, `"deliveries"`, `"queue_sequence"`, `"worker"`} {
		if !strings.Contains(openAPIText, requiredText) {
			t.Fatalf("OpenAPI schemas missing %s: %s", requiredText, openAPIText)
		}
	}
}
