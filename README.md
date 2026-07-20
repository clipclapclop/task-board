# Task Board

Task Board is a private household task handoff service for humans and software agents. It is a
single Go binary with server-rendered pages, a versioned JSON API, and SQLite storage.

## Update & redeploy

On Host system run:

```sh
sudo systemctl start task-board-deploy.service
sudo journalctl -u task-board-deploy.service -n 200 --no-pager
```

then verify it with:

```sh
sudo journalctl -u task-board-deploy.service -n 200 --no-pager
```

## Local start

```sh
go run ./cmd/task-board
```

Open `http://localhost:8080`. The first start creates the configured default human administrator
(`chad` by default). The browser actor selector is a household attribution convenience; Tailscale
is the production network-access boundary.

Create an API token for an actor:

```sh
go run ./cmd/task-board actor create --username example-worker --name "Example Worker" --kind worker
go run ./cmd/task-board token create --actor example-worker --name local-development
```

Store the returned token in `TASK_BOARD_TOKEN`; it is shown only once. Begin every agent session
with `GET /api/v1/whoami`. Workers receive status-first ready windows through
`POST /api/v1/work/ready` and report individual outcomes through
`POST /api/v1/work/{task_id}/complete`; they do not browse the general task API.

## Verification

```sh
go test ./...
go vet ./...
go build ./cmd/task-board
```

See the [worker contract](docs/worker-contract.md) for integration behavior and the served
`/api/v1/openapi.json` for exact API schemas. [ContainerBot operations](docs/operations.md) covers
deployment, and the authoritative product scope is in
[task-board-requirements.md](task-board-requirements.md).
