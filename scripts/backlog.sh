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
#
# POSIX sh and awk only — this repository has no dependencies, and neither does
# its tooling.

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
backlog="$root/BACKLOG.md"
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
			id, title, prio, size, labels, \
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
				ms, mstitle[ms], target[ms], badge(phase[ms]), \
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
*)
	echo "usage: scripts/backlog.sh [lint|roadmap|check|stats|next [n]]" >&2
	exit 2
	;;
esac
