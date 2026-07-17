#!/bin/sh
set -eu

checkout=/srv/task-board/checkout
deploy="$checkout/infra/containerbot"
state=/var/lib/task-board
image=$(cat "$state/deployed-image")
archive=$(find /home/serviceuser/nas/docs/TaskBoardBackups -maxdepth 1 -type f -name 'task-board-*.tar.gz' -printf '%f\n' | sort | tail -n 1)
test -n "$archive"
cd "$deploy"
cp "$state/secrets/app.env" .env
chmod 600 .env
TASK_BOARD_IMAGE="$image" docker compose --profile tools run --rm backup backup rehearse "/nas/$archive"
