# ContainerBot operations

## One-time setup

1. Create a Cloudflare DNS-only A record for `task-board.oorangy.com` pointing to
   `100.122.228.62`. Do not proxy it through Cloudflare and do not add a public IP.
2. Install a read-only GitHub deploy key for this repository as
   `/home/serviceuser/.ssh/task_board_deploy_ed25519` and configure the checkout remote.
3. Clone the repository to `/srv/task-board/checkout` as `serviceuser`.
4. From that checkout, run `sudo infra/containerbot/install-systemd.sh`.
5. Confirm `caddy validate --config /etc/caddy/Caddyfile` succeeds.

The install script preserves the pre-change Caddyfile, installs a private site snippet, creates
state directories, and enables the daily backup and monthly rehearsal timers.

## Deploy

Deploy explicitly:

```sh
sudo systemctl start task-board-deploy.service
sudo journalctl -u task-board-deploy.service -n 200 --no-pager
```

The deployment fetches `origin/main`, builds and tests a commit-tagged image, snapshots the current
database, starts the candidate, and waits for readiness. A failed candidate restores the snapshot
and previous image when one exists.

Verify:

```sh
curl --fail https://task-board.oorangy.com/health/ready
sudo ss -lnt | grep -E '100\.122\.228\.62:(80|443)|127\.0\.0\.1:8787'
```

Only Caddy listens on the Tailscale address; Docker publishes the app on loopback.

## Accounts and tokens

The first successful start creates `chad` as the default human administrator if the database is
empty. Further actors and tokens can be managed in `/admin/users`.

For emergency CLI provisioning:

```sh
cd /srv/task-board/checkout/infra/containerbot
TASK_BOARD_IMAGE="$(cat /var/lib/task-board/deployed-image)" \
  docker compose exec app /task-board token create --actor ACTOR --name emergency
```

Tokens are shown once. Store agent tokens in the agent's environment or a mode-0600 secret file.

## Backups

`task-board-backup.timer` runs once daily around 03:20 local time. It creates and immediately
verifies a consistent SQLite archive under `TaskBoardBackups` on the NAS and mirrors Git refs.
Retention is 30 newest daily archives plus one per month for the last 12 months.

Run manually:

```sh
sudo systemctl start task-board-backup.service
sudo journalctl -u task-board-backup.service -n 100 --no-pager
```

The monthly rehearsal extracts the newest archive to an isolated temporary directory, verifies
the manifest and SHA-256, opens the restored SQLite database, applies no destructive operation,
and runs the database readiness check.

See [recovery.md](recovery.md) for replacement-host and rollback procedures.
