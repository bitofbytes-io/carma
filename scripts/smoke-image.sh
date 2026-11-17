#!/usr/bin/env bash
# Starts a built carma image against Postgres with development auth and fails
# unless it serves /health within 30 s and Docker reports the container healthy.
# Usage: scripts/smoke-image.sh IMAGE   (needs CARMA_TEST_DATABASE_URL)
set -euo pipefail

image="${1:?usage: smoke-image.sh IMAGE}"
: "${CARMA_TEST_DATABASE_URL:?CARMA_TEST_DATABASE_URL is required}"
name="carma-smoke-$$"
port=4700 # The image's HEALTHCHECK and default PORT.

cleanup() {
	status=$?
	if [ "$status" -ne 0 ] && docker inspect "$name" >/dev/null 2>&1; then
		echo "::group::container logs"
		docker logs "$name" 2>&1 || true
		echo "::endgroup::"
		docker inspect --format '{{json .State.Health}}' "$name" 2>&1 || true
	fi
	docker rm -f "$name" >/dev/null 2>&1 || true
	exit "$status"
}
trap cleanup EXIT

serves_health() { curl -fs --max-time 2 -o /dev/null "http://127.0.0.1:${port}/health"; }
is_healthy() { [ "$(docker inspect --format '{{.State.Health.Status}}' "$name")" = healthy ]; }

# The container shares the host network, so anything already on the port would
# answer /health in its place.
if curl -s --max-time 2 -o /dev/null "http://127.0.0.1:${port}/"; then
	echo "port ${port} is already in use on the host" >&2
	exit 1
fi

# Host networking lets the container reach a Postgres published on the host.
# The health flags only shorten the image's 30 s interval; the probe command
# itself is the Dockerfile's HEALTHCHECK.
DATABASE_URL="$CARMA_TEST_DATABASE_URL" docker run --detach --name "$name" \
	--network host \
	--env APP_ENV=development --env AUTH_MODE=development --env DATABASE_URL \
	--env METRICS_ADDR=off \
	--health-interval 2s --health-start-period 0s --health-retries 3 \
	"$image" >/dev/null

wait_for() { # wait_for DESCRIPTION SECONDS COMMAND...
	local what="$1" limit="$2" started="$SECONDS"
	shift 2
	while [ $((SECONDS - started)) -lt "$limit" ]; do
		if [ "$(docker inspect --format '{{.State.Running}}' "$name")" != true ]; then
			echo "container exited while waiting for $what" >&2
			return 1
		fi
		if "$@"; then
			echo "$what after $((SECONDS - started))s"
			return 0
		fi
		sleep 1
	done
	echo "timed out waiting for $what (${limit}s)" >&2
	return 1
}

wait_for "/health served" 30 serves_health
wait_for "container healthy" 30 is_healthy
