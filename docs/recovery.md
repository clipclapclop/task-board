# Recovery

## Restore the database on ContainerBot

1. Stop the app: `cd /srv/task-board/checkout/infra/containerbot && docker compose stop app`.
2. Select a verified archive from `/home/serviceuser/nas/docs/TaskBoardBackups`.
3. Preserve the current `/var/lib/task-board/task-board.sqlite3` outside that directory.
4. Run the deployed image with `/state` and `/nas` mounted and execute
   `backup restore /nas/ARCHIVE`.
5. Start the app and verify `/health/ready`, actor listing, task count, and a task event timeline.

Restore refuses archives whose manifest, size, checksum, tar members, or SQLite integrity check is
invalid. The application must be stopped so no writes race the atomic database replacement.

## Replacement host

1. Install Debian, Docker Compose, Caddy with the Cloudflare DNS module, Tailscale, Git, and CIFS
   support. Recreate `serviceuser` UID 1034/GID 100.
2. Join the Tailnet and mount the NAS at the existing serviceuser paths.
3. Clone the NAS Git mirror or GitHub repository to `/srv/task-board/checkout`.
4. Install systemd/Caddy assets and confirm Caddy binds only the new node's Tailscale address.
5. Copy the newest verified database archive into the NAS backup directory and restore it.
6. Deploy the recorded revision when available, otherwise deploy the tested main revision.
7. Reissue agent tokens if the database could have been exposed; otherwise existing hashed-token
   records and client secrets continue to work.
8. Verify HTTPS from a Tailnet client and confirm the service is unreachable outside Tailscale.
