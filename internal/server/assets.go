package server

import (
	"net/http"
)

func (s *Server) style(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public,max-age=86400")
	_, _ = w.Write([]byte(styleCSS))
}
func (s *Server) htmx(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "public,max-age=86400")
	_, _ = w.Write([]byte(htmxJS))
}
func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(openAPIJSON))
}

const portalTemplate = `{{define "portal"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · Task Board</title><link rel="stylesheet" href="/static/style.css"><script defer src="/static/htmx.min.js"></script></head>
<body><header><a class="brand" href="/tasks">Task Board</a><nav><a href="/tasks">Tasks</a><a href="/tasks/new">New</a><a href="/profile">Profile</a><a href="/docs/worker-contract.md">Worker contract</a>{{if .Current.IsAdmin}}<a href="/admin/users">Users</a><a href="/admin/projects">Projects</a>{{end}}</nav>
<form class="actor-switch" method="post" action="/switch-actor"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input type="hidden" name="next" value="/tasks"><label>Acting as <select name="actor" onchange="this.form.submit()">{{range .Actors}}{{if eq .Kind "human"}}{{if .Active}}<option value="{{.Username}}" {{selected .Username $.Current.Username}}>{{.DisplayName}}</option>{{end}}{{end}}{{end}}</select></label><noscript><button>Switch</button></noscript></form></header>
<main>{{if .Error}}<div class="alert error">{{.Error}}</div>{{end}}{{if .Notice}}<div class="alert">{{.Notice}}</div>{{end}}{{if .NewToken}}<section class="token-reveal"><h2>Copy this token now</h2><p>It will not be shown again.</p><code>{{.NewToken}}</code></section>{{end}}

{{if eq .Page "tasks"}}<div class="title-row"><h1>Tasks</h1><a class="button" href="/tasks/new">Create task</a></div>
<div class="tabs"><a class="{{if eq .View "mine"}}active{{end}}" href="/tasks?view=mine">My Tasks</a><a class="{{if eq .View "created"}}active{{end}}" href="/tasks?view=created">Created by Me</a><a class="{{if eq .View "all"}}active{{end}}" href="/tasks?view=all">All Tasks</a></div>
<form class="filters" method="get"><input type="hidden" name="view" value="{{.View}}"><input name="q" value="{{.Filter.Get "q"}}" placeholder="Search title and description"><select name="project"><option value="">All projects</option>{{range .Projects}}<option value="{{.ID}}" {{selected .ID $.Filter.Get "project"}}>{{.Name}}</option>{{end}}</select><select name="status"><option value="">All statuses</option>{{range $s:=strings "todo doing done failed cancelled"}}<option value="{{$s}}" {{selected $s ($.Filter.Get "status")}}>{{$s}}</option>{{end}}</select><select name="assigned_to"><option value="">Any assignee</option>{{range .Actors}}{{if .Active}}<option value="{{.ID}}" {{selected .ID ($.Filter.Get "assigned_to")}}>Assigned: {{.DisplayName}}</option>{{end}}{{end}}</select><select name="created_by"><option value="">Any creator</option>{{range .Actors}}<option value="{{.ID}}" {{selected .ID ($.Filter.Get "created_by")}}>Created: {{.DisplayName}}</option>{{end}}</select><select name="actionable"><option value="">Any readiness</option><option value="true" {{selected "true" (.Filter.Get "actionable")}}>Actionable</option><option value="false" {{selected "false" (.Filter.Get "actionable")}}>Blocked or terminal</option></select><input name="updated_after" value="{{.Filter.Get "updated_after"}}" placeholder="Updated after (RFC 3339)"><button>Filter</button></form>
{{if .Tasks}}<div class="task-list">{{range .Tasks}}<article class="task-card"><div><a class="task-title" href="/tasks/{{.ID}}">{{.Title}}</a><div class="meta"><span class="status {{.Status}}">{{.Status}}</span>{{if .IsBlocked}}<span class="blocked">blocked by {{len .BlockedBy}}</span>{{else if .Actionable}}<span class="actionable">actionable</span>{{end}} · updated {{timefmt .UpdatedAt}}</div></div><span class="mono">{{short .ID}}</span></article>{{end}}</div>{{if .NextURL}}<p><a class="button" href="{{.NextURL}}">Next page</a></p>{{end}}{{else}}<div class="empty">No tasks match this view.</div>{{end}}

{{else if eq .Page "task-new"}}<h1>New task</h1><form class="stack" method="post" action="/tasks/new"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><label>Title<input required maxlength="200" name="title" autofocus></label><label>Description<textarea name="description" rows="7"></textarea></label><div class="grid"><label>Assign to<select required name="assigned_to">{{range .Actors}}{{if .Active}}<option value="{{.ID}}" {{selected .ID $.Current.ID}}>{{.DisplayName}} ({{.Kind}})</option>{{end}}{{end}}</select></label><label>Project<select required name="project_id"><option value="" disabled selected>Select a project</option>{{range .Projects}}{{if not .ArchivedAt}}<option value="{{.ID}}">{{.Name}}</option>{{end}}{{end}}</select></label></div><label>Blocked by<select name="blocked_by" multiple size="6">{{range .Tasks}}<option value="{{.ID}}">{{.Title}}</option>{{end}}</select><small>Optional; use Ctrl/Cmd to select more than one.</small></label><button>Create task</button></form>

{{else if eq .Page "task-detail"}}<div class="title-row"><div><h1>{{.Task.Title}}</h1><div class="meta"><span class="status {{.Task.Status}}">{{.Task.Status}}</span>{{if .Task.IsBlocked}}<span class="blocked">blocked</span>{{else if .Task.Actionable}}<span class="actionable">actionable</span>{{end}} · version {{.Task.Version}}</div></div><a href="/tasks">Back</a></div>
{{if .Task.BlockedBy}}<section><h2>Blocked by</h2><ul>{{range .Task.BlockedBy}}<li><a href="/tasks/{{.ID}}">{{.Title}}</a> <span class="status {{.Status}}">{{.Status}}</span></li>{{end}}</ul></section>{{end}}
{{$canEdit:=or .Current.IsAdmin (eq .Current.ID .Task.CreatedBy)}}{{$canWork:=or .Current.IsAdmin (eq .Current.ID .Task.AssignedTo)}}
{{if and $canEdit (not .Task.Terminal) (ne .Task.Status "doing")}}<section><h2>Edit</h2><form class="stack" method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input type="hidden" name="version" value="{{.Task.Version}}"><input type="hidden" name="action" value="edit"><label>Title<input required maxlength="200" name="title" value="{{.Task.Title}}"></label><label>Description<textarea name="description" rows="7">{{.Task.Description}}</textarea></label><div class="grid"><label>Assignee<select name="assigned_to">{{range .Actors}}{{if .Active}}<option value="{{.ID}}" {{selected .ID $.Task.AssignedTo}}>{{.DisplayName}}</option>{{end}}{{end}}</select></label><label>Project<select required name="project_id">{{range .Projects}}<option value="{{.ID}}" {{selected .ID $.Task.ProjectID}}>{{.Name}}</option>{{end}}</select></label></div><label>Dependencies<select name="blocked_by" multiple size="6">{{range .Tasks}}{{if ne .ID $.Task.ID}}<option value="{{.ID}}">{{.Title}}</option>{{end}}{{end}}</select></label><button>Save details</button></form></section>{{else}}<section><h2>Description</h2><div class="prose">{{if .Task.Description}}{{.Task.Description}}{{else}}No description.{{end}}</div></section>{{end}}
{{if and $canWork (not .Task.Terminal)}}<section><h2>Work</h2><form class="stack" method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input type="hidden" name="version" value="{{.Task.Version}}"><input type="hidden" name="action" value="work"><label>Status<select name="status">{{range $s:=strings "todo doing done failed"}}<option value="{{$s}}" {{selected $s $.Task.Status}}>{{$s}}</option>{{end}}</select></label><label>Result<textarea name="result" rows="5">{{.Task.Result}}</textarea></label><button>Update work</button></form></section>{{else if .Task.Result}}<section><h2>Result</h2><div class="prose">{{.Task.Result}}</div></section>{{end}}
{{if and $canEdit (not .Task.Terminal)}}<form method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input type="hidden" name="version" value="{{.Task.Version}}"><button class="danger" name="action" value="cancel">Cancel task</button></form>{{end}}{{if and .Current.IsAdmin .Task.Terminal}}<form method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input type="hidden" name="version" value="{{.Task.Version}}"><button name="action" value="reopen">Reopen task</button></form>{{end}}
<section><h2>History</h2><ol class="timeline">{{range .Events}}<li><strong>{{.EventType}}</strong> by <span class="mono">{{short .ActorID}}</span> <time>{{timefmt .CreatedAt}}</time><pre>{{printf "%v" .Changes}}</pre></li>{{end}}</ol></section>

{{else if eq .Page "profile"}}<h1>{{.User.DisplayName}}</h1><p><code>{{.User.Username}}</code> · {{.User.Kind}} · {{.User.Role}}</p><section><h2>API tokens</h2><form class="inline" method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input name="name" required placeholder="Token name"><button name="action" value="create-token">Create token</button></form>{{template "tokens" .}}</section>

{{else if eq .Page "users"}}<div class="title-row"><h1>Users and workers</h1><a href="/admin/export">Download export</a></div><div class="cards">{{range .Actors}}<a class="user-card" href="/admin/users/{{.ID}}"><strong>{{.DisplayName}}</strong><span><code>{{.Username}}</code> · {{.Kind}} · {{.Role}}{{if not .Active}} · disabled{{end}}</span></a>{{end}}</div><section><h2>Create actor</h2><form class="stack" method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><div class="grid"><label>Username<input name="username" required pattern="[a-z0-9_-]+"></label><label>Display name<input name="display_name" required></label><label>Kind<select name="kind"><option value="human">human</option><option value="worker">worker</option></select></label><label>Role<select name="role"><option value="member">member</option><option value="admin">admin</option></select></label></div><button>Create actor</button></form></section>

{{else if eq .Page "user-detail"}}<h1>{{.User.DisplayName}}</h1><p><code>{{.User.Username}}</code> · {{.User.Kind}}</p><form class="stack" method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input type="hidden" name="action" value="update"><label>Display name<input name="display_name" value="{{.User.DisplayName}}" required></label><label>Role<select name="role"><option value="member" {{selected "member" .User.Role}}>member</option>{{if eq .User.Kind "human"}}<option value="admin" {{selected "admin" .User.Role}}>admin</option>{{end}}</select></label><label class="check"><input type="checkbox" name="active" {{checked .User.Active}}> Active</label><button>Save user</button></form><section><h2>API tokens</h2><form class="inline" method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><input name="name" required placeholder="Token name"><button name="action" value="create-token">Create token</button></form>{{template "tokens" .}}</section>

{{else if eq .Page "projects"}}<h1>Projects</h1><div class="cards">{{range .Projects}}<form class="user-card stack" method="post" action="/admin/projects/{{.ID}}"><input type="hidden" name="csrf_token" value="{{$.CSRF}}"><label>Name<input name="name" value="{{.Name}}" required></label><label>Description<input name="description" value="{{.Description}}"></label><label class="check"><input type="checkbox" name="archived" {{if .ArchivedAt}}checked{{end}}> Archived</label><button>Save</button></form>{{end}}</div><section><h2>Create project</h2><form class="stack" method="post"><input type="hidden" name="csrf_token" value="{{.CSRF}}"><label>Name<input name="name" required></label><label>Description<textarea name="description"></textarea></label><button>Create project</button></form></section>

{{else if eq .Page "error"}}<h1>{{.Title}}</h1><p>{{.Error}}</p><a href="/tasks">Return to tasks</a>{{end}}
</main><footer>Private household service · API <a href="/api/v1/openapi.json">v1 OpenAPI</a></footer></body></html>{{end}}
{{define "tokens"}}{{if .Tokens}}<div class="token-list">{{range .Tokens}}<div><span><strong>{{.Name}}</strong> <code>{{.Prefix}}…</code>{{if .RevokedAt}} · revoked{{else if .ExpiresAt}} · expires {{timefmt .ExpiresAt}}{{end}}</span>{{if not .RevokedAt}}<form method="post"><input type="hidden" name="csrf_token" value="{{$.CSRF}}"><input type="hidden" name="token_id" value="{{.ID}}"><button class="link danger-text" name="action" value="revoke-token">Revoke</button></form>{{end}}</div>{{end}}</div>{{else}}<p>No API tokens.</p>{{end}}{{end}}`

