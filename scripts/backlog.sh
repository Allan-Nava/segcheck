#!/bin/sh
# backlog.sh — lint BACKLOG.md and generate ROADMAP.md from it.
#
# BACKLOG.md is the single source of truth: every planned item lives there with a
# stable SC-n id and a trailing `<!-- sc: ... -->` metadata comment. ROADMAP.md is
# a generated view of the same data, grouped by milestone.
#
#   scripts/backlog.sh lint       validate BACKLOG.md (ids, metadata, milestones)
#   scripts/backlog.sh roadmap    regenerate ROADMAP.md
#   scripts/backlog.sh check      fail if ROADMAP.md is stale (CI gate)
#   scripts/backlog.sh stats      one-line summary
#   scripts/backlog.sh next [n]   the n highest-priority open items (default 5)
#   scripts/backlog.sh issues     plan the GitHub issue sync (see below)
#
# POSIX sh and awk only — this repository has no dependencies, and neither does
# its tooling.
#
# BACKLOG_FILE and BACKLOG_ISSUES_SNAPSHOT override the backlog path and the
# source of "which issues exist already". Both exist so the issue planner can be
# tested without a network call and without creating anything on a public
# repository — see scripts/backlog_issues_test.sh.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
backlog="${BACKLOG_FILE:-$root/BACKLOG.md}"
roadmap="$root/ROADMAP.md"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/segcheck-backlog.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT HUP TERM

# ---------------------------------------------------------------------------
# Parse BACKLOG.md into tab-separated records:
#   M <msid> <mstitle> <target> <phase> <order>
#   I <msid> <id> <num> <status> <prio> <size> <labels> <ver> <title>
# Diagnostics go to stderr with a line number and exit non-zero.
# ---------------------------------------------------------------------------
parse() {
	awk '
	function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }

	# The text between the first pair of ** on the line.
	function bold(s,   p, rest, q) {
		p = index(s, "**"); if (!p) return ""
		rest = substr(s, p + 2)
		q = index(rest, "**"); if (!q) return ""
		return substr(rest, 1, q - 1)
	}

	# The body of a `<!-- tag: ... -->` comment, or "".
	function comment(s, tag,   p, rest, q) {
		p = index(s, "<!-- " tag ":"); if (!p) return ""
		rest = substr(s, p + length("<!-- " tag ":"))
		q = index(rest, "-->"); if (!q) return ""
		return trim(substr(rest, 1, q - 1))
	}

	function err(n, msg) { printf "BACKLOG.md:%d: %s\n", n, msg > "/dev/stderr"; bad++ }

	function flush(   b, id, ttl, dash, meta, n, kv, i, eq, k, v) {
		if (buf == "") return
		b = buf; buf = ""

		ttl = bold(b)
		dash = index(ttl, " \342\200\224 ")          # em dash, byte-literal
		if (!dash) { err(bufline, "item title must read `**SC-n — Name**`"); return }
		id = substr(ttl, 1, dash - 1)
		ttl = trim(substr(ttl, dash + length(" \342\200\224 ")))
		if (id !~ /^SC-[0-9]+$/) { err(bufline, "bad id `" id "`"); return }
		if (ttl == "") { err(bufline, id ": empty title"); return }
		if (msid == "") { err(bufline, id ": item outside any milestone"); return }

		prio = ""; size = ""; labels = ""; ver = ""
		meta = comment(b, "sc")
		if (meta == "") { err(bufline, id ": missing `<!-- sc: ... -->` metadata"); return }
		n = split(meta, kv, /[ \t]+/)
		for (i = 1; i <= n; i++) {
			eq = index(kv[i], "=")
			if (!eq) { err(bufline, id ": metadata `" kv[i] "` is not key=value"); continue }
			k = substr(kv[i], 1, eq - 1); v = substr(kv[i], eq + 1)
			if (k == "prio") prio = v
			else if (k == "size") size = v
			else if (k == "labels") labels = v
			else if (k == "ver") ver = v
			else err(bufline, id ": unknown metadata key `" k "`")
		}
		printf "I\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n", \
			msid, id, substr(id, 4) + 0, bufstatus, prio, size, labels, ver, ttl
		printf "L\t%d\t%s\n", bufline, id       # id -> line, for lint messages
	}

	/^```/ { fence = !fence; next }
	fence  { next }

	/^## / {
		flush()
		line = $0
		head = substr(line, 4)
		p = index(head, "<!--"); if (p) head = substr(head, 1, p - 1)
		head = trim(head)
		sub(/ \342\234\205$/, "", head)                 # tolerate a trailing check mark
		dash = index(head, " \342\200\224 ")
		if (head !~ /^M[0-9]+([ \t]|$)/) { msid = ""; next }   # a prose section
		if (!dash) { printf "BACKLOG.md:%d: milestone must read `## Mn — Title`\n", NR > "/dev/stderr"; bad++; msid = ""; next }
		msid = substr(head, 1, dash - 1)
		mstitle = trim(substr(head, dash + length(" \342\200\224 ")))
		target = ""; phase = ""
		meta = comment(line, "ms")
		if (meta == "") { printf "BACKLOG.md:%d: %s: missing `<!-- ms: ... -->` metadata\n", NR, msid > "/dev/stderr"; bad++ }
		n = split(meta, kv, /[ \t]+/)
		for (i = 1; i <= n; i++) {
			eq = index(kv[i], "=")
			if (!eq) continue
			k = substr(kv[i], 1, eq - 1); v = substr(kv[i], eq + 1)
			if (k == "target") target = v
			else if (k == "phase") phase = v
			else { printf "BACKLOG.md:%d: %s: unknown milestone key `%s`\n", NR, msid, k > "/dev/stderr"; bad++ }
		}
		order++
		printf "M\t%s\t%s\t%s\t%s\t%d\n", msid, mstitle, target, phase, order
		printf "N\t%d\t%s\n", NR, msid
		next
	}

	/^- \[[ x]\] / {
		flush()
		bufline = NR
		bufstatus = (substr($0, 4, 1) == "x") ? "done" : "open"
		buf = $0
		next
	}

	# A continuation line of the current item: indented, and not a new bullet.
	buf != "" && /^[ \t]+[^ \t]/ { buf = buf " " $0; next }
	buf != "" { flush() }

	END { flush(); exit (bad ? 1 : 0) }
	' "$backlog"
}

