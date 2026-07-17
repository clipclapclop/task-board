#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then echo "run as root" >&2; exit 1; fi

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
install -d -o serviceuser -g users -m 0750 /srv/task-board /var/lib/task-board /var/lib/task-board/secrets /var/lib/task-board/predeploy
if [ ! -f /var/lib/task-board/secrets/app.env ]; then
	install -o serviceuser -g users -m 0600 "$source_dir/app.env.example" /var/lib/task-board/secrets/app.env
fi
for unit in task-board-deploy.service task-board-backup.service task-board-backup.timer task-board-restore-rehearsal.service task-board-restore-rehearsal.timer; do
	install -m 0644 "$source_dir/$unit" "/etc/systemd/system/$unit"
done

install -d -o caddy -g caddy -m 0750 /etc/caddy/conf.d
install -o caddy -g caddy -m 0640 "$source_dir/task-board.Caddyfile" /etc/caddy/conf.d/task-board.Caddyfile
if ! grep -Fq 'import /etc/caddy/conf.d/*.Caddyfile' /etc/caddy/Caddyfile; then
	cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.pre-task-board"
	printf '\nimport /etc/caddy/conf.d/*.Caddyfile\n' >> /etc/caddy/Caddyfile
fi
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
systemctl daemon-reload
systemctl enable --now task-board-backup.timer task-board-restore-rehearsal.timer
echo "Installed. Deploy with: systemctl start task-board-deploy.service"