const styleCSS = `:root{color-scheme:light dark;--bg:#f5f6f8;--panel:#fff;--text:#17202a;--muted:#667085;--line:#d8dde6;--accent:#3867d6;--danger:#b42318;--ok:#18794e}@media(prefers-color-scheme:dark){:root{--bg:#11151b;--panel:#1b212a;--text:#edf1f7;--muted:#a8b0bd;--line:#38414e;--accent:#83a9ff;--danger:#ff8a80;--ok:#77d6a5}}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,sans-serif}header{display:flex;align-items:center;gap:1.3rem;padding:.8rem max(1rem,calc((100% - 1100px)/2));background:var(--panel);border-bottom:1px solid var(--line);position:sticky;top:0;z-index:2}.brand{font-weight:800;color:var(--text);text-decoration:none;font-size:1.1rem}nav{display:flex;gap:1rem}nav a,a{color:var(--accent)}.actor-switch{margin-left:auto}.actor-switch label{display:flex;align-items:center;gap:.45rem;color:var(--muted)}main{max-width:1100px;margin:2rem auto;padding:0 1rem;min-height:75vh}footer{max-width:1100px;margin:2rem auto;padding:1rem;color:var(--muted);border-top:1px solid var(--line)}h1{margin:.2rem 0 1rem;font-size:1.8rem}h2{font-size:1.1rem;margin-top:0}section{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:1.1rem;margin:1rem 0}.title-row,.inline,.grid{display:flex;gap:1rem;align-items:center}.title-row{justify-content:space-between}.grid>*{flex:1}.button,button{display:inline-block;border:0;border-radius:6px;background:var(--accent);color:white;padding:.55rem .85rem;text-decoration:none;font:inherit;cursor:pointer}button.danger{background:var(--danger)}button.link{background:none;padding:.2rem;color:var(--accent)}button.danger-text{color:var(--danger)}input,select,textarea{width:100%;padding:.55rem;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--text);font:inherit}select[multiple]{min-height:7rem}.stack{display:grid;gap:.9rem}.stack label{display:grid;gap:.3rem;font-weight:600}.check{display:flex!important;align-items:center;grid-template-columns:auto 1fr!important}.check input{width:auto}.tabs{display:flex;gap:.3rem;margin-bottom:1rem;border-bottom:1px solid var(--line)}.tabs a{padding:.6rem .8rem;text-decoration:none}.tabs .active{border-bottom:3px solid var(--accent);font-weight:700}.filters{display:grid;grid-template-columns:2fr 1fr 1fr auto;gap:.6rem;margin:1rem 0}.task-list,.cards{display:grid;gap:.65rem}.task-card,.user-card{display:flex;justify-content:space-between;align-items:center;padding:1rem;background:var(--panel);border:1px solid var(--line);border-radius:8px;text-decoration:none;color:var(--text)}.user-card.stack{display:grid;align-items:stretch}.task-title{font-weight:750;text-decoration:none;font-size:1.03rem}.meta,.mono,small,time{color:var(--muted)}.mono,code{font-family:ui-monospace,monospace}.status,.blocked,.actionable{display:inline-block;border-radius:999px;padding:.08rem .5rem;font-size:.8rem;background:var(--line)}.status.done,.actionable{color:var(--ok)}.status.failed,.status.cancelled,.blocked{color:var(--danger)}.empty,.alert,.token-reveal{padding:1rem;border-radius:8px;background:var(--panel);border:1px solid var(--line)}.alert.error{border-color:var(--danger)}.token-reveal{border-color:var(--ok)}.token-reveal code{display:block;overflow-wrap:anywhere;font-size:1rem;padding:.7rem;background:var(--bg)}.prose{white-space:pre-wrap}.timeline{padding-left:1.4rem}.timeline li{margin-bottom:1rem}.timeline pre{white-space:pre-wrap;color:var(--muted)}.token-list>div{display:flex;justify-content:space-between;padding:.55rem 0;border-bottom:1px solid var(--line)}@media(max-width:700px){header{flex-wrap:wrap}nav{order:3;width:100%;overflow:auto}.actor-switch{margin-left:auto}.grid,.title-row,.inline{align-items:stretch;flex-direction:column}.filters{grid-template-columns:1fr}.task-card{align-items:flex-start}.mono{display:none}}`