if ! parse >"$tmp/data.tsv"; then
	echo "backlog.sh: BACKLOG.md does not parse — fix the errors above" >&2
	exit 1
fi

# ---------------------------------------------------------------------------
lint() {
	awk -F'\t' '
	function err(msg) { print "BACKLOG.md: " msg > "/dev/stderr"; bad++ }

	BEGIN {
		split("high med low", p, " ");    for (i in p) okprio[p[i]] = 1
		split("S M L XL", s, " ");        for (i in s) oksize[s[i]] = 1
		split("shipped now next later ongoing", f, " "); for (i in f) okphase[f[i]] = 1
		split("parser check output cli delivery integration tests docs release project", l, " ")
		for (i in l) oklabel[l[i]] = 1
	}

	$1 == "L" { line[$3] = $2; next }
	$1 == "N" { msline[$3] = $2; next }

	$1 == "M" {
		nms++
		if ($2 in seenms) err($2 " is declared twice")
		seenms[$2] = 1
		msphase[$2] = $5; mstarget[$2] = $4
		if (!($5 in okphase)) err($2 ": phase `" $5 "` is not shipped|now|next|later|ongoing")
		if ($4 == "") err($2 ": no target release")
		if ($4 != "ongoing" && $4 !~ /^v[0-9]+\.[0-9]+\.[0-9]+$/) err($2 ": target `" $4 "` is not vX.Y.Z or ongoing")
		next
	}

	$1 == "I" {
		id = $3; num = $4 + 0; nitems++
		if (id in seen) err(id " is used twice (ids are stable and unique)")
		seen[id] = 1
		if (num > max) max = num
		ms = $2; status = $5; prio = $6; size = $7; labels = $8; ver = $9

		if (!(prio in okprio)) err(id ": prio `" prio "` is not high|med|low")
		if (!(size in oksize)) err(id ": size `" size "` is not S|M|L|XL")
		if (labels == "") err(id ": no labels")
		n = split(labels, ls, ",")
		for (i = 1; i <= n; i++)
			if (!(ls[i] in oklabel)) err(id ": unknown label `" ls[i] "`")

		if (status == "done" && ver == "") err(id " is checked off but carries no `ver=` — say which release shipped it")
		if (status == "open" && ver != "") err(id " is open but carries `ver=" ver "` — check it off or drop the version")
		if (ver != "" && ver != "unreleased" && ver !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) err(id ": ver `" ver "` is not X.Y.Z or unreleased")

		if (msphase[ms] == "shipped" && status == "open") err(id " is open inside " ms ", which is marked shipped")
		if (msphase[ms] != "shipped" && msphase[ms] != "ongoing" && status == "done" && ver != "unreleased")
			warn = warn "\n  " id " is done inside " ms " (" msphase[ms] ") — consider marking the milestone shipped"
		next
	}

	END {
		for (i = 1; i <= max; i++)
			if (!("SC-" i in seen)) err("SC-" i " is missing — ids run 1..N with no gaps; retire an item with status done, never by deleting it")
		if (bad) { printf "\n%d problem(s) in BACKLOG.md\n", bad > "/dev/stderr"; exit 1 }
		if (warn != "") printf "note:%s\n", warn
		printf "BACKLOG.md OK — %d items across %d milestones\n", nitems, nms
	}
	' "$tmp/data.tsv"
}

