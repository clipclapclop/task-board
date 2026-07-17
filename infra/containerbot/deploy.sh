#!/bin/sh
set -eu

checkout=/srv/task-board/checkout
deploy="$checkout/infra/containerbot"
state=/var/lib/task-board
revision_file="$state/deployed-revision"
image_file="$state/deployed-image"
export GIT_SSH_COMMAND="ssh -i /home/serviceuser/.ssh/task_board_deploy_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new"

cd "$checkout"
git fetch origin main
git checkout --force main
git reset --hard origin/main
revision=$(git rev-parse HEAD)
image="task-board:$revision"
previous_image=$(cat "$image_file" 2>/dev/null || true)

docker build --pull --build-arg "REVISION=$revision" --tag "$image" --file "$deploy/Dockerfile" "$checkout"

cp "$state/secrets/app.env" "$deploy/.env"
chmod 600 "$deploy/.env"

predeploy_archive=
if [ -n "$previous_image" ] && [ -s "$state/task-board.sqlite3" ]; then
	predeploy_archive=$(docker run --rm --user 1034:100 \
		-v "$state:/state" \
		-e TASK_BOARD_DATABASE=/state/task-board.sqlite3 \
		"$previous_image" backup create --destination /state/predeploy | tail -n 1)
fi

cd "$deploy"
TASK_BOARD_IMAGE="$image" docker compose up -d --no-build app
healthy=0
attempt=1
while [ "$attempt" -le 12 ]; do
	if curl --fail --silent http://127.0.0.1:8787/health/ready >/dev/null; then healthy=1; break; fi
	sleep 5
	attempt=$((attempt + 1))
done

if [ "$healthy" = 1 ]; then
	printf '%s\n' "$revision" > "$revision_file"
	printf '%s\n' "$image" > "$image_file"
	exit 0
fi

TASK_BOARD_IMAGE="$image" docker compose down
if [ -n "$previous_image" ]; then
	if [ -n "$predeploy_archive" ]; then
		docker run --rm --user 1034:100 \
			-v "$state:/state" \
			-e TASK_BOARD_DATABASE=/state/task-board.sqlite3 \
			"$previous_image" backup restore "$predeploy_archive"
	fi
	TASK_BOARD_IMAGE="$previous_image" docker compose up -d --no-build app
fi
exit 1
