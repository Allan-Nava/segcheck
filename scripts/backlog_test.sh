#!/bin/sh
# backlog_test.sh — tests for `backlog.sh lint`, `roadmap`, `check`, `stats`
# and `next`. The issue planner has its own file, backlog_issues_test.sh.
#
# The test-first rule in AGENTS.md covers the tooling, and this is the one place
# it was not applied. That is how the generator shipped a bug that split a table
# row in three the first time an item title contained a `|` (SC-63): the
# roadmap's format is a markdown table, an item title is free text, and nothing
# had ever put the two together in a test. A generator nobody tests is one that
# fails on the next character a title happens to contain, and it fails by
# producing a document that still looks plausible.
#
# The stakes on `lint` are the mirror image: it is a CI gate, so a rule that
# silently stopped firing lets a malformed backlog through — which is how ids
# stop being stable, and ids being stable forever is the one promise BACKLOG.md
# makes to every commit message and CHANGELOG entry that references one.
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

# assert_fails <name> <backlog-file> <needle>
# The backlog is malformed and lint has to say so, naming the problem.
assert_fails() {
	checks=$((checks + 1))
	if BACKLOG_FILE="$2" sh "$script" lint >"$tmp/lint.out" 2>&1; then
		fail "$1 — lint accepted it"
		sed 's/^/       /' "$tmp/lint.out" >&2
		return
	fi
	if grep -qF "$3" "$tmp/lint.out"; then
		echo "ok   $1"
	else
		fail "$1 — lint failed for the wrong reason, wanted: $3"
		sed 's/^/       /' "$tmp/lint.out" >&2
	fi
}

# ---------------------------------------------------------------------------
# A backlog holding the two characters that have broken the generator before and
# the two orderings the roadmap has to get right.
# ---------------------------------------------------------------------------
cat >"$tmp/BACKLOG.md" <<'EOF'
# Backlog — fixture

## M1 — First things <!-- ms: target=v0.1.0 phase=now -->

- [x] **SC-1 — Shipped and high**: nothing more to do here.
  <!-- sc: prio=high size=S labels=parser ver=0.1.0 -->
- [ ] **SC-2 — `--profile apple|dash-if|none`**: a pipe is legal in a title, and
  SC-63 is named exactly like this.
  <!-- sc: prio=med size=M labels=cli -->

## M2 — Later things <!-- ms: target=v0.2.0 phase=later -->

- [ ] **SC-3 — An em dash — right here**: it has to survive the round trip.
  <!-- sc: prio=low size=L labels=docs,tests -->
- [ ] **SC-4 — Open and high, listed after a low one**: priority decides the
  order inside a milestone, not the order they were written in.
  <!-- sc: prio=high size=M labels=check -->
EOF

export BACKLOG_FILE="$tmp/BACKLOG.md"
export ROADMAP_FILE="$tmp/ROADMAP.md"

# ---------------------------------------------------------------------------
# The generated roadmap
# ---------------------------------------------------------------------------
sh "$script" roadmap >"$tmp/roadmap.log" 2>&1 || {
	echo "FAIL: roadmap exited non-zero" >&2
	sed 's/^/       /' "$tmp/roadmap.log" >&2
	exit 1
}

checks=$((checks + 1))
if [ -f "$tmp/ROADMAP.md" ]; then
	echo "ok   roadmap writes where ROADMAP_FILE points"
else
	fail "roadmap did not write to ROADMAP_FILE — it would have overwritten the repository's own"
	exit 1
fi

# A pipe inside a title must not be read as a column separator: the row it lands
# in has a fixed number of cells and the title is one of them.
assert_contains "a pipe in a title is escaped" '`--profile apple\|dash-if\|none`' "$tmp/ROADMAP.md"
checks=$((checks + 1))
if [ "$(grep -c '^| \*\*SC-2\*\*' "$tmp/ROADMAP.md" 2>/dev/null || grep -c 'SC-2' "$tmp/ROADMAP.md")" -ge 1 ]; then
	echo "ok   the row carrying a pipe is still one row"
else
	fail "SC-2 is missing from the roadmap entirely"
fi

# Every row of one markdown table has the same number of cells, and an escaped
# pipe is not one of them. This is the assertion SC-63 would have failed: the
# roadmap holds several tables, so the count is per table and the run of rows
# ends where the blank line does.
awk '
	/^\|/ {
		row = $0
		gsub(/\\\|/, "", row)          # an escaped pipe is content, not a cell wall
		n = split(row, _, "\\|")
		if (width == 0) { width = n; next }
		if (n != width) print "row " NR " has " n " cells, the table has " width
		next
	}
	{ width = 0 }
' "$tmp/ROADMAP.md" >"$tmp/widths.txt"
checks=$((checks + 1))
if [ ! -s "$tmp/widths.txt" ]; then
	echo "ok   every row of every table has the same number of cells"
else
	fail "a table row was split: $(tr '\n' ';' <"$tmp/widths.txt")"
	grep -n '^|' "$tmp/ROADMAP.md" | sed 's/^/       /' >&2
fi

assert_contains "an em dash survives" "An em dash — right here" "$tmp/ROADMAP.md"
assert_contains "the generated banner is there" "GENERATED by scripts/backlog.sh" "$tmp/ROADMAP.md"

# Inside a milestone the highest priority comes first, whatever order the
# backlog lists them in: the roadmap is read to decide what to do next.
checks=$((checks + 1))
if [ "$(grep -n 'SC-4' "$tmp/ROADMAP.md" | head -1 | cut -d: -f1)" \
	-lt "$(grep -n 'An em dash' "$tmp/ROADMAP.md" | head -1 | cut -d: -f1)" ]; then
	echo "ok   a high-priority item sorts above a low one in the same milestone"
