#!/bin/sh
set -eu

checkout=/srv/task-board/checkout
deploy="$checkout/infra/containerbot"
state=/var/lib/task-board
image=$(cat "$state/deployed-image")
cd "$deploy"
cp "$state/secrets/app.env" .env
chmod 600 .env
TASK_BOARD_IMAGE="$image" docker compose --profile tools run --rm backup backup create --destination /nas

mirror=/home/serviceuser/nas/docs/TaskBoard/task-board.git
mkdir -p "$(dirname "$mirror")"
if [ -d "$mirror" ]; then
	git --git-dir="$mirror" remote update --prune
else
	git clone --mirror "$checkout" "$mirror"
fi
git --git-dir="$mirror" fsck --full
