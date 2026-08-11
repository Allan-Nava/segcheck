#!/bin/sh
# coverage_test.sh — tests for the coverage ratchet's decision.
#
# Measuring coverage needs `go test`; deciding what the number means does not.
# COVERAGE_ACTUAL injects a measurement so the decision table can be asserted
# directly, which is the half with the rules in it:
#
#   below the floor          fail — this is the regression SC-48 exists to catch
#   at the floor             pass
#   a little above           pass, so a one-statement wobble does not nag
#   well above               fail, asking for the floor to be raised: a ratchet
#                            that never tightens is just a floor
#   no floor recorded        fail with the command that records one
#
# POSIX sh and awk only, like the rest of scripts/.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
script="$root/scripts/coverage.sh"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/segcheck-coverage-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT HUP TERM

failures=0
checks=0

fail() {
	failures=$((failures + 1))
	echo "FAIL: $1" >&2
	[ -n "${2:-}" ] && sed 's/^/       /' "$2" >&2
	return 0
}

# expect_pass <name> <actual> <floor>
expect_pass() {
	checks=$((checks + 1))
	echo "$3" >"$tmp/floor"
	if COVERAGE_ACTUAL="$2" COVERAGE_FLOOR_FILE="$tmp/floor" \
		sh "$script" check >"$tmp/out" 2>&1; then
		echo "ok   $1"
	else
		fail "$1 — expected to pass with actual=$2 floor=$3" "$tmp/out"
	fi
}

# expect_fail <name> <actual> <floor> <needle>
expect_fail() {
	checks=$((checks + 1))
	echo "$3" >"$tmp/floor"
	if COVERAGE_ACTUAL="$2" COVERAGE_FLOOR_FILE="$tmp/floor" \
		sh "$script" check >"$tmp/out" 2>&1; then
		fail "$1 — expected to fail with actual=$2 floor=$3" "$tmp/out"
	elif ! grep -qF "$4" "$tmp/out"; then
		fail "$1 — failed, but the message did not mention: $4" "$tmp/out"
	else
		echo "ok   $1"
	fi
}

# ---------------------------------------------------------------------------
# The regression SC-48 is about: a commit that lowers coverage.
# ---------------------------------------------------------------------------
expect_fail "a drop below the floor fails" 98.40 99.64 "fell from 99.64% to 98.40%"
# One statement's worth. A gate that tolerates "small" drops accumulates them.
expect_fail "even a small drop fails" 99.60 99.64 "fell from 99.64% to 99.60%"

# The floor itself is fine, and so is standing still.
expect_pass "exactly at the floor passes" 99.64 99.64
expect_pass "a hair above passes" 99.68 99.64

# ---------------------------------------------------------------------------
# A ratchet has to tighten, or the next regression is measured against a stale
# number and a real loss can hide inside the slack.
# ---------------------------------------------------------------------------
expect_fail "a real gain must be locked in" 99.95 99.64 "update"
expect_fail "a large gain says the new figure" 100.00 95.00 "100.00"

# ---------------------------------------------------------------------------
# With no floor recorded there is nothing to compare against, and silently
# passing would mean the gate does nothing on a fresh clone.
# ---------------------------------------------------------------------------
checks=$((checks + 1))
if COVERAGE_ACTUAL=99.64 COVERAGE_FLOOR_FILE="$tmp/absent" \
	sh "$script" check >"$tmp/out" 2>&1; then
	fail "a missing floor file passed" "$tmp/out"
elif ! grep -qF "update" "$tmp/out"; then
	fail "a missing floor file did not say how to record one" "$tmp/out"
else
	echo "ok   a missing floor file fails with the command to record one"
fi

# A floor file with rubbish in it is the same situation: it must not be read as
# zero, which would make every run pass.
checks=$((checks + 1))
printf 'not a number\n' >"$tmp/floor"
if COVERAGE_ACTUAL=10.00 COVERAGE_FLOOR_FILE="$tmp/floor" \
	sh "$script" check >"$tmp/out" 2>&1; then
	fail "an unreadable floor was treated as zero" "$tmp/out"
else
	echo "ok   an unreadable floor fails rather than passing everything"
fi

# ---------------------------------------------------------------------------
# update records the measurement, and is what the failures above tell you to run.
# ---------------------------------------------------------------------------
checks=$((checks + 1))
: >"$tmp/floor2"
COVERAGE_ACTUAL=99.95 COVERAGE_FLOOR_FILE="$tmp/floor2" sh "$script" update >"$tmp/out" 2>&1
if grep -qx '99.95' "$tmp/floor2"; then
	echo "ok   update records the current figure"
else
	fail "update did not record the figure" "$tmp/floor2"
fi

# And after updating, check passes — the loop the developer actually follows.
checks=$((checks + 1))
if COVERAGE_ACTUAL=99.95 COVERAGE_FLOOR_FILE="$tmp/floor2" sh "$script" check >"$tmp/out" 2>&1; then
	echo "ok   check passes once the floor is updated"
else
	fail "check still failed after update" "$tmp/out"
fi

# ---------------------------------------------------------------------------
# The ratchet only goes up: update must refuse to record a figure below the
# floor, or a regression could be laundered into the baseline.
# ---------------------------------------------------------------------------
checks=$((checks + 1))
echo "99.64" >"$tmp/floor3"
if COVERAGE_ACTUAL=90.00 COVERAGE_FLOOR_FILE="$tmp/floor3" \
	sh "$script" update >"$tmp/out" 2>&1; then
	fail "update lowered the floor" "$tmp/out"
elif ! grep -qx '99.64' "$tmp/floor3"; then
	fail "update overwrote the floor with a lower figure" "$tmp/floor3"
else
	echo "ok   update refuses to lower the floor"
fi

echo
if [ "$failures" -gt 0 ]; then
	echo "$failures of $checks checks failed" >&2
	exit 1
fi
echo "all $checks checks passed"