const openAPIJSON = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Task Board API",
    "version": "1.0.0",
    "description": "Private household task coordination API. Humans use the general task API; workers receive owned work through ready windows and report outcomes through complete."
  },
  "servers": [{"url": "https://task-board.oorangy.com"}],
  "components": {
    "securitySchemes": {"bearer": {"type": "http", "scheme": "bearer"}},
    "schemas": {
      "Problem": {
        "type": "object",
        "required": ["title", "status", "code"],
        "properties": {
          "title": {"type": "string"},
          "status": {"type": "integer"},
          "detail": {"type": "string"},
          "code": {"type": "string"}
        }
      },
      "Actor": {
        "type": "object",
        "required": ["id", "username", "display_name", "kind", "role", "active"],
        "properties": {
          "id": {"type": "string"},
          "username": {"type": "string"},
          "display_name": {"type": "string"},
          "kind": {"type": "string", "enum": ["human", "worker"]},
          "role": {"type": "string", "enum": ["member", "admin"]},
          "active": {"type": "boolean"}
        }
      },
      "Task": {
        "type": "object",
        "required": ["id", "title", "description", "project_id", "created_by", "assigned_to", "status", "result", "version", "queue_sequence"],
        "properties": {
          "id": {"type": "string"},
          "title": {"type": "string", "maxLength": 200},
          "description": {"type": "string", "maxLength": 20000},
          "project_id": {"type": "string"},
          "created_by": {"type": "string"},
          "assigned_to": {"type": "string"},
          "status": {"type": "string", "enum": ["todo", "doing", "done", "failed", "cancelled"]},
          "result": {"type": "string", "maxLength": 20000},
          "version": {"type": "integer", "minimum": 1},
          "queue_sequence": {"type": "integer", "minimum": 1, "readOnly": true}
        }
      },
      "ReadyInput": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "project_id": {"type": "string"},
          "count": {"type": "integer", "default": 1, "minimum": 1, "maximum": 32}
        }
      },
      "DependencyResult": {
        "type": "object",
        "required": ["task_id", "title", "result"],
        "properties": {
          "task_id": {"type": "string"},
          "title": {"type": "string"},
          "result": {"type": "string"}
        }
      },
      "TaskDelivery": {
        "type": "object",
        "required": ["delivery", "task", "dependency_results"],
        "properties": {
          "delivery": {"type": "string", "enum": ["claimed", "resumed"]},
          "task": {"$ref": "#/components/schemas/Task"},
          "dependency_results": {"type": "array", "items": {"$ref": "#/components/schemas/DependencyResult"}}
        }
      },
      "ReadyResponse": {
        "type": "object",
        "required": ["count", "deliveries"],
        "properties": {
          "project_id": {"type": "string"},
          "count": {"type": "integer", "minimum": 1, "maximum": 32},
          "deliveries": {"type": "array", "items": {"$ref": "#/components/schemas/TaskDelivery"}}
        }
      },
      "CompleteInput": {
        "type": "object",
        "required": ["status"],
        "additionalProperties": false,
        "properties": {
          "status": {"type": "string", "enum": ["done", "failed"]},
          "result": {"type": "string", "maxLength": 20000}
        }
      },
      "CreateTaskInput": {
        "type": "object",
        "required": ["title", "project_id", "assigned_to"],
        "additionalProperties": false,
        "properties": {
          "title": {"type": "string", "maxLength": 200},
          "description": {"type": "string", "maxLength": 20000},
          "project_id": {"type": "string"},
          "assigned_to": {"type": "string"},
          "blocked_by": {"type": "array", "items": {"type": "string"}}
        }
      }
    }
  },
  "security": [{"bearer": []}],
  "paths": {
    "/api/v1/whoami": {"get": {"summary": "Return the token actor", "responses": {"200": {"description": "Actor", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Actor"}}}}}}},
    "/api/v1/actors": {"get": {"summary": "List active actors", "responses": {"200": {"description": "Actors"}}}},
    "/api/v1/projects": {"get": {"summary": "List projects", "responses": {"200": {"description": "Projects"}}}},
    "/api/v1/work/ready": {
      "post": {
        "summary": "Return a status-first window of owned and newly claimed work",
        "description": "Active workers only. Omit project_id to reconcile across all projects. Existing doing work precedes newly claimed todo work in queue_sequence order.",
        "x-problem-codes": ["unsupported_actor_kind", "invalid_project", "invalid_count"],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ReadyInput"}}}},
        "responses": {
          "200": {"description": "Status-first ready window", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ReadyResponse"}}}},
          "204": {"description": "No owned or actionable work"},
          "403": {"description": "Token does not identify an active worker"},
          "422": {"description": "Invalid project or count"}
        }
      }
    },
    "/api/v1/work/{task_id}/complete": {
      "post": {
        "summary": "Complete work owned by the worker",
        "x-problem-codes": ["unsupported_actor_kind", "work_not_owned", "completion_conflict"],
        "parameters": [{"in": "path", "name": "task_id", "required": true, "schema": {"type": "string"}}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CompleteInput"}}}},
        "responses": {
          "200": {"description": "Completed task or identical replay"},
          "409": {"description": "Work is not owned or completion conflicts"}
        }
      }
    },
    "/api/v1/tasks": {
      "get": {"summary": "List and filter tasks (human actors only)", "responses": {"200": {"description": "Task page"}, "403": {"description": "Worker task access forbidden"}}},
      "post": {
        "summary": "Create a project-bearing task",
        "parameters": [{"in": "header", "name": "Idempotency-Key", "schema": {"type": "string"}}],
        "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateTaskInput"}}}},
        "responses": {"201": {"description": "Created"}, "422": {"description": "Project missing or invalid"}}
      }
    },
    "/api/v1/tasks/{id}": {
      "get": {"summary": "Get task and history (human actors only)", "responses": {"200": {"description": "Task"}, "403": {"description": "Worker task access forbidden"}}},
      "patch": {
        "summary": "Update a task (human actors only)",
        "x-problem-codes": ["worker_task_access_forbidden", "queue_sequence_conflict"],
        "parameters": [{"in": "header", "name": "If-Match", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "Updated"}, "409": {"description": "Doing task details are frozen"}, "412": {"description": "Stale version"}, "428": {"description": "Missing version"}}
      }
    },
    "/api/v1/tasks/{id}/reopen": {"post": {"summary": "Administrator reopen", "responses": {"200": {"description": "Reopened"}}}},
    "/api/v1/export": {"get": {"summary": "Sanitized administrator export", "responses": {"200": {"description": "Export"}}}},
    "/api/v1/openapi.json": {"get": {"security": [], "summary": "OpenAPI document", "responses": {"200": {"description": "OpenAPI"}}}}
  }
}`
