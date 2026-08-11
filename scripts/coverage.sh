#!/bin/sh
# coverage.sh — measure total statement coverage, and ratchet it (SC-48).
#
#   scripts/coverage.sh report    measure and print
#   scripts/coverage.sh check     fail if coverage dropped, or if a gain is
#                                 unrecorded (the CI gate)
#   scripts/coverage.sh update    record the current figure as the new floor
#
# The recorded floor lives in scripts/coverage.floor and is committed, the same
# way ROADMAP.md is generated-but-committed: the check is then a comparison
# against a value in the tree rather than a number hard-coded in a workflow, so
# raising it is a reviewable diff.
#
# It only goes up. A drop fails, which is the regression this exists to catch —
# a check merged without its test shows up in the build rather than in a review
# three weeks later. A gain beyond the tolerance also fails, asking to be
# recorded: a ratchet that never tightens is just a floor, and the slack it
# leaves is room for a real loss to hide in.
#
# Two things about the measurement are not what `go test -cover ./...` does, and
# both matter.
#
# 1. `-coverpkg=./...` credits every package for the code it exercises. Without
#    it `internal/media/mediatest` reports 0.0% because it has no test files of
#    its own, even though every parser test runs through it — an artefact that
#    made the total read about six points lower than the truth.
#
# 2. That profile carries one copy of each block per test binary, and
#    `go tool cover -func` sums them instead of merging, so a block covered by
#    one binary out of seven reads as 1/7 covered. The awk below merges by
#    position first: a block is covered if any binary reached it.
#
# `-count=1` is required. A cached package result carries the line numbers the
# source had when it was cached, so a stale entry mixes two versions of a file
# into one profile and the percentages become nonsense.
#
# COVERAGE_ACTUAL injects a measurement, so the decision can be tested without
# running the suite — see scripts/coverage_test.sh.
#
# POSIX sh and awk only, like the rest of scripts/: the zero-dependency rule
# covers the tooling too.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
floor_file="${COVERAGE_FLOOR_FILE:-$root/scripts/coverage.floor}"
profile="${COVERAGE_PROFILE:-$root/cover.out}"
# How far coverage may rise before the floor has to be raised with it. 0.25 of a
# percentage point is about six statements at the current size — enough that
# removing a couple of uncovered lines does not nag, small enough that a real
# improvement gets locked in.
tolerance="${COVERAGE_TOLERANCE:-0.25}"

# measure prints `<covered> <total> <pct>`.
measure() {
	if [ -n "${COVERAGE_ACTUAL:-}" ]; then
		printf '0 0 %s\n' "$COVERAGE_ACTUAL"
		return
	fi
	go test -count=1 -coverpkg=./... -coverprofile="$profile" ./... >/dev/null
	awk '
		/^mode:/ { next }
		{
			# $1 is file:startLine.startCol,endLine.endCol — the block identity.
			if (!($1 in stmts)) stmts[$1] = $2
			hits[$1] += $3
		}
		END {
			for (block in stmts) {
				total += stmts[block]
				if (hits[block] > 0) covered += stmts[block]
			}
			if (total == 0) { print "coverage: no statements found" > "/dev/stderr"; exit 1 }
			printf "%d %d %.2f\n", covered, total, 100 * covered / total
		}
	' "$profile"
}

read_floor() {
	if [ ! -f "$floor_file" ]; then
		echo ""
		return
	fi
	# Exactly one number on one line, or nothing: an unreadable floor must not
	# be read as zero, which would make every run pass.
	awk 'NR == 1 && $0 ~ /^[0-9]+(\.[0-9]+)?$/ { print $0; found = 1 } END { exit !found }' \
		"$floor_file" 2>/dev/null || echo ""
}

human() {
	if [ "$1" = "0" ] && [ "$2" = "0" ]; then
		printf 'coverage: %s%%\n' "$3"
	else
		printf 'coverage: %s/%s statements = %s%%\n' "$1" "$2" "$3"
	fi
}

set -- ${1:-report} "${2:-}"
cmd=$1

m=$(measure)
covered=$(echo "$m" | cut -d' ' -f1)
total=$(echo "$m" | cut -d' ' -f2)
pct=$(echo "$m" | cut -d' ' -f3)

case "$cmd" in
report)
	human "$covered" "$total" "$pct"
	floor=$(read_floor)
	[ -n "$floor" ] && printf 'floor:    %s%%\n' "$floor"
	;;

check)
	human "$covered" "$total" "$pct"
	floor=$(read_floor)
	if [ -z "$floor" ]; then
		echo "::error::no coverage floor recorded in $floor_file" >&2
		echo "run: scripts/coverage.sh update   (and commit the result)" >&2
		exit 1
	fi
	printf 'floor:    %s%%\n' "$floor"

	verdict=$(awk -v a="$pct" -v f="$floor" -v t="$tolerance" 'BEGIN {
		if (a + 0 < f + 0) { print "below"; exit }
		if (a + 0 > f + 0 + t + 0) { print "gained"; exit }
		print "ok"
	}')
	case "$verdict" in
	below)
		echo "::error::coverage fell from $floor% to $pct% — a commit lowered it" >&2
		echo "cover the new code, or say why the floor should move and run: scripts/coverage.sh update" >&2
		exit 1
		;;
	gained)
		echo "::error::coverage rose to $pct%, above the recorded floor of $floor% — lock it in" >&2
		echo "run: scripts/coverage.sh update   (and commit the result)" >&2
		exit 1
		;;
	*)
		echo "coverage holds at the floor"
		;;
	esac
	;;

update)
	floor=$(read_floor)
	if [ -n "$floor" ]; then
		if awk -v a="$pct" -v f="$floor" 'BEGIN { exit !(a + 0 < f + 0) }'; then
			echo "::error::refusing to lower the floor from $floor% to $pct%" >&2
			echo "the ratchet only goes up: cover the regression instead" >&2
			exit 1
		fi
	fi
	printf '%s\n' "$pct" >"$floor_file"
	human "$covered" "$total" "$pct"
	echo "recorded $pct% as the floor in ${floor_file#"$root/"}"
	;;

*)
	echo "usage: scripts/coverage.sh [report|check|update]" >&2
	exit 2
	;;
esac