# ---------------------------------------------------------------------------
generate() {
	awk -F'\t' '
	function bar(done, total,   filled, i, out) {
		if (total == 0) return "`----------` n/a"
		filled = int(done * 10 / total + 0.5)
		out = ""
		for (i = 0; i < 10; i++) out = out (i < filled ? "#" : ".")
		return "`" out "` " int(done * 100 / total + 0.5) "%"
	}
	function badge(phase) {
		if (phase == "shipped") return "shipped"
		if (phase == "now")     return "**now**"
		if (phase == "next")    return "next"
		if (phase == "later")   return "later"
		return "ongoing"
	}
	function prank(p) { return (p == "high") ? 1 : (p == "med") ? 2 : 3 }

	# A pipe inside a table cell ends the cell, and a backtick does not protect
	# it — `--profile apple|dash-if|none` would silently split one row into
	# three columns. Built by concatenation rather than gsub because the meaning
	# of a backslash in a gsub replacement is not portable.
	function esc(s,   out, i, c) {
		out = ""
		for (i = 1; i <= length(s); i++) {
			c = substr(s, i, 1)
			out = out ((c == "|") ? "\\|" : c)
		}
		return out
	}

	function key(status, prio, num,   n) {
		n = sprintf("%03d", num)
		return ((status == "open") ? "0" : "1") prank(prio) n
	}

	$1 == "M" {
		o = $6 + 0
		ord[o] = $2; mstitle[$2] = $3; target[$2] = $4; phase[$2] = $5
		nms = (o > nms) ? o : nms
		next
	}
	$1 == "I" {
		ms = $2; id = $3; num = $4 + 0; status = $5; prio = $6; size = $7; labels = $8; ver = $9; title = $10
		k = key(status, prio, num)
		cnt[ms]++
		row[ms, k] = sprintf("| **%s** — %s | %s | %s | %s | %s |", \
			id, esc(title), prio, size, esc(labels), \
			(status != "done") ? "open" : \
			(ver == "unreleased") ? "done, unreleased" : "shipped `" ver "`")
		keys[ms] = keys[ms] " " k
		if (status == "done") { done[ms]++; ndone++ } else { open[ms]++; nopen++ }
		total++
		n = split(labels, ls, ",")
		for (i = 1; i <= n; i++) {
			lab[ls[i]]++
			if (status == "open") labopen[ls[i]]++
		}
		if (status == "open" && (phase[ms] == "now" || phase[ms] == "next" || phase[ms] == "ongoing")) {
			upk = prank(prio) sprintf("%03d", num)
			up[upk] = sprintf("- **%s** — %s · `%s` · size `%s` · %s (%s, target `%s`)", \
				id, title, prio, size, labels, ms, target[ms])
			nup++
		}
		next
	}

	function emit_ms(ms,   n, ks, i, j, t, sorted) {
		n = split(keys[ms], ks, " ")
		# ks[] has a leading empty field from the leading space; sort what is there.
		for (i = 1; i <= n; i++)
			for (j = i + 1; j <= n; j++)
				if (ks[j] != "" && (ks[i] == "" || ks[i] > ks[j])) { t = ks[i]; ks[i] = ks[j]; ks[j] = t }
		print ""
		printf "### %s — %s\n\n", ms, mstitle[ms]
		printf "Target `%s` · %s · %d open · %d shipped · %s\n\n", \
			target[ms], badge(phase[ms]), open[ms] + 0, done[ms] + 0, bar(done[ms] + 0, cnt[ms] + 0)
		print "| Item | Priority | Size | Labels | Status |"
		print "|---|---|---|---|---|"
		for (i = 1; i <= n; i++) if (ks[i] != "") print row[ms, ks[i]]
	}

	END {
		print "# Roadmap — segcheck"
		print ""
		print "<!-- GENERATED by scripts/backlog.sh roadmap — do not edit by hand. -->"
		print ""
		print "> This page is **generated** from [BACKLOG.md](BACKLOG.md), the single source"
		print "> of truth for planned work. Regenerate it with `scripts/backlog.sh roadmap`"
		print "> after editing the backlog — CI fails when the two disagree."
		print ""
		printf "**%d items · %d shipped · %d open · %d milestones.**\n", total, ndone, nopen, nms
		print ""
		print "## At a glance"
		print ""
		print "| Milestone | Target | Phase | Progress | Open | Shipped |"
		print "|---|---|---|---|---|---|"
		for (o = 1; o <= nms; o++) {
			ms = ord[o]
			printf "| **%s** — %s | `%s` | %s | %s | %d | %d |\n", \
				ms, esc(mstitle[ms]), target[ms], badge(phase[ms]), \
				bar(done[ms] + 0, cnt[ms] + 0), open[ms] + 0, done[ms] + 0
		}
		print ""
		print "## Next up"
		print ""
		print "The open items with the highest priority in the milestones that are in flight."
		print ""
		n = asortkeys()
		shown = 0
		for (i = 1; i <= n && shown < 8; i++) { print up[sk[i]]; shown++ }
		if (shown == 0) print "_Nothing in flight._"
		print ""
		print "## Milestones"
		for (o = 1; o <= nms; o++) emit_ms(ord[o])
		print ""
		print "## By label"
		print ""
		print "| Label | Items | Open |"
		print "|---|---|---|"
		n = 0
		for (l in lab) { n++; ls[n] = l }
		for (i = 1; i <= n; i++)
			for (j = i + 1; j <= n; j++)
				if (labopen[ls[i]] + 0 < labopen[ls[j]] + 0 || \
				    (labopen[ls[i]] + 0 == labopen[ls[j]] + 0 && ls[i] > ls[j])) { t = ls[i]; ls[i] = ls[j]; ls[j] = t }
		for (i = 1; i <= n; i++) printf "| `%s` | %d | %d |\n", ls[i], lab[ls[i]], labopen[ls[i]] + 0
	}

	function asortkeys(   k, n, i, j, t) {
		n = 0
		for (k in up) { n++; sk[n] = k }
		for (i = 1; i <= n; i++)
			for (j = i + 1; j <= n; j++)
				if (sk[i] > sk[j]) { t = sk[i]; sk[i] = sk[j]; sk[j] = t }
		return n
	}
	' "$tmp/data.tsv"
}

