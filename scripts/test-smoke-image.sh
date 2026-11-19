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

# Every watchdog appends the pid of the sleep it starts to this file, so the
# test only ever looks at (and signals) sleeps that its own watchdogs started.
sleeper_log="$work/sleeper-pids"
: >"$sleeper_log"

# own_sleepers: print the pids logged in $sleeper_log that are still running
# `sleep $case_limit`. The command line is checked too, so a logged pid the
# system has since reused for another process is not mistaken for a sleeper.
# ps -p -o is POSIX, so this works on both Linux and macOS.
own_sleepers() {
	local pid
	while read -r pid; do
		if ps -p "$pid" -o args= 2>/dev/null | awk -v limit="$case_limit" \
			'$1 ~ /(^|\/)sleep$/ && $2 == limit { found = 1 } END { exit !found }'; then
			echo "$pid"
		fi
	done <"$sleeper_log"
}

ready_tries=200 # Polls of 0.05s, about ten seconds, for a watchdog to register.
mark_ready() { : >"$work/watchdog-ready"; } # The watchdog's readiness signal.

# run_bounded LIMIT OUTFILE COMMAND...: run COMMAND with its output in OUTFILE
# and return its status, or 124 if it was still running after LIMIT seconds.
# A background sleeper kills the command's whole process group (job control
# gives it one), so stubs it spawned cannot linger. The watchdog gets its own
# group too. The command is not waited on until the watchdog has logged its
# sleep, so a command that ends at once cannot send TERM before the sleep is
# registered in $sleeper_log. If the watchdog never registers, both groups are
# killed with SIGKILL, which skips the watchdog's trap but still takes its
# sleep down, and it returns 125. Portable bash only.
run_bounded() {
	local limit="$1" outfile="$2" pid watcher status=0 tries=0
	shift 2
	rm -f "$work/timed-out" "$work/watchdog-ready"
	set -m
	"$@" >"$outfile" 2>&1 &
	pid=$!
	(
		# On TERM, take the sleep down too and leave without killing anything.
		# jobs -p lists the sleep as soon as it is forked, so this holds even
		# if TERM lands before $! could have been saved.
		trap 'kill $(jobs -p) 2>/dev/null; exit 0' TERM
		sleep "$limit" &
		echo "$!" >>"$sleeper_log"
		mark_ready
		wait
		: >"$work/timed-out"
		kill -KILL -- "-$pid"
	) >/dev/null 2>&1 &
	watcher=$!
	set +m
	until [ -e "$work/watchdog-ready" ]; do
		tries=$((tries + 1))
		if [ "$tries" -gt "$ready_tries" ]; then
			kill -KILL -- "-$pid" "-$watcher" 2>/dev/null || true
			wait "$pid" "$watcher" 2>/dev/null || true
			return 125
		fi
		sleep 0.05
	done
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
	if [ "$status" -eq 125 ]; then
		echo "FAIL $name: watchdog did not start"
		failures=$((failures + 1))
	elif [ "$status" -eq 124 ]; then
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

# Cleanup: a case that ends before its limit must take the watchdog's sleep
# down with it. Each watchdog logs the pid of its sleep, and any of those still
# running afterwards was leaked by the watchdog. run_bounded holds back its
# cleanup signal until the watchdog has logged, so a case that finishes at once
# cannot signal before its sleep is registered; the case itself may still end
# first. Only logged pids are checked or signalled, so no other process on the
# machine is touched.
stub curl 'exit 0'
stub perl 'exit 1'
run_case "watchdog cleanup case (passing)" zero "container healthy after"
stub curl 'rm -f "$CARMA_SMOKE_UPTIME_FILE"; exit 0'
run_case "watchdog cleanup case (failing)" nonzero \
	"could not read the monotonic clock while waiting for /health served"
# The watchdog's TERM trap signals the sleep and exits without waiting for it,
# so give a sleep that was just signalled a moment to die before calling it a leak.
leaked=$(own_sleepers)
for _ in 1 2 3; do
	[ -n "$leaked" ] || break
	sleep 1
	leaked=$(own_sleepers)
done
if [ -z "$leaked" ]; then
	echo "ok   watchdog sleep exits when a case finishes early"
else
	echo "FAIL watchdog sleep exits when a case finishes early: leaked sleep pid(s): $leaked"
	# shellcheck disable=SC2086 # one pid per word
	kill $leaked 2>/dev/null || true
	failures=$((failures + 1))
fi

# A watchdog that starts its sleep but never reports ready is killed with
# SIGKILL, which skips its TERM trap. The sleep must go down with it.
mark_ready() { :; }
ready_tries=20
startup_out=$(run_case "watchdog startup" zero "" 2>&1)
mark_ready() { : >"$work/watchdog-ready"; }
ready_tries=200
if ! grep -qF "FAIL watchdog startup: watchdog did not start" <<<"$startup_out"; then
	echo "FAIL watchdog startup failure is reported: output was:"
	echo "$startup_out"
	failures=$((failures + 1))
else
	leaked=$(own_sleepers)
	for _ in 1 2 3; do
		[ -n "$leaked" ] || break
		sleep 1
		leaked=$(own_sleepers)
	done
	if [ -z "$leaked" ]; then
		echo "ok   watchdog startup failure takes its sleep down"
	else
		echo "FAIL watchdog startup failure takes its sleep down: leaked sleep pid(s): $leaked"
		# shellcheck disable=SC2086 # one pid per word
		kill $leaked 2>/dev/null || true
		failures=$((failures + 1))
	fi
fi

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
