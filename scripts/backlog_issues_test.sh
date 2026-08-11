#!/bin/sh
# backlog_issues_test.sh — tests for `backlog.sh issues`.
#
# The whole point of the design under test is that deciding *what* to do is
# separable from doing it. The planner reads BACKLOG.md and a snapshot of the
# issues that already exist, and prints a plan; only `--apply` then talks to
# GitHub. That makes the interesting half — which items become issues, which get
# closed, which are left alone — assertable without a network call and without
# creating anything on a public repository.
#
# A sync that gets this wrong is not a cosmetic problem: it either opens a
# duplicate issue for every item on every push, or closes issues for work that is
# still open.
#
# POSIX sh and awk only, like the rest of scripts/.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
script="$root/scripts/backlog.sh"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/segcheck-backlog-test.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT HUP TERM

failures=0
checks=0

fail() {
	failures=$((failures + 1))
	echo "FAIL: $1" >&2
}

# assert_plan <name> <expected-plan-file> <actual-plan-file>
assert_plan() {
	checks=$((checks + 1))
	if diff -u "$2" "$3" >"$tmp/diff" 2>&1; then
		echo "ok   $1"
	else
		fail "$1"
		sed 's/^/       /' "$tmp/diff" >&2
	fi
}

# assert_contains <name> <needle> <file>
assert_contains() {
	checks=$((checks + 1))
	if grep -qF "$2" "$3"; then
		echo "ok   $1"
	else
		fail "$1 — expected to find: $2"
		sed 's/^/       /' "$3" >&2
	fi
}

# assert_absent <name> <needle> <file>
assert_absent() {
	checks=$((checks + 1))
	if grep -qF "$2" "$3"; then
		fail "$1 — did not expect to find: $2"
		sed 's/^/       /' "$3" >&2
	else
		echo "ok   $1"
	fi
}

# ---------------------------------------------------------------------------
# A small backlog covering every state the planner has to tell apart.
# ---------------------------------------------------------------------------
cat >"$tmp/BACKLOG.md" <<'EOF'
# Backlog — fixture

## M1 — Shipped things <!-- ms: target=v0.1.0 phase=shipped -->

- [x] **SC-1 — Already shipped and already closed**: nothing to do here.
  <!-- sc: prio=high size=S labels=parser ver=0.1.0 -->
- [x] **SC-2 — Shipped but its issue is still open**: the sync has to close it.
  <!-- sc: prio=med size=M labels=check ver=0.1.0 -->
- [x] **SC-3 — Shipped and never had an issue**: must not be created now.
  <!-- sc: prio=low size=S labels=docs ver=0.1.0 -->

## M2 — Planned things <!-- ms: target=v0.2.0 phase=now -->

- [ ] **SC-4 — Open with an issue already open**: leave it alone.
  <!-- sc: prio=high size=L labels=check,parser -->
- [ ] **SC-5 — Open with no issue yet**: create it.
  <!-- sc: prio=med size=M labels=output -->
- [ ] **SC-6 — Open but its issue was closed**: reopen it.
  <!-- sc: prio=low size=S labels=tests -->
- [ ] **SC-7 — `--profile apple|dash-if|none`**: a pipe is legal in a title, and
  the real SC-63 is named exactly like this.
  <!-- sc: prio=med size=S labels=cli -->
EOF

# The issues that already exist, as the planner receives them:
#   <id> <tab> <number> <tab> <state> <tab> <title>
# The title is carried so a renamed item can be spotted — and so that the bug
# which opened 44 issues titled "SC-n — " cannot come back unnoticed.
cat >"$tmp/snapshot.tsv" <<'EOF'
SC-1	101	closed	SC-1 — Already shipped and already closed
SC-2	102	open	SC-2 — Shipped but its issue is still open
SC-4	104	open	SC-4 — Open with an issue already open
SC-6	106	closed	SC-6 — Open but its issue was closed
EOF

export BACKLOG_FILE="$tmp/BACKLOG.md"
export BACKLOG_ISSUES_SNAPSHOT="$tmp/snapshot.tsv"

# ---------------------------------------------------------------------------
# The plan
# ---------------------------------------------------------------------------
sh "$script" issues >"$tmp/plan.txt" 2>"$tmp/plan.err" || {
	echo "FAIL: issues exited non-zero" >&2
	sed 's/^/       /' "$tmp/plan.err" >&2
	exit 1
}

# Only the action lines, so the human-readable summary can change freely.
grep -E '^(CREATE|CLOSE|REOPEN|OK|SKIP)' "$tmp/plan.txt" >"$tmp/actions.txt" || true

cat >"$tmp/want.txt" <<'EOF'
OK	SC-1	101
CLOSE	SC-2	102
SKIP	SC-3	-
OK	SC-4	104
CREATE	SC-5	-
REOPEN	SC-6	106
CREATE	SC-7	-
EOF
assert_plan "the plan distinguishes every state" "$tmp/want.txt" "$tmp/actions.txt"

# A dry run must not be able to change anything, so it must not invoke gh.
assert_absent "the dry run does not call gh" "gh issue" "$tmp/plan.txt"

# ---------------------------------------------------------------------------
# Shipped work is never given a new issue
# ---------------------------------------------------------------------------
assert_absent "a shipped item with no issue is not created" "CREATE	SC-3" "$tmp/actions.txt"

