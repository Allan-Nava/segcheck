#!/bin/sh
# coverage.sh — measure total statement coverage and enforce a floor.
#
# Two things here are not what `go test -cover ./...` does, and both matter.
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
# POSIX sh and awk only, like the rest of scripts/: the zero-dependency rule
# covers the tooling too.

set -eu

FLOOR="${1:-0}"
PROFILE="${COVERAGE_PROFILE:-cover.out}"

go test -count=1 -coverpkg=./... -coverprofile="$PROFILE" ./... >/dev/null

awk -v floor="$FLOOR" '
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
        if (total == 0) {
            print "coverage: no statements found" > "/dev/stderr"
            exit 1
        }
        pct = 100 * covered / total
        printf "coverage: %d/%d statements = %.2f%%\n", covered, total, pct
        if (floor + 0 > 0 && pct < floor + 0) {
            printf "::error::coverage %.2f%% is below the %s%% floor\n", pct, floor > "/dev/stderr"
            exit 1
        }
    }
' "$PROFILE"
