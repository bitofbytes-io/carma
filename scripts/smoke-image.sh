#!/usr/bin/env bash
# Starts a built carma image against Postgres with development auth and fails
# unless it serves /health within 30 s and Docker reports the container healthy.
# Usage: scripts/smoke-image.sh IMAGE   (needs CARMA_TEST_DATABASE_URL)
# The database must be a disposable test database: the server runs session
# cleanup and background jobs against it as soon as it starts. Without the
# variable a local run skips; CI (CI set) fails.
set -euo pipefail

image="${1:?usage: smoke-image.sh IMAGE}"
if [ -z "${CARMA_TEST_DATABASE_URL:-}" ]; then
	if [ -n "${CI:-}" ]; then
		echo "CARMA_TEST_DATABASE_URL is required in CI" >&2
		exit 1
	fi
	echo "skipping image smoke test: set CARMA_TEST_DATABASE_URL to a disposable test database to run it"
	exit 0
fi
# Every probe is bounded by timeout(1) so the 30 s budget holds even when
# docker stalls, so refuse to run without it (macOS: brew install coreutils).
if ! command -v timeout >/dev/null 2>&1; then
	echo "timeout(1) is required to bound the smoke test's probes; install coreutils" >&2
	exit 1
fi
# The PID alone can repeat across runs, so identify this run by a unique id and
# only touch containers carrying its label.
run_id="$(date +%s)-$$-$RANDOM$RANDOM"
name="carma-smoke-$run_id"
label="carma.smoke.run=$run_id"
port=4700 # The image's HEALTHCHECK and default PORT.
limit_ms=30000

# Each docker call in cleanup is bounded on its own so a stalled daemon cannot
# hang the exit path; the original exit status is kept whatever they do.
# Only a container labelled with this run's id is collected or removed, so an
# older container left behind is never touched.
cleanup_limit=5
cleanup() {
	status=$?
	trap - EXIT
	local owned
	if ! owned=$(timeout "$cleanup_limit" docker ps --all --quiet --no-trunc --filter "label=$label" 2>/dev/null); then
		echo "could not look up this run's container $name; remove it manually: docker rm -f $name" >&2
		exit "$status"
	fi
	if [ -n "$owned" ]; then
		if [ "$status" -ne 0 ]; then
			echo "::group::container logs"
			timeout "$cleanup_limit" docker logs "$owned" 2>&1 || true
			echo "::endgroup::"
			timeout "$cleanup_limit" docker inspect --format '{{json .State.Health}}' "$owned" 2>&1 || true
		fi
		# A failed removal is reported but does not change the exit status; the
		# leftover container would keep holding the port for the next run.
		if ! timeout "$cleanup_limit" docker rm -f "$owned" >/dev/null 2>&1; then
			echo "could not remove container $name; remove it manually: docker rm -f $name" >&2
		fi
	fi
	exit "$status"
}
trap cleanup EXIT

# Milliseconds on a monotonic clock, so a wall-clock adjustment (NTP step,
# manual change) during the run cannot shorten or extend the 30 s budget.
# /proc/uptime (Linux) is monotonic; elsewhere (macOS) perl's CLOCK_MONOTONIC is.
# EPOCHREALTIME and SECONDS follow the wall clock, so they are not used.
now_ms() {
	local up _
	if [ -r /proc/uptime ] && read -r up _ </proc/uptime && [ -n "$up" ]; then
		up="${up/[.,]/}" # centiseconds; the fraction always has two digits
		echo $((10#$up * 10))
	else
		perl -MTime::HiRes=clock_gettime,CLOCK_MONOTONIC -e 'printf "%d\n", clock_gettime(CLOCK_MONOTONIC) * 1000'
	fi
}

fmt_s() { printf '%d.%03d' $(($1 / 1000)) $(($1 % 1000)); } # milliseconds -> seconds

# bounded SECONDS COMMAND...: run COMMAND, killing it after SECONDS.
bounded() { timeout "$@"; }

# The container shares the host network, so anything already listening on the
# port would answer /health in its place. Connect directly so stalled and
# non-HTTP listeners count too.
for host in 127.0.0.1 ::1; do
	# shellcheck disable=SC2016 # $0 and $1 are meant for the inner bash.
	if bounded 2 bash -c 'exec 3<>"/dev/tcp/$0/$1"' "$host" "$port" 2>/dev/null; then
		echo "port ${port} is already in use on the host" >&2
		exit 1
	fi
done

# Host networking lets the container reach a Postgres published on the host.
# The health flags only shorten the image's 30 s interval; the probe command
# itself is the Dockerfile's HEALTHCHECK.
launch_limit=20
if ! DATABASE_URL="$CARMA_TEST_DATABASE_URL" timeout "$launch_limit" docker run --detach --name "$name" --label "$label" \
	--network host \
	--env APP_ENV=development --env AUTH_MODE=development --env DATABASE_URL \
	--env METRICS_ADDR=off \
	--health-interval 2s --health-start-period 0s --health-retries 3 \
	"$image" >/dev/null; then
	echo "docker run did not start the container within ${launch_limit}s (or failed)" >&2
	exit 1
fi

# One 30 s budget covers both checks. Every command below is bounded by the
# time left, and the deadline is rechecked before a success is accepted, so
# nothing past 30 s can pass.
#
# The clock is read into its own checked variable, never inside an arithmetic
# expansion: a read that fails without output would otherwise vanish into the
# sum (limit_ms - ( - started_ms)) and stretch the budget. A failed or
# non-numeric read aborts the wait instead.
clock_ms() { # clock_ms VAR: set VAR to the monotonic clock in ms, or fail
	local reading # not named like any VAR a caller passes: locals shadow it
	reading=$(now_ms) || return 1
	[[ "$reading" =~ ^[0-9]+$ ]] || return 1
	printf -v "$1" '%s' "$reading"
}

started_ms=
clock_ms started_ms || { echo "could not read the monotonic clock" >&2; exit 1; }

expired() {
	echo "timed out waiting for $1 (30s)" >&2
	return 1
}

# refresh_left WHAT: set the caller's `left` to the ms remaining. Fails, with
# a message, when the clock cannot be read or the budget is spent.
refresh_left() {
	local now
	if ! clock_ms now; then
		echo "could not read the monotonic clock while waiting for $1" >&2
		return 1
	fi
	left=$((limit_ms - (now - started_ms)))
	[ "$left" -gt 0 ] || { expired "$1"; return 1; }
}

serves_health() { curl -fs --noproxy '*' --max-time "$1" -o /dev/null "http://127.0.0.1:${port}/health"; }
is_healthy() { [ "$(bounded "$1" docker inspect --format '{{.State.Health.Status}}' "$name")" = healthy ]; }

wait_for() { # wait_for DESCRIPTION PROBE; PROBE gets the seconds left
	local what="$1" probe="$2" left running
	while :; do
		refresh_left "$what" || return 1
		running=$(bounded "$(fmt_s "$left")" docker inspect --format '{{.State.Running}}' "$name" 2>/dev/null || true)
		refresh_left "$what" || return 1
		if [ "$running" != true ]; then
			echo "container exited or could not be inspected while waiting for $what" >&2
			return 1
		fi
		if "$probe" "$(fmt_s "$left")" 2>/dev/null; then
			refresh_left "$what" || return 1
			echo "$what after $(fmt_s $((limit_ms - left)))s"
			return 0
		fi
		refresh_left "$what" || return 1
		sleep "$(fmt_s $((left < 1000 ? left : 1000)))"
	done
}

wait_for "/health served" serves_health
wait_for "container healthy" is_healthy