# ---------------------------------------------------------------------------
stats() {
	awk -F'\t' '
	$1 == "I" { total++; if ($5 == "done") d++; else { o++; prio[$6]++ } }
	$1 == "M" { ms++ }
	END { printf "%d items · %d shipped · %d open (%d high, %d med, %d low) · %d milestones\n", \
		total, d, o, prio["high"] + 0, prio["med"] + 0, prio["low"] + 0, ms }
	' "$tmp/data.tsv"
}

next_items() {
	n=${1:-5}
	awk -F'\t' -v want="$n" '
	function prank(p) { return (p == "high") ? 1 : (p == "med") ? 2 : 3 }
	$1 == "M" { phase[$2] = $5; target[$2] = $4 }
	$1 == "I" && $5 == "open" {
		k = prank($6) sprintf("%03d", $4)
		if (phase[$2] == "later") k = "9" k
		line[k] = sprintf("%-7s %-4s %-2s  %-28s %s", $3, $6, $7, $10, $2 " → " target[$2])
		n++; ks[n] = k
	}
	END {
		for (i = 1; i <= n; i++)
			for (j = i + 1; j <= n; j++)
				if (ks[i] > ks[j]) { t = ks[i]; ks[i] = ks[j]; ks[j] = t }
		for (i = 1; i <= n && i <= want; i++) print line[ks[i]]
	}
	' "$tmp/data.tsv"
}