else
	fail "SC-4 (high) is listed after SC-3 (low)"
fi

# ---------------------------------------------------------------------------
# check is the CI gate: it has to notice a roadmap that no longer matches.
# ---------------------------------------------------------------------------
sh "$script" check >"$tmp/check1.txt" 2>&1 || {
	fail "check called a freshly generated roadmap stale"
	sed 's/^/       /' "$tmp/check1.txt" >&2
}
checks=$((checks + 1))
echo "ok   check passes on a freshly generated roadmap"

echo "an edit nobody regenerated" >>"$tmp/ROADMAP.md"
checks=$((checks + 1))
if sh "$script" check >"$tmp/check2.txt" 2>&1; then
	fail "check passed on a stale roadmap — the CI gate does nothing"
else
	echo "ok   check fails on a stale roadmap"
fi
sh "$script" roadmap >/dev/null 2>&1

# ---------------------------------------------------------------------------
# stats and next
# ---------------------------------------------------------------------------
sh "$script" stats >"$tmp/stats.txt" 2>&1
assert_contains "stats counts every item" "4 items" "$tmp/stats.txt"
assert_contains "stats counts what shipped" "1 shipped" "$tmp/stats.txt"
assert_contains "stats breaks the open ones down by priority" "3 open (1 high, 1 med, 1 low)" "$tmp/stats.txt"

sh "$script" next 2 >"$tmp/next.txt" 2>&1
assert_contains "next leads with the highest priority open item" "SC-4" "$tmp/next.txt"
assert_absent "next does not offer work that is already done" "SC-1" "$tmp/next.txt"
checks=$((checks + 1))
if [ "$(wc -l <"$tmp/next.txt")" -eq 2 ]; then
	echo "ok   next honours the count it was given"
else
	fail "next 2 printed $(wc -l <"$tmp/next.txt") lines"
	sed 's/^/       /' "$tmp/next.txt" >&2
fi

# ---------------------------------------------------------------------------
# lint. Each of these is a rule that exists because breaking it costs something
# real, so each is asserted to fail for its own reason rather than merely to
# fail.
# ---------------------------------------------------------------------------
head="# Backlog — fixture

## M1 — Things <!-- ms: target=v0.1.0 phase=now -->
"

# An id used twice: two commits reference SC-2 and mean different work.
printf '%s\n%s\n' "$head" '- [ ] **SC-1 — One**: a.
  <!-- sc: prio=med size=S labels=cli -->
- [ ] **SC-1 — Two**: b.
  <!-- sc: prio=med size=S labels=cli -->' >"$tmp/dup.md"
assert_fails "a duplicate id is rejected" "$tmp/dup.md" "SC-1"

# A hole in the sequence: an id was deleted rather than marked done, and the
# next item to be filed will reuse a number that already means something.
printf '%s\n%s\n' "$head" '- [ ] **SC-1 — One**: a.
  <!-- sc: prio=med size=S labels=cli -->
- [ ] **SC-3 — Three**: the gap where SC-2 was.
  <!-- sc: prio=med size=S labels=cli -->' >"$tmp/gap.md"
assert_fails "a gap in the sequence is rejected" "$tmp/gap.md" "SC-2"

# Metadata that is not one of the values the tooling understands: an unreadable
# priority sorts nowhere, so the item quietly stops being offered by `next`.
printf '%s\n%s\n' "$head" '- [ ] **SC-1 — One**: a.
  <!-- sc: prio=urgent size=S labels=cli -->' >"$tmp/prio.md"
assert_fails "an unknown priority is rejected" "$tmp/prio.md" "prio"

printf '%s\n%s\n' "$head" '- [ ] **SC-1 — One**: a.
  <!-- sc: prio=med size=XXL labels=cli -->' >"$tmp/size.md"
assert_fails "an unknown size is rejected" "$tmp/size.md" "size"

# No metadata comment at all: the item exists in prose and not in the data.
printf '%s\n%s\n' "$head" '- [ ] **SC-1 — One**: a.' >"$tmp/nometa.md"
assert_fails "an item with no metadata is rejected" "$tmp/nometa.md" "SC-1"

# A done item with no ver=: the CHANGELOG says when a thing shipped and the
# backlog has to agree, or neither can be used to answer the question.
printf '%s\n%s\n' "$head" '- [x] **SC-1 — One**: shipped, apparently.
  <!-- sc: prio=med size=S labels=cli -->' >"$tmp/nover.md"
assert_fails "a done item with no version is rejected" "$tmp/nover.md" "ver"

# An open item inside a milestone marked shipped: the milestone is a claim about
# every item under it.
printf '%s\n%s\n' "# Backlog — fixture

## M1 — Things <!-- ms: target=v0.1.0 phase=shipped -->
" '- [ ] **SC-1 — One**: still open.
  <!-- sc: prio=med size=S labels=cli -->' >"$tmp/phase.md"
assert_fails "an open item in a shipped milestone is rejected" "$tmp/phase.md" "shipped"

# And the fixture that is correct must pass, or every assertion above is about a
# linter that rejects everything.
checks=$((checks + 1))
if BACKLOG_FILE="$tmp/BACKLOG.md" sh "$script" lint >"$tmp/lintok.txt" 2>&1; then
	echo "ok   a well-formed backlog passes"
else
	fail "lint rejected the well-formed fixture"
	sed 's/^/       /' "$tmp/lintok.txt" >&2
fi

echo
if [ "$failures" -gt 0 ]; then
	echo "$failures of $checks checks failed" >&2
	exit 1
fi
echo "all $checks checks passed"