# ---------------------------------------------------------------------------
# Idempotence: with every open item already having an open issue, and every
# shipped item a closed one, there is nothing left to do. A sync that is not
# idempotent opens duplicates on every push.
# ---------------------------------------------------------------------------
cat >"$tmp/settled.tsv" <<'EOF'
SC-1	101	closed	SC-1 — Already shipped and already closed
SC-2	102	closed	SC-2 — Shipped but its issue is still open
SC-3	103	closed	SC-3 — Shipped and never had an issue
SC-4	104	open	SC-4 — Open with an issue already open
SC-5	105	open	SC-5 — Open with no issue yet
SC-6	106	open	SC-6 — Open but its issue was closed
SC-7	107	open	SC-7 — `--profile apple|dash-if|none`
EOF
BACKLOG_ISSUES_SNAPSHOT="$tmp/settled.tsv" sh "$script" issues >"$tmp/plan2.txt"
grep -E '^(CREATE|CLOSE|REOPEN)' "$tmp/plan2.txt" >"$tmp/changes2.txt" || true
checks=$((checks + 1))
if [ -s "$tmp/changes2.txt" ]; then
	fail "a settled backlog still wanted changes"
	sed 's/^/       /' "$tmp/changes2.txt" >&2
else
	echo "ok   a settled backlog is a no-op"
fi

# ---------------------------------------------------------------------------
# The title. This is the assertion that was missing, and its absence is why a
# field-index slip in issue_meta — reading `ver` where the title lives — opened 44
# public issues named "SC-n — " with nothing after the dash. `ver` is empty for
# every open item, so the mistake was invisible to a plan-level or body-level test.
# ---------------------------------------------------------------------------
sh "$script" issues --title SC-5 >"$tmp/title.txt"
assert_contains "the title carries the item's name" "SC-5 — Open with no issue yet" "$tmp/title.txt"
# The bug's exact signature: a line that ends at the em dash with nothing after it.
checks=$((checks + 1))
if grep -qE ' \xe2\x80\x94[[:space:]]*$' "$tmp/title.txt"; then
	fail "the title ends at the dash with no name after it" "$tmp/title.txt"
else
	echo "ok   the title is not left dangling after the dash"
fi

# A pipe in the title must survive intact: it was the field separator until SC-63
# proved a title can contain one, and the issue was published truncated.
sh "$script" issues --title SC-7 >"$tmp/pipe.txt"
assert_contains "a pipe in the title survives" 'SC-7 — `--profile apple|dash-if|none`' "$tmp/pipe.txt"

# An issue whose title has drifted from the item is corrected rather than left, and
# rather than being closed and reopened.
cat >"$tmp/drifted.tsv" <<'EOF'
SC-4	104	open	SC-4 — Some older name
SC-5	105	open	SC-5 — Open with no issue yet
SC-6	106	open	SC-6 — Open but its issue was closed
EOF
BACKLOG_ISSUES_SNAPSHOT="$tmp/drifted.tsv" sh "$script" issues >"$tmp/plan5.txt"
grep -E '^(CREATE|CLOSE|REOPEN|OK|SKIP|RETITLE)' "$tmp/plan5.txt" >"$tmp/actions5.txt" || true
assert_contains "a drifted title is corrected" "RETITLE	SC-4	104" "$tmp/actions5.txt"
assert_absent "correcting a title does not close the issue" "CLOSE	SC-4" "$tmp/actions5.txt"
assert_contains "a matching title is left alone" "OK	SC-5	105" "$tmp/actions5.txt"

# ---------------------------------------------------------------------------
# The milestone filter, so a first run can be limited deliberately rather than
# opening the whole backlog at once.
# ---------------------------------------------------------------------------
sh "$script" issues --milestones M2 >"$tmp/plan3.txt"
grep -E '^(CREATE|CLOSE|REOPEN|OK|SKIP)' "$tmp/plan3.txt" >"$tmp/actions3.txt" || true
assert_contains "the filter keeps its milestone" "CREATE	SC-5" "$tmp/actions3.txt"
assert_absent "the filter excludes other milestones" "SC-2" "$tmp/actions3.txt"

# ---------------------------------------------------------------------------
# The created issue carries what a reader needs: the backlog prose, the id, the
# milestone and its target.
# ---------------------------------------------------------------------------
sh "$script" issues --body SC-5 >"$tmp/body.txt"
assert_contains "the body carries the item's prose" "create it." "$tmp/body.txt"
assert_contains "the body names the id" "SC-5" "$tmp/body.txt"
assert_contains "the body names the milestone" "M2 — Planned things" "$tmp/body.txt"
assert_contains "the body names the release target" "v0.2.0" "$tmp/body.txt"
assert_contains "the body points back at BACKLOG.md" "BACKLOG.md" "$tmp/body.txt"

# ---------------------------------------------------------------------------
# An unparseable backlog must stop the sync rather than plan against half of it:
# a partial plan would close issues for items it simply failed to read.
# ---------------------------------------------------------------------------
cat >"$tmp/bad.md" <<'EOF'
# Backlog — broken fixture

## M1 — Things <!-- ms: target=v0.1.0 phase=now -->

- [ ] **SC-1 — Missing its metadata comment**: no trailing sc comment at all.
EOF
checks=$((checks + 1))
if BACKLOG_FILE="$tmp/bad.md" sh "$script" issues >"$tmp/plan4.txt" 2>&1; then
	fail "a malformed backlog was planned against anyway"
	sed 's/^/       /' "$tmp/plan4.txt" >&2
else
	echo "ok   a malformed backlog stops the sync"
fi

echo
if [ "$failures" -gt 0 ]; then
	echo "$failures of $checks checks failed" >&2
	exit 1
fi
echo "all $checks checks passed"