# ---------------------------------------------------------------------------
# GitHub issue sync.
#
# BACKLOG.md stays the source of truth; the issues are a view of it, the same way
# ROADMAP.md is. Deciding what to do is kept separate from doing it: `issues`
# prints a plan and touches nothing, `issues --apply` executes that plan. The
# planner is what the tests assert, because the failure modes are all in the
# decision — a sync that is not idempotent opens a duplicate for every item on
# every push, and one that misreads a tick closes issues for work still open.
# ---------------------------------------------------------------------------

# item_bodies emits `<id> <tab> <prose>` — the item's text with the metadata
# comment and the bold title removed, whitespace collapsed onto one line.
item_bodies() {
	awk '
	function trim(s) { sub(/^[ \t]+/, "", s); sub(/[ \t]+$/, "", s); return s }

	function flush(   b, p, rest, q, id, dash) {
		if (buf == "") return
		b = buf; buf = ""
		p = index(b, "**"); if (!p) return
		rest = substr(b, p + 2)
		q = index(rest, "**"); if (!q) return
		id = substr(rest, 1, q - 1)
		dash = index(id, " \342\200\224 ")
		if (!dash) return
		id = substr(id, 1, dash - 1)

		# Everything after the closing ** of the title, minus a leading colon.
		rest = substr(rest, q + 2)
		sub(/^[ \t]*:[ \t]*/, "", rest)
		# Drop the metadata comment wherever it sits.
		p = index(rest, "<!-- sc:")
		if (p) rest = substr(rest, 1, p - 1)
		gsub(/[ \t]+/, " ", rest)
		printf "%s\t%s\n", id, trim(rest)
	}

	/^- \[[ x]\] \*\*SC-/ { flush(); buf = $0; next }
	buf != "" && /^[ \t]+[^ \t]/ { buf = buf " " $0; next }
	buf != "" { flush() }
	END { flush() }
	' "$backlog"
}

# existing_issues emits `<id> <tab> <number> <tab> <state>` for the issues that
# already exist. A snapshot file stands in for GitHub under test.
existing_issues() {
	if [ -n "${BACKLOG_ISSUES_SNAPSHOT:-}" ]; then
		cat "$BACKLOG_ISSUES_SNAPSHOT"
		return
	fi
	# The id is the title prefix, which is the only durable link back to the
	# backlog: issue numbers are assigned by GitHub and ids never change.
	gh issue list --state all --limit 500 \
		--json number,title,state \
		--jq '.[] | "\(.title)\t\(.number)\t\(.state | ascii_downcase)"' |
		awk -F'\t' '{
			ttl = $1
			dash = index(ttl, " \342\200\224 ")
			id = dash ? substr(ttl, 1, dash - 1) : ttl
			if (id ~ /^SC-[0-9]+$/) printf "%s\t%s\t%s\n", id, $2, $3
		}'
}

# plan_issues prints one action line per item, plus a human-readable summary on
# stderr-free trailing lines. Actions:
#   CREATE <id> -        an open item with no issue
#   REOPEN <id> <num>    an open item whose issue was closed
#   CLOSE  <id> <num>    a shipped item whose issue is still open
#   OK     <id> <num>    already in the right state
#   SKIP   <id> -        shipped and never had an issue: do not create one now
plan_issues() {
	awk -v filter="$1" -F'\t' -v exfile="$tmp/existing.tsv" '
	BEGIN {
		n = split(filter, f, ",")
		for (i = 1; i <= n; i++) if (f[i] != "") want[f[i]] = 1
	}
	FILENAME == exfile { ex_num[$1] = $2; ex_state[$1] = $3; next }
	$1 == "M" { ms_title[$2] = $3; ms_target[$2] = $4; next }
	$1 == "I" {
		msid = $2; id = $3; status = $5
		if (length(want) && !(msid in want)) next
		num = (id in ex_num) ? ex_num[id] : ""
		st = (id in ex_state) ? ex_state[id] : ""
		if (status == "open") {
			if (num == "")            printf "CREATE\t%s\t-\n", id
			else if (st == "closed")  printf "REOPEN\t%s\t%s\n", id, num
			else                      printf "OK\t%s\t%s\n", id, num
		} else {
			if (num == "")            printf "SKIP\t%s\t-\n", id
			else if (st == "open")    printf "CLOSE\t%s\t%s\n", id, num
			else                      printf "OK\t%s\t%s\n", id, num
		}
	}
	' "$tmp/existing.tsv" "$tmp/data.tsv"
}

