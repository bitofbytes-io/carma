#!/usr/bin/env bash
# Regression tests for scripts/smoke-image.sh's clock handling. docker, curl,
# timeout, perl and bash are replaced by stubs on PATH, so no Docker or
# Postgres is involved. The fake clock never advances, so each case also runs
# under a watchdog: a regression that keeps polling would otherwise hang
# `make test`. The passing cases return at once; only the watchdog probe
# really sleeps (the script's polling loop) until the watchdog kills it.
# The watchdog is pure bash, so it needs no coreutils timeout(1), which stock
# macOS lacks. Usage: scripts/test-smoke-image.sh
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/smoke-image.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
stubs="$work/bin"
mkdir "$stubs"

case_limit=20 # Seconds a case may run; a healthy one takes well under one.

stub() { # stub NAME BODY...: write an executable stub into the PATH dir
	local name="$1"
	shift
	printf '#!/bin/sh\n%s\n' "$*" >"$stubs/$name"
	chmod +x "$stubs/$name"
}

# timeout drops its duration and runs the command, which resolves to a stub.
stub timeout 'shift; exec "$@"'
# The host port check (bash -c 'exec 3<>/dev/tcp/...') must find nothing listening.
stub bash 'exit 1'
stub docker '
case "$1" in
run) echo container-id ;;
ps) ;;
inspect) case "$3" in *Health*) echo healthy ;; *) echo true ;; esac ;;
esac'

failures=0

# run_bounded LIMIT OUTFILE COMMAND...: run COMMAND with its output in OUTFILE
# and return its status, or 124 if it was still running after LIMIT seconds.
# A background sleeper kills the command's whole process group (job control
# gives it one), so stubs it spawned cannot linger. Portable bash only.
run_bounded() {
	local limit="$1" outfile="$2" pid watcher status=0
	shift 2
	rm -f "$work/timed-out"
	set -m
	"$@" >"$outfile" 2>&1 &
	pid=$!
	set +m
	(
		# On TERM, take the sleep down too and leave without killing anything.
		# jobs -p lists the sleep as soon as it is forked, so this holds even
		# if TERM lands before $! could have been saved.
		trap 'kill $(jobs -p) 2>/dev/null; exit 0' TERM
		sleep "$limit" &
		wait
		: >"$work/timed-out"
		kill -KILL -- "-$pid"
	) >/dev/null 2>&1 &
	watcher=$!
	wait "$pid" 2>/dev/null || status=$?
	kill "$watcher" 2>/dev/null || true
	wait "$watcher" 2>/dev/null || true
	if [ -e "$work/timed-out" ]; then
		return 124
	fi
	return "$status"
}

# run_case NAME zero|nonzero PATTERN: run the script with the current stubs.
# The exit status must be zero or nonzero as given, and PATTERN (may be empty)
# must appear in its output. A case still running after $case_limit seconds is
# killed and fails whatever its expected status.
run_case() {
	local name="$1" want="$2" pattern="$3" status=0 out
	echo "100.00 400.00" >"$work/uptime"
	run_bounded "$case_limit" "$work/out" env PATH="$stubs:$PATH" \
		CARMA_SMOKE_UPTIME_FILE="$work/uptime" \
		CARMA_TEST_DATABASE_URL=postgres://stub CI= \
		"$BASH" "$script" stub-image || status=$?
	out=$(<"$work/out")
	if [ "$status" -eq 124 ]; then
		echo "FAIL $name: still running after ${case_limit}s (watchdog)"
		echo "$out"
		failures=$((failures + 1))
	elif { [ "$want" = zero ] && [ "$status" -ne 0 ]; } ||
		{ [ "$want" = nonzero ] && [ "$status" -eq 0 ]; }; then
		echo "FAIL $name: exit status $status, want $want"
		echo "$out"
		failures=$((failures + 1))
	elif [ -n "$pattern" ] && ! grep -qF -- "$pattern" <<<"$out"; then
		echo "FAIL $name: output lacks '$pattern'"
		echo "$out"
		failures=$((failures + 1))
	else
		echo "ok   $name"
	fi
}

# Control: a readable clock lets the script pass, so the failures below come
# from the clock and not from the stubs.
stub curl 'exit 0'
stub perl 'exit 1'
run_case "readable clock passes" zero "container healthy after"

# The probe succeeds, then the clock stops being readable: the uptime file is
# gone and perl fails without output. The success must not be accepted.
stub curl 'rm -f "$CARMA_SMOKE_UPTIME_FILE"; exit 0'
stub perl 'exit 1'
run_case "clock read fails right after a successful probe" nonzero \
	"could not read the monotonic clock while waiting for /health served"

# Same, but the fallback clock prints something that is not a number.
stub perl 'echo not-a-number'
run_case "clock read returns a non-numeric value" nonzero \
	"could not read the monotonic clock while waiting for /health served"

# The watchdog itself: a probe that never succeeds under the frozen clock polls
# forever, so the case must be killed and reported rather than hang. The case
# runs in a subshell so its expected failure is not counted.
stub curl 'exit 1'
stub perl 'exit 1'
case_limit=2
watchdog_out=$(run_case "watchdog probe" zero "" 2>&1)
if grep -qF "FAIL watchdog probe: still running after 2s (watchdog)" <<<"$watchdog_out"; then
	echo "ok   watchdog stops a case that never finishes"
else
	echo "FAIL watchdog stops a case that never finishes: output was:"
	echo "$watchdog_out"
	failures=$((failures + 1))
fi

[ "$failures" -eq 0 ]
