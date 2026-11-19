#!/usr/bin/env bash
# Regression tests for scripts/smoke-image.sh's clock handling. docker, curl,
# timeout, perl and bash are replaced by stubs on PATH, so no Docker, Postgres
# or sleeping is involved. Usage: scripts/test-smoke-image.sh
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/smoke-image.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
stubs="$work/bin"
mkdir "$stubs"

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

# run_case NAME zero|nonzero PATTERN: run the script with the current stubs.
# The exit status must be zero or nonzero as given, and PATTERN (may be empty)
# must appear in its output.
run_case() {
	local name="$1" want="$2" pattern="$3" status=0 out
	echo "100.00 400.00" >"$work/uptime"
	out=$(PATH="$stubs:$PATH" CARMA_SMOKE_UPTIME_FILE="$work/uptime" \
		CARMA_TEST_DATABASE_URL=postgres://stub CI= \
		"$BASH" "$script" stub-image 2>&1) || status=$?
	if { [ "$want" = zero ] && [ "$status" -ne 0 ]; } ||
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

[ "$failures" -eq 0 ]