# issue_body composes the markdown body for one item.
issue_body() {
	awk -v want="$1" -F'\t' \
		-v bodyfile="$tmp/bodies.tsv" \
		-v repo_blob="https://github.com/Allan-Nava/segcheck/blob/main" '
	FILENAME == bodyfile { body[$1] = $2; next }
	$1 == "M" { ms_title[$2] = $3; ms_target[$2] = $4; next }
	$1 == "I" && $3 == want {
		printf "%s\n\n", body[want]
		print "---"
		print ""
		printf "Planned work, tracked in [BACKLOG.md](%s/BACKLOG.md) as `%s` under **%s — %s**, targeted at %s (priority %s, size %s).\n",
			repo_blob, want, $2, ms_title[$2], ms_target[$2], $6, $7
		print ""
		print "`BACKLOG.md` is the single source of truth: it carries the stable `SC-n` id that commits and the CHANGELOG reference, and [ROADMAP.md](" repo_blob "/ROADMAP.md) is generated from it. This issue is a view of that item, kept in step by `scripts/backlog.sh issues --apply`, so closing it means ticking the item in the backlog and regenerating the roadmap in the same commit."
	}
	' "$tmp/bodies.tsv" "$tmp/data.tsv"
}

# issue_meta prints `<title>|<labels>|<milestone title>` for one item.
issue_meta() {
	awk -v want="$1" -F'\t' '
	$1 == "M" { ms_title[$2] = $3; next }
	$1 == "I" && $3 == want {
		printf "%s \342\200\224 %s|%s,prio-%s|%s \342\200\224 %s\n",
			$3, $9, $8, $6, $2, ms_title[$2]
	}
	' "$tmp/data.tsv"
}

# ensure_milestone creates the GitHub milestone for an M-n if it is missing, and
# is quiet when it already exists.
ensure_milestone() {
	title=$1
	if gh api "repos/:owner/:repo/milestones?state=all&per_page=100" \
		--jq '.[].title' 2>/dev/null | grep -qxF "$title"; then
		return 0
	fi
	gh api "repos/:owner/:repo/milestones" -f title="$title" \
		-f description="Backlog milestone $title. Source of truth: BACKLOG.md" >/dev/null
	echo "  created milestone $title"
}

# ensure_labels creates the label vocabulary if it is missing. Without this a
# fresh clone — or a fork — fails on the first `--apply` with an unknown label,
# and `gh issue create` would otherwise be capable of opening the issue with the
# labels silently dropped.
ensure_labels() {
	gh label list --limit 200 --json name --jq '.[].name' >"$tmp/labels.txt" 2>/dev/null || : >"$tmp/labels.txt"
	# Keep in step with the vocabulary lint enforces, plus the priorities.
	for spec in \
		"parser|1d76db|Segment and manifest readers" \
		"check|0e8a16|An analysis that compares media against the manifest" \
		"output|5319e7|Renderers: terminal, JSON, markdown" \
		"cli|fbca04|Flags, exit codes, usage" \
		"delivery|c2e0c6|Docker image, packaging, install" \
		"integration|bfd4f2|Using segcheck from other systems" \
		"tests|d4c5f9|Test coverage and test tooling" \
		"docs|0075ca|Documentation and the Pages site" \
		"release|b60205|Tagging, artefacts, signing" \
		"project|6a737d|Backlog, roadmap, repo hygiene" \
		"prio-high|b60205|High priority in BACKLOG.md" \
		"prio-med|fbca04|Medium priority in BACKLOG.md" \
		"prio-low|c2e0c6|Low priority in BACKLOG.md"; do
		name=${spec%%|*}
		rest=${spec#*|}
		colour=${rest%%|*}
		desc=${rest#*|}
		if ! grep -qxF "$name" "$tmp/labels.txt"; then
			gh label create "$name" --color "$colour" --description "$desc" >/dev/null
			echo "  created label $name"
		fi
	done
}

apply_issues() {
	ensure_labels
	while IFS="$(printf '\t')" read -r action id num; do
		case "$action" in
		CREATE)
			meta=$(issue_meta "$id")
			ttl=${meta%%|*}
			rest=${meta#*|}
			labels=${rest%%|*}
			ms=${rest#*|}
			ensure_milestone "$ms"
			issue_body "$id" >"$tmp/body.md"
			set -- gh issue create --title "$ttl" --body-file "$tmp/body.md" --milestone "$ms"
			# One --label per name; a label that does not exist is a hard error
			# rather than a silently unlabelled issue.
			old_ifs=$IFS
			IFS=,
			for l in $labels; do
				[ -n "$l" ] && set -- "$@" --label "$l"
			done
			IFS=$old_ifs
			url=$("$@")
			echo "  created $id  $url"
			;;
		CLOSE)
			gh issue close "$num" \
				--comment "Shipped: the backlog item is ticked in BACKLOG.md. Closed by \`scripts/backlog.sh issues --apply\`." >/dev/null
			echo "  closed $id  #$num"
			;;
		REOPEN)
			gh issue reopen "$num" \
				--comment "Reopened: the backlog item is open again in BACKLOG.md. Reopened by \`scripts/backlog.sh issues --apply\`." >/dev/null
			echo "  reopened $id  #$num"
			;;
		esac
	done
}

issues_cmd() {
	apply=0
	filter=""
	body_of=""
	while [ $# -gt 0 ]; do
		case "$1" in
		--apply) apply=1 ;;
		--dry-run) apply=0 ;;
		--milestones)
			shift
			filter="${1:-}"
			[ -n "$filter" ] || {
				echo "backlog.sh: --milestones needs a value, e.g. --milestones M11,M12" >&2
				exit 2
			}
			;;
		--milestones=*) filter="${1#--milestones=}" ;;
		--body)
			shift
			body_of="${1:-}"
			[ -n "$body_of" ] || {
				echo "backlog.sh: --body needs an SC-n id" >&2
				exit 2
			}
			;;
		*)
			echo "backlog.sh: unknown option for issues: $1" >&2
			exit 2
			;;
		esac
		shift
	done

	item_bodies >"$tmp/bodies.tsv"

	if [ -n "$body_of" ]; then
		issue_body "$body_of"
		return 0
	fi

	existing_issues >"$tmp/existing.tsv"
	plan_issues "$filter" >"$tmp/plan.tsv"

	create=$(grep -c '^CREATE' "$tmp/plan.tsv" || true)
	close=$(grep -c '^CLOSE' "$tmp/plan.tsv" || true)
	reopen=$(grep -c '^REOPEN' "$tmp/plan.tsv" || true)

	cat "$tmp/plan.tsv"
	echo ""
	echo "plan: $create to create, $close to close, $reopen to reopen"

	if [ "$apply" -eq 0 ]; then
		if [ "$((create + close + reopen))" -gt 0 ]; then
			echo "dry run — nothing was changed. Re-run with --apply to execute."
		else
			echo "nothing to do: the issues already match BACKLOG.md"
		fi
		return 0
	fi

	if [ "$((create + close + reopen))" -eq 0 ]; then
		echo "nothing to do"
		return 0
	fi
	echo "applying:"
	grep -E '^(CREATE|CLOSE|REOPEN)' "$tmp/plan.tsv" | apply_issues
}

case "${1:-lint}" in
lint)
	lint
	;;
roadmap)
	generate >"$tmp/ROADMAP.md"
	mv "$tmp/ROADMAP.md" "$roadmap"
	echo "wrote ROADMAP.md — $(stats)"
	;;
check)
	lint >/dev/null
	generate >"$tmp/ROADMAP.md"
	if ! diff -u "$roadmap" "$tmp/ROADMAP.md"; then
		echo "" >&2
		echo "ROADMAP.md is stale: run scripts/backlog.sh roadmap and commit the result" >&2
		exit 1
	fi
	echo "ROADMAP.md is up to date"
	;;
stats)
	stats
	;;
next)
	next_items "${2:-5}"
	;;
issues)
	shift
	issues_cmd "$@"
	;;
*)
	echo "usage: scripts/backlog.sh [lint|roadmap|check|stats|next [n]]" >&2
	echo "       scripts/backlog.sh issues [--apply] [--milestones M11,M12] [--body SC-n]" >&2
	exit 2
	;;
esac
